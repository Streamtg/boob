package main

import (
	"bytes"
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
	token   string
	baseURL string
	client  *http.Client
}

func NewBot(token string) *Bot {
	return &Bot{
		token:   token,
		baseURL: "https://api.telegram.org/bot" + token,
		client: &http.Client{
			Timeout: 0,
			Transport: &http.Transport{
				MaxIdleConns:        5,
				MaxIdleConnsPerHost: 5,
				IdleConnTimeout:     90 * time.Second,
			},
		},
	}
}

// uploadClient returns a fresh HTTP client for file uploads (not shared with polling)
func uploadClient() *http.Client {
	return &http.Client{
		Timeout: 10 * time.Minute,
		Transport: &http.Transport{
			MaxIdleConns:        10,
			MaxIdleConnsPerHost: 10,
			IdleConnTimeout:     60 * time.Second,
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

func (b *Bot) sendAction(chatID int64, action string) {
	body, _ := json.Marshal(map[string]interface{}{"chat_id": chatID, "action": action})
	b.post("sendChatAction", "application/json", strings.NewReader(string(body)))
}

// uploadFile uploads a local file to Telegram
func (b *Bot) uploadFile(chatID int64, filePath string, caption string) error {
	file, err := os.Open(filePath)
	if err != nil {
		return fmt.Errorf("open file: %w", err)
	}
	defer file.Close()

	fi, err := file.Stat()
	if err != nil {
		return fmt.Errorf("stat file: %w", err)
	}
	if fi.Size() == 0 {
		return fmt.Errorf("file is empty")
	}

	filename := filepath.Base(filePath)
	client := uploadClient()
	defer client.CloseIdleConnections()

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)

	writer.WriteField("chat_id", strconv.FormatInt(chatID, 10))
	if caption != "" {
		writer.WriteField("caption", caption)
	}

	part, err := writer.CreateFormFile("document", filename)
	if err != nil {
		return fmt.Errorf("create form file: %w", err)
	}
	if _, err := io.Copy(part, file); err != nil {
		return fmt.Errorf("copy data: %w", err)
	}

	if err := writer.Close(); err != nil {
		return fmt.Errorf("close writer: %w", err)
	}

	req, err := http.NewRequest("POST", b.baseURL+"/sendDocument", &body)
	if err != nil {
		return fmt.Errorf("new request: %w", err)
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())

	log.Printf("[%d] 📤 Uploading: %s (%s)", chatID, filename, formatBytes(fi.Size()))

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("do request: %w", err)
	}
	defer resp.Body.Close()

	respData, _ := io.ReadAll(resp.Body)

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
	Torrent    *torrent.Torrent // keep reference to torrent for file reading
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

	bot.sendAction(chatID, "typing")
	data, fe := fetchURL(input)
	if fe != nil {
		e.mu.Lock()
		delete(e.tasks, chatID)
		e.mu.Unlock()
		bot.sendMsg(chatID, fmt.Sprintf("❌ *Fetch failed:* `%s`", fe.Error()), 0, true)
		return
	}
	mi, metaErr := metainfo.Load(bytes.NewReader(data))
	if metaErr != nil {
		e.mu.Lock()
		delete(e.tasks, chatID)
		e.mu.Unlock()
		bot.sendMsg(chatID, fmt.Sprintf("❌ *Parse error:* `%s`", metaErr.Error()), 0, true)
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

func (e *Engine) downloadLoop(bot *Bot, chatID int64, replyTo int, t *torrent.Torrent) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("[%d] panic recovered in downloadLoop: %v", chatID, r)
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
			e.mu.Unlock()

			var speed string
			e.mu.Lock()
			startTime := e.tasks[chatID].StartedAt
			e.mu.Unlock()
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

	// Wait a moment to ensure all pieces are fully flushed to disk
	log.Printf("[%d] download complete, waiting for pieces to flush...", chatID)
	time.Sleep(3 * time.Second)

	// Copy task data before modifying the map
	e.mu.Lock()
	taskCopy := *e.tasks[chatID]
	taskCopy.Files = make([]FileEntry, len(e.tasks[chatID].Files))
	copy(taskCopy.Files, e.tasks[chatID].Files)
	// Keep torrent reference for file reading
	torrentRef := e.tasks[chatID].Torrent
	e.tasks[chatID].Done = true
	e.tasks[chatID].Progress = 100
	e.mu.Unlock()

	bot.sendMsg(chatID, fmt.Sprintf("✅ *Download complete:* `%s`\n⏳ *Uploading to Telegram…*", name), 0, true)

	// Upload files (pass torrent reference for reading if needed)
	e.uploadFiles(bot, chatID, torrentRef, &taskCopy)
}

// saveAndUploadFile reads from torrent reader, saves permanently to disk, uploads, and deletes
func (e *Engine) saveAndUploadFile(bot *Bot, chatID int64, torrentFile *torrent.File, fe FileEntry, safeName string, caption string) (bool, error) {
	fullPath := filepath.Join(e.storage, fe.DisplayPath)

	// Create directory structure
	dir := filepath.Dir(fullPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		log.Printf("[%d] mkdir failed: %v", chatID, err)
		return false, fmt.Errorf("mkdir: %w", err)
	}

	// Read file data from torrent reader
	reader := torrentFile.NewReader()
	data, readErr := io.ReadAll(reader)
	reader.Close()

	if readErr != nil {
		log.Printf("[%d] torrent read error: %v", chatID, readErr)
		return false, fmt.Errorf("torrent read: %w", readErr)
	}

	if len(data) == 0 {
		return false, fmt.Errorf("empty data from torrent reader")
	}

	log.Printf("[%d] read %d bytes from torrent reader", chatID, len(data))

	// Save file permanently to disk
	if err := os.WriteFile(fullPath, data, 0644); err != nil {
		log.Printf("[%d] save to disk failed: %v", chatID, err)
		return false, fmt.Errorf("save to disk: %w", err)
	}
	log.Printf("[%d] 💾 saved permanently: %s", chatID, fullPath)

	// Upload to Telegram
	uploadErr := bot.uploadFile(chatID, fullPath, caption)
	if uploadErr != nil {
		log.Printf("[%d] upload failed: %v", chatID, uploadErr)
		// Don't delete — user can retry
		return false, uploadErr
	}

	// Delete from disk AFTER successful upload
	if err := os.Remove(fullPath); err != nil {
		log.Printf("[%d] delete after upload failed: %v", chatID, err)
	} else {
		log.Printf("[%d] 🗑 deleted after upload: %s", chatID, fullPath)
	}

	return true, nil
}

func (e *Engine) uploadFiles(bot *Bot, chatID int64, torrentRef *torrent.Torrent, task *Task) {
	files := task.Files
	torrentName := task.Name

	e.mu.Lock()
	delete(e.tasks, chatID)
	e.mu.Unlock()

	ok, fail := 0, 0
	totalFiles := len(files)

	bot.sendMsg(chatID,
		fmt.Sprintf("📤 *Starting upload:* %d file(s)", totalFiles), 0, true)

	for i, fe := range files {
		safeName := filepath.Base(fe.DisplayPath)

		maxSize := int64(2000) * 1024 * 1024
		if fe.Length > maxSize {
			log.Printf("[%d] file too large (%s > 2GB): %s", chatID, formatBytes(fe.Length), safeName)
			bot.sendMsg(chatID,
				fmt.Sprintf("⚠️ *File too large:* `%s` (%s)\nTelegram limit is 2GB per file.",
					safeName, formatBytes(fe.Length)), 0, true)
			fail++
			continue
		}

		caption := fmt.Sprintf("[%d/%d] %s — %s", i+1, totalFiles, torrentName, safeName)

		log.Printf("[%d] 📤 uploading [%d/%d]: %s (%s)",
			chatID, i+1, totalFiles, safeName, formatBytes(fe.Length))

		bot.sendAction(chatID, "upload_document")

		progMsg, _ := bot.sendMsg(chatID,
			fmt.Sprintf("📤 *Uploading:* `%s` (%d/%d)\n📊 %s",
				safeName, i+1, totalFiles, formatBytes(fe.Length)),
			0, true)

		// Find the file in the torrent
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
			bot.sendMsg(chatID,
				fmt.Sprintf("❌ *File not found in torrent:* `%s`", safeName), 0, true)
			fail++
		} else {
			success, uploadErr := e.saveAndUploadFile(bot, chatID, torrentFile, fe, safeName, caption)
			if success {
				ok++
				bot.sendMsg(chatID,
					fmt.Sprintf("✅ *Uploaded:* `%s` (%d/%d)", safeName, i+1, totalFiles), 0, true)
			} else {
				bot.sendMsg(chatID,
					fmt.Sprintf("❌ *Upload failed:* `%s` — %v", safeName, uploadErr), 0, true)
				fail++
			}
		}

		if progMsg > 0 {
			bot.editMsg(chatID, progMsg,
				fmt.Sprintf("✅ *Done:* `%s`", safeName))
		}

		time.Sleep(500 * time.Millisecond)
	}

	if fail > 0 {
		bot.sendMsg(chatID,
			fmt.Sprintf("🎉 *Done!* %d file(s) uploaded, %d failed.", ok, fail), 0, true)
	} else {
		bot.sendMsg(chatID,
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

func fetchURL(raw string) ([]byte, error) {
	client := &http.Client{Timeout: 60 * time.Second}
	resp, err := client.Get(raw)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return io.ReadAll(resp.Body)
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
• Files are uploaded directly to Telegram.
• Seeding continues after upload.
• No root needed.`

// ─────────────────────────────────────────────────────────────────────────────
// Main
// ─────────────────────────────────────────────────────────────────────────────

func main() {
	var token, storage string
	flag.StringVar(&token, "token", "", "Telegram bot token from @BotFather")
	flag.StringVar(&storage, "storage", "./downloads", "Directory for downloads")
	flag.Parse()

	if token == "" {
		fmt.Println("❌  Usage: go run main.go -token YOUR_BOT_TOKEN")
		fmt.Println("   Get your token from https://t.me/BotFather")
		os.Exit(1)
	}

	// Create download folder
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
	log.Printf("✅  Torrent client ready. Storage: %s", storage)

	bot := NewBot(token)
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

	// Main polling loop — NEVER exits while bot is running
	offset := 0
	for {
		data, err := bot.api("getUpdates", map[string]string{
			"timeout": "120",
			"offset":  strconv.Itoa(offset),
		})
		if err != nil {
			log.Printf("⚠️  getUpdates error: %v", err)
			time.Sleep(3 * time.Second)
			continue
		}

		var ups struct {
			OK     bool      `json:"ok"`
			Result []Update `json:"result"`
		}
		json.Unmarshal(data, &ups)

		if !ups.OK || len(ups.Result) == 0 {
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
