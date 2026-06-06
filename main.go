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
	channelID int64
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

// postWithRetry sends a POST request with retry on 429 rate limit
func (b *Bot) postWithRetry(path string, mime string, body io.Reader, maxRetries int) ([]byte, error) {
	for attempt := 0; attempt <= maxRetries; attempt++ {
		data, err := b.post(path, mime, body)
		if err != nil {
			return nil, err
		}

		// Check for rate limit (429)
		var errResult struct {
			OK          bool `json:"ok"`
			ErrorCode   int  `json:"error_code"`
			RetryAfter  int  `json:"retry_after"`
			Description string `json:"description"`
		}
		json.Unmarshal(data, &errResult)

		if errResult.ErrorCode == 429 {
			wait := 5
			if errResult.RetryAfter > 0 {
				wait = errResult.RetryAfter + 1
			}
			if attempt < maxRetries {
				log.Printf("⚠️  Rate limited, retrying in %ds (attempt %d/%d)", wait, attempt+1, maxRetries)
				time.Sleep(time.Duration(wait) * time.Second)
				continue
			}
			return data, fmt.Errorf("rate limited after %d retries, retry_after=%d", maxRetries, errResult.RetryAfter)
		}

		return data, nil
	}
	return nil, fmt.Errorf("exceeded max retries")
}

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
	data, err := b.postWithRetry("sendMessage", "application/json", strings.NewReader(string(body)), 3)
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
	b.postWithRetry("editMessageText", "application/json", strings.NewReader(string(body)), 2)
}

func (b *Bot) answerCallback(queryID string) {
	body, _ := json.Marshal(map[string]interface{}{"callback_query_id": queryID})
	b.post("answerCallbackQuery", "application/json", strings.NewReader(string(body)))
}

func (b *Bot) sendAction(chatID int64) {
	body, _ := json.Marshal(map[string]interface{}{"chat_id": chatID, "action": "upload_document"})
	b.post("sendChatAction", "application/json", strings.NewReader(string(body)))
}

// uploadFile uploads a local file to Telegram using streaming (io.Pipe)
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

	// Use io.Pipe for streaming — no memory buffer accumulation
	pr, pw := io.Pipe()
	writer := multipart.NewWriter(pw)
	errChan := make(chan error, 1)

	// Writer goroutine — streams file data through the pipe
	go func() {
		defer pw.Close()

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

		if err := writer.Close(); err != nil {
			errChan <- fmt.Errorf("close writer: %w", err)
			return
		}
		errChan <- nil
	}()

	// Execute HTTP request
	client := &http.Client{
		Timeout: 15 * time.Minute,
		Transport: &http.Transport{
			MaxIdleConns:        20,
			MaxIdleConnsPerHost: 20,
			IdleConnTimeout:     120 * time.Second,
		},
	}
	defer client.CloseIdleConnections()

	req, err := http.NewRequest("POST", b.baseURL+"/sendDocument", pr)
	if err != nil {
		return fmt.Errorf("new request: %w", err)
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())

	log.Printf("[%d] 📤 Uploading: %s (%s)", chatID, filename, formatBytes(stat.Size()))

	resp, err := client.Do(req)
	if err != nil {
		werr := <-errChan
		if werr != nil {
			log.Printf("[%d] writer error: %v", chatID, werr)
		}
		return fmt.Errorf("do request: %w", err)
	}
	defer resp.Body.Close()

	respData, _ := io.ReadAll(resp.Body)

	// Check writer goroutine result
	select {
	case werr := <-errChan:
		if werr != nil {
			log.Printf("[%d] writer error: %v", chatID, werr)
		}
	default:
	}

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

// validTrackerScheme returns true for tracker schemes the library supports
func validTrackerScheme(rawURL string) bool {
	u, err := url.Parse(rawURL)
	if err != nil {
		return false
	}
	scheme := strings.ToLower(u.Scheme)
	return scheme == "udp" || scheme == "http" || scheme == "https"
}

// cleanMagnetURL removes trackers with invalid schemes (like "DHT") that crash the library
func cleanMagnetURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if !strings.HasPrefix(raw, "magnet:?") {
		return raw
	}

	u, err := url.Parse(raw)
	if err != nil {
		return raw
	}

	rawTrackers := u.Query()["tr"]
	if len(rawTrackers) == 0 {
		return raw
	}

	var validTrackers []string
	for _, tr := range rawTrackers {
		if validTrackerScheme(tr) {
			validTrackers = append(validTrackers, tr)
		} else {
			log.Printf("⚠️  Removed invalid tracker: %s", tr)
		}
	}

	if len(validTrackers) == 0 {
		log.Printf("⚠️  All trackers filtered out from magnet")
	}

	q := url.Values{}
	for k, vals := range u.Query() {
		if k == "tr" {
			for _, v := range validTrackers {
				q.Add(k, v)
			}
		} else {
			for _, v := range vals {
				q.Add(k, v)
			}
		}
	}

	rebuilt := u.Scheme + "://" + u.Host + u.Path
	if len(q) > 0 {
		rebuilt += "?" + q.Encode()
	}

	return rebuilt
}

func (e *Engine) startDownloadMagnet(bot *Bot, chatID int64, replyTo int, rawMagnet string) {
	e.mu.Lock()
	if _, active := e.tasks[chatID]; active {
		e.mu.Unlock()
		bot.sendMsg(chatID, "⏳ Download already in progress. Use /cancel first.", replyTo, true)
		return
	}
	e.tasks[chatID] = &Task{StartedAt: time.Now()}
	e.mu.Unlock()

	cleaned := cleanMagnetURL(rawMagnet)
	log.Printf("[%d] cleaned magnet: %s", chatID, cleaned)

	var t *torrent.Torrent
	func() {
		defer func() {
			if r := recover(); r != nil {
				log.Printf("[%d] PANIC in AddMagnet (recovered): %v", chatID, r)
			}
		}()
		var err error
		t, err = e.tc.AddMagnet(cleaned)
		if err != nil {
			e.mu.Lock()
			delete(e.tasks, chatID)
			e.mu.Unlock()
			bot.sendMsg(chatID, fmt.Sprintf("❌ *Magnet error:* `%s`", err.Error()), 0, true)
			return
		}
	}()

	if t == nil {
		e.mu.Lock()
		delete(e.tasks, chatID)
		e.mu.Unlock()
		bot.sendMsg(chatID, "❌ *Magnet failed silently (library panic recovered)*", 0, true)
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

	// Limit to 10MB max — safety limit
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
			e.mu.Lock()
			delete(e.tasks, chatID)
			e.mu.Unlock()
		}
	}()

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
	if e.tasks[chatID] == nil {
		e.mu.Unlock()
		return
	}
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

	log.Printf("[%d] download complete, waiting for pieces to flush...", chatID)
	time.Sleep(3 * time.Second)

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

// saveFileFromTorrent reads from the torrent and saves to disk with exact byte count
// Uses torrent reader but carefully respects the declared file length
func (e *Engine) saveFileFromTorrent(torrentFile *torrent.File, expectedLen int64, outputPath string) (int64, error) {
	dir := filepath.Dir(outputPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return 0, fmt.Errorf("mkdir: %w", err)
	}

	outFile, err := os.Create(outputPath)
	if err != nil {
		return 0, fmt.Errorf("create file: %w", err)
	}

	reader := torrentFile.NewReader()
	if reader == nil {
		outFile.Close()
		return 0, fmt.Errorf("NewReader returned nil")
	}
	defer reader.Close()

	buf := make([]byte, 65536)
	var totalWritten int64

	// Read EXACTLY the expected number of bytes (or until EOF)
	for totalWritten < expectedLen {
		remaining := expectedLen - totalWritten
		if remaining < int64(len(buf)) {
			buf = make([]byte, remaining)
		}

		n, readErr := reader.Read(buf)
		if n > 0 {
			nw, writeErr := outFile.Sync() // Force write to disk
			// Actually write the data
			_, writeErr = outFile.Write(buf[:n])
			if writeErr != nil {
				outFile.Close()
				os.Remove(outputPath)
				return totalWritten, fmt.Errorf("write: %w", writeErr)
			}
			totalWritten += int64(n)
		}
		if readErr != nil {
			if readErr == io.EOF {
				// Reached EOF before expected length
				log.Printf("⚠️  torrent reader returned EOF at %d/%d bytes", totalWritten, expectedLen)
				break
			}
			outFile.Close()
			os.Remove(outputPath)
			return totalWritten, fmt.Errorf("read: %w", readErr)
		}
	}

	outFile.Close()

	// If we wrote more than expected, truncate the file
	if totalWritten > expectedLen {
		f, err := os.OpenFile(outputPath, os.O_RDWR, 0644)
		if err == nil {
			f.Truncate(expectedLen)
			f.Close()
		}
		totalWritten = expectedLen
	}

	return totalWritten, nil
}

// uploadFromDisk reads the file directly from disk and streams to Telegram
// This is more reliable than using the torrent reader for the upload phase
func (b *Bot) uploadFromDisk(chatID int64, filePath string, caption string) error {
	file, err := os.Open(filePath)
	if err != nil {
		return fmt.Errorf("open: %w", err)
	}
	defer file.Close()

	stat, err := file.Stat()
	if err != nil {
		return fmt.Errorf("stat: %w", err)
	}
	if stat.Size() == 0 {
		return fmt.Errorf("file empty")
	}

	filename := filepath.Base(filePath)

	pr, pw := io.Pipe()
	writer := multipart.NewWriter(pw)
	errChan := make(chan error, 1)

	go func() {
		defer pw.Close()
		defer writer.Close()

		writer.WriteField("chat_id", strconv.FormatInt(chatID, 10))
		if caption != "" {
			writer.WriteField("caption", caption)
		}

		part, err := writer.CreateFormFile("document", filename)
		if err != nil {
			errChan <- err
			return
		}

		// Stream in chunks from the FILE (not torrent reader)
		buf := make([]byte, 65536)
		for {
			n, err := file.Read(buf)
			if n > 0 {
				if _, werr := part.Write(buf[:n]); werr != nil {
					errChan <- werr
					return
				}
			}
			if err != nil {
				if err != io.EOF {
					errChan <- err
				}
				break
			}
		}
		errChan <- nil
	}()

	client := &http.Client{
		Timeout: 15 * time.Minute,
		Transport: &http.Transport{
			MaxIdleConns:        20,
			MaxIdleConnsPerHost: 20,
			IdleConnTimeout:     120 * time.Second,
		},
	}
	defer client.CloseIdleConnections()

	req, err := http.NewRequest("POST", b.baseURL+"/sendDocument", pr)
	if err != nil {
		return fmt.Errorf("new request: %w", err)
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())

	resp, err := client.Do(req)
	if err != nil {
		<-errChan
		return fmt.Errorf("do request: %w", err)
	}
	defer resp.Body.Close()

	respData, _ := io.ReadAll(resp.Body)

	// Wait for writer goroutine
	werr := <-errChan
	if werr != nil {
		log.Printf("[%d] writer goroutine error: %v", chatID, werr)
	}

	var result struct {
		OK          bool   `json:"ok"`
		Description string `json:"description"`
	}
	json.Unmarshal(respData, &result)

	if !result.OK {
		return fmt.Errorf("upload error: %s", result.Description)
	}

	return nil
}

func (e *Engine) uploadFiles(bot *Bot, chatID int64, torrentRef *torrent.Torrent, task *Task) {
	files := task.Files
	torrentName := task.Name

	e.mu.Lock()
	delete(e.tasks, chatID)
	e.mu.Unlock()

	ok, fail := 0, 0
	totalFiles := len(files)

	targetChat := bot.getChatID(chatID)
	bot.sendMsg(targetChat, fmt.Sprintf("📤 *Starting upload:* %d file(s)", totalFiles), 0, true)

	for i, fe := range files {
		safeName := filepath.Base(fe.DisplayPath)

		// Telegram limit is 2000 MB
		maxSize := int64(2000) * 1024 * 1024
		if fe.Length > maxSize {
			bot.sendMsg(targetChat,
				fmt.Sprintf("⚠️ *File too large:* `%s` (%s)\nTelegram limit is 2GB per file.",
					safeName, formatBytes(fe.Length)), 0, true)
			fail++
			continue
		}

		caption := fmt.Sprintf("[%d/%d] %s — %s", i+1, totalFiles, torrentName, safeName)

		log.Printf("[%d] 📤 saving then uploading [%d/%d]: %s (%s)",
			chatID, i+1, totalFiles, safeName, formatBytes(fe.Length))

		bot.sendAction(targetChat)

		progMsg, _ := bot.sendMsg(targetChat,
			fmt.Sprintf("📤 *Saving:* `%s` (%d/%d)\n📊 %s",
				safeName, i+1, totalFiles, formatBytes(fe.Length)),
			0, true)

		// Find the torrent file reference
		var tf *torrent.File
		if torrentRef != nil {
			for _, f := range torrentRef.Files() {
				if f.DisplayPath() == fe.DisplayPath {
					tf = f
					break
				}
			}
		}

		if tf == nil {
			log.Printf("[%d] file not found in torrent: %s", chatID, safeName)
			bot.sendMsg(targetChat, fmt.Sprintf("❌ *File not found in torrent:* `%s`", safeName), 0, true)
			fail++
			continue
		}

		// Save file from torrent to disk with EXACT byte count
		diskPath := filepath.Join(e.storage, fe.DisplayPath)

		written, saveErr := e.saveFileFromTorrent(tf, fe.Length, diskPath)
		if saveErr != nil {
			log.Printf("[%d] save error: %v", chatID, saveErr)
			bot.sendMsg(targetChat, fmt.Sprintf("❌ *Save failed:* `%s` — %v", safeName, saveErr), 0, true)
			fail++
			continue
		}

		log.Printf("[%d] 💾 saved to disk: %s (%s)", chatID, diskPath, formatBytes(written))

		// Now upload the file from disk
		bot.sendAction(targetChat)

		uploadMsg, _ := bot.sendMsg(targetChat,
			fmt.Sprintf("📤 *Uploading:* `%s` (%d/%d)\n📊 %s",
				safeName, i+1, totalFiles, formatBytes(written)),
			0, true)

		uploadErr := bot.uploadFromDisk(targetChat, diskPath, caption)

		if uploadErr != nil {
			log.Printf("[%d] upload failed: %v", chatID, uploadErr)
			bot.sendMsg(targetChat, fmt.Sprintf("❌ *Upload failed:* `%s` — %v", safeName, uploadErr), 0, true)
			fail++
		} else {
			log.Printf("[%d] ✅ Uploaded: %s", chatID, safeName)
			bot.sendMsg(targetChat, fmt.Sprintf("✅ *Uploaded:* `%s` (%d/%d)", safeName, i+1, totalFiles), 0, true)
			ok++
		}

		// Delete from disk AFTER successful upload (or after failed upload attempt)
		if err := os.Remove(diskPath); err != nil {
			log.Printf("[%d] warning: could not delete %s: %v", chatID, diskPath, err)
		} else {
			log.Printf("[%d] 🗑 deleted: %s", chatID, diskPath)
		}

		// Update progress message
		if uploadMsg > 0 {
			status := "✅ Done"
			if uploadErr != nil {
				status = "❌ Failed"
			}
			bot.editMsg(targetChat, uploadMsg, fmt.Sprintf("%s: `%s`", status, safeName))
		}

		// Small delay between files
		time.Sleep(500 * time.Millisecond)
	}

	if fail > 0 {
		bot.sendMsg(targetChat, fmt.Sprintf("🎉 *Done!* %d uploaded, %d failed.", ok, fail), 0, true)
	} else {
		bot.sendMsg(targetChat, fmt.Sprintf("🎉 *All done!* %d file(s) uploaded.", ok), 0, true)
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
	flag.StringVar(&channelStr, "channel", "", "Telegram channel/group ID")
	flag.Parse()

	if token == "" {
		fmt.Println("❌  Usage: go run main.go -token YOUR_BOT_TOKEN [-channel CHANNEL_ID]")
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

	cfg := torrent.NewDefaultClientConfig()
	cfg.DataDir = storage
	cfg.NoUpload = false
	cfg.Seed = true
	cfg.SetListenAddr("0.0.0.0:0")
	cfg.DisableIPv6 = true
	cfg.DisableIPv4 = false
	cfg.NoDHT = false
	cfg.DisableTCP = false

	tc, err := torrent.NewClient(cfg)
	if err != nil {
		log.Fatalf("❌  Torrent client error: %v", err)
	}
	log.Printf("✅  Torrent client ready")

	bot := NewBot(token, channelID)

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

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigChan
		log.Println("⚡ Shutting down...")
		tc.Close()
		os.Exit(0)
	}()

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
