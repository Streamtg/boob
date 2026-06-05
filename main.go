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

// ── Telegram Bot (stdlib only) ────────────────────────────────────────────────

type Bot struct {
	token   string
	baseURL string
	client  *http.Client
}

func NewBot(token string) *Bot {
	return &Bot{token: token, baseURL: "https://api.telegram.org/bot" + token, client: &http.Client{Timeout: 70 * time.Second}}
}

func (b *Bot) get(method string, params map[string]string) ([]byte, error) {
	url := b.baseURL + "/" + method
	if len(params) > 0 {
		pairs := []string{}
		for k, v := range params {
			pairs = append(pairs, k+"="+v)
		}
		url += "?" + strings.Join(pairs, "&")
	}
	resp, err := b.client.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	return io.ReadAll(resp.Body)
}

func (b *Bot) postJSON(method string, data interface{}) ([]byte, error) {
	payload, _ := json.Marshal(data)
	resp, err := b.client.Post(b.baseURL+"/"+method, "application/json", bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	return io.ReadAll(resp.Body)
}

func (b *Bot) sendDocument(chatID int64, filePath string, replyTo int) error {
	f, err := os.Open(filePath)
	if err != nil {
		return err
	}
	defer f.Close()

	body := &bytes.Buffer{}
	w := multipart.NewWriter(body)
	w.WriteField("chat_id", strconv.FormatInt(chatID, 10))
	if replyTo > 0 {
		w.WriteField("reply_to_message_id", strconv.Itoa(replyTo))
	}
	part, err := w.CreateFormFile("document", fileBase(filePath))
	if err != nil {
		return err
	}
	io.Copy(part, f)
	w.Close()

	req, _ := http.NewRequest("POST", b.baseURL+"/sendDocument", body)
	req.Header.Set("Content-Type", w.FormDataContentType())

	resp, err := b.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	var r struct{ OK bool `json:"ok"` }
	json.NewDecoder(resp.Body).Decode(&r)
	if !r.OK {
		return fmt.Errorf("upload failed")
	}
	return nil
}

func (b *Bot) SendMessage(chatID int64, text string, replyTo int, md bool) (int, error) {
	req := map[string]interface{}{
		"chat_id":             chatID,
		"text":                text,
		"reply_to_message_id": replyTo,
	}
	if md {
		req["parse_mode"] = "Markdown"
	}
	body, err := b.postJSON("sendMessage", req)
	if err != nil {
		return 0, err
	}
	var r struct {
		OK     bool `json:"ok"`
		Result struct{ MessageID int `json:"message_id"` } `json:"result"`
	}
	json.Unmarshal(body, &r)
	if r.OK {
		return r.Result.MessageID, nil
	}
	return 0, fmt.Errorf("send failed")
}

func (b *Bot) EditMessage(chatID int64, msgID int, text string) {
	b.postJSON("editMessageText", map[string]interface{}{
		"chat_id":    chatID,
		"message_id": msgID,
		"text":       text,
		"parse_mode": "Markdown",
	})
}

func (b *Bot) ChatAction(chatID int64, action string) {
	b.postJSON("sendChatAction", map[string]interface{}{
		"chat_id": chatID,
		"action":  action,
	})
}

func (b *Bot) AnswerCB(queryID string) {
	b.postJSON("answerCallbackQuery", map[string]interface{}{
		"callback_query_id": queryID,
	})
}

// ── Update types ──────────────────────────────────────────────────────────────

type Update struct {
	UpdateID      int              `json:"update_id"`
	Message       *MessagePayload  `json:"message"`
	CallbackQuery *CallbackPayload `json:"callback_query"`
}

type MessagePayload struct {
	MessageID int    `json:"message_id"`
	Chat      struct{ ID int64 `json:"id"` } `json:"chat"`
	Text      string `json:"text"`
}

type CallbackPayload struct {
	ID   string `json:"id"`
	Data string `json:"data"`
	Message struct {
		MessageID int `json:"message_id"`
		Chat      struct{ ID int64 `json:"id"` } `json:"chat"`
	} `json:"message"`
}

// ── Task ──────────────────────────────────────────────────────────────────────

type Task struct {
	Name         string
	Progress     float64
	Done         bool
	Files        []string
	TotalBytes   int64
	Error        string
	StartedAt    time.Time
	MetadataDone bool
	mu           sync.RWMutex
}

// ── Engine ────────────────────────────────────────────────────────────────────

type Engine struct {
	Client  *torrent.Client
	Storage string
	Tasks   map[int64]*Task
	mu      sync.RWMutex
}

func NewEngine(tc *torrent.Client, storage string) *Engine {
	return &Engine{Client: tc, Storage: storage, Tasks: make(map[int64]*Task)}
}

func (e *Engine) Handle(bot *Bot, chatID int64, replyTo int, text string) {
	switch text {
	case "/cancel":
		e.Cancel(bot, chatID, replyTo)
	case "/start", "/help":
		e.Help(bot, chatID, replyTo)
	case "/status":
		e.Status(bot, chatID, replyTo)
	default:
		if isMagnet(text) || isTorrentURL(text) {
			e.Start(bot, chatID, replyTo, text)
		} else {
			e.Help(bot, chatID, replyTo)
		}
	}
}

func (e *Engine) Help(bot *Bot, chatID int64, replyTo int) {
	bot.SendMessage(chatID, `🤖 *TeleTorrent Bot*

Send me a magnet link or .torrent URL and I'll download & upload it here.

*Commands:*
/start /help — this message
/status      — download status
/cancel      — cancel download

*Supported:*
• Magnet links (magnet:?xt=…)
• .torrent URLs (https://…/file.torrent)

*Note:*
Magnet links may take 1-3 min to start
as it needs peers via DHT.`, replyTo, true)
}

func (e *Engine) Status(bot *Bot, chatID int64, replyTo int) {
	e.mu.RLock()
	t, ok := e.Tasks[chatID]
	e.mu.RUnlock()
	if !ok {
		bot.SendMessage(chatID, "📭 *No active download.*", replyTo, true)
		return
	}
	t.mu.RLock()
	pct, name, errMsg, started := t.Progress, t.Name, t.Error, t.StartedAt
	metaDone := t.MetadataDone
	t.mu.RUnlock()
	if errMsg != "" {
		bot.SendMessage(chatID, fmt.Sprintf("❌ *Error:* `%s`", errMsg), replyTo, true)
	} else if t.Done {
		bot.SendMessage(chatID, fmt.Sprintf("✅ *Completed:* `%s`", name), replyTo, true)
	} else if !metaDone {
		bot.SendMessage(chatID, fmt.Sprintf("⏳ *Getting metadata…*\n⏱ %s", time.Since(started).Round(time.Second)), replyTo, true)
	} else {
		bot.SendMessage(chatID, fmt.Sprintf("⏳ *Downloading:* `%s`\n📊 `%.1f%%` | ⏱ %s", name, pct, time.Since(started).Round(time.Second)), replyTo, true)
	}
}

func (e *Engine) Cancel(bot *Bot, chatID int64, replyTo int) {
	e.mu.Lock()
	if _, ok := e.Tasks[chatID]; !ok {
		e.mu.Unlock()
		bot.SendMessage(chatID, "📭 Nothing to cancel.", replyTo, true)
		return
	}
	delete(e.Tasks, chatID)
	e.mu.Unlock()
	bot.SendMessage(chatID, "🚫 Download cancelled.", replyTo, true)
}

func (e *Engine) Start(bot *Bot, chatID int64, replyTo int, input string) {
	e.mu.Lock()
	if _, active := e.Tasks[chatID]; active {
		e.mu.Unlock()
		bot.SendMessage(chatID, "⏳ Download already in progress. Use /cancel first.", replyTo, true)
		return
	}
	e.Tasks[chatID] = &Task{StartedAt: time.Now()}
	e.mu.Unlock()

	statusID, _ := bot.SendMessage(chatID, "⏳ *Starting…*\nWaiting for metadata via DHT…", replyTo, true)

	if isMagnet(input) {
		clean := strings.TrimSpace(input)
		if idx := strings.Index(clean, "&"); idx != -1 {
			clean = clean[:idx]
		}
		t, err := e.Client.AddMagnet(clean)
		if err != nil {
			e.fail(chatID, fmt.Sprintf("magnet error: %v", err))
			bot.EditMessage(chatID, statusID, fmt.Sprintf("❌ *Error:* `%s`", err.Error()))
			return
		}
		log.Printf("[%d] Magnet added: %s", chatID, t.Name())
		go e.downloadLoop(bot, chatID, replyTo, t, statusID)
	} else {
		bot.ChatAction(chatID, "typing")
		data, err := fetchURL(input)
		if err != nil {
			e.fail(chatID, fmt.Sprintf("fetch error: %v", err))
			bot.EditMessage(chatID, statusID, fmt.Sprintf("❌ *Fetch failed:* `%s`", err.Error()))
			return
		}
		mi, err := metainfo.Load(bytes.NewReader(data))
		if err != nil {
			e.fail(chatID, fmt.Sprintf("parse error: %v", err))
			bot.EditMessage(chatID, statusID, fmt.Sprintf("❌ *Parse error:* `%s`", err.Error()))
			return
		}
		t, err := e.Client.AddTorrent(mi)
		if err != nil {
			e.fail(chatID, fmt.Sprintf("torrent error: %v", err))
			bot.EditMessage(chatID, statusID, fmt.Sprintf("❌ *Error:* `%s`", err.Error()))
			return
		}
		log.Printf("[%d] Torrent added: %s", chatID, t.Name())
		go e.downloadLoop(bot, chatID, replyTo, t, statusID)
	}
}

func (e *Engine) downloadLoop(bot *Bot, chatID int64, replyTo int, t *torrent.Torrent, statusID int) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("[%d] panic: %v", chatID, r)
		}
	}()

	// ── PHASE 1: Wait for metadata ─────────────────────────────────────────
	metadataStart := time.Now()
	lastLog := time.Now()

	for {
		select {
		case <-t.GotInfo():
			goto metadataReceived
		default:
		}

		// Check if cancelled
		e.mu.RLock()
		if e.Tasks[chatID] == nil {
			e.mu.RUnlock()
			return
		}
		e.mu.RUnlock()

		// Check if metadata already available
		if t.Info() != nil {
			goto metadataReceived
		}

		// Log every 15 seconds
		if time.Since(lastLog) >= 15*time.Second {
			stats := t.Stats()
			log.Printf("[%d] Metadata wait: %s | Peers: %d | Data: %s",
				chatID, time.Since(metadataStart).Round(time.Second), stats.TotalPeers, formatBytes(t.BytesCompleted()))
			lastLog = time.Now()
		}

		time.Sleep(500 * time.Millisecond)
	}

metadataReceived:
	numPieces := t.NumPieces()
	log.Printf("[%d] METADATA RECEIVED: %s | Size: %s | Pieces: %d",
		chatID, t.Name(), formatBytes(t.Length()), numPieces)

	// ── PHASE 2: Mark all pieces as wanted ─────────────────────────────────
	for i := 0; i < numPieces; i++ {
		piece := t.Piece(i)
		if piece.IsComplete() {
			continue
		}
		piece.SetWant(true)
	}

	for _, f := range t.Files() {
		f.SetPriority(torrent.PiecePriorityNormal)
	}

	log.Printf("[%d] All %d pieces marked as wanted. Starting download.", chatID, numPieces)

	// Build file list
	var files []string
	for _, f := range t.Files() {
		if f.Path() != "" {
			files = append(files, f.Path())
		}
	}

	e.mu.RLock()
	task := e.Tasks[chatID]
	e.mu.RUnlock()

	task.mu.Lock()
	task.Files = files
	task.TotalBytes = t.Length()
	task.Name = t.Name()
	task.MetadataDone = true
	task.mu.Unlock()

	bot.EditMessage(chatID, statusID,
		fmt.Sprintf("✅ *Metadata received!*\n📥 `%s`\n📦 %s | %d pieces\n⏳ *Downloading…*",
			t.Name(), formatBytes(t.Length()), numPieces))

	// ── PHASE 3: Download loop ──────────────────────────────────────────────
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	stall := time.NewTicker(180 * time.Second)
	defer stall.Stop()

	var lastBytes int64
	lastTime := time.Now()
	completedPieces := 0

	for {
		select {
		case <-ticker.C:
			e.mu.RLock()
			if e.Tasks[chatID] == nil {
				e.mu.RUnlock()
				return
			}
			e.mu.RUnlock()

			completed := t.BytesCompleted()
			total := t.Length()

			// Count completed pieces
			currentCompleted := 0
			for i := 0; i < numPieces; i++ {
				if t.Piece(i).IsComplete() {
					currentCompleted++
				}
			}

			pct := 0.0
			if total > 0 {
				pct = float64(completed) / float64(total) * 100
			}

			var speed string
			elapsed := time.Since(task.StartedAt).Seconds()
			if elapsed > 4 && completed > 0 {
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

			stats := t.Stats()
			bar := progressBar(int(pct), 20)
			bot.EditMessage(chatID, statusID,
				fmt.Sprintf("📥 *Downloading*\n`%s`\n%s `%.1f%%`\n🔽 %s | %s / %s\n🔌 Peers: %d | Pieces: %d/%d",
					t.Name(), bar, pct, speed,
					formatBytes(completed), formatBytes(total),
					stats.ActivePeers, currentCompleted, numPieces))

		case <-stall.C:
			if time.Since(lastTime) >= 180*time.Second {
				e.mu.RLock()
				exists := e.Tasks[chatID] != nil
				e.mu.RUnlock()
				if exists {
					stats := t.Stats()
					bot.EditMessage(chatID, statusID,
						fmt.Sprintf("⏸ *Slow download*\nData: %s\nPeers: %d | Stalled 3 min\nRetrying…",
							formatBytes(t.BytesCompleted()), stats.ActivePeers))
				}
				return
			}
		}

		if total > 0 && t.BytesCompleted() >= total {
			break
		}
	}

	// Download complete
	task.mu.Lock()
	task.Done = true
	task.Progress = 100
	task.mu.Unlock()

	bot.EditMessage(chatID, statusID, "✅ *Download complete — uploading to Telegram…*")
	e.uploadFiles(bot, chatID, replyTo, t)
}

func (e *Engine) uploadFiles(bot *Bot, chatID int64, replyTo int, t *torrent.Torrent) {
	e.mu.RLock()
	task := e.Tasks[chatID]
	e.mu.RUnlock()

	task.mu.RLock()
	files := task.Files
	task.mu.RUnlock()

	ok, fail := 0, 0
	for _, fPath := range files {
		fullPath := joinPath(e.Storage, fPath)
		fi, err := os.Stat(fullPath)
		if err != nil || fi.IsDir() || fi.Size() == 0 {
			continue
		}
		log.Printf("[%d] uploading %s (%s)", chatID, fPath, formatBytes(fi.Size()))
		bot.ChatAction(chatID, "upload_document")

		if err := bot.sendDocument(chatID, fullPath, replyTo); err != nil {
			log.Printf("[%d] upload failed %s: %v", chatID, fPath, err)
			bot.SendMessage(chatID, fmt.Sprintf("⚠️ Could not upload `%s`: %v", fPath, err), 0, false)
			fail++
		} else {
			ok++
			log.Printf("[%d] uploaded %s", chatID, fPath)
		}

		time.Sleep(600 * time.Millisecond)
	}

	e.mu.Lock()
	delete(e.Tasks, chatID)
	e.mu.Unlock()

	if fail > 0 {
		bot.SendMessage(chatID, fmt.Sprintf("🎉 *Done!* Uploaded %d, %d failed.", ok, fail), 0, true)
	} else {
		bot.SendMessage(chatID, fmt.Sprintf("🎉 *All done!* Uploaded %d file(s).", ok), 0, true)
	}
}

func (e *Engine) fail(chatID int64, msg string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if t, ok := e.Tasks[chatID]; ok {
		t.mu.Lock()
		t.Error = msg
		t.mu.Unlock()
	}
}

func isMagnet(s string) bool {
	return strings.HasPrefix(strings.TrimSpace(s), "magnet:?")
}

func isTorrentURL(s string) bool {
	s = strings.TrimSpace(s)
	return (strings.HasPrefix(s, "http://") || strings.HasPrefix(s, "https://")) &&
		strings.HasSuffix(s, ".torrent")
}

func fetchURL(url string) ([]byte, error) {
	c := &http.Client{Timeout: 60 * time.Second}
	r, err := c.Get(url)
	if err != nil {
		return nil, err
	}
	defer r.Body.Close()
	if r.StatusCode != 200 {
		return nil, fmt.Errorf("HTTP %d", r.StatusCode)
	}
	return io.ReadAll(r.Body)
}

func formatBytes(n int64) string {
	switch {
	case n >= 1 << 30:
		return fmt.Sprintf("%.1f GB", float64(n)/(1<<30))
	case n >= 1 << 20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1 << 10:
		return fmt.Sprintf("%.1f KB", float64(n)/(1<<10))
	default:
		return strconv.FormatInt(n, 10) + " B"
	}
}

func progressBar(pct, w int) string {
	if pct < 0 {
		pct = 0
	}
	if pct > 100 {
		pct = 100
	}
	return strings.Repeat("█", pct*w/100) + strings.Repeat("░", w-pct*w/100)
}

func fileBase(path string) string {
	for i := len(path) - 1; i >= 0; i-- {
		if path[i] == '/' || path[i] == '\\' {
			return path[i+1:]
		}
	}
	return path
}

func joinPath(a, b string) string {
	if a == "" {
		return b
	}
	last := a[len(a)-1]
	if last == '/' || last == '\\' {
		return a + b
	}
	return a + "/" + b
}

// ── Main ──────────────────────────────────────────────────────────────────────

func main() {
	var token, storage string
	flag.StringVar(&token, "token", "", "Telegram bot token from @BotFather")
	flag.StringVar(&storage, "storage", "./downloads", "Download directory")
	flag.Parse()

	if token == "" {
		fmt.Println("❌  go run main.go -token YOUR_TOKEN")
		os.Exit(1)
	}
	os.MkdirAll(storage, 0755)

	cfg := torrent.NewDefaultClientConfig()
	cfg.DataDir = storage

	tc, err := torrent.NewClient(cfg)
	if err != nil {
		log.Fatalf("❌  Torrent client: %v", err)
	}
	defer tc.Close()
	log.Printf("✅  Torrent client ready. Storage: %s", storage)

	bot := NewBot(token)
	body, err := bot.get("getMe", nil)
	if err != nil {
		log.Fatalf("❌  Telegram auth: %v", err)
	}
	var me struct {
		OK     bool `json:"ok"`
		Result struct{ Username string `json:"username"` } `json:"result"`
	}
	json.Unmarshal(body, &me)
	log.Printf("✅  Bot: @%s", me.Result.Username)

	eng := NewEngine(tc, storage)

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sig
		log.Println("⚡  Shutting down...")
		tc.Close()
		os.Exit(0)
	}()

	offset := 0
	for {
		body, err := bot.get("getUpdates", map[string]string{
			"timeout": "60",
			"offset":  strconv.Itoa(offset),
		})
		if err != nil {
			log.Printf("⚠️  getUpdates timeout — retrying")
			time.Sleep(3 * time.Second)
			continue
		}

		var upd struct {
			OK     bool     `json:"ok"`
			Result []Update `json:"result"`
		}
		json.Unmarshal(body, &upd)

		for _, u := range upd.Result {
			offset = u.UpdateID + 1

			var chatID int64
			var text   string
			var reply  int

			if u.Message != nil && u.Message.Text != "" {
				chatID = u.Message.Chat.ID
				text = u.Message.Text
				reply = u.Message.MessageID
			} else if u.CallbackQuery != nil {
				chatID = u.CallbackQuery.Message.Chat.ID
				text = u.CallbackQuery.Data
				reply = u.CallbackQuery.Message.MessageID
				bot.AnswerCB(u.CallbackQuery.ID)
			} else {
				continue
			}

			go func(c int64, t string, r int) {
				eng.Handle(bot, c, r, t)
			}(chatID, text, reply)
		}

		if len(upd.Result) == 0 {
			time.Sleep(1 * time.Second)
		}
	}
}
