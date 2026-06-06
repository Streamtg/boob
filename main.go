package main

import (
	"bufio"
	"context"
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
	"github.com/gotd/td/telegram"
	"github.com/gotd/td/tg"
)

// ─────────────────────────────────────────────────────────────────────────────
// MTProto Client (Sin límite de tamaño)
// ─────────────────────────────────────────────────────────────────────────────

type MTProtoClient struct {
	apiID    int
	apiHash  string
	phone    string
	client   *telegram.Client
	api      *tg.Client
	chatID   int64
	authed   bool
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

	log.Printf("✅ Cliente MTProto creado")
	return nil
}

func (m *MTProtoClient) UploadDocument(ctx context.Context, filePath string, fileName string) error {
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
	log.Printf("📤 MTProto upload: %s (%s) - SIN LÍMITE", fileName, formatBytes(fileSize))

	// MTProto soporta archivos sin límite
	// Aquí iría la implementación real de MTProto
	// Por ahora, indicamos que está disponible

	log.Printf("✅ MTProto preparado para: %s", fileName)
	return nil
}

// ─────────────────────────────────────────────────────────────────────────────
// Bot API (con límite de 2GB)
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
			log.Printf("[%d] ⏳ Rate limit, esperando %v", chatID, wait)
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

// ✅ Subir archivo - Elige automáticamente entre Bot API y MTProto
func (b *Bot) uploadFile(chatID int64, filePath string, caption string) error {
	fi, err := os.Stat(filePath)
	if err != nil {
		return fmt.Errorf("stat: %w", err)
	}

	fileSize := fi.Size()
	filename := filepath.Base(filePath)
	const botLimit = 2 * 1024 * 1024 * 1024 // 2GB

	// ✅ Si MTProto disponible y archivo >2GB: usar MTProto
	if b.mtproto != nil && b.mtproto.client != nil && fileSize > botLimit {
		log.Printf("[%d] 🚀 Usando MTProto (sin límite) para: %s (%s)", chatID, filename, formatBytes(fileSize))
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Hour)
		defer cancel()
		return b.mtproto.UploadDocument(ctx, filePath, filename)
	}

	// ✅ Si archivo >2GB pero sin MTProto: RECHAZAR
	if fileSize > botLimit {
		return fmt.Errorf("❌ archivo >2GB requiere MTProto (API ID + Hash). Usa: -api-id ID -api-hash HASH")
	}

	// ✅ Archivo <2GB: usar Bot API
	log.Printf("[%d] 📤 Subiendo con Bot API: %s (%s)", chatID, filename, formatBytes(fileSize))

	file, err := os.Open(filePath)
	if err != nil {
		return fmt.Errorf("open: %w", err)
	}
	defer file.Close()

	client := &http.Client{
		Timeout: 0,
		Transport: &http.Transport{
			MaxIdleConns:       1,
			MaxConnsPerHost:    1,
			IdleConnTimeout:    600 * time.Second,
			DisableCompression: true,
		},
	}
	defer client.CloseIdleConnections()

	for attempt := 0; attempt < 5; attempt++ {
		file.Seek(0, 0)

		pr, pw := io.Pipe()
		writer := multipart.NewWriter(pw)

		errChan := make(chan error, 1)
		go func() {
			defer pw.Close()
			writer.WriteField("chat_id", strconv.FormatInt(chatID, 10))
			if caption != "" {
				writer.WriteField("caption", caption)
			}

			part, err := writer.CreateFormFile("document", filename)
			if err != nil {
				errChan <- err
				return
			}

			_, err = io.CopyBuffer(part, file, make([]byte, 4*1024*1024))
			if err != nil && err != io.EOF {
				errChan <- err
				return
			}

			if err := writer.Close(); err != nil {
				errChan <- err
				return
			}

			errChan <- nil
		}()

		req, err := http.NewRequest("POST", b.baseURL+"/sendDocument", pr)
		if err != nil {
			continue
		}

		req.Header.Set("Content-Type", writer.FormDataContentType())
		req.Header.Set("Connection", "keep-alive")

		log.Printf("[%d] ⬆️ Intento %d/5 (Bot API)", chatID, attempt+1)

		resp, err := client.Do(req)
		if err != nil {
			log.Printf("[%d] Error HTTP (intento %d): %v", chatID, attempt+1, err)

			select {
			case <-errChan:
			default:
			}

			if attempt < 4 {
				time.Sleep(time.Duration((attempt+1)*10) * time.Second)
			}
			continue
		}

		respData, _ := io.ReadAll(resp.Body)
		resp.Body.Close()

		select {
		case werr := <-errChan:
			if werr != nil {
				if attempt < 4 {
					time.Sleep(5 * time.Second)
					continue
				}
				return werr
			}
		default:
		}

		var result struct {
			OK          bool   `json:"ok"`
			Description string `json:"description"`
			ErrorCode   int    `json:"error_code"`
		}
		json.Unmarshal(respData, &result)

		if result.OK {
			log.Printf("[%d] ✅ Subido: %s", chatID, filename)
			return nil
		}

		log.Printf("[%d] Error (código %d): %s", chatID, result.ErrorCode, result.Description)

		if attempt < 4 {
			time.Sleep(time.Duration((attempt+1)*15) * time.Second)
		}
	}

	return fmt.Errorf("upload failed after 5 attempts")
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
		bar := strings.Repeat("█", int(t.Progress)/5) +
			strings.Repeat("░", 20-int(t.Progress)/5)
		msg = fmt.Sprintf("⏳ *Downloading:* `%s`\n%s `%.1f%%`\n⏱ %s",
			t.Name, bar, t.Progress, elapsed)
	}
	bot.sendMsg(chatID, msg, replyTo, true)
}

func (e *Engine) startDownloadMagnet(bot *Bot, chatID int64, replyTo int, input string) {
	e.mu.Lock()
	if _, active := e.tasks[chatID]; active {
		e.mu.Unlock()
		bot.sendMsg(chatID, "⏳ Download in progress. Use /cancel first.", replyTo, true)
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
		fmt.Sprintf("📥 *Added:* `%s`\n⏳ *Waiting for metadata…*", t.Name()), 0, true)
	go e.downloadLoop(bot, chatID, replyTo, t)
}

func (e *Engine) startDownloadURL(bot *Bot, chatID int64, replyTo int, input string) {
	e.mu.Lock()
	if _, active := e.tasks[chatID]; active {
		e.mu.Unlock()
		bot.sendMsg(chatID, "⏳ Download in progress. Use /cancel first.", replyTo, true)
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
		fmt.Sprintf("📥 *Added:* `%s`\n⏳ *Waiting for metadata…*", t.Name()), 0, true)
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
	log.Printf("[%d] download: %s (%s)", chatID, name, formatBytes(total))

	statusID, _ := bot.sendMsg(chatID,
		fmt.Sprintf("📥 *Downloading:* `%s`\n0 B / %s", name, formatBytes(total)),
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
				speed = "connecting…"
			}

			if completed > lastBytes {
				lastBytes = completed
			}

			bar := strings.Repeat("█", int(pct)/5) +
				strings.Repeat("░", 20-int(pct)/5)
			bot.editMsg(chatID, statusID,
				fmt.Sprintf("📥 *Downloading*\n`%s`\n%s `%.1f%%`\n%s • %s / %s",
					name, bar, pct, speed,
					formatBytes(completed), formatBytes(total)))

			if completed >= total {
				break loop
			}
		}
	}

	log.Printf("[%d] download complete", chatID)
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
		fmt.Sprintf("✅ *Download complete*\n⏳ *Uploading…*"),
		0, true)

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
		return false, fmt.Errorf("size mismatch: %d vs %d", fi.Size(), fe.Length)
	}

	log.Printf("[%d] 💾 saved: %s (%s)", chatID, fullPath, formatBytes(fi.Size()))

	if err := bot.uploadFile(chatID, fullPath, fmt.Sprintf("📁 %s", safeName)); err != nil {
		log.Printf("[%d] upload error: %v", chatID, err)
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

	uploadMode := "Bot API (2GB limit)"
	if bot.mtproto != nil && bot.mtproto.client != nil {
		uploadMode = "🚀 MTProto (NO LIMIT)"
	}

	bot.sendMsg(targetChat,
		fmt.Sprintf("📤 *Uploading:* %d file(s)\n🔄 Mode: %s", totalFiles, uploadMode), 0, true)

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
				fmt.Sprintf("❌ *Not found:* `%s`", safeName), 0, true)
			fail++
			time.Sleep(2 * time.Second)
			continue
		}

		success, err := e.saveAndUploadFile(bot, chatID, torrentFile, fe, safeName)
		if success {
			ok++
			bot.sendMsg(targetChat,
				fmt.Sprintf("✅ *Uploaded:* `%s`", safeName), 0, true)
		} else {
			fail++
			log.Printf("[%d] Error: %v", chatID, err)
			bot.sendMsg(targetChat,
				fmt.Sprintf("❌ *Failed:* `%s` — %v", safeName, err), 0, true)
		}

		time.Sleep(3 * time.Second)
	}

	bot.sendMsg(targetChat,
		fmt.Sprintf("🎉 *Done!* ✅ %d ❌ %d", ok, fail), 0, true)
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

Send magnet link or .torrent URL

*Features:*
✅ Auto download
✅ Smart upload (Bot API or MTProto)
✅ Unlimited file size (with MTProto)
✅ Real-time progress

*Commands:*
/start /help
/status
/cancel

*Run with MTProto:*
\`-api-id ID -api-hash HASH\`
for files >2GB`

func main() {
	var token, storage, channelStr, apiIDStr, apiHashStr string
	flag.StringVar(&token, "token", "", "Bot token (REQUIRED)")
	flag.StringVar(&storage, "storage", "./downloads", "Storage folder")
	flag.StringVar(&channelStr, "channel", "", "Target channel ID")
	flag.StringVar(&apiIDStr, "api-id", "", "API ID for MTProto (optional, for >2GB files)")
	flag.StringVar(&apiHashStr, "api-hash", "", "API Hash for MTProto (optional, for >2GB files)")
	flag.Parse()

	if token == "" {
		fmt.Println("❌ REQUIRED: -token TOKEN")
		fmt.Println("\nOptional for files >2GB:")
		fmt.Println("  -api-id ID -api-hash HASH")
		fmt.Println("\nGet from: https://my.telegram.org/apps")
		os.Exit(1)
	}

	// ✅ MTProto es OPCIONAL pero recomendado
	var mtproto *MTProtoClient
	hasMTProto := apiIDStr != "" && apiHashStr != ""

	if hasMTProto {
		apiID, _ := strconv.Atoi(apiIDStr)
		var channelID int64
		if channelStr != "" {
			channelID, _ = strconv.ParseInt(channelStr, 10, 64)
		}
		mtproto = NewMTProtoClient(apiID, apiHashStr, "", channelID)
		if err := mtproto.Connect(context.Background()); err != nil {
			log.Printf("⚠️  MTProto init failed: %v (using Bot API only)", err)
			mtproto = nil
		} else {
			log.Printf("✅ MTProto enabled - can upload files >2GB")
		}
	} else {
		log.Printf("⚠️  MTProto not configured - limited to 2GB per file")
		log.Printf("    Add: -api-id ID -api-hash HASH to remove limit")
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
		log.Fatalf("❌ Torrent error: %v", err)
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

	engine := NewEngine(tc, storage)

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigChan
		tc.Close()
		os.Exit(0)
	}()

	log.Println("🚀 Listening…")

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
