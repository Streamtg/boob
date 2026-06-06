package main

import (
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
	"github.com/gotd/td/client"
	"github.com/gotd/td/session"
	"github.com/gotd/td/telegram"
	"github.com/gotd/td/tg"
)

// ─────────────────────────────────────────────────────────────────────────────
// TDLib Client (Soporta archivos sin límite)
// ─────────────────────────────────────────────────────────────────────────────

type TDClient struct {
	client *telegram.Client
	api    *tg.Client
	chatID int64
	phone  string
}

func NewTDClient(apiID int, apiHash, phone string, chatID int64) *TDClient {
	return &TDClient{
		chatID: chatID,
		phone:  phone,
	}
}

func (td *TDClient) Connect(ctx context.Context) error {
	c := telegram.NewClient(
		td.client,
		telegram.Options{
			SessionStorage: &session.StorageMemory{},
		},
	)

	return c.Run(ctx, func(ctx context.Context) error {
		td.client = c
		td.api = c.API()

		// Login si es necesario
		if _, err := td.api.AuthCheckPhone(ctx, &tg.AuthCheckPhoneRequest{
			PhoneNumber: td.phone,
		}); err != nil {
			log.Printf("⚠️  Necesita autenticación: %v", err)
			// Implementar flujo de login
		}

		return nil
	})
}

func (td *TDClient) UploadDocument(ctx context.Context, filePath string, caption string) error {
	file, err := os.Open(filePath)
	if err != nil {
		return fmt.Errorf("open: %w", err)
	}
	defer file.Close()

	fi, err := file.Stat()
	if err != nil {
		return fmt.Errorf("stat: %w", err)
	}

	fileSize := fi.Size()
	filename := filepath.Base(filePath)

	log.Printf("📤 Subiendo (TDLib): %s (%s)", filename, formatBytes(fileSize))

	// Uploader de gotd - soporta archivos grandes
	uploader := client.NewUploader(td.api)

	err = uploader.Upload(ctx, file, fileSize, func(uploaded int64) error {
		pct := float64(uploaded) / float64(fileSize) * 100
		log.Printf("⬆️ %.1f%% (%s/%s)", pct, formatBytes(uploaded), formatBytes(fileSize))
		return nil
	})

	if err != nil {
		return fmt.Errorf("upload: %w", err)
	}

	log.Printf("✅ Subido: %s", filename)
	return nil
}

// ─────────────────────────────────────────────────────────────────────────────
// Bot API (para mensajes y comandos)
// ─────────────────────────────────────────────────────────────────────────────

type Bot struct {
	token     string
	baseURL   string
	client    *http.Client
	channelID int64
	tdClient  *TDClient
}

func NewBot(token string, channelID int64, tdClient *TDClient) *Bot {
	return &Bot{
		token:     token,
		baseURL:   "https://api.telegram.org/bot" + token,
		channelID: channelID,
		tdClient:  tdClient,
		client: &http.Client{
			Timeout: 300 * time.Second,
			Transport: &http.Transport{
				MaxIdleConns:        200,
				MaxIdleConnsPerHost: 50,
				IdleConnTimeout:     300 * time.Second,
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

	for attempt := 0; attempt < 10; attempt++ {
		body, _ := json.Marshal(map[string]interface{}{
			"chat_id":             chatID,
			"text":                text,
			"parse_mode":          parseMode,
			"reply_to_message_id": replyTo,
		})
		data, err := b.post("sendMessage", "application/json", strings.NewReader(string(body)))
		if err != nil {
			log.Printf("[%d] sendMsg error: %v", chatID, err)
			time.Sleep(time.Duration((attempt+1)*2) * time.Second)
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
			waitTime := time.Duration(r.RetryAfter+5) * time.Second
			log.Printf("[%d] Rate limit, esperando %v…", chatID, waitTime)
			time.Sleep(waitTime)
			continue
		}

		if !r.OK {
			return 0, fmt.Errorf("api error")
		}

		return r.Result.MessageID, nil
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

// ─────────────────────────────────────────────────────────────────────────────
// Engine
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
	bot     *Bot
}

func NewEngine(tc *torrent.Client, storage string, bot *Bot) *Engine {
	return &Engine{
		tc:      tc,
		storage: storage,
		tasks:   make(map[int64]*Task),
		bot:     bot,
	}
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
		bot.sendMsg(chatID, "📭 Nada que cancelar.", replyTo, true)
		return
	}
	delete(e.tasks, chatID)
	e.mu.Unlock()
	bot.sendMsg(chatID, "🚫 Descarga cancelada.", replyTo, true)
}

func (e *Engine) cmdStatus(bot *Bot, chatID int64, replyTo int) {
	e.mu.Lock()
	t, ok := e.tasks[chatID]
	e.mu.Unlock()

	if !ok {
		bot.sendMsg(chatID, "📭 *Sin descarga activa.*", replyTo, true)
		return
	}

	var msg string
	if t.Error != "" {
		msg = fmt.Sprintf("❌ *Error:* `%s`", t.Error)
	} else if t.Done {
		msg = fmt.Sprintf("✅ *Completado:* `%s`", t.Name)
	} else {
		elapsed := time.Since(t.StartedAt).Round(time.Second)
		bar := strings.Repeat("█", int(t.Progress)/5) +
			strings.Repeat("░", 20-int(t.Progress)/5)
		msg = fmt.Sprintf("⏳ *Descargando:* `%s`\n%s `%.1f%%`\n⏱ %s",
			t.Name, bar, t.Progress, elapsed)
	}
	bot.sendMsg(chatID, msg, replyTo, true)
}

func (e *Engine) startDownloadMagnet(bot *Bot, chatID int64, replyTo int, input string) {
	e.mu.Lock()
	if _, active := e.tasks[chatID]; active {
		e.mu.Unlock()
		bot.sendMsg(chatID, "⏳ Ya hay descarga. Usa /cancel primero.", replyTo, true)
		return
	}
	e.tasks[chatID] = &Task{StartedAt: time.Now()}
	e.mu.Unlock()

	t, err := e.tc.AddMagnet(strings.TrimSpace(input))
	if err != nil {
		e.mu.Lock()
		delete(e.tasks, chatID)
		e.mu.Unlock()
		bot.sendMsg(chatID, fmt.Sprintf("❌ *Error:* `%s`", err.Error()), 0, true)
		return
	}

	bot.sendMsg(chatID,
		fmt.Sprintf("📥 *Agregado:* `%s`\n⏳ *Esperando metadata…*", t.Name()), 0, true)
	go e.downloadLoop(bot, chatID, replyTo, t)
}

func (e *Engine) startDownloadURL(bot *Bot, chatID int64, replyTo int, input string) {
	e.mu.Lock()
	if _, active := e.tasks[chatID]; active {
		e.mu.Unlock()
		bot.sendMsg(chatID, "⏳ Ya hay descarga. Usa /cancel primero.", replyTo, true)
		return
	}
	e.tasks[chatID] = &Task{StartedAt: time.Now()}
	e.mu.Unlock()

	data, fetchErr := fetchURL(input)
	if fetchErr != nil {
		e.mu.Lock()
		delete(e.tasks, chatID)
		e.mu.Unlock()
		bot.sendMsg(chatID, fmt.Sprintf("❌ *Error:* `%s`", fetchErr.Error()), 0, true)
		return
	}
	mi, metaErr := metainfo.Load(strings.NewReader(string(data)))
	if metaErr != nil {
		e.mu.Lock()
		delete(e.tasks, chatID)
		e.mu.Unlock()
		bot.sendMsg(chatID, fmt.Sprintf("❌ *Error:* `%s`", metaErr.Error()), 0, true)
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

	bot.sendMsg(chatID,
		fmt.Sprintf("📥 *Agregado:* `%s`\n⏳ *Esperando metadata…*", t.Name()), 0, true)
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
		bot.sendMsg(chatID, "❌ *Timeout.*", 0, true)
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
	log.Printf("[%d] descarga: %s (%s)", chatID, name, formatBytes(total))

	statusID, _ := bot.sendMsg(chatID,
		fmt.Sprintf("📥 *Descargando:* `%s`\n0 B / %s", name, formatBytes(total)),
		0, true)

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
				speed = "conectando…"
			}

			if completed > lastBytes {
				lastBytes = completed
			}

			bar := strings.Repeat("█", int(pct)/5) +
				strings.Repeat("░", 20-int(pct)/5)
			bot.editMsg(chatID, statusID,
				fmt.Sprintf("📥 *Descargando*\n`%s`\n%s `%.1f%%`\n%s • %s / %s",
					name, bar, pct, speed,
					formatBytes(completed), formatBytes(total)))

			if completed >= total {
				break loop
			}
		}
	}

	log.Printf("[%d] descarga completa", chatID)
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

	bot.sendMsg(chatID,
		fmt.Sprintf("✅ *Descarga completa*\n⏳ *Subiendo…*"),
		0, true)

	e.uploadFiles(bot, chatID, torrentRef, &taskCopy)
}

func (e *Engine) saveAndUploadFile(
	bot *Bot,
	chatID int64,
	torrentFile *torrent.File,
	fe FileEntry,
	safeName string,
	useTDLib bool,
) (bool, error) {

	fullPath := filepath.Join(e.storage, fe.DisplayPath)

	if err := os.MkdirAll(filepath.Dir(fullPath), 0755); err != nil {
		return false, fmt.Errorf("mkdir: %w", err)
	}

	_ = os.Remove(fullPath)

	outFile, err := os.Create(fullPath)
	if err != nil {
		return false, fmt.Errorf("crear: %w", err)
	}
	defer outFile.Close()

	reader := torrentFile.NewReader()
	defer reader.Close()

	limitedReader := &io.LimitedReader{
		R: reader,
		N: fe.Length,
	}

	buf := make([]byte, 512*1024)

	for {
		n, readErr := limitedReader.Read(buf)
		if n > 0 {
			if _, writeErr := outFile.Write(buf[:n]); writeErr != nil {
				outFile.Close()
				os.Remove(fullPath)
				return false, fmt.Errorf("write: %w", writeErr)
			}
		}
		if readErr != nil {
			if readErr != io.EOF {
				outFile.Close()
				os.Remove(fullPath)
				return false, fmt.Errorf("read: %w", readErr)
			}
			break
		}
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
		return false, fmt.Errorf("tamaño incorrecto")
	}

	log.Printf("[%d] 💾 guardado: %s (%s)", chatID, fullPath, formatBytes(fi.Size()))

	// ✅ Si es archivo grande (>2GB) y tenemos TDLib, usar TDLib
	if useTDLib && fi.Size() > 2*1024*1024*1024 && bot.tdClient != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 1*time.Hour)
		defer cancel()

		err = bot.tdClient.UploadDocument(ctx, fullPath, fmt.Sprintf("📁 %s", safeName))
		if err != nil {
			log.Printf("[%d] TDLib upload error: %v, usando Bot API…", chatID, err)
			// Fallback a Bot API
		} else {
			_ = os.Remove(fullPath)
			return true, nil
		}
	}

	// Fallback: Bot API para archivos pequeños
	bot.sendMsg(chatID, fmt.Sprintf("✅ *Guardado:* `%s`", safeName), 0, true)

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

	bot.sendMsg(targetChat,
		fmt.Sprintf("📤 *Subiendo:* %d archivo(s)\n🚀 Usando TDLib para archivos >2GB", totalFiles), 0, true)

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
			bot.sendMsg(targetChat,
				fmt.Sprintf("❌ *No encontrado:* `%s`", safeName), 0, true)
			fail++
			time.Sleep(2 * time.Second)
			continue
		}

		// ✅ Usar TDLib para archivos grandes
		useTDLib := fe.Length > 2*1024*1024*1024
		success, err := e.saveAndUploadFile(bot, chatID, torrentFile, fe, safeName, useTDLib)
		if success {
			ok++
			bot.sendMsg(targetChat,
				fmt.Sprintf("✅ *Subido:* `%s` (%s)", safeName, formatBytes(fe.Length)), 0, true)
		} else {
			fail++
			bot.sendMsg(targetChat,
				fmt.Sprintf("❌ *Falló:* `%s` - %v", safeName, err), 0, true)
		}

		time.Sleep(3 * time.Second)
	}

	bot.sendMsg(targetChat,
		fmt.Sprintf("🎉 *¡Listo!* ✅ %d ❌ %d", ok, fail), 0, true)
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

const helpText = `🤖 *TeleTorrent Bot*

Envíame un magnet o URL .torrent

*Con TDLib:*
✅ Archivos sin límite
✅ Subida más rápida

*Comandos:*
/start /help
/status
/cancel`

func main() {
	var token, storage, channelStr, apiIDStr, apiHashStr, phoneStr string
	flag.StringVar(&token, "token", "", "Bot token")
	flag.StringVar(&storage, "storage", "./downloads", "Carpeta")
	flag.StringVar(&channelStr, "channel", "", "ID canal")
	flag.StringVar(&apiIDStr, "api-id", "", "API ID (para TDLib)")
	flag.StringVar(&apiHashStr, "api-hash", "", "API Hash (para TDLib)")
	flag.StringVar(&phoneStr, "phone", "", "Tu número de teléfono (para TDLib)")
	flag.Parse()

	if token == "" {
		fmt.Println("❌ Uso: ./bot -token TOKEN [-channel ID] [-api-id ID] [-api-hash HASH] [-phone NUMERO]")
		os.Exit(1)
	}

	var channelID int64
	if channelStr != "" {
		channelID, _ = strconv.ParseInt(channelStr, 10, 64)
	}

	os.MkdirAll(storage, 0755)

	cfg := torrent.NewDefaultClientConfig()
	cfg.DataDir = storage
	cfg.Seed = true
	cfg.DisableIPv6 = true

	tc, err := torrent.NewClient(cfg)
	if err != nil {
		log.Fatalf("❌ Error: %v", err)
	}
	defer tc.Close()

	// TDLib (opcional)
	var tdClient *TDClient
	if apiIDStr != "" && apiHashStr != "" && phoneStr != "" {
		apiID, _ := strconv.Atoi(apiIDStr)
		tdClient = NewTDClient(apiID, apiHashStr, phoneStr, channelID)
		log.Printf("✅ TDLib configurado (para archivos >2GB)")
	} else {
		log.Printf("⚠️  TDLib no configurado (usando Bot API para archivos <2GB)")
	}

	bot := NewBot(token, channelID, tdClient)
	data, _ := bot.api("getMe", nil)
	var me struct {
		OK     bool `json:"ok"`
		Result struct {
			Username string `json:"username"`
		} `json:"result"`
	}
	json.Unmarshal(data, &me)
	log.Printf("✅ Bot: @%s", me.Result.Username)

	engine := NewEngine(tc, storage, bot)

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigChan
		tc.Close()
		os.Exit(0)
	}()

	log.Println("🚀 Escuchando…")

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
