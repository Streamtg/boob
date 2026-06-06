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
				MaxIdleConns:        100,
				MaxIdleConnsPerHost: 100,
				IdleConnTimeout:     90 * time.Second,
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

// uploadFile sends a file to Telegram using multipart/form-data.
// It properly formats the multipart body and checks the API response.
func (b *Bot) uploadFile(chatID int64, filePath string, replyTo int, caption string) error {
	file, err := os.Open(filePath)
	if err != nil {
		return fmt.Errorf("open file: %w", err)
	}
	defer file.Close()

	data, err := io.ReadAll(file)
	if err != nil {
		return fmt.Errorf("read file: %w", err)
	}

	filename := filepath.Base(filePath)

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)

	// Add chat_id field
	writer.WriteField("chat_id", strconv.FormatInt(chatID, 10))

	// Add reply_to_message_id if provided
	if replyTo > 0 {
		writer.WriteField("reply_to_message_id", strconv.Itoa(replyTo))
	}

	// Add caption
	if caption != "" {
		writer.WriteField("caption", caption)
	}

	// Add document field
	part, err := writer.CreateFormFile("document", filename)
	if err != nil {
		return fmt.Errorf("create form file: %w", err)
	}
	if _, err := part.Write(data); err != nil {
		return fmt.Errorf("write data: %w", err)
	}

	if err := writer.Close(); err != nil {
		return fmt.Errorf("close writer: %w", err)
	}

	req, err := http.NewRequest("POST", b.baseURL+"/sendDocument", &body)
	if err != nil {
		return fmt.Errorf("new request: %w", err)
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())

	log.Printf("[%d] Uploading file: %s (%s)", chatID, filename, formatBytes(int64(len(data))))

	resp, err := b.client.Do(req)
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

	log.Printf("[%d] ✅ uploaded: %s", chatID, filename)
	return nil
}

// uploadFileByURL sends a file by URL (Telegram can fetch it)
func (b *Bot) uploadFileByURL(chatID int64, fileURL string, replyTo int, caption string) error {
	body, _ := json.Marshal(map[string]interface{}{
		"chat_id":             chatID,
		"document":            fileURL,
		"reply_to_message_id": replyTo,
		"caption":             caption,
	})
	data, err := b.post("sendDocument", "application/json", strings.NewReader(string(body)))
	if err != nil {
		return fmt.Errorf("sendDocument: %w", err)
	}
	var r struct {
		OK bool `json:"ok"`
	}
	json.Unmarshal(data, &r)
	if !r.OK {
		return fmt.Errorf("API error: %s", string(data))
	}
	return nil
}

// ─────────────────────────────────────────────────────────────────────────────
// Download engine
// ─────────────────────────────────────────────────────────────────────────────

type Task struct {
	mu         sync.RWMutex
	Name       string
	Progress   float64
	Done       bool
	Files      []string
	TotalBytes int64
	Error      string
	StartedAt  time.Time
}

type Engine struct {
	tc      *torrent.Client
	storage string
	tasks   map[int64]*Task
	mu      sync.RWMutex
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
	if _, ok := e.tasks[chatID]; !ok {
		e.mu.Unlock()
		bot.sendMsg(chatID, "📭 Nothing to cancel.", replyTo, true)
		return
	}
	delete(e.tasks, chatID)
	e.mu.Unlock()
	bot.sendMsg(chatID, "🚫 Download cancelled.", replyTo, true)
}

func (e *Engine) cmdStatus(bot *Bot, chatID int64, replyTo int) {
	e.mu.RLock()
	t, ok := e.tasks[chatID]
	e.mu.RUnlock()
	if !ok {
		bot.sendMsg(chatID, "📭 *No active download.*", replyTo, true)
		return
	}
	t.mu.RLock()
	pct, name, err, started := t.Progress, t.Name, t.Error, t.StartedAt
	t.mu.RUnlock()
	switch {
	case err != "":
		bot.sendMsg(chatID, fmt.Sprintf("❌ *Error:* `%s`", err), replyTo, true)
	case t.Done:
		bot.sendMsg(chatID, fmt.Sprintf("✅ *Completed:* `%s`", name), replyTo, true)
	default:
		elapsed := time.Since(started).Round(time.Second)
		bar := strings.Repeat("█", int(pct)/5) + strings.Repeat("░", 20-int(pct)/5)
		bot.sendMsg(chatID,
			fmt.Sprintf("⏳ *Downloading:* `%s`\n%s `%.1f%%`\n⏱ %s",
				name, bar, pct, elapsed), replyTo, true)
	}
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
		e.fail(chatID, fmt.Sprintf("magnet error: %v", err))
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
		e.fail(chatID, fmt.Sprintf("fetch error: %v", fe))
		bot.sendMsg(chatID, fmt.Sprintf("❌ *Fetch failed:* `%s`", fe.Error()), 0, true)
		return
	}
	mi, metaErr := metainfo.Load(bytes.NewReader(data))
	if metaErr != nil {
		e.fail(chatID, fmt.Sprintf("torrent parse error: %v", metaErr))
		bot.sendMsg(chatID, fmt.Sprintf("❌ *Parse error:* `%s`", metaErr.Error()), 0, true)
		return
	}
	t, err := e.tc.AddTorrent(mi)
	if err != nil {
		e.fail(chatID, fmt.Sprintf("add error: %v", err))
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
			log.Printf("[%d] panic in downloadLoop: %v", chatID, r)
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

	var files []string
	for _, f := range t.Files() {
		files = append(files, f.DisplayPath())
	}

	e.mu.RLock()
	task := e.tasks[chatID]
	e.mu.RUnlock()

	task.mu.Lock()
	task.Name = name
	task.Files = files
	task.TotalBytes = total
	task.mu.Unlock()

	t.DownloadAll()
	log.Printf("[%d] download started: %s (%s)", chatID, name, formatBytes(total))

	statusID, _ := bot.sendMsg(chatID,
		fmt.Sprintf("📥 *Downloading:* `%s`\n%s / %s", name, "0 B", formatBytes(total)), 0, true)

	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()
	stall := time.NewTicker(120 * time.Second)
	defer stall.Stop()

	var lastBytes int64
	lastTime := time.Now()

	for {
		select {
		case <-ticker.C:
			completed := t.BytesCompleted()
			pct := float64(completed) / float64(total) * 100

			e.mu.RLock()
			if e.tasks[chatID] == nil {
				e.mu.RUnlock()
				return
			}
			e.mu.RUnlock()

			var speed string
			if elapsed := time.Since(task.StartedAt).Seconds(); elapsed > 5 && completed > 0 {
				bps := int64(float64(completed) / elapsed)
				speed = fmt.Sprintf("%s/s", formatBytes(bps))
			} else {
				speed = "connecting…"
			}

			task.mu.Lock()
			task.Progress = pct
			task.mu.Unlock()

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
				e.mu.RLock()
				exists := e.tasks[chatID] != nil
				e.mu.RUnlock()
				if exists {
					bot.sendMsg(chatID, "❌ *Stalled — no peers for 2 minutes.*", 0, true)
				}
				return
			}
		}

		if t.BytesCompleted() >= total {
			break
		}
	}

	task.mu.Lock()
	task.Done = true
	task.Progress = 100
	task.mu.Unlock()

	bot.sendMsg(chatID, fmt.Sprintf("✅ *Download complete:* `%s`\n⏳ *Uploading to Telegram…*", name), 0, true)

	// Upload files to Telegram
	e.uploadFiles(bot, chatID, t)
}

func (e *Engine) uploadFiles(bot *Bot, chatID int64, t *torrent.Torrent) {
	e.mu.RLock()
	task := e.tasks[chatID]
	e.mu.RUnlock()

	task.mu.RLock()
	files := task.Files
	torrentName := task.Name
	e.mu.RUnlock()

	ok, fail := 0, 0
	totalFiles := len(files)

	bot.sendMsg(chatID,
		fmt.Sprintf("📤 *Starting upload:* %d file(s)", totalFiles), 0, true)

	for i, fPath := range files {
		// Sanitize path for storage directory
		safeName := filepath.Base(fPath)
		full := e.storage + "/" + fPath

		fi, err := os.Stat(full)
		if err != nil {
			log.Printf("[%d] file not found: %s", chatID, full)
			bot.sendMsg(chatID, fmt.Sprintf("⚠️ File not found: `%s`", safeName), 0, true)
			fail++
			continue
		}
		if fi.IsDir() {
			continue
		}
		if fi.Size() == 0 {
			log.Printf("[%d] skipping empty file: %s", chatID, full)
			continue
		}

		// Limit file size to 2000MB for Telegram
		maxSize := int64(2000) * 1024 * 1024
		if fi.Size() > maxSize {
			log.Printf("[%d] file too large (%s > 2GB): %s", chatID, formatBytes(fi.Size()), safeName)
			bot.sendMsg(chatID,
				fmt.Sprintf("⚠️ *File too large:* `%s` (%s)\nTelegram limit is 2GB per file.",
					safeName, formatBytes(fi.Size())), 0, true)
			fail++
			continue
		}

		caption := fmt.Sprintf("[%d/%d] %s — %s", i+1, totalFiles, torrentName, safeName)

		log.Printf("[%d] 📤 uploading [%d/%d]: %s (%s)",
			chatID, i+1, totalFiles, safeName, formatBytes(fi.Size()))

		bot.sendAction(chatID, "upload_document")

		// Send a progress message
		progMsg, _ := bot.sendMsg(chatID,
			fmt.Sprintf("📤 *Uploading:* `%s` (%d/%d)\n📊 %s / %s",
				safeName, i+1, totalFiles, formatBytes(fi.Size()), formatBytes(fi.Size())),
			0, true)

		// Upload the file
		err = bot.uploadFile(chatID, full, 0, caption)
		if err != nil {
			log.Printf("[%d] upload failed %s: %v", chatID, safeName, err)
			bot.sendMsg(chatID,
				fmt.Sprintf("❌ *Upload failed:* `%s` — %v", safeName, err), 0, true)
			fail++
		} else {
			ok++
			bot.sendMsg(chatID,
				fmt.Sprintf("✅ *Uploaded:* `%s` (%d/%d)", safeName, i+1, totalFiles), 0, true)
		}

		// Update progress message
		if progMsg > 0 {
			bot.editMsg(chatID, progMsg,
				fmt.Sprintf("✅ *Done:* `%s`", safeName))
		}

		time.Sleep(1 * time.Second)
	}

	e.mu.Lock()
	delete(e.tasks, chatID)
	e.mu.Unlock()

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
		t.mu.Lock()
		t.Error = msg
		t.mu.Unlock()
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

	os.MkdirAll(storage, 0755)

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
	defer tc.Close()
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

			if u.Message != nil && u.Message.Text != "" {
				chatID := u.Message.Chat.ID
				text := u.Message.Text
				if text == "" {
					text = u.Message.Caption
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
