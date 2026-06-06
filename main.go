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
// Telegram Bot API
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

// ─────────────────────────────────────────────────────────────────────────────
// Types
// ─────────────────────────────────────────────────────────────────────────────

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
		buf := make([]byte, 65536)
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

	// Esperar metadata
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

	// ── Recopilar archivos ──────────────────────────────────────────────────
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
		// Single-file torrent sin Files()
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

// ─────────────────────────────────────────────────────────────────────────────
// saveAndUploadFile — guarda en disco y sube a Telegram
// ─────────────────────────────────────────────────────────────────────────────

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

	reader := torrentFile.NewReader()
	reader.SetResponsive()
	reader.Seek(0, io.SeekStart)

	buf := make([]byte, 65536)
	totalWritten := int64(0)

	for {
		n, readErr := reader.Read(buf)
		if n > 0 {
			nw, writeErr := outFile.Write(buf[:n])
			if writeErr != nil {
				reader.Close()
				outFile.Close()
				os.Remove(fullPath)
				return false, fmt.Errorf("escribir disco: %w", writeErr)
			}
			totalWritten += int64(nw)
		}
		if readErr != nil {
			if readErr != io.EOF {
				reader.Close()
				outFile.Close()
				os.Remove(fullPath)
				return false, fmt.Errorf("leer torrent: %w", readErr)
			}
			break
		}
	}

	reader.Close()

	if err := outFile.Close(); err != nil {
		os.Remove(fullPath)
		return false, fmt.Errorf("cerrar archivo: %w", err)
	}

	// Verificar integridad
	if totalWritten != fe.Length {
		os.Remove(fullPath)
		return false, fmt.Errorf(
			"archivo incompleto: %s de %s escritos",
			formatBytes(totalWritten),
			formatBytes(fe.Length),
		)
	}

	log.Printf("[%d] 💾 guardado: %s (%s)", chatID, fullPath, formatBytes(totalWritten))

	// Subir a Telegram
	if err := bot.uploadFile(chatID, fullPath, caption); err != nil {
		// No eliminar si falla la subida (para debug)
		return false, err
	}

	// Eliminar solo tras subida exitosa
	if err := os.Remove(fullPath); err != nil {
		log.Printf("[%d] aviso: no se pudo eliminar %s: %v", chatID, fullPath, err)
	} else {
		log.Printf("[%d] 🗑 eliminado: %s", chatID, fullPath)
	}

	return true, nil
}

// ─────────────────────────────────────────────────────────────────────────────
// uploadFiles — sube todos los archivos del torrent
// ─────────────────────────────────────────────────────────────────────────────

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

	// ── Fix: asegurar que tengamos archivos ────────────────────────────────
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
		fmt.Sprintf("📤 *Iniciando subida:* %d archivo(s)\n📁 `%s`",
			totalFiles, torrentName),
		0, true)

	for i, fe := range files {
		safeName := filepath.Base(fe.DisplayPath)

		// Límite Telegram: 2 GB
		const maxSize = int64(2000) * 1024 * 1024
		if fe.Length > maxSize {
			log.Printf("[%d] archivo muy grande: %s (%s)", chatID, safeName, formatBytes(fe.Length))
			bot.sendMsg(targetChat,
				fmt.Sprintf("⚠️ *Omitido (>2GB):* `%s`\n📊 %s",
					safeName, formatBytes(fe.Length)),
				0, true)
			fail++
			continue
		}

		caption := fmt.Sprintf("[%d/%d] %s — %s",
			i+1, totalFiles, torrentName, safeName)

		log.Printf("[%d] 📤 [%d/%d] %s (%s)",
			chatID, i+1, totalFiles, safeName, formatBytes(fe.Length))

		bot.sendAction(targetChat, "upload_document")

		progMsg, _ := bot.sendMsg(targetChat,
			fmt.Sprintf("📤 *Subiendo* `%s`\n📦 %d/%d • %s",
				safeName, i+1, totalFiles, formatBytes(fe.Length)),
			0, true)

		// ── Buscar archivo en torrent ──────────────────────────────────────
		var torrentFile *torrent.File
		if torrentRef != nil {
			torrentFiles := torrentRef.Files()
			switch len(torrentFiles) {
			case 0:
				// Sin archivos — no se puede subir
			case 1:
				// Single-file: usar directamente
				torrentFile = torrentFiles[0]
			default:
				// Multi-file: buscar por DisplayPath
				for _, f := range torrentFiles {
					if f.DisplayPath() == fe.DisplayPath {
						torrentFile = f
						break
					}
				}
			}
		}

		if torrentFile == nil {
			log.Printf("[%d] archivo no encontrado en torrent: %s", chatID, safeName)
			bot.sendMsg(targetChat,
				fmt.Sprintf("❌ *No encontrado en torrent:* `%s`", safeName),
				0, true)
			if progMsg > 0 {
				bot.editMsg(targetChat, progMsg,
					fmt.Sprintf("❌ *No encontrado:* `%s`", safeName))
			}
			fail++
			continue
		}

		success, uploadErr := e.saveAndUploadFile(
			bot, chatID, torrentFile, fe, safeName, caption)

		if success {
			ok++
			if progMsg > 0 {
				bot.editMsg(targetChat, progMsg,
					fmt.Sprintf("✅ *Subido:* `%s` (%d/%d)",
						safeName, i+1, totalFiles))
			}
		} else {
			log.Printf("[%d] fallo subida: %v", chatID, uploadErr)
			bot.sendMsg(targetChat,
				fmt.Sprintf("❌ *Falló:* `%s`\n`%v`", safeName, uploadErr),
				0, true)
			if progMsg > 0 {
				bot.editMsg(targetChat, progMsg,
					fmt.Sprintf("❌ *Falló:* `%s`", safeName))
			}
			fail++
		}

		time.Sleep(500 * time.Millisecond)
	}

	// Resumen final
	if fail > 0 {
		bot.sendMsg(targetChat,
			fmt.Sprintf("🎉 *Listo!*\n✅ %d subidos\n❌ %d fallaron\n📁 `%s`",
				ok, fail, torrentName),
			0, true)
	} else {
		bot.sendMsg(targetChat,
			fmt.Sprintf("🎉 *¡Todo subido!*\n✅ %d archivo(s)\n📁 `%s`",
				ok, torrentName),
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

Envíame un magnet link o URL .torrent y lo descargo y subo al canal.

*Comandos:*
/start /help — este mensaje
/status      — estado de descarga activa
/cancel      — cancelar descarga actual

*Formatos soportados:*
• Magnet links _(magnet:?xt=…)_
• URL .torrent _(https://…/archivo.torrent)_

*Límites:*
• Máximo 2 GB por archivo (límite de Telegram)`

// ─────────────────────────────────────────────────────────────────────────────
// Main
// ─────────────────────────────────────────────────────────────────────────────

func main() {
	var token, storage, channelStr string
	flag.StringVar(&token, "token", "", "Bot token de @BotFather")
	flag.StringVar(&storage, "storage", "./downloads", "Directorio de descargas")
	flag.StringVar(&channelStr, "channel", "", "ID del canal destino (ej: -1003213143951)")
	flag.Parse()

	if token == "" {
		fmt.Println("❌  Uso: ./tele-torrent-bot -token TOKEN [-channel ID_CANAL]")
		fmt.Println("   Obtén tu token en https://t.me/BotFather")
		os.Exit(1)
	}

	var channelID int64
	if channelStr != "" {
		var err error
		channelID, err = strconv.ParseInt(channelStr, 10, 64)
		if err != nil {
			log.Fatalf("❌  ID de canal inválido: %s", channelStr)
		}
		log.Printf("📢 Canal destino: %s", channelStr)
	}

	if err := os.MkdirAll(storage, 0755); err != nil {
		log.Fatalf("❌  No se puede crear directorio: %v", err)
	}
	log.Printf("📁 Directorio de descargas: %s", storage)

	// ── Configurar cliente torrent ─────────────────────────────────────────
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
		log.Fatalf("❌  Error cliente torrent: %v", err)
	}
	defer tc.Close()
	log.Printf("✅  Cliente torrent listo")

	// ── Conectar bot ───────────────────────────────────────────────────────
	bot := NewBot(token, channelID)
	data, err := bot.api("getMe", nil)
	if err != nil {
		log.Fatalf("❌  Error auth Telegram: %v", err)
	}
	var me struct {
		OK     bool `json:"ok"`
		Result struct {
			Username string `json:"username"`
			ID       int64  `json:"id"`
		} `json:"result"`
	}
	json.Unmarshal(data, &me)
	if !me.OK {
		log.Fatalf("❌  Token inválido")
	}
	log.Printf("✅  Bot conectado: @%s (ID: %d)", me.Result.Username, me.Result.ID)

	engine := NewEngine(tc, storage)

	// ── Señales de sistema ─────────────────────────────────────────────────
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigChan
		log.Println("⚡ Cerrando bot…")
		tc.Close()
		os.Exit(0)
	}()

	log.Println("🚀 Bot escuchando mensajes…")

	// ── Loop principal ─────────────────────────────────────────────────────
	offset := 0
	for {
		data, err := bot.api("getUpdates", map[string]string{
			"timeout":         "120",
			"offset":          strconv.Itoa(offset),
			"allowed_updates": `["message","callback_query"]`,
		})
		if err != nil {
			log.Printf("⚠️  getUpdates: %v — reintentando en 5s", err)
			time.Sleep(5 * time.Second)
			continue
		}

		var ups struct {
			OK     bool     `json:"ok"`
			Result []Update `json:"result"`
		}
		if err := json.Unmarshal(data, &ups); err != nil {
			log.Printf("⚠️  parse updates: %v", err)
			time.Sleep(5 * time.Second)
			continue
		}

		if !ups.OK {
			log.Printf("⚠️  API devolvió !ok: %s", string(data))
			time.Sleep(5 * time.Second)
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
				go engine.Handle(bot, chatID, u.Message.MessageID, text)

			} else if u.CallbackQuery != nil {
				chatID := u.CallbackQuery.Message.Chat.ID
				bot.answerCallback(u.CallbackQuery.ID)
				go engine.Handle(bot, chatID,
					u.CallbackQuery.Message.MessageID,
					u.CallbackQuery.Data)
			}
		}
	}
}
