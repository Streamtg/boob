package main

import (
	"bytes"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/anacrolix/torrent"
	"github.com/anacrolix/torrent/metainfo"
	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

const (
	progressInterval = 2 * time.Second
	stallTimeout     = 90 * time.Second
	uploadPause      = 200 * time.Millisecond
	maxRetries       = 3
	retryDelay       = 2 * time.Second
	botAPILimit      = 50 * 1024 * 1024
)

type Engine struct {
	client     *torrent.Client
	storage    string
	tasks      map[int64]*TaskStatus
	mu         sync.RWMutex
	magnetRe   *regexp.Regexp
	mtproto    *MTProtoClient
	cache      *FileCache
	bot        *tgbotapi.BotAPI
	chatID     int64
	maxSizeMB  int64
}

func NewEngine(cfg Config) (*Engine, error) {
	if err := os.MkdirAll(cfg.Storage, 0755); err != nil {
		return nil, fmt.Errorf("mkdir %s: %w", cfg.Storage, err)
	}
	tc := torrent.NewDefaultClientConfig()
	tc.DataDir = cfg.Storage
	tc.Seed = true
	tc.Debug = false
	tc.ListenPort = cfg.Port
	tc.NoDHT = false
	tc.DownloadRateLimiter = nil // Ilimitado por defecto

	client, err := torrent.NewClient(tc)
	if err != nil {
		return nil, fmt.Errorf("new client: %w", err)
	}

	return &Engine{
		client:    client,
		storage:   cfg.Storage,
		tasks:     make(map[int64]*TaskStatus),
		magnetRe:  regexp.MustCompile(`&.*$`),
		cache:     NewFileCache(cfg.Storage),
		maxSizeMB: cfg.MaxFileSize,
	}, nil
}

func (e *Engine) InitMTProto(chatID int64) {
	e.chatID = chatID
	e.mtproto = NewMTProtoClient()
	if err := e.mtproto.Start(chatID); err != nil {
		log.Printf("MTProto start error: %v", err)
		return
	}
	if !e.mtproto.WaitReady() {
		log.Println("MTProto no disponible")
		return
	}
	log.Println("MTProto listo!")
}

func (e *Engine) Close() {
	e.client.Close()
	if e.mtproto != nil {
		e.mtproto.Close()
	}
}

func (e *Engine) HandleMessage(bot *tgbotapi.BotAPI, chatID int64, replyTo int, text string) {
	e.bot = bot
	switch {
	case text == "/start" || text == "/help":
		e.cmdHelp(bot, chatID, replyTo)
	case text == "/status":
		e.cmdStatus(bot, chatID, replyTo)
	case text == "/cancel":
		e.cmdCancel(bot, chatID, replyTo)
	case text == "/mtproto":
		e.cmdMTProto(bot, chatID, replyTo)
	case text == "/cache":
		e.cmdCache(bot, chatID, replyTo)
	case strings.HasPrefix(strings.TrimSpace(text), "magnet:?"):
		e.startDownload(bot, chatID, replyTo, text)
	case isTorrentURL(text):
		e.startDownload(bot, chatID, replyTo, text)
	default:
		e.send(bot, chatID, replyTo, "*Comandos:* /help, /status, /cancel, /mtproto, /cache")
	}
}

func (e *Engine) cmdMTProto(bot *tgbotapi.BotAPI, chatID int64, replyTo int) {
	if e.mtproto != nil && e.mtproto.IsAuthed() {
		e.send(bot, chatID, replyTo, "*MTProto activado!* Archivos grandes OK.")
		return
	}
	e.send(bot, chatID, replyTo, "*Iniciando MTProto...*\nRevisa la terminal.")
	go func() {
		e.mtproto = NewMTProtoClient()
		if err := e.mtproto.Start(chatID); err != nil {
			e.send(bot, chatID, replyTo, fmt.Sprintf("*Error:* `%s`", err.Error()))
			return
		}
		if !e.mtproto.WaitReady() {
			e.send(bot, chatID, replyTo, "*Error: No autenticado*")
			return
		}
		e.send(bot, chatID, replyTo, "*MTProto listo!* Archivos grandes OK.")
	}()
}

func (e *Engine) cmdCache(bot *tgbotapi.BotAPI, chatID int64, replyTo int) {
	entries := e.cache.FindByTorrentName("")
	e.send(bot, chatID, replyTo, fmt.Sprintf("*Cache:* %d archivos cacheados.", len(entries)))
}

func (e *Engine) cmdHelp(bot *tgbotapi.BotAPI, chatID int64, replyTo int) {
	e.send(bot, chatID, replyTo,
		"*TeleTorrent Bot*\n\n"+
			"Envia magnet link o .torrent URL.\n"+
			"Archivos ORIGINALES, sin comprimir.\n\n"+
			"*/help* - ayuda\n"+
			"*/status* - progreso\n"+
			"*/cancel* - cancelar\n"+
			"*/mtproto* - activar MTProto\n"+
			"*/cache* - ver cache")
}

func (e *Engine) cmdStatus(bot *tgbotapi.BotAPI, chatID int64, replyTo int) {
	e.mu.RLock()
	task, ok := e.tasks[chatID]
	e.mu.RUnlock()
	if !ok {
		e.send(bot, chatID, replyTo, "*Sin descargas activas.*")
		return
	}
	task.mu.RLock()
	pct, name, errMsg := task.Progress, task.Name, task.Error
	started, done := task.StartedAt, task.Done
	down, total := task.Downloaded, task.TotalBytes
	task.mu.RUnlock()
	switch {
	case errMsg != "":
		e.send(bot, chatID, replyTo, fmt.Sprintf("*Error:* `%s`", errMsg))
	case done:
		e.send(bot, chatID, replyTo, fmt.Sprintf("*Completado:* `%s`", name))
	default:
		elapsed := time.Since(started).Round(time.Second)
		speed := ""
		if elapsed.Seconds() > 4 && down > 0 {
			speed = " | " + formatBytes(int64(float64(down)/elapsed.Seconds())) + "/s"
		}
		e.send(bot, chatID, replyTo,
			fmt.Sprintf("*Descargando:* `%s`\n`%.1f%%` | %s / %s%s\n%s",
				name, pct, formatBytes(down), formatBytes(total), speed, elapsed))
	}
}

func (e *Engine) cmdCancel(bot *tgbotapi.BotAPI, chatID int64, replyTo int) {
	e.mu.Lock()
	if _, ok := e.tasks[chatID]; !ok {
		e.mu.Unlock()
		e.send(bot, chatID, replyTo, "*Nada que cancelar.*")
		return
	}
	delete(e.tasks, chatID)
	e.mu.Unlock()
	e.send(bot, chatID, replyTo, "*Descarga cancelada.*")
}

func (e *Engine) startDownload(bot *tgbotapi.BotAPI, chatID int64, replyTo int, input string) {
	e.mu.Lock()
	if _, active := e.tasks[chatID]; active {
		e.mu.Unlock()
		e.send(bot, chatID, replyTo, "*Ya hay una descarga activa.* Usa /cancel.")
		return
	}
	e.tasks[chatID] = &TaskStatus{StartedAt: time.Now()}
	e.mu.Unlock()

	msg := tgbotapi.NewMessage(chatID, "*Iniciando descarga...*")
	msg.ReplyToMessageID = replyTo
	msg.ParseMode = "Markdown"
	sent, err := bot.Send(msg)
	if err != nil {
		e.mu.Lock()
		delete(e.tasks, chatID)
		e.mu.Unlock()
		return
	}
	statusMsgID := sent.MessageID

	var t *torrent.Torrent
	var addErr error

	if strings.HasPrefix(strings.TrimSpace(input), "magnet:?") {
		clean := strings.TrimSpace(input)
		if idx := strings.Index(clean, "&"); idx != -1 {
			clean = clean[:idx]
		}
		if !strings.HasPrefix(clean, "magnet:?xt=") {
			e.edit(bot, statusMsgID, chatID, "*Magnet invalido.*")
			cleanup(e, chatID)
			return
		}
		t, addErr = e.client.AddMagnet(clean)
	} else {
		data, fetchErr := fetchURL(input)
		if fetchErr != nil {
			e.edit(bot, statusMsgID, chatID, fmt.Sprintf("*Error fetch:* `%s`", fetchErr.Error()))
			cleanup(e, chatID)
			return
		}
		mi, parseErr := metainfo.Load(bytes.NewReader(data))
		if parseErr != nil {
			e.edit(bot, statusMsgID, chatID, fmt.Sprintf("*Error parse:* `%s`", parseErr.Error()))
			cleanup(e, chatID)
			return
		}
		t, addErr = e.client.AddTorrent(mi)
	}
	if addErr != nil {
		e.edit(bot, statusMsgID, chatID, fmt.Sprintf("*Error add:* `%s`", addErr.Error()))
		cleanup(e, chatID)
		return
	}

	select {
	case <-t.GotInfo():
	case <-time.After(30 * time.Second):
		e.edit(bot, statusMsgID, chatID, "*Timeout metadatos.*")
		t.Drop()
		cleanup(e, chatID)
		return
	}

	name := t.Name()
	totalLen := t.Length()
	e.mu.RLock()
	if task, ok := e.tasks[chatID]; ok {
		task.mu.Lock()
		task.InfoHash = t.InfoHash().HexString()
		task.Name = name
		task.TotalBytes = totalLen
		for _, f := range t.Files() {
			if p := f.Path(); p != "" {
				task.Files = append(task.Files, p)
			}
		}
		task.mu.Unlock()
	}
	e.mu.RUnlock()

	t.DownloadAll()
	e.edit(bot, statusMsgID, chatID,
		fmt.Sprintf("*Agregado:* `%s`\nTamano: `%s`\n*Buscando peers...*", name, formatBytes(totalLen)))
	go e.downloadLoop(bot, chatID, replyTo, t, statusMsgID)
}

func (e *Engine) downloadLoop(bot *tgbotapi.BotAPI, chatID int64, replyTo int, t *torrent.Torrent, statusMsgID int) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("[%d] panic: %v", chatID, r)
		}
	}()
	ticker := time.NewTicker(progressInterval)
	defer ticker.Stop()
	stallTicker := time.NewTicker(stallTimeout)
	defer stallTicker.Stop()
	var lastBytes int64
	lastTime := time.Now()

	e.mu.RLock()
	task := e.tasks[chatID]
	e.mu.RUnlock()
	if task == nil {
		return
	}

	for {
		e.mu.RLock()
		exists := e.tasks[chatID] != nil
		e.mu.RUnlock()
		if !exists {
			t.Drop()
			return
		}
		completed := t.BytesCompleted()
		total := t.Length()
		if completed >= total && total > 0 {
			break
		}
		select {
		case <-ticker.C:
			pct := float64(completed) / float64(total) * 100
			elapsed := time.Since(task.StartedAt).Seconds()
			speed := "conectando..."
			if elapsed > 4 && completed > 0 {
				speed = formatBytes(int64(float64(completed)/elapsed)) + "/s"
			}
			task.mu.Lock()
			task.Progress = pct
			task.Downloaded = completed
			task.mu.Unlock()
			bar := progressBar(int(pct), 20)
			e.edit(bot, statusMsgID, chatID,
				fmt.Sprintf("*Descargando:* `%s`\n%s `%.1f%%`\n%s | %s / %s",
					t.Name(), bar, pct, speed, formatBytes(completed), formatBytes(total)))
			if completed > lastBytes {
				lastBytes = completed
				lastTime = time.Now()
			}
		case <-stallTicker.C:
			if time.Since(lastTime) >= stallTimeout {
				log.Printf("[%d] stall", chatID)
				e.edit(bot, statusMsgID, chatID, "*Sin peers.*")
				e.mu.Lock()
				delete(e.tasks, chatID)
				e.mu.Unlock()
				t.Drop()
				return
			}
		}
	}
	task.mu.Lock()
	task.Done = true
	task.Progress = 100
	task.mu.Unlock()

	e.edit(bot, statusMsgID, chatID, "*Descarga completa - enviando archivos originales...*")

	e.mu.RLock()
	task2 := e.tasks[chatID]
	e.mu.RUnlock()
	if task2 == nil {
		return
	}

	task2.mu.RLock()
	files := make([]string, len(task2.Files))
	copy(files, task2.Files)
	task2.mu.RUnlock()

	uploaded, failed := 0, 0
	for _, fPath := range files {
		fullPath := filepath.Join(e.storage, fPath)
		fi, err := os.Stat(fullPath)
		if err != nil {
			log.Printf("[%d] stat %s: %v", chatID, fPath, err)
			failed++
			continue
		}
		if fi.IsDir() || fi.Size() == 0 {
			continue
		}

		cleanName := sanitizeFilename(filepath.Base(fPath))

		// Verificar cache primero
		if cached := e.cache.FindByMD5(fullPath); cached != nil {
			log.Printf("[%d] CACHE hit para %s, reusando file_id", chatID, cleanName)
			sob := tgbotapi.NewDocument(chatID, tgbotapi.FileID(cached.TgFileID))
			sob.ReplyToMessageID = replyTo
			if _, err := bot.Send(sob); err == nil {
				uploaded++
				time.Sleep(uploadPause)
				continue
			}
		}

		// Archivos pequeños -> Bot API
		if fi.Size() <= botAPILimit {
			log.Printf("[%d] BotAPI: %s (%s)", chatID, cleanName, formatBytes(fi.Size()))
			bot.Send(tgbotapi.NewChatAction(chatID, tgbotapi.ChatUploadDocument))
			sent := false
			for attempt := 0; attempt < maxRetries; attempt++ {
				doc := tgbotapi.NewDocument(chatID, tgbotapi.FilePath(fullPath))
				doc.ReplyToMessageID = replyTo
				if _, sendErr := bot.Send(doc); sendErr == nil {
					sent = true
					break
				} else {
					log.Printf("[%d] attempt %d: %v", chatID, attempt+1, sendErr)
					if strings.Contains(sendErr.Error(), "429") {
						time.Sleep(retryDelay * time.Duration(attempt+1))
					} else {
						break
					}
				}
			}
			if sent {
				uploaded++
			} else {
				failed++
			}
			time.Sleep(uploadPause)
			continue
		}

		// Archivos grandes -> MTProto
		if e.mtproto != nil && e.mtproto.IsAuthed() {
			log.Printf("[%d] MTProto: %s (%s)", chatID, cleanName, formatBytes(fi.Size()))
			e.edit(bot, statusMsgID, chatID,
				fmt.Sprintf("*Enviando (MTProto):* `%s`\n%s", cleanName, formatBytes(fi.Size())))

			sendErr := e.mtproto.SendLargeFile(fullPath, cleanName, replyTo)
			if sendErr != nil {
				log.Printf("[%d] MTProto error: %v", chatID, sendErr)
				e.send(bot, chatID, replyTo, fmt.Sprintf("*Error:* `%s`", sendErr.Error()))
				failed++
			} else {
				uploaded++
			}
		} else {
			e.send(bot, chatID, replyTo,
				fmt.Sprintf("*Archivo grande:* `%s` (%s)\nUsa /mtproto", cleanName, formatBytes(fi.Size())))
			failed++
		}
		time.Sleep(uploadPause)
	}

	e.mu.Lock()
	delete(e.tasks, chatID)
	e.mu.Unlock()

	summary := fmt.Sprintf("*Completado!* %d archivo(s) enviados.", uploaded)
	if failed > 0 {
		summary = fmt.Sprintf("*Completado!* %d enviados, %d fallaron.", uploaded, failed)
	}
	bot.Send(tgbotapi.NewMessage(chatID, summary))
	bot.Request(tgbotapi.NewDeleteMessage(chatID, statusMsgID))
}

func sanitizeFilename(name string) string {
	r := strings.NewReplacer("[", "", "]", "", "{", "", "}", "", "(", "", ")", "", "|", "-")
	return strings.TrimSpace(r.Replace(name))
}

func (e *Engine) send(bot *tgbotapi.BotAPI, chatID int64, replyTo int, text string) {
	msg := tgbotapi.NewMessage(chatID, text)
	msg.ReplyToMessageID = replyTo
	msg.ParseMode = "Markdown"
	if _, err := bot.Send(msg); err != nil {
		log.Printf("[%d] send err: %v", chatID, err)
	}
}

func (e *Engine) edit(bot *tgbotapi.BotAPI, msgID int, chatID int64, text string) {
	edit := tgbotapi.NewEditMessageText(chatID, msgID, text)
	edit.ParseMode = "Markdown"
	if _, err := bot.Request(edit); err != nil {
		if !strings.Contains(err.Error(), "400") {
			log.Printf("[%d] edit err: %v", chatID, err)
		}
	}
}

func cleanup(e *Engine, chatID int64) {
	e.mu.Lock()
	delete(e.tasks, chatID)
	e.mu.Unlock()
}

func isTorrentURL(s string) bool {
	s = strings.TrimSpace(s)
	return (strings.HasPrefix(s, "http://") || strings.HasPrefix(s, "https://")) &&
		strings.HasSuffix(s, ".torrent")
}

func fetchURL(rawURL string) ([]byte, error) {
	c := &http.Client{Timeout: 60 * time.Second}
	resp, err := c.Get(rawURL)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
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
	default:
		return strconv.FormatInt(n, 10) + " B"
	}
}

func progressBar(percent, width int) string {
	if percent < 0 {
		percent = 0
	}
	if percent > 100 {
		percent = 100
	}
	filled := percent * width / 100
	return strings.Repeat("=", filled) + strings.Repeat("-", width-filled)
}
