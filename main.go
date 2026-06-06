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
			return 0, fmt.Errorf("api error code %d", r.ErrorCode)
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

func (b *Bot) uploadFileLarge(chatID int64, filePath string, caption string) error {
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

	log.Printf("[%d] 📤 Subiendo: %s (%s)", chatID, filename, formatBytes(fileSize))

	client := &http.Client{
		Timeout: 30 * time.Minute,
		Transport: &http.Transport{
			MaxIdleConns:        10,
			MaxIdleConnsPerHost: 10,
			IdleConnTimeout:     300 * time.Second,
		},
	}
	defer client.CloseIdleConnections()

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

		buf := make([]byte, 1024*1024)
		lastProgress := int64(0)

		for {
			n, readErr := file.Read(buf)
			if n > 0 {
				if _, writeErr := part.Write(buf[:n]); writeErr != nil {
					errChan <- writeErr
					return
				}
				lastProgress += int64(n)
				pct := float64(lastProgress) / float64(fileSize) * 100
				if int64(pct)%10 == 0 || pct > 99 {
					log.Printf("[%d] ⬆️ %.1f%%", chatID, pct)
				}
			}
			if readErr != nil {
				if readErr != io.EOF {
					errChan <- readErr
				}
				break
			}
		}

		if err := writer.Close(); err != nil {
			errChan <- err
			return
		}

		errChan <- nil
	}()

	req, err := http.NewRequest("POST", b.baseURL+"/sendDocument", pr)
	if err != nil {
		return err
	}

	req.Header.Set("Content-Type", writer.FormDataContentType())

	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		resp, err := client.Do(req)
		if err != nil {
			lastErr = err
			log.Printf("[%d] Intento %d fallido: %v", chatID, attempt+1, err)
			if attempt < 2 {
				time.Sleep(10 * time.Second)
				file.Seek(0, 0)
			}
			continue
		}
		defer resp.Body.Close()

		respData, _ := io.ReadAll(resp.Body)

		select {
		case werr := <-errChan:
			if werr != nil {
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

		if !result.OK {
			if result.ErrorCode == 413 {
				return fmt.Errorf("archivo demasiado grande")
			}
			lastErr = fmt.Errorf("upload error: %s", result.Description)
			if attempt < 2 {
				log.Printf("[%d] Reintentando (intento %d/3)…", chatID, attempt+1)
				time.Sleep(10 * time.Second)
				continue
			}
		} else {
			log.Printf("[%d] ✅ Subido: %s", chatID, filename)
			return nil
		}
	}

	return lastErr
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
	totalWritten := int64(0)

	for {
		n, readErr := limitedReader.Read(buf)
		if n > 0 {
			if _, writeErr := outFile.Write(buf[:n]); writeErr != nil {
				outFile.Close()
				os.Remove(fullPath)
				return false, fmt.Errorf("write: %w", writeErr)
			}
			totalWritten += int64(n)
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

	log.Printf("[%d] 💾 guardado: %s", chatID, fullPath)

	err = bot.uploadFileLarge(chatID, fullPath, fmt.Sprintf("📁 %s", safeName))
	if err != nil {
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

	bot.sendMsg(targetChat,
		fmt.Sprintf("📤 *Subiendo:* %d archivo(s)", totalFiles), 0, true)

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

		success, err := e.saveAndUploadFile(bot, chatID, torrentFile, fe, safeName)
		if success {
			ok++
			bot.sendMsg(targetChat,
				fmt.Sprintf("✅ *Subido:* `%s`", safeName), 0, true)
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

*Comandos:*
/start /help — ayuda
/status — estado
/cancel — cancelar

*Características:*
✅ Descarga de torrents
✅ Subida completa sin división
✅ Soporte hasta 2GB por archivo
✅ Reintentos automáticos`

func main() {
	var token, storage, channelStr string
	flag.StringVar(&token, "token", "", "Bot token")
	flag.StringVar(&storage, "storage", "./downloads", "Carpeta de descargas")
	flag.StringVar(&channelStr, "channel", "", "ID del canal destino")
	flag.Parse()

	if token == "" {
		fmt.Println("❌ Uso: ./bot -token TOKEN [-channel ID_CANAL]")
		fmt.Println("\n📌 Obtén tu token en: https://t.me/BotFather")
		os.Exit(1)
	}

	var channelID int64
	if channelStr != "" {
		var err error
		channelID, err = strconv.ParseInt(channelStr, 10, 64)
		if err != nil {
			log.Fatalf("❌ ID de canal inválido: %s", channelStr)
		}
		log.Printf("📢 Canal destino: %d", channelID)
	}

	if err := os.MkdirAll(storage, 0755); err != nil {
		log.Fatalf("❌ No se puede crear directorio: %v", err)
	}
	log.Printf("📁 Directorio: %s", storage)

	cfg := torrent.NewDefaultClientConfig()
	cfg.DataDir = storage
	cfg.Seed = true
	cfg.DisableIPv6 = true

	tc, err := torrent.NewClient(cfg)
	if err != nil {
		log.Fatalf("❌ Error torrent: %v", err)
	}
	defer tc.Close()

	bot := NewBot(token, channelID)
	data, err := bot.api("getMe", nil)
	if err != nil {
		log.Fatalf("❌ Error autenticación: %v", err)
	}

	var me struct {
		OK     bool `json:"ok"`
		Result struct {
			Username string `json:"username"`
		} `json:"result"`
	}
	json.Unmarshal(data, &me)

	if !me.OK {
		log.Fatalf("❌ Token inválido o expirado")
	}

	log.Printf("✅ Bot conectado: @%s", me.Result.Username)

	engine := NewEngine(tc, storage)

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigChan
		log.Println("⚡ Cerrando bot…")
		tc.Close()
		os.Exit(0)
	}()

	log.Println("🚀 Bot escuchando mensajes…")

	offset := 0
	for {
		data, err := bot.api("getUpdates", map[string]string{
			"timeout": "120",
			"offset":  strconv.Itoa(offset),
		})
		if err != nil {
			log.Printf("⚠️  Error getUpdates: %v", err)
			time.Sleep(5 * time.Second)
			continue
		}

		var ups struct {
			OK     bool     `json:"ok"`
			Result []Update `json:"result"`
		}

		if err := json.Unmarshal(data, &ups); err != nil {
			log.Printf("⚠️  Error parse updates: %v", err)
			continue
		}

		if !ups.OK {
			log.Printf("⚠️  API error")
			time.Sleep(5 * time.Second)
			continue
		}

		for _, u := range ups.Result {
			offset = u.UpdateID + 1

			if u.Message != nil && u.Message.Text != "" {
				go engine.Handle(bot, u.Message.Chat.ID, u.Message.MessageID, u.Message.Text)
			}
		}
	}
}
