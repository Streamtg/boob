package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
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
		client:  &http.Client{Timeout: 30 * time.Second},
	}
}

func (b *Bot) api(method string, params map[string]string) ([]byte, error) {
	url := b.baseURL + "/" + method
	if len(params) > 0 {
		for k, v := range params {
			url += "&" + k + "=" + v
		}
	}
	resp, err := b.client.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	return io.ReadAll(resp.Body)
}

// ── Types ─────────────────────────────────────────────────────────────────────

type Update struct {
	UpdateID      int `json:"update_id"`
	Message       struct {
		MessageID int    `json:"message_id"`
		Chat      struct {
			ID int64 `json:"id"`
		} `json:"chat"`
		Text string `json:"text"`
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

type multipartWriter struct {
	buf []byte
}

func (m *multipartWriter) addField(k, v string) {
	m.buf = append(m.buf,
		[]byte(fmt.Sprintf("--BOUNDARY\r\nContent-Disposition: form-data; name=\"%s\"\r\n\r\n%s\r\n", k, v))...)
}

func (m *multipartWriter) addFile(field, filename string, r io.Reader) {
	m.buf = append(m.buf,
		[]byte(fmt.Sprintf(
			"--BOUNDARY\r\nContent-Disposition: form-data; name=\"%s\"; filename=\"%s\"\r\n\r\n",
			field, filename))...)
	data, _ := io.ReadAll(r)
	m.buf = append(m.buf, data...)
	m.buf = append(m.buf, "\r\n"...)
}

func (m *multipartWriter) finish() (io.ReadCloser, string) {
	m.buf = append(m.buf, "--BOUNDARY--\r\n"...)
	return io.NopCloser(strings.NewReader(string(m.buf))),
		"multipart/form-data; boundary=BOUNDARY"
}

func (b *Bot) sendMsg(chatID int64, text string, replyTo int, markdown bool) (int, error) {
	body, _ := json.Marshal(map[string]interface{}{
		"chat_id":             chatID,
		"text":                text,
		"parse_mode":          map[bool]string{true: "Markdown", false: ""}[markdown],
		"reply_to_message_id": replyTo,
	})
	data, err := b.post("sendMessage", "application/json", strings.NewReader(string(body)))
	if err != nil {
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
	b.post("editMessageText", "application/json", strings.NewReader(string(body)))
}

func (b *Bot) answerCallback(queryID string) {
	body, _ := json.Marshal(map[string]interface{}{"callback_query_id": queryID})
	b.post("answerCallbackQuery", "application/json", strings.NewReader(string(body)))
}

func (b *Bot) sendAction(chatID int64, action string) {
	body, _ := json.Marshal(map[string]interface{}{"chat_id": chatID, "action": action})
	b.post("sendChatAction", "application/json", strings.NewReader(string(body)))
}

func (b *Bot) post(path, mime string, body io.Reader) ([]byte, error) {
	resp, err := b.client.Post(b.baseURL+"/"+path, mime, body)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	return io.ReadAll(resp.Body)
}

func (b *Bot) uploadFile(chatID int64, filepath string, replyTo int) error {
	file, err := os.Open(filepath)
	if err != nil {
		return err
	}
	defer file.Close()

	fi, err := file.Stat()
	if err != nil {
		return err
	}

	mp := multipartWriter{}
	mp.addField("chat_id", strconv.FormatInt(chatID, 10))
	if replyTo > 0 {
		mp.addField("reply_to_message_id", strconv.Itoa(replyTo))
	}
	mp.addFile("document", fi.Name(), file)
	reader, ctype := mp.finish()

	req, err := http.NewRequest("POST", b.baseURL+"/sendDocument", reader)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", ctype)

	resp, err := b.client.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}

// ─────────────────────────────────────────────────────────────────────────────
// Download engine
// ─────────────────────────────────────────────────────────────────────────────

type Task struct {
	mu          sync.RWMutex
	Name        string
	Progress    float64
	Done        bool
	Files       []string
	TotalBytes  int64
	Error       string
	StartedAt   time.Time
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
	switch text {
	case "/cancel":
		e.cmdCancel(bot, chatID, replyTo)
	case "/start", "/help":
		bot.sendMsg(chatID, helpText, replyTo, true)
	case "/status":
		e.cmdStatus(bot, chatID, replyTo)
	default:
		if isMagnet(text) || isTorrentURL(text) {
			e.startDownload(bot, chatID, replyTo, text)
		} else {
			bot.sendMsg(chatID, helpText, replyTo, true)
		}
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
		bot.sendMsg(chatID,
			fmt.Sprintf("⏳ *Downloading:* `%s`\n📊 `%.1f%%` | ⏱ %s", name, pct, elapsed), replyTo, true)
	}
}

func (e *Engine) startDownload(bot *Bot, chatID int64, replyTo int, input string) {
	e.mu.Lock()
	if _, active := e.tasks[chatID]; active {
		e.mu.Unlock()
		bot.sendMsg(chatID, "⏳ Download already in progress. Use /cancel first.", replyTo, true)
		return
	}
	e.tasks[chatID] = &Task{StartedAt: time.Now()}
	e.mu.Unlock()

	statusID, _ := bot.sendMsg(chatID, "⏳ *Starting download…*", replyTo, true)

	var t *torrent.Torrent
	var err error

	if isMagnet(input) {
		clean := strings.TrimSpace(input)
		if idx := strings.Index(clean, "&"); idx != -1 {
			clean = clean[:idx]
		}
		t, err = e.tc.AddMagnet(clean)
		log.Printf("[%d] magnet added", chatID)
	} else {
		bot.sendAction(chatID, "typing")
		data, fe := fetchURL(input)
		if fe != nil {
			e.fail(chatID, fmt.Sprintf("fetch error: %v", fe))
			bot.editMsg(chatID, statusID, fmt.Sprintf("❌ *Fetch failed:* `%s`", fe.Error()))
			return
		}
		mi, metaErr := metainfo.Load(strings.NewReader(string(data)))
		if metaErr != nil {
			e.fail(chatID, fmt.Sprintf("torrent parse error: %v", metaErr))
			bot.editMsg(chatID, statusID, fmt.Sprintf("❌ *Parse error:* `%s`", metaErr.Error()))
			return
		}
		t, err = e.tc.AddTorrent(mi)
		log.Printf("[%d] torrent from URL", chatID)
	}
	if err != nil {
		e.fail(chatID, fmt.Sprintf("add error: %v", err))
		bot.editMsg(chatID, statusID, fmt.Sprintf("❌ *Error:* `%s`", err.Error()))
		return
	}

	e.mu.RLock()
	e.tasks[chatID].Name = t.Name()
	e.mu.RUnlock()

	bot.editMsg(chatID, statusID,
		fmt.Sprintf("📥 *Added:* `%s`\n⏳ *Waiting for peers…*", t.Name()))

	go e.downloadLoop(bot, chatID, replyTo, t, statusID)
}

func (e *Engine) downloadLoop(bot *Bot, chatID int64, replyTo int, t *torrent.Torrent, statusID int) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("[%d] panic: %v", chatID, r)
		}
	}()

	<-t.GotInfo()

	var files []string
	for _, f := range t.Files() {
		if f.DisplayPath() != "" {
			files = append(files, f.DisplayPath())
		}
	}

	e.mu.RLock()
	task := e.tasks[chatID]
	e.mu.RUnlock()

	task.mu.Lock()
	task.Files = files
	task.TotalBytes = t.Length()
	task.mu.Unlock()

	// Use DownloadAll() to start downloading all pieces
	t.DownloadAll()

	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	stall := time.NewTicker(90 * time.Second)
	defer stall.Stop()

	var lastBytes int64
	lastTime := time.Now()

	// Capture total before the loop so it's in scope below
	total := t.Length()

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
			if elapsed := time.Since(task.StartedAt).Seconds(); elapsed > 4 && completed > 0 {
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
					t.Name(), bar, pct, speed, formatBytes(completed), formatBytes(total)))

		case <-stall.C:
			if time.Since(lastTime) >= 90*time.Second {
				e.mu.RLock()
				exists := e.tasks[chatID] != nil
				e.mu.RUnlock()
				if exists {
					bot.editMsg(chatID, statusID, "❌ *Stalled — no peers for 90s.*")
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

	bot.editMsg(chatID, statusID, "✅ *Download complete — uploading to Telegram…*")
	e.uploadFiles(bot, chatID, replyTo, t)
}

func (e *Engine) uploadFiles(bot *Bot, chatID int64, replyTo int, t *torrent.Torrent) {
	e.mu.RLock()
	task := e.tasks[chatID]
	e.mu.RUnlock()

	task.mu.RLock()
	files := task.Files
	task.mu.RUnlock()

	ok, fail := 0, 0
	for _, fPath := range files {
		full := e.storage + "/" + fPath
		fi, err := os.Stat(full)
		if err != nil || fi.IsDir() || fi.Size() == 0 {
			continue
		}

		log.Printf("[%d] uploading %s (%s)", chatID, fPath, formatBytes(fi.Size()))
		bot.sendAction(chatID, "upload_document")

		if err := bot.uploadFile(chatID, full, replyTo); err != nil {
			log.Printf("[%d] upload failed %s: %v", chatID, fPath, err)
			bot.sendMsg(chatID, fmt.Sprintf("⚠️ Upload failed: %s — %v", fPath, err), 0, false)
			fail++
		} else {
			ok++
		}
		time.Sleep(500 * time.Millisecond)
	}

	e.mu.Lock()
	delete(e.tasks, chatID)
	e.mu.Unlock()

	if fail > 0 {
		bot.sendMsg(chatID, fmt.Sprintf("🎉 *Done!* %d uploaded, %d failed.", ok, fail), 0, true)
	} else {
		bot.sendMsg(chatID, fmt.Sprintf("🎉 *All done!* %d file(s) uploaded.", ok), 0, true)
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
	resp, err := http.DefaultClient.Get(raw)
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

	// Use NewDefaultClientConfig() instead of torrent.Config
	cfg := torrent.NewDefaultClientConfig()
	cfg.DataDir = storage
	cfg.NoUpload = false
	cfg.Seed = true
	cfg.SetListenAddr("0.0.0.0:0")
	cfg.DisableIPv6 = false
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
			"timeout": "60",
			"offset":  strconv.Itoa(offset),
		})
		if err != nil {
			log.Printf("⚠️  getUpdates error: %v", err)
			time.Sleep(5 * time.Second)
			continue
		}

		var ups struct {
			OK     bool      `json:"ok"`
			Result []Update `json:"result"`
		}
		json.Unmarshal(data, &ups)

		for _, u := range ups.Result {
			offset = u.UpdateID + 1

			var chatID int64
			var text string
			var replyTo int

			// Message is a struct, not a pointer — check if Text is non-empty
			if u.Message.Text != "" {
				chatID = u.Message.Chat.ID
				text = u.Message.Text
				replyTo = u.Message.MessageID
			} else if u.CallbackQuery != nil {
				chatID = u.CallbackQuery.Message.Chat.ID
				text = u.CallbackQuery.Data
				replyTo = u.CallbackQuery.Message.MessageID
				bot.answerCallback(u.CallbackQuery.ID)
			} else {
				continue
			}

			go func(chat int64, body string, reply int) {
				engine.Handle(bot, chat, reply, body)
			}(chatID, text, replyTo)
		}

		if len(ups.Result) == 0 {
			time.Sleep(1 * time.Second)
		}
	}
}
