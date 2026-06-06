package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/anacrolix/torrent"
	"github.com/anacrolix/torrent/metainfo"
)

// ─────────────────────────────────────────────────────────────────────────────
// Telegram Bot API — stdlib only, no external deps
// ─────────────────────────────────────────────────────────────────────────────

type Bot struct {
	token     string
	baseURL   string
	client    *http.Client
	channelID int64 // target chat ID (channel or group)
}

func NewBot(token string, channelID int64) *Bot {
	return &Bot{
		token:     token,
		baseURL:   "https://api.telegram.org/bot" + token,
		channelID: channelID,
		client: &http.Client{
			Timeout: 180 * time.Second,
			Transport: &http.Transport{
				MaxIdleConns:        200,
				MaxIdleConnsPerHost: 50,
				IdleConnTimeout:     200 * time.Second,
			},
		},
	}
}

// uploadClient returns a fresh HTTP client for file uploads
func uploadClient() *http.Client {
	return &http.Client{
		Timeout: 15 * time.Minute,
		Transport: &http.Transport{
			MaxIdleConns:        20,
			MaxIdleConnsPerHost: 20,
			IdleConnTimeout:     120 * time.Second,
		},
	}
}

func (b *Bot) api(method string, params map[string]string) ([]byte, error) {
	reqURL := b.baseURL + "/" + method
	if len(params) > 0 {
		q := url.Values{}
		for k, v := range params {
			q.Set(k, v)
		}
		reqURL += "?" + q.Encode()
	}
	resp, err := b.client.Get(reqURL)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	return io.ReadAll(resp.Body)
}

func (b *Bot) post(path string, mime string, body io.Reader) ([]byte, error) {
	resp, err := b.client.Post(b.baseURL+"/"+path, mime, body)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	return io.ReadAll(resp.Body)
}

// getChatID returns the configured channel ID (or per-message chatID if channel not set)
func (b *Bot) getChatID(msgChatID int64) int64 {
	if b.channelID != 0 {
		return b.channelID
	}
	return msgChatID
}

// ── Types ─────────────────────────────────────────────────────────────────────

type Update struct {
	UpdateID      int `json:"update_id"`
	Message       *struct {
		MessageID int    `json:"message_id"`
		Chat      struct {
			ID int64 `json:"id"`
		} `json:"chat"`
		Text    string `json:"text"`
		Caption string `json:"caption"`
	} `json:"message"`
	CallbackQuery *struct {
		ID      string `json:"id"`
		Data    string `json:"data"`
		Message struct {
			MessageID int   `json:"message_id"`
			Chat      struct {
				ID int64 `json:"id"`
			} `json:"chat"`
		} `json:"message"`
	} `json:"callback_query"`
}

func (b *Bot) sendMsg(chatID int64, text string, replyTo int, markdown bool) (int, error) {
	var parseMode string
	if markdown {
		parseMode = "Markdown"
	}
	body, _ := json.Marshal(map[string]interface{}{
		"chat_id":             chatID,
		"text":                text,
		"parse_mode":          parseMode,
		"reply_to_message_id": replyTo,
	})
	data, err := b.post("sendMessage", "application/json", strings.NewReader(string(body)))
	if err != nil {
		log.Printf("[%d] sendMsg error: %v", chatID, err)
		return 0, err
	}
	var r struct {
		OK     bool `json:"ok"`
		Result struct {
			MessageID int `json:"message_id"`
		} `json:"result"`
	}
	json.Unmarshal(data, &r)
	if !r.OK {
		log.Printf("[%d] sendMsg API error: %s", chatID, string(data))
		return 0, fmt.Errorf("api error")
	}
	return r.Result.MessageID, nil
}

func (b *Bot) editMsg(chatID int64, msgID int, text string) {
	body, _ := json.Marshal(map[string]interface{}{
		"chat_id":    chatID,
		"message_id": msgID,
		"text":       text,
		"parse_mode": "Markdown",
	})
	_, err := b.post("editMessageText", "application/json", strings.NewReader(string(body)))
	if err != nil {
		log.Printf("[%d] editMsg error: %v", chatID, err)
	}
}

func (b *Bot) answerCallback(queryID string) {
	body, _ := json.Marshal(map[string]interface{}{"callback_query_id": queryID})
	b.post("answerCallbackQuery", "application/json", strings.NewReader(string(body)))
}

func (b *Bot) sendAction(chatID int64) {
	body, _ := json.Marshal(map[string]interface{}{"chat_id": chatID, "action": "upload_document"})
	b.post("sendChatAction", "application/json", strings.NewReader(string(body)))
}

// uploadFile streams a file from disk to Telegram using io.Pipe — minimal memory footprint
func (b *Bot) uploadFile(chatID int64, filePath string, caption string) error {
	file, err := os.Open(filePath)
	if err != nil {
		return fmt.Errorf("open file: %w", err)
	}
	defer file.Close()

	stat, err := file.Stat()
	if err != nil {
		return fmt.Errorf("stat file: %w", err)
	}
	if stat.Size() == 0 {
		return fmt.Errorf("file is empty")
	}

	filename := filepath.Base(filePath)
	client := uploadClient()
	defer client.CloseIdleConnections()

	// Use io.Pipe for true streaming — no memory buffer accumulation
	pr, pw := io.Pipe()
	writer := multipart.NewWriter(pw)

	// Error channel for the writer goroutine
	errChan := make(chan error, 1)

	// Start writer goroutine that streams file data through the pipe
	go func() {
		defer pw.Close()

		// Write form fields
		if err := writer.WriteField("chat_id", strconv.FormatInt(chatID, 10)); err != nil {
			errChan <- fmt.Errorf("write chat_id field: %w", err)
			return
		}
		if caption != "" {
			if err := writer.WriteField("caption", caption); err != nil {
				errChan <- fmt.Errorf("write caption field: %w", err)
				return
			}
		}

		// Create the file part
		part, err := writer.CreateFormFile("document", filename)
		if err != nil {
			errChan <- fmt.Errorf("create form file: %w", err)
			return
		}

		// Stream file in 64KB chunks — minimal memory usage
		buf := make([]byte, 65536)
		for {
			n, readErr := file.Read(buf)
			if n > 0 {
				if _, writeErr := part.Write(buf[:n]); writeErr != nil {
					errChan <- fmt.Errorf("write to multipart: %w", writeErr)
					return
				}
			}
			if readErr != nil {
				if readErr != io.EOF {
					errChan <- fmt.Errorf("read file: %w", readErr)
					return
				}
				break
			}
		}

		// Close the multipart writer (sends EOF to the pipe reader)
		if err := writer.Close(); err != nil {
			errChan <- fmt.Errorf("close writer: %w", err)
			return
		}
		errChan <- nil
	}()

	// Create HTTP request with the pipe reader as the body
	req, err := http.NewRequest("POST", b.baseURL+"/sendDocument", pr)
	if err != nil {
		return fmt.Errorf("new request: %w", err)
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())

	log.Printf("[%d] 📤 Uploading: %s (%s)", chatID, filename, formatBytes(stat.Size()))

	// Execute the request
	resp, err := client.Do(req)
	if err != nil {
		// Check if writer goroutine has an error
		select {
		case werr := <-errChan:
			if werr != nil {
				log.Printf("[%d] writer error: %v", chatID, werr)
			}
		default:
		}
		return fmt.Errorf("do request: %w", err)
	}
	defer resp.Body.Close()

	// Read response body
	respData, err := io.ReadAll(resp.Body)
	if err != nil {
		log.Printf("[%d] warning: could not read response body: %v", chatID, err)
		respData = []byte{}
	}

	// Wait for writer goroutine to finish and check its error
	werr := <-errChan
	if werr != nil {
		log.Printf("[%d] writer error: %v", chatID, werr)
		return werr
	}

	// Parse response
	var result struct {
		OK          bool   `json:"ok"`
		Description string `json:"description"`
	}
	json.Unmarshal(respData, &result)

	if !result.OK {
		log.Printf("[%d] upload failed: %s", chatID, string(respData))
		return fmt.Errorf("upload error: %s", result.Description)
	}

	log.Printf("[%d] ✅ Uploaded: %s", chatID, filename)
	return nil
}

// ─────────────────────────────────────────────────────────────────────────────
// Download engine
// ─────────────────────────────────────────────────────────────────────────────

type Task struct {
	Name       string
	Progress   float64
	Done       bool
	Files      []FileEntry
	TotalBytes int64
	Error      string
	StartedAt  time.Time
	Torrent    *torrent.Torrent
}

type FileEntry struct {
	DisplayPath string
	Length      int64
}

type Engine struct {
	tc      *torrent.Client
	storage string
	tasks   map[int64]*Task
	mu      sync.Mutex
}

func NewEngine(tc *torrent.Client, storage string) *Engine {
	return &Engine{tc: tc, storage: storage, tasks: make(map[int64]*Task)}
}

func (e *Engine) Handle(bot *Bot, chatID int64, replyTo int, text string) {
	log.Printf("[%d] ▶️  %q", chatID, text)

	switch text {
	case "/cancel":
		e.cmdCancel(bot, chatID, replyTo)
	case "/start", "/help":
		e.cmdStart(bot, chatID, replyTo)
	case "/status":
		e.cmdStatus(bot, chatID, replyTo)
	default:
		if isMagnet(text) {
			e.startDownloadMagnet(bot, chatID, replyTo, text)
		} else if isTorrentURL(text) {
			e.startDownloadURL(bot, chatID, replyTo, text)
		} else {
			e.cmdStart(bot, chatID, replyTo)
		}
	}
}

func (e *Engine) cmdStart(bot *Bot, chatID int64, replyTo int) {
	bot.sendMsg(chatID, helpText, replyTo, true)
}

func (e *Engine) cmdCancel(bot *Bot, chatID int64, replyTo int) {
	e.mu.Lock()
	_, ok := e.tasks[chatID]
	if !ok {
		e.mu.Unlock()
		bot.sendMsg(chatID, "📭 Nothing to cancel.", replyTo, true)
		return
	}
	delete(e.tasks, chatID)
	e.mu.Unlock()
	bot.sendMsg(chatID, "🚫 Download cancelled.", replyTo, true)
}

func (e *Engine) cmdStatus(bot *Bot, chatID int64, replyTo int) {
	e.mu.Lock()
	t, ok := e.tasks[chatID]
	e.mu.Unlock()

	if !ok {
		bot.sendMsg(chatID, "📭 *No active download.*", replyTo, true)
		return
	}

	var msg string
	if t.Error != "" {
		msg = fmt.Sprintf("❌ *Error:* `%s`", t.Error)
	} else if t.Done {
		msg = fmt.Sprintf("✅ *Completed:* `%s`", t.Name)
	} else {
		elapsed := time.Since(t.StartedAt).Round(time.Second)
		bar := strings.Repeat("█", int(t.Progress)/5) + strings.Repeat("░", 20-int(t.Progress)/5)
		msg = fmt.Sprintf("⏳ *Downloading:* `%s`\n%s `%.1f%%`\n⏱ %s",
			t.Name, bar, t.Progress, elapsed)
	}
	bot.sendMsg(chatID, msg, replyTo, true)
}

func (e *Engine) startDownloadMagnet(bot *Bot, chatID int64, replyTo int, input string) {
	e.mu.Lock()
	if _, active := e.tasks[chatID]; active {
		e.mu.Unlock()
		bot.sendMsg(chatID, "⏳ Download already in progress. Use /cancel first.", replyTo, true)
		return
	}
	e.tasks[chatID] = &Task{StartedAt: time.Now()}
	e.mu.Unlock()

	clean := strings.TrimSpace(input)
	t, err := e.tc.AddMagnet(clean)
	if err != nil {
		e.mu.Lock()
		delete(e.tasks, chatID)
		e.mu.Unlock()
		bot.sendMsg(chatID, fmt.Sprintf("❌ *Magnet error:* `%s`", err.Error()), 0, true)
		return
	}
	log.Printf("[%d] magnet added: %s", chatID, t.Name())

	bot.sendMsg(chatID, fmt.Sprintf("📥 *Added:* `%s`\n⏳ *Waiting for metadata…*", t.Name()), 0, true)
	go e.downloadLoop(bot, chatID, replyTo, t)
}

func (e *Engine) startDownloadURL(bot *Bot, chatID int64, replyTo int, input string) {
	e.mu.Lock()
	if _, active := e.tasks[chatID]; active {
		e.mu.Unlock()
		bot.sendMsg(chatID, "⏳ Download already in progress. Use /cancel first.", replyTo, true)
		return
	}
	e.tasks[chatID] = &Task{StartedAt: time.Now()}
	e.mu.Unlock()

	bot.sendAction(chatID)

	// Stream-fetch the .torrent file to avoid loading it fully into memory
	mi, fetchErr := e.fetchTorrentFile(input)
	if fetchErr != nil {
		e.mu.Lock()
		delete(e.tasks, chatID)
		e.mu.Unlock()
		bot.sendMsg(chatID, fmt.Sprintf("❌ *Fetch failed:* `%s`", fetchErr.Error()), 0, true)
		return
	}

	t, err := e.tc.AddTorrent(mi)
	if err != nil {
		e.mu.Lock()
		delete(e.tasks, chatID)
		e.mu.Unlock()
		bot.sendMsg(chatID, fmt.Sprintf("❌ *Error:* `%s`", err.Error()), 0, true)
		return
	}
	log.Printf("[%d] torrent from URL: %s", chatID, t.Name())

	bot.sendMsg(chatID, fmt.Sprintf("📥 *Added:* `%s`\n⏳ *Waiting for metadata…*", t.Name()), 0, true)
	go e.downloadLoop(bot, chatID, replyTo, t)
}

// fetchTorrentFile downloads a .torrent file using streaming to avoid memory spikes
func (e *Engine) fetchTorrentFile(rawURL string) (*metainfo.MetaInfo, error) {
	client := &http.Client{Timeout: 60 * time.Second}
	resp, err := client.Get(rawURL)
	if err != nil {
		return nil, fmt.Errorf("HTTP GET: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	// Limit response body size to prevent memory issues (10MB max for .torrent files)
	// Typical .torrent files are < 1MB; this is a safety limit
	limitedReader := &io.LimitedReader{R: resp.Body, N: 10 * 1024 * 1024}

	mi, err := metainfo.Load(limitedReader)
	if err != nil {
		return nil, fmt.Errorf("parse torrent: %w", err)
	}
	return mi, nil
}

func (e *Engine) downloadLoop(bot *Bot, chatID int64, replyTo int, t *torrent.Torrent) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("[%d] panic recovered in downloadLoop: %v", chatID, r)
		}
	}()

	// Wait for metadata (info dictionary) to be received
	select {
	case <-t.GotInfo():
	case <-time.After(120 * time.Second):
		bot.sendMsg(chatID, "❌ *Timeout: no metadata after 2 minutes.*", 0, true)
		e.mu.Lock()
		delete(e.tasks, chatID)
		e.mu.Unlock()
		return
	}

	info := t.Info()
	name := t.Name()
	if info != nil {
		name = info.Name
	}
	total := t.Length()

	var files []FileEntry
	for _, f := range t.Files() {
		files = append(files, FileEntry{
			DisplayPath: f.DisplayPath(),
			Length:      f.Length(),
		})
	}

	e.mu.Lock()
	e.tasks[chatID].Name = name
	e.tasks[chatID].Files = files
	e.tasks[chatID].TotalBytes = total
	e.tasks[chatID].Torrent = t
	e.mu.Unlock()

	t.DownloadAll()
	log.Printf("[%d] download started: %s (%s)", chatID, name, formatBytes(total))

	statusID, _ := bot.sendMsg(chatID,
		fmt.Sprintf("📥 *Downloading:* `%s`\n%s / %s", name, "0 B", formatBytes(total)), 0, true)

	ticker := time.NewTicker(3 * time.Second)
	stall := time.NewTicker(120 * time.Second)

	var lastBytes int64
	lastTime := time.Now()

	for {
		select {
		case <-ticker.C:
			completed := t.BytesCompleted()
			pct := float64(completed) / float64(total) * 100

			e.mu.Lock()
			if e.tasks[chatID] == nil {
				e.mu.Unlock()
				ticker.Stop()
				stall.Stop()
				return
			}
			e.tasks[chatID].Progress = pct
			startTime := e.tasks[chatID].StartedAt
			e.mu.Unlock()

			var speed string
			elapsed := time.Since(startTime).Seconds()
			if elapsed > 5 && completed > 0 {
				bps := int64(float64(completed) / elapsed)
				speed = fmt.Sprintf("%s/s", formatBytes(bps))
			} else {
				speed = "connecting…"
			}

			if completed > lastBytes {
				lastBytes = completed
				lastTime = time.Now()
			}

			bar := strings.Repeat("█", int(pct)/5) + strings.Repeat("░", 20-int(pct)/5)
			bot.editMsg(chatID, statusID,
				fmt.Sprintf("📥 *Downloading*\n`%s`\n%s `%.1f%%`\n%sw %s / %s",
					name, bar, pct, speed, formatBytes(completed), formatBytes(total)))

		case <-stall.C:
			if time.Since(lastTime) >= 120*time.Second {
				e.mu.Lock()
				if e.tasks[chatID] != nil {
					e.mu.Unlock()
					bot.sendMsg(chatID, "❌ *Stalled — no peers for 2 minutes.*", 0, true)
				} else {
					e.mu.Unlock()
				}
				ticker.Stop()
				stall.Stop()
				return
			}
		}

		if t.BytesCompleted() >= total {
			ticker.Stop()
			stall.Stop()
			break
		}
	}

	log.Printf("[%d] download complete, ensuring all data is flushed...", chatID)

	// Give torrent library a moment to flush all pieces to storage
	// This is critical for NewReader() to read the complete file
	time.Sleep(2 * time.Second)

	// Copy task data while holding the lock, then release it before upload
	e.mu.Lock()
	if e.tasks[chatID] == nil {
		e.mu.Unlock()
		return
	}
	taskCopy := *e.tasks[chatID]
	taskCopy.Files = make([]FileEntry, len(e.tasks[chatID].Files))
	copy(taskCopy.Files, e.tasks[chatID].Files)
	torrentRef := e.tasks[chatID].Torrent
	e.tasks[chatID].Done = true
	e.tasks[chatID].Progress = 100
	e.mu.Unlock()

	bot.sendMsg(chatID, fmt.Sprintf("✅ *Download complete:* `%s`\n⏳ *Uploading to Telegram…*", name), 0, true)

	e.uploadFiles(bot, chatID, torrentRef, &taskCopy)
}

// saveAndUploadFile reads from torrent reader in chunks, saves to disk in chunks,
// then streams upload via io.Pipe — never loads more than 64KB into memory
func (e *Engine) saveAndUploadFile(bot *Bot, chatID int64, torrentFile *torrent.File, fe FileEntry, safeName string, caption string) (bool, error) {
	fullPath := filepath.Join(e.storage, fe.DisplayPath)

	dir := filepath.Dir(fullPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return false, fmt.Errorf("mkdir: %w", err)
	}

	// Create output file
	outFile, err := os.Create(fullPath)
	if err != nil {
		return false, fmt.Errorf("create file: %w", err)
	}

	// Get reader from torrent file — this reads data from the torrent's storage
	reader := torrentFile.NewReader()
	if reader == nil {
		outFile.Close()
		os.Remove(fullPath)
		return false, fmt.Errorf("NewReader returned nil")
	}
	defer reader.Close()

	// Stream from torrent reader to disk in 64KB chunks
	// This avoids loading the entire file into memory
	buf := make([]byte, 65536)
	totalWritten := int64(0)
	readErrors := 0

	for {
		n, readErr := reader.Read(buf)
		if n > 0 {
			nw, writeErr := outFile.Write(buf[:n])
			if writeErr != nil {
				outFile.Close()
				os.Remove(fullPath)
				return false, fmt.Errorf("write to disk: %w", writeErr)
			}
			totalWritten += int64(nw)
		}
		if readErr != nil {
			if readErr != io.EOF {
				readErrors++
				// Allow up to 3 read errors before failing (handles transient issues)
				if readErrors >= 3 {
					outFile.Close()
					os.Remove(fullPath)
					return false, fmt.Errorf("torrent read error (x%d): %w", readErrors, readErr)
				}
				log.Printf("[%d] torrent read error (attempt %d): %v", chatID, readErrors, readErr)
				time.Sleep(100 * time.Millisecond)
				continue
			}
			break
		}
	}

	// Ensure all data is written to disk
	if err := outFile.Close(); err != nil {
		os.Remove(fullPath)
		return false, fmt.Errorf("close output: %w", err)
	}

	// Verify file was written correctly
	verifyFile, err := os.Open(fullPath)
	if err != nil {
		return false, fmt.Errorf("verify file: %w", err)
	}
	verifyStat, _ := verifyFile.Stat()
	verifyFile.Close()

	if verifyStat.Size() != fe.Length {
		log.Printf("[%d] ⚠️ file size mismatch: wrote %d bytes, expected %d bytes for %s",
			chatID, verifyStat.Size(), fe.Length, safeName)
	}

	log.Printf("[%d] 💾 saved to disk: %s (%s)", chatID, fullPath, formatBytes(totalWritten))

	// Stream upload from disk to Telegram via io.Pipe — minimal memory
	uploadErr := bot.uploadFile(chatID, fullPath, caption)
	if uploadErr != nil {
		log.Printf("[%d] upload failed: %v", chatID, uploadErr)
		return false, uploadErr
	}

	// Delete from disk AFTER successful upload
	if err := os.Remove(fullPath); err != nil {
		log.Printf("[%d] warning: could not delete after upload: %v", chatID, err)
	} else {
		log.Printf("[%d] 🗑 deleted after upload: %s", chatID, fullPath)
	}

	return true, nil
}

func (e *Engine) uploadFiles(bot *Bot, chatID int64, torrentRef *torrent.Torrent, task *Task) {
	files := task.Files
	torrentName := task.Name

	// Remove task from map BEFORE starting upload — allows new downloads to start
	e.mu.Lock()
	delete(e.tasks, chatID)
	e.mu.Unlock()

	ok, fail := 0, 0
	totalFiles := len(files)

	// Use configured channel ID if set, otherwise use the user's chat ID
	targetChat := bot.getChatID(chatID)
	bot.sendMsg(targetChat,
		fmt.Sprintf("📤 *Starting upload:* %d file(s)", totalFiles), 0, true)

	for i, fe := range files {
		safeName := filepath.Base(fe.DisplayPath)

		// Telegram Bot API limit is 2000 MB per file
		maxSize := int64(2000) * 1024 * 1024
		if fe.Length > maxSize {
			log.Printf("[%d] file too large (%s > 2GB): %s", chatID, formatBytes(fe.Length), safeName)
			bot.sendMsg(targetChat,
				fmt.Sprintf("⚠️ *File too large:* `%s` (%s)\nTelegram limit is 2GB per file.",
					safeName, formatBytes(fe.Length)), 0, true)
			fail++
			continue
		}

		caption := fmt.Sprintf("[%d/%d] %s — %s", i+1, totalFiles, torrentName, safeName)

		log.Printf("[%d] 📤 uploading [%d/%d]: %s (%s)",
			chatID, i+1, totalFiles, safeName, formatBytes(fe.Length))

		bot.sendAction(targetChat)

		progMsg, _ := bot.sendMsg(targetChat,
			fmt.Sprintf("📤 *Uploading:* `%s` (%d/%d)\n📊 %s",
				safeName, i+1, totalFiles, formatBytes(fe.Length)),
			0, true)

		// Find the corresponding file in the torrent
		var torrentFile *torrent.File
		if torrentRef != nil {
			for _, f := range torrentRef.Files() {
				if f.DisplayPath() == fe.DisplayPath {
					torrentFile = f
					break
				}
			}
		}

		if torrentFile == nil {
			log.Printf("[%d] file not found in torrent: %s", chatID, safeName)
			bot.sendMsg(targetChat,
				fmt.Sprintf("❌ *File not found in torrent:* `%s`", safeName), 0, true)
			fail++
		} else {
			success, uploadErr := e.saveAndUploadFile(bot, chatID, torrentFile, fe, safeName, caption)
			if success {
				ok++
				bot.sendMsg(targetChat,
					fmt.Sprintf("✅ *Uploaded:* `%s` (%d/%d)", safeName, i+1, totalFiles), 0, true)
			} else {
				bot.sendMsg(targetChat,
					fmt.Sprintf("❌ *Upload failed:* `%s` — %v", safeName, uploadErr), 0, true)
				fail++
			}
		}

		if progMsg > 0 {
			bot.editMsg(targetChat, progMsg,
				fmt.Sprintf("✅ *Done:* `%s`", safeName))
		}

		// Small delay between files to avoid overwhelming the Telegram API
		time.Sleep(500 * time.Millisecond)
	}

	if fail > 0 {
		bot.sendMsg(targetChat,
			fmt.Sprintf("🎉 *Done!* %d file(s) uploaded, %d failed.", ok, fail), 0, true)
	} else {
		bot.sendMsg(targetChat,
			fmt.Sprintf("🎉 *All done!* %d file(s) uploaded successfully.", ok), 0, true)
	}
}

func (e *Engine) fail(chatID int64, msg string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if t, ok := e.tasks[chatID]; ok {
		t.Error = msg
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Helpers
// ─────────────────────────────────────────────────────────────────────────────

func isMagnet(s string) bool {
	return strings.HasPrefix(strings.TrimSpace(s), "magnet:?")
}

func isTorrentURL(s string) bool {
	s = strings.TrimSpace(s)
	return (strings.HasPrefix(s, "http://") || strings.HasPrefix(s, "https://")) &&
		strings.HasSuffix(s, ".torrent")
}

func formatBytes(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1f GB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KB", float64(n)/(1<<10))
	default:
		return strconv.FormatInt(n, 10) + " B"
	}
}

const helpText = `🤖 *TeleTorrent Bot*

Send me a magnet link or .torrent URL and I'll download & upload it here.

*Commands:*
/start /help — this message
/status      — active download status
/cancel      — cancel current download

*Supported:*
• Magnet links (magnet:?xt=…)
• .torrent URL (https://…/file.torrent)

*Notes:*
• Files are uploaded directly to the configured channel.
• Files are deleted from disk after successful upload.
• Bot never crashes — all errors are handled.`

// ─────────────────────────────────────────────────────────────────────────────
// Main
// ─────────────────────────────────────────────────────────────────────────────

func main() {
	var token, storage, channelStr string
	flag.StringVar(&token, "token", "", "Telegram bot token from @BotFather")
	flag.StringVar(&storage, "storage", "./downloads", "Directory for downloads")
	flag.StringVar(&channelStr, "channel", "", "Telegram channel/group ID to send files to (e.g. -1003213143951)")
	flag.Parse()

	if token == "" {
		fmt.Println("❌  Usage: go run main.go -token YOUR_BOT_TOKEN [-channel CHANNEL_ID]")
		fmt.Println("   Get your token from https://t.me/BotFather")
		os.Exit(1)
	}

	var channelID int64
	if channelStr != "" {
		var err error
		channelID, err = strconv.ParseInt(channelStr, 10, 64)
		if err != nil {
			log.Fatalf("❌  Invalid channel ID: %s", channelStr)
		}
		log.Printf("📢 Target channel: %s", channelStr)
	}

	if err := os.MkdirAll(storage, 0755); err != nil {
		log.Fatalf("❌  Cannot create storage directory: %v", err)
	}
	log.Printf("📁 Storage directory: %s", storage)

	// Configure the torrent client
	cfg := torrent.NewDefaultClientConfig()
	cfg.DataDir = storage
	cfg.NoUpload = false      // Allow seeding
	cfg.Seed = true           // Continue seeding after download
	cfg.SetListenAddr("0.0.0.0:0")
	cfg.DisableIPv6 = true    // IPv6 can sometimes cause issues
	cfg.DisableIPv4 = false
	cfg.NoDHT = false         // Need DHT for magnet links
	cfg.DisableTCP = false

	tc, err := torrent.NewClient(cfg)
	if err != nil {
		log.Fatalf("❌  Torrent client error: %v", err)
	}
	log.Printf("✅  Torrent client ready. Storage: %s", storage)

	// Initialize bot
	bot := NewBot(token, channelID)

	// Verify bot authentication
	data, err := bot.api("getMe", nil)
	if err != nil {
		log.Fatalf("❌  Telegram auth error: %v", err)
	}
	var me struct {
		OK bool `json:"ok"`
		Result struct {
			Username string `json:"username"`
		} `json:"result"`
	}
	json.Unmarshal(data, &me)
	log.Printf("✅  Bot logged in as @%s", me.Result.Username)

	engine := NewEngine(tc, storage)

	// Handle shutdown signals gracefully
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigChan
		log.Println("⚡ Shutting down...")
		tc.Close()
		os.Exit(0)
	}()

	// Main polling loop — never exits on errors, only on signals
	offset := 0
	for {
		data, err := bot.api("getUpdates", map[string]string{
			"timeout": "120",
			"offset":  strconv.Itoa(offset),
		})
		if err != nil {
			log.Printf("⚠️  getUpdates error: %v — retrying in 5s", err)
			time.Sleep(5 * time.Second)
			continue
		}

		var ups struct {
			OK     bool      `json:"ok"`
			Result []Update `json:"result"`
		}
		if err := json.Unmarshal(data, &ups); err != nil {
			log.Printf("⚠️  JSON parse error: %v — retrying in 5s", err)
			time.Sleep(5 * time.Second)
			continue
		}

		if !ups.OK {
			log.Printf("⚠️  API returned !ok — retrying in 5s")
			time.Sleep(5 * time.Second)
			continue
		}

		if len(ups.Result) == 0 {
			continue
		}

		for _, u := range ups.Result {
			offset = u.UpdateID + 1

			if u.Message != nil {
				chatID := u.Message.Chat.ID
				text := u.Message.Text
				if text == "" {
					text = u.Message.Caption
				}
				if text == "" {
					continue
				}
				replyTo := u.Message.MessageID
				go engine.Handle(bot, chatID, replyTo, text)
			} else if u.CallbackQuery != nil {
				chatID := u.CallbackQuery.Message.Chat.ID
				text := u.CallbackQuery.Data
				replyTo := u.CallbackQuery.Message.MessageID
				bot.answerCallback(u.CallbackQuery.ID)
				go engine.Handle(bot, chatID, replyTo, text)
			}
		}
	}
}
