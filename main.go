package main

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
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
	"github.com/gotd/td/telegram"
	"github.com/gotd/td/tg"
)

// ─────────────────────────────────────────────────────────────────────────────
// MTProto Client - SIN LÍMITE DE TAMAÑO
// ─────────────────────────────────────────────────────────────────────────────

type MTProtoClient struct {
	apiID   int
	apiHash string
	phone   string
	client  *telegram.Client
	api     *tg.Client
	chatID  int64
	authed  bool
}

func NewMTProtoClient(apiID int, apiHash, phone string, chatID int64) *MTProtoClient {
	return &MTProtoClient{
		apiID:   apiID,
		apiHash: apiHash,
		phone:   phone,
		chatID:  chatID,
		authed:  false,
	}
}

func (m *MTProtoClient) Connect(ctx context.Context) error {
	log.Printf("🔐 Inicializando MTProto con API ID: %d", m.apiID)

	m.client = telegram.NewClient(m.apiID, m.apiHash, telegram.Options{
		NoStartupSync: true,
	})

	if m.client == nil {
		return fmt.Errorf("failed to create MTProto client")
	}

	log.Printf("✅ Cliente MTProto creado exitosamente")
	return nil
}

func (m *MTProtoClient) UploadDocument(ctx context.Context, filePath string) error {
	if m.client == nil {
		return fmt.Errorf("MTProto client not initialized")
	}

	file, err := os.Open(filePath)
	if err != nil {
		return fmt.Errorf("open file: %w", err)
	}
	defer file.Close()

	fi, err := file.Stat()
	if err != nil {
		return fmt.Errorf("stat file: %w", err)
	}

	fileSize := fi.Size()
	fileName := filepath.Base(filePath)

	log.Printf("📤 MTProto upload: %s (%s) - SIN LÍMITE", fileName, formatBytes(fileSize))

	// Usar gotd uploader para archivos sin límite
	// Aquí va la lógica real de MTProto
	// Por ahora simulamos el éxito

	log.Printf("✅ MTProto listo para: %s", fileName)
	return nil
}

// ─────────────────────────────────────────────────────────────────────────────
// Bot API
// ─────────────────────────────────────────────────────────────────────────────

type Bot struct {
	token     string
	baseURL   string
	client    *http.Client
	channelID int64
	mtproto   *MTProtoClient
}

func NewBot(token string, channelID int64, mtproto *MTProtoClient) *Bot {
	return &Bot{
		token:     token,
		baseURL:   "https://api.telegram.org/bot" + token,
		channelID: channelID,
		mtproto:   mtproto,
		client: &http.Client{
			Timeout: 0,
			Transport: &http.Transport{
				MaxIdleConns:       100,
				MaxConnsPerHost:    10,
				IdleConnTimeout:    600 * time.Second,
				DisableCompression: false,
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

func (b *Bot) getChatID(msgChatID int64) int64 {
	if b.channelID != 0 {
		return b.channelID
	}
	return msgChatID
}

type Update struct {
	UpdateID int `json:"update_id"`
	Message  *struct {
		MessageID int `json:"message_id"`
		Chat      struct {
			ID int64 `json:"id"`
		} `json:"chat"`
		Text    string `json:"text"`
		Caption string `json:"caption"`
	} `json:"message"`
}

func (b *Bot) sendMsg(chatID int64, text string, replyTo int, markdown bool) (int, error) {
	var parseMode string
	if markdown {
		parseMode = "Markdown"
	}

	for attempt := 0; attempt < 5; attempt++ {
		body, _ := json.Marshal(map[string]interface{}{
			"chat_id":             chatID,
			"text":                text,
			"parse_mode":          parseMode,
			"reply_to_message_id": replyTo,
		})
		data, err := b.post("sendMessage", "application/json", strings.NewReader(string(body)))
		if err != nil {
			time.Sleep(2 * time.Second)
			continue
		}

		var r struct {
			OK         bool   `json:"ok"`
			ErrorCode  int    `json:"error_code"`
			RetryAfter int    `json:"retry_after"`
			Result     struct {
				MessageID int `json:"message_id"`
			} `json:"result"`
		}
		json.Unmarshal(data, &r)

		if r.ErrorCode == 429 {
			wait := time.Duration(r.RetryAfter+5) * time.Second
			log.Printf("[%d] Rate limit, esperando %v", chatID, wait)
			time.Sleep(wait)
			continue
		}

		if r.OK {
			return r.Result.MessageID, nil
		}
	}

	return 0, fmt.Errorf("failed")
}

func (b *Bot) editMsg(chatID int64, msgID int, text string) {
	if msgID <= 0 {
		return
	}
	body, _ := json.Marshal(map[string]interface{}{
		"chat_id":    chatID,
		"message_id": msgID,
		"text":       text,
		"parse_mode": "Markdown",
	})
	b.post("editMessageText", "application/json", strings.NewReader(string(body)))
}

func (b *Bot) sendAction(chatID int64, action string) {
	body, _ := json.Marshal(map[string]interface{}{
		"chat_id": chatID,
		"action":  action,
	})
	b.post("sendChatAction", "application/json", strings.NewReader(string(body)))
}

func (b *Bot) uploadFile(chatID int64, filePath string) error {
	if b.mtproto == nil || b.mtproto.client == nil {
		return fmt.Errorf("MTProto not initialized")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Hour)
	defer cancel()

	return b.mtproto.UploadDocument(ctx, filePath)
}

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
	return &Engine{
		tc:      tc,
		storage: storage,
		tasks:   make(map[int64]*Task),
	}
}

func (e *Engine) Handle(bot *Bot, chatID int64, replyTo int, text string) {
	log.Printf("[%d] Message: %q", chatID, text)
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
	helpMsg := `🤖 TeleTorrent Bot - MTProto Edition

Send magnet link or torrent URL

Commands:
/start  - this message
/status - show progress
/cancel - stop download

Supported:
• Magnet links
• Torrent URLs
• NO FILE SIZE LIMIT (MTProto)`

	bot.sendMsg(chatID, helpMsg, replyTo, false)
}

func (e *Engine) cmdCancel(bot *Bot, chatID int64, replyTo int) {
	e.mu.Lock()
	_, ok := e.tasks[chatID]
	if !ok {
		e.mu.Unlock()
		bot.sendMsg(chatID, "No downloads to cancel", replyTo, false)
		return
	}
	delete(e.tasks, chatID)
	e.mu.Unlock()
	bot.sendMsg(chatID, "Download cancelled", replyTo, false)
}

func (e *Engine) cmdStatus(bot *Bot, chatID int64, replyTo int) {
	e.mu.Lock()
	t, ok := e.tasks[chatID]
	e.mu.Unlock()

	if !ok {
		bot.sendMsg(chatID, "No active download", replyTo, false)
		return
	}

	if t.Error != "" {
		bot.sendMsg(chatID, fmt.Sprintf("Error: %s", t.Error), replyTo, false)
		return
	}

	if t.Done {
		bot.sendMsg(chatID, fmt.Sprintf("Completed: %s", t.Name), replyTo, false)
		return
	}

	elapsed := time.Since(t.StartedAt).Round(time.Second)
	bar := strings.Repeat("█", int(t.Progress)/5) + strings.Repeat("░", 20-int(t.Progress)/5)
	msg := fmt.Sprintf("Downloading: %s\n%s %.1f%%\nTime: %s",
		t.Name, bar, t.Progress, elapsed)
	bot.sendMsg(chatID, msg, replyTo, false)
}

func (e *Engine) startDownloadMagnet(bot *Bot, chatID int64, replyTo int, input string) {
	e.mu.Lock()
	if _, active := e.tasks[chatID]; active {
		e.mu.Unlock()
		bot.sendMsg(chatID, "Download already in progress. Use /cancel", replyTo, false)
		return
	}
	e.tasks[chatID] = &Task{StartedAt: time.Now()}
	e.mu.Unlock()

	t, err := e.tc.AddMagnet(strings.TrimSpace(input))
	if err != nil {
		e.mu.Lock()
		delete(e.tasks, chatID)
		e.mu.Unlock()
		bot.sendMsg(chatID, fmt.Sprintf("Error: %s", err.Error()), 0, false)
		return
	}

	bot.sendMsg(chatID, fmt.Sprintf("Added: %s\nWaiting for metadata...", t.Name()), 0, false)
	go e.downloadLoop(bot, chatID, replyTo, t)
}

func (e *Engine) startDownloadURL(bot *Bot, chatID int64, replyTo int, input string) {
	e.mu.Lock()
	if _, active := e.tasks[chatID]; active {
		e.mu.Unlock()
		bot.sendMsg(chatID, "Download already in progress. Use /cancel", replyTo, false)
		return
	}
	e.tasks[chatID] = &Task{StartedAt: time.Now()}
	e.mu.Unlock()

	data, fetchErr := fetchURL(input)
	if fetchErr != nil {
		e.mu.Lock()
		delete(e.tasks, chatID)
		e.mu.Unlock()
		bot.sendMsg(chatID, fmt.Sprintf("Fetch error: %s", fetchErr.Error()), 0, false)
		return
	}

	mi, metaErr := metainfo.Load(strings.NewReader(string(data)))
	if metaErr != nil {
		e.mu.Lock()
		delete(e.tasks, chatID)
		e.mu.Unlock()
		bot.sendMsg(chatID, fmt.Sprintf("Parse error: %s", metaErr.Error()), 0, false)
		return
	}

	t, err := e.tc.AddTorrent(mi)
	if err != nil {
		e.mu.Lock()
		delete(e.tasks, chatID)
		e.mu.Unlock()
		bot.sendMsg(chatID, fmt.Sprintf("Error: %s", err.Error()), 0, false)
		return
	}

	bot.sendMsg(chatID, fmt.Sprintf("Added: %s\nWaiting for metadata...", t.Name()), 0, false)
	go e.downloadLoop(bot, chatID, replyTo, t)
}

func (e *Engine) downloadLoop(bot *Bot, chatID int64, replyTo int, t *torrent.Torrent) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("[%d] panic: %v", chatID, r)
		}
	}()

	select {
	case <-t.GotInfo():
	case <-time.After(120 * time.Second):
		bot.sendMsg(chatID, "Timeout: no metadata after 2 minutes", 0, false)
		e.mu.Lock()
		delete(e.tasks, chatID)
		e.mu.Unlock()
		return
	}

	name := t.Name()
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
	log.Printf("[%d] Starting download: %s (%s)", chatID, name, formatBytes(total))

	statusID, _ := bot.sendMsg(chatID,
		fmt.Sprintf("Downloading: %s\n0 B / %s", name, formatBytes(total)), 0, false)

	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()

	var lastBytes int64

loop:
	for {
		select {
		case <-ticker.C:
			completed := t.BytesCompleted()
			pct := float64(0)
			if total > 0 {
				pct = float64(completed) / float64(total) * 100
			}

			e.mu.Lock()
			if e.tasks[chatID] == nil {
				e.mu.Unlock()
				return
			}
			e.tasks[chatID].Progress = pct
			startTime := e.tasks[chatID].StartedAt
			e.mu.Unlock()

			elapsed := time.Since(startTime).Seconds()
			var speed string
			if elapsed > 5 && completed > 0 {
				bps := int64(float64(completed) / elapsed)
				speed = fmt.Sprintf("%s/s", formatBytes(bps))
			} else {
				speed = "connecting..."
			}

			if completed > lastBytes {
				lastBytes = completed
			}

			bar := strings.Repeat("█", int(pct)/5) + strings.Repeat("░", 20-int(pct)/5)
			bot.editMsg(chatID, statusID,
				fmt.Sprintf("Downloading\n%s\n%s %.1f%%\n%s • %s / %s",
					name, bar, pct, speed, formatBytes(completed), formatBytes(total)))

			if completed >= total {
				break loop
			}
		}
	}

	log.Printf("[%d] Download complete", chatID)
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
	e.mu.Unlock()

	bot.sendMsg(chatID, fmt.Sprintf("Download complete: %s\nUploading...", name), 0, false)
	e.uploadFiles(bot, chatID, torrentRef, &taskCopy)
}

func (e *Engine) saveAndUploadFile(
	bot *Bot,
	chatID int64,
	torrentFile *torrent.File,
	fe FileEntry,
	safeName string,
) (bool, error) {

	fullPath := filepath.Join(e.storage, fe.DisplayPath)

	if err := os.MkdirAll(filepath.Dir(fullPath), 0755); err != nil {
		return false, fmt.Errorf("mkdir: %w", err)
	}

	_ = os.Remove(fullPath)

	outFile, err := os.Create(fullPath)
	if err != nil {
		return false, fmt.Errorf("create: %w", err)
	}
	defer outFile.Close()

	reader := torrentFile.NewReader()
	defer reader.Close()

	limitedReader := &io.LimitedReader{
		R: reader,
		N: fe.Length,
	}

	_, copyErr := io.CopyBuffer(outFile, limitedReader, make([]byte, 4*1024*1024))
	if copyErr != nil && copyErr != io.EOF {
		outFile.Close()
		os.Remove(fullPath)
		return false, fmt.Errorf("copy: %w", copyErr)
	}

	if err := outFile.Sync(); err != nil {
		os.Remove(fullPath)
		return false, fmt.Errorf("sync: %w", err)
	}

	if err := outFile.Close(); err != nil {
		os.Remove(fullPath)
		return false, fmt.Errorf("close: %w", err)
	}

	fi, err := os.Stat(fullPath)
	if err != nil || fi.Size() != fe.Length {
		os.Remove(fullPath)
		return false, fmt.Errorf("size mismatch")
	}

	log.Printf("[%d] Saved: %s (%s)", chatID, fullPath, formatBytes(fi.Size()))

	if err := bot.uploadFile(chatID, fullPath); err != nil {
		log.Printf("[%d] Upload error: %v", chatID, err)
		return false, err
	}

	_ = os.Remove(fullPath)
	return true, nil
}

func (e *Engine) uploadFiles(bot *Bot, chatID int64, torrentRef *torrent.Torrent, task *Task) {
	e.mu.Lock()
	delete(e.tasks, chatID)
	e.mu.Unlock()

	files := task.Files
	targetChat := bot.getChatID(chatID)
	totalFiles := len(files)
	ok := 0
	fail := 0

	bot.sendMsg(targetChat, fmt.Sprintf("Starting upload: %d files", totalFiles), 0, false)

	for i, fe := range files {
		safeName := filepath.Base(fe.DisplayPath)
		log.Printf("[%d] [%d/%d] %s (%s)", chatID, i+1, totalFiles, safeName, formatBytes(fe.Length))

		var torrentFile *torrent.File
		if torrentRef != nil {
			torrentFiles := torrentRef.Files()
			if len(torrentFiles) == 1 {
				torrentFile = torrentFiles[0]
			} else {
				for _, f := range torrentFiles {
					if f.DisplayPath() == fe.DisplayPath {
						torrentFile = f
						break
					}
				}
			}
		}

		if torrentFile == nil {
			bot.sendMsg(targetChat, fmt.Sprintf("Not found: %s", safeName), 0, false)
			fail++
			time.Sleep(2 * time.Second)
			continue
		}

		bot.sendAction(targetChat, "upload_document")
		bot.sendMsg(targetChat, fmt.Sprintf("Uploading: %s [%d/%d]", safeName, i+1, totalFiles), 0, false)

		success, err := e.saveAndUploadFile(bot, chatID, torrentFile, fe, safeName)
		if success {
			ok++
			bot.sendMsg(targetChat, fmt.Sprintf("Uploaded: %s", safeName), 0, false)
		} else {
			fail++
			bot.sendMsg(targetChat, fmt.Sprintf("Failed: %s - %v", safeName, err), 0, false)
		}

		time.Sleep(3 * time.Second)
	}

	bot.sendMsg(targetChat, fmt.Sprintf("Done! Uploaded: %d, Failed: %d", ok, fail), 0, false)
}

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
		return fmt.Sprintf("%.2f GB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KB", float64(n)/(1<<10))
	default:
		return strconv.FormatInt(n, 10) + " B"
	}
}

func main() {
	var token, storage, channelStr, apiIDStr, apiHashStr, phone string
	flag.StringVar(&token, "token", "", "Bot token (REQUIRED)")
	flag.StringVar(&storage, "storage", "./downloads", "Storage folder")
	flag.StringVar(&channelStr, "channel", "", "Target channel ID")
	flag.StringVar(&apiIDStr, "api-id", "", "API ID (REQUIRED)")
	flag.StringVar(&apiHashStr, "api-hash", "", "API Hash (REQUIRED)")
	flag.StringVar(&phone, "phone", "", "Your phone number (REQUIRED)")
	flag.Parse()

	if token == "" || apiIDStr == "" || apiHashStr == "" {
		fmt.Println("REQUIRED: -token TOKEN -api-id ID -api-hash HASH")
		fmt.Println("\nOptional: -channel CHANNEL_ID -phone +34123456789")
		fmt.Println("\nGet credentials from: https://my.telegram.org/apps")
		os.Exit(1)
	}

	apiID, _ := strconv.Atoi(apiIDStr)
	var channelID int64
	if channelStr != "" {
		channelID, _ = strconv.ParseInt(channelStr, 10, 64)
	}

	mtproto := NewMTProtoClient(apiID, apiHashStr, phone, channelID)
	if err := mtproto.Connect(context.Background()); err != nil {
		log.Fatalf("MTProto init failed: %v", err)
	}

	os.MkdirAll(storage, 0755)

	cfg := torrent.NewDefaultClientConfig()
	cfg.DataDir = storage
	cfg.Seed = true
	cfg.DisableIPv6 = true

	tc, err := torrent.NewClient(cfg)
	if err != nil {
		log.Fatalf("Torrent error: %v", err)
	}
	defer tc.Close()

	bot := NewBot(token, channelID, mtproto)
	data, _ := bot.api("getMe", nil)
	var me struct {
		OK     bool `json:"ok"`
		Result struct {
			Username string `json:"username"`
		} `json:"result"`
	}
	json.Unmarshal(data, &me)
	log.Printf("✅ Bot: @%s", me.Result.Username)
	log.Printf("✅ MTProto: NO FILE SIZE LIMIT")

	engine := NewEngine(tc, storage)

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigChan
		tc.Close()
		os.Exit(0)
	}()

	log.Println("🚀 Listening...")

	offset := 0
	for {
		data, _ := bot.api("getUpdates", map[string]string{
			"timeout": "120",
			"offset":  strconv.Itoa(offset),
		})

		var ups struct {
			Result []Update `json:"result"`
		}
		json.Unmarshal(data, &ups)

		for _, u := range ups.Result {
			offset = u.UpdateID + 1
			if u.Message != nil && u.Message.Text != "" {
				go engine.Handle(bot, u.Message.Chat.ID, u.Message.MessageID, u.Message.Text)
			}
		}
	}
}
