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
			Timeout: 180 * time.Second,
			Transport: &http.Transport{
				MaxIdleConns:        200,
				MaxIdleConnsPerHost: 50,
				IdleConnTimeout:     200 * time.Second,
			},
		},
	}
}

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
	CallbackQuery *struct {
		ID      string `json:"id"`
		Data    string `json:"data"`
		Message struct {
			MessageID int `json:"message_id"`
			Chat      struct {
				ID int64 `json:"id"`
			} `json:"chat"`
		} `json:"message"`
	} `json:"callback_query"`
}

// ── Enviar mensaje con reintentos ──
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
			log.Printf("[%d] sendMsg error: %v", chatID, err)
			time.Sleep(time.Duration((attempt+1)*2) * time.Second)
			continue
		}

		var r struct {
			OK     bool   `json:"ok"`
			Result struct {
				MessageID int `json:"message_id"`
			} `json:"result"`
			ErrorCode int `json:"error_code"`
		}
		json.Unmarshal(data, &r)

		if r.ErrorCode == 429 {
			// Rate limit
			log.Printf("[%d] Rate limit (429), esperando 25s…", chatID)
			time.Sleep(25 * time.Second)
			continue
		}

		if !r.OK {
			log.Printf("[%d] sendMsg API error: %s", chatID, string(data))
			return 0, fmt.Errorf("api error")
		}
		return r.Result.MessageID, nil
	}

	return 0, fmt.Errorf("failed after 5 attempts")
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
	body, _ := json.Marshal(map[string]interface{}{
		"chat_id": chatID,
		"action":  action,
	})
	b.post("sendChatAction", "application/json", strings.NewReader(string(body)))
}

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
			errChan <- fmt.Errorf("create form file: %w", err)
			return
		}
		buf := make([]byte, 1024*1024) // 1 MB chunks
		for {
			n, readErr := file.Read(buf)
			if n > 0 {
				if _, writeErr := part.Write(buf[:n]); writeErr != nil {
					errChan <- fmt.Errorf("write multipart: %w", writeErr)
					return
				}
			}
			if readErr != nil {
				if readErr != io.EOF {
					errChan <- fmt.Errorf("read file: %w", readErr)
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

	req, err := http.NewRequest("POST", b.baseURL+"/sendDocument", pr)
	if err != nil {
		return fmt.Errorf("new request: %w", err)
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())

	log.Printf("[%d] 📤 Subiendo: %s (%s)", chatID, filename, formatBytes(fi.Size()))

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("do request: %w", err)
	}
	defer resp.Body.Close()

	respData, _ := io.ReadAll(resp.Body)

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

	log.Printf("[%d] ✅ Subido: %s", chatID, filename)
	return nil
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
		bot.sendMsg(chatID, "⏳ Ya hay una descarga activa. Usa /cancel primero.", replyTo, true)
		return
	}
	e.tasks[chatID] = &Task{StartedAt: time.Now()}
	e.mu.Unlock()

	t, err := e.tc.AddMagnet(strings.TrimSpace(input))
	if err != nil {
		e.mu.Lock()
		delete(e.tasks, chatID)
		e.mu.Unlock()
		bot.sendMsg(chatID, fmt.Sprintf("❌ *Error magnet:* `%s`", err.Error()), 0, true)
		return
	}
	log.Printf("[%d] magnet agregado: %s", chatID, t.Name())
	bot.sendMsg(chatID,
		fmt.Sprintf("📥 *Agregado:* `%s`\n⏳ *Esperando metadata…*", t.Name()), 0, true)
	go e.downloadLoop(bot, chatID, replyTo, t)
}

func (e *Engine) startDownloadURL(bot *Bot, chatID int64, replyTo int, input string) {
	e.mu.Lock()
	if _, active := e.tasks[chatID]; active {
		e.mu.Unlock()
		bot.sendMsg(chatID, "⏳ Ya hay una descarga activa. Usa /cancel primero.", replyTo, true)
		return
	}
	e.tasks[chatID] = &Task{StartedAt: time.Now()}
	e.mu.Unlock()

	bot.sendAction(chatID, "typing")
	data, fetchErr := fetchURL(input)
	if fetchErr != nil {
		e.mu.Lock()
		delete(e.tasks, chatID)
		e.mu.Unlock()
		bot.sendMsg(chatID, fmt.Sprintf("❌ *Error descarga:* `%s`", fetchErr.Error()), 0, true)
		return
	}
	mi, metaErr := metainfo.Load(strings.NewReader(string(data)))
	if metaErr != nil {
		e.mu.Lock()
		delete(e.tasks, chatID)
		e.mu.Unlock()
		bot.sendMsg(chatID, fmt.Sprintf("❌ *Error parse:* `%s`", metaErr.Error()), 0, true)
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
	log.Printf("[%d] torrent URL: %s", chatID, t.Name())
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
		bot.sendMsg(chatID, "❌ *Timeout: sin metadata tras 2 minutos.*", 0, true)
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
	torrentFiles := t.Files()

	if len(torrentFiles) > 0 {
		for _, f := range torrentFiles {
			files = append(files, FileEntry{
				DisplayPath: f.DisplayPath(),
				Length:      f.Length(),
			})
		}
	} else {
		files = []FileEntry{{
			DisplayPath: name,
			Length:      total,
		}}
	}

	e.mu.Lock()
	e.tasks[chatID].Name = name
	e.tasks[chatID].Files = files
	e.tasks[chatID].TotalBytes = total
	e.tasks[chatID].Torrent = t
	e.mu.Unlock()

	t.DownloadAll()
	log.Printf("[%d] descarga iniciada: %s (%s)", chatID, name, formatBytes(total))

	statusID, _ := bot.sendMsg(chatID,
		fmt.Sprintf("📥 *Descargando:* `%s`\n0 B / %s", name, formatBytes(total)),
		0, true)

	ticker := time.NewTicker(3 * time.Second)
	stall := time.NewTicker(120 * time.Second)
	defer ticker.Stop()
	defer stall.Stop()

	var lastBytes int64
	lastTime := time.Now()

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
				lastTime = time.Now()
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

		case <-stall.C:
			if time.Since(lastTime) >= 120*time.Second {
				e.mu.Lock()
				if e.tasks[chatID] != nil {
					e.mu.Unlock()
					bot.sendMsg(chatID,
						"❌ *Sin peers por 2 minutos. Descarga detenida.*", 0, true)
				} else {
					e.mu.Unlock()
				}
				return
			}
		}
	}

	log.Printf("[%d] descarga completa, esperando flush…", chatID)
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

	bot.sendMsg(chatID,
		fmt.Sprintf("✅ *Descarga completa:* `%s`\n⏳ *Subiendo a Telegram…*", name),
		0, true)

	e.uploadFiles(bot, chatID, torrentRef, &taskCopy)
}

// ✅ FIX CRÍTICO: Leer COMPLETAMENTE del torrent con buffer GRANDE
func (e *Engine) saveAndUploadFile(
	bot *Bot,
	chatID int64,
	torrentFile *torrent.File,
	fe FileEntry,
	safeName string,
	caption string,
) (bool, error) {

	fullPath := filepath.Join(e.storage, fe.DisplayPath)

	if err := os.MkdirAll(filepath.Dir(fullPath), 0755); err != nil {
		return false, fmt.Errorf("mkdir: %w", err)
	}

	outFile, err := os.Create(fullPath)
	if err != nil {
		return false, fmt.Errorf("crear archivo: %w", err)
	}
	defer outFile.Close()

	reader := torrentFile.NewReader()
	defer reader.Close()

	// ✅ FIX: Usar io.CopyBuffer con buffer GRANDE (4 MB)
	largeBuf := make([]byte, 4*1024*1024)
	totalWritten, copyErr := io.CopyBuffer(outFile, reader, largeBuf)

	if copyErr != nil {
		os.Remove(fullPath)
		return false, fmt.Errorf("copy: %w", copyErr)
	}

	if err := outFile.Sync(); err != nil {
		os.Remove(fullPath)
		return false, fmt.Errorf("sync: %w", err)
	}

	// Verificar integridad
	if totalWritten != fe.Length {
		os.Remove(fullPath)
		log.Printf("[%d] ERROR: escrito %d bytes pero esperaba %d",
			chatID, totalWritten, fe.Length)
		return false, fmt.Errorf(
			"incompleto: %s / %s",
			formatBytes(totalWritten),
			formatBytes(fe.Length),
		)
	}

	log.Printf("[%d] 💾 guardado: %s (%s)", chatID, fullPath, formatBytes(totalWritten))

	// Subir a Telegram
	if err := bot.uploadFile(chatID, fullPath, caption); err != nil {
		return false, err
	}

	// Eliminar tras subida exitosa
	if err := os.Remove(fullPath); err != nil {
		log.Printf("[%d] aviso: no se pudo eliminar %s", chatID, fullPath)
	} else {
		log.Printf("[%d] 🗑 eliminado: %s", chatID, fullPath)
	}

	return true, nil
}

// ✅ FIX: Controlar rate limit con delays entre mensajes
func (e *Engine) uploadFiles(
	bot *Bot,
	chatID int64,
	torrentRef *torrent.Torrent,
	task *Task,
) {
	e.mu.Lock()
	delete(e.tasks, chatID)
	e.mu.Unlock()

	files := task.Files
	torrentName := task.Name
	targetChat := bot.getChatID(chatID)

	if len(files) == 0 && torrentRef != nil {
		for _, f := range torrentRef.Files() {
			files = append(files, FileEntry{
				DisplayPath: f.DisplayPath(),
				Length:      f.Length(),
			})
		}
	}
	if len(files) == 0 && torrentRef != nil {
		files = []FileEntry{{
			DisplayPath: torrentRef.Name(),
			Length:      torrentRef.Length(),
		}}
	}

	totalFiles := len(files)
	ok, fail := 0, 0

	bot.sendMsg(targetChat,
		fmt.Sprintf("📤 *Iniciando subida:* %d archivo(s)", totalFiles),
		0, true)

	// ✅ Delay de 2 segundos entre cada archivo para evitar rate limit
	for i, fe := range files {
		safeName := filepath.Base(fe.DisplayPath)

		const maxSize = int64(2000) * 1024 * 1024
		if fe.Length > maxSize {
			bot.sendMsg(targetChat,
				fmt.Sprintf("⚠️ *Omitido (>2GB):* `%s`", safeName),
				0, true)
			fail++
			time.Sleep(2 * time.Second) // ← Delay
			continue
		}

		caption := fmt.Sprintf("[%d/%d] %s", i+1, totalFiles, safeName)

		log.Printf("[%d] 📤 [%d/%d] %s (%s)",
			chatID, i+1, totalFiles, safeName, formatBytes(fe.Length))

		bot.sendAction(targetChat, "upload_document")

		progMsg, _ := bot.sendMsg(targetChat,
			fmt.Sprintf("📤 *Subiendo* [%d/%d] `%s`",
				i+1, totalFiles, safeName),
			0, true)

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
				fmt.Sprintf("❌ *No encontrado:* `%s`", safeName),
				0, true)
			fail++
			time.Sleep(2 * time.Second) // ← Delay
			continue
		}

		success, uploadErr := e.saveAndUploadFile(
			bot, chatID, torrentFile, fe, safeName, caption)

		if success {
			ok++
			if progMsg > 0 {
				bot.editMsg(targetChat, progMsg,
					fmt.Sprintf("✅ *Subido* [%d/%d] `%s`",
						i+1, totalFiles, safeName))
			}
		} else {
			bot.sendMsg(targetChat,
				fmt.Sprintf("❌ *Falló:* `%s` — %v", safeName, uploadErr),
				0, true)
			fail++
		}

		// ✅ Delay entre archivos
		time.Sleep(2 * time.Second)
	}

	// Resumen final
	if fail > 0 {
		bot.sendMsg(targetChat,
			fmt.Sprintf("🎉 *Listo!* ✅ %d ❌ %d", ok, fail),
			0, true)
	} else {
		bot.sendMsg(targetChat,
			fmt.Sprintf("🎉 *¡Éxito!* ✅ %d archivos", ok),
			0, true)
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

Envíame un magnet link o URL .torrent

*Comandos:*
/start /help — ayuda
/status      — estado
/cancel      — cancelar

*Soportado:*
• Magnets
• URLs .torrent`

func main() {
	var token, storage, channelStr string
	flag.StringVar(&token, "token", "", "Bot token")
	flag.StringVar(&storage, "storage", "./downloads", "Carpeta")
	flag.StringVar(&channelStr, "channel", "", "ID canal")
	flag.Parse()

	if token == "" {
		fmt.Println("❌ Uso: ./bot -token TOKEN [-channel ID]")
		os.Exit(1)
	}

	var channelID int64
	if channelStr != "" {
		channelID, _ = strconv.ParseInt(channelStr, 10, 64)
		log.Printf("📢 Canal: %s", channelStr)
	}

	os.MkdirAll(storage, 0755)

	cfg := torrent.NewDefaultClientConfig()
	cfg.DataDir = storage
	cfg.Seed = true

	tc, _ := torrent.NewClient(cfg)
	defer tc.Close()

	bot := NewBot(token, channelID)
	data, _ := bot.api("getMe", nil)
	var me struct {
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
		log.Println("⚡ Cerrando…")
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
