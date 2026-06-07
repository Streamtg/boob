package main

import (
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/anacrolix/torrent"
	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

// ─────────────────────────────────────────────────────────────────────────────
// Constantes globales
// ─────────────────────────────────────────────────────────────────────────────
const (
	ProgressUpdateInterval = 2 * time.Second
	StallTimeout           = 90 * time.Second
	UploadPause            = 500 * time.Millisecond
	MaxRetries             = 5
	RetryDelay             = 2 * time.Second
)

// ─────────────────────────────────────────────────────────────────────────────
// Config — parámetros de línea de comandos
// ─────────────────────────────────────────────────────────────────────────────
type Config struct {
	Token     string
	ChannelID int64
	Storage   string
	Port      int
}

func parseFlags() Config {
	var (
		token   = flag.String("token", "", "Telegram Bot Token (requerido)")
		channel = flag.String("channel", "", "Telegram Channel/Group ID (opcional)")
		storage = flag.String("storage", "./downloads", "Directorio de descargas")
		port    = flag.Int("port", 0, "Puerto DHT (0 = aleatorio)")
	)
	flag.Parse()

	if *token == "" {
		log.Fatal("Token requerido. Usa: -token \"TU_TOKEN\"")
	}

	cfg := Config{
		Token:   *token,
		Storage: *storage,
		Port:    *port,
	}

	if *channel != "" {
		chID, err := strconv.ParseInt(*channel, 10, 64)
		if err != nil {
			log.Fatalf("Channel ID invalido: %v", err)
		}
		cfg.ChannelID = chID
	}

	return cfg
}

// ─────────────────────────────────────────────────────────────────────────────
// TaskStatus — seguimiento de descarga por chat
// ─────────────────────────────────────────────────────────────────────────────
type TaskStatus struct {
	InfoHash   string
	Name       string
	Progress   float64
	Done       bool
	Files      []string
	TotalBytes int64
	Downloaded int64
	Error      string
	StartedAt  time.Time
	mu         sync.RWMutex
}

// ─────────────────────────────────────────────────────────────────────────────
// Engine — motor de descarga torrent + control de Telegram
// ─────────────────────────────────────────────────────────────────────────────
type Engine struct {
	client   *torrent.Client
	storage  string
	tasks    map[int64]*TaskStatus
	mu       sync.RWMutex
	magnetRe *regexp.Regexp
}

// NewEngine — crea el cliente torrent y el motor
func NewEngine(cfg Config) (*Engine, error) {
	if err := os.MkdirAll(cfg.Storage, 0755); err != nil {
		return nil, fmt.Errorf("creando storage: %w", err)
	}

	torrentCfg := torrent.NewDefaultClientConfig()
	torrentCfg.DataDir = cfg.Storage
	torrentCfg.Seed = true
	torrentCfg.Debug = false
	torrentCfg.ListenPort = cfg.Port
	torrentCfg.NoDHT = false

	client, err := torrent.NewClient(torrentCfg)
	if err != nil {
		return nil, fmt.Errorf("creando cliente torrent: %w", err)
	}

	magnetRe := regexp.MustCompile(`[&?].*$`)

	return &Engine{
		client:   client,
		storage:  cfg.Storage,
		tasks:    make(map[int64]*TaskStatus),
		magnetRe: magnetRe,
	}, nil
}

// Close — cierra el cliente torrent
func (e *Engine) Close() {
	e.client.Close()
}

// ─────────────────────────────────────────────────────────────────────────────
// HandleMessage — rutea los mensajes entrantes
// ─────────────────────────────────────────────────────────────────────────────
func (e *Engine) HandleMessage(bot *tgbotapi.BotAPI, chatID int64, replyTo int, text string) {
	switch {
	case text == "/start" || text == "/help":
		e.cmdHelp(bot, chatID, replyTo)
	case text == "/status":
		e.cmdStatus(bot, chatID, replyTo)
	case text == "/cancel":
		e.cmdCancel(bot, chatID, replyTo)
	case strings.HasPrefix(strings.TrimSpace(text), "magnet:?"):
		e.startDownload(bot, chatID, replyTo, text)
	case isTorrentURL(text):
		e.startDownload(bot, chatID, replyTo, text)
	default:
		e.sendMarkdown(bot, chatID, replyTo,
			"*No reconozco ese comando.*\nEnvia /help para ver los comandos disponibles.")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Comandos: /help, /status, /cancel
// ─────────────────────────────────────────────────────────────────────────────
func (e *Engine) cmdHelp(bot *tgbotapi.BotAPI, chatID int64, replyTo int) {
	help := "*TeleTorrent Bot v2.0*\n\n" +
		"Enviame un *magnet link* o una *URL directa a un .torrent* " +
		"y yo lo descargare y te enviare los archivos de vuelta.\n\n" +
		"*Comandos:*\n" +
		"- /start o /help — Muestra este mensaje\n" +
		"- /status — Estado de la descarga actual\n" +
		"- /cancel — Cancela la descarga actual\n\n" +
		"*Soportado:*\n" +
		"- Magnet links: `magnet:?xt=urn:btih:...`\n" +
		"- URLs directas .torrent: `https://ejemplo.com/archivo.torrent`\n\n" +
		"*Notas:*\n" +
		"- Solo una descarga activa por chat.\n" +
		"- Timeout por falta de peers: 90 segundos."
	e.sendMarkdown(bot, chatID, replyTo, help)
}

func (e *Engine) cmdStatus(bot *tgbotapi.BotAPI, chatID int64, replyTo int) {
	e.mu.RLock()
	task, ok := e.tasks[chatID]
	e.mu.RUnlock()

	if !ok {
		e.sendMarkdown(bot, chatID, replyTo, "*No hay descargas activas.*")
		return
	}

	task.mu.RLock()
	pct := task.Progress
	name := task.Name
	errMsg := task.Error
	started := task.StartedAt
	done := task.Done
	downloaded := task.Downloaded
	total := task.TotalBytes
	task.mu.RUnlock()

	switch {
	case errMsg != "":
		e.sendMarkdown(bot, chatID, replyTo,
			fmt.Sprintf("*Error:* `%s`", errMsg))
	case done:
		e.sendMarkdown(bot, chatID, replyTo,
			fmt.Sprintf("*Completado:* `%s`", name))
	default:
		elapsed := time.Since(started).Round(time.Second)
		speed := ""
		if elapsed.Seconds() > 4 && downloaded > 0 {
			bps := int64(float64(downloaded) / elapsed.Seconds())
			speed = " | " + formatBytes(bps) + "/s"
		}
		e.sendMarkdown(bot, chatID, replyTo,
			fmt.Sprintf("*Descargando:* `%s`\n`%.1f%%` | %s / %s%s\n%s",
				name, pct,
				formatBytes(downloaded), formatBytes(total),
				speed, elapsed))
	}
}

func (e *Engine) cmdCancel(bot *tgbotapi.BotAPI, chatID int64, replyTo int) {
	e.mu.Lock()
	if _, ok := e.tasks[chatID]; !ok {
		e.mu.Unlock()
		e.sendMarkdown(bot, chatID, replyTo, "*No hay nada que cancelar.*")
		return
	}
	delete(e.tasks, chatID)
	e.mu.Unlock()
	e.sendMarkdown(bot, chatID, replyTo, "*Descarga cancelada.*")
}

// ─────────────────────────────────────────────────────────────────────────────
// startDownload — inicia una descarga desde magnet o URL .torrent
// ─────────────────────────────────────────────────────────────────────────────
func (e *Engine) startDownload(bot *tgbotapi.BotAPI, chatID int64, replyTo int, input string) {
	// Verificar si ya hay una descarga activa
	e.mu.Lock()
	if _, active := e.tasks[chatID]; active {
		e.mu.Unlock()
		e.sendMarkdown(bot, chatID, replyTo, "*Ya hay una descarga en progreso.* Usa /cancel primero.")
		return
	}
	e.tasks[chatID] = &TaskStatus{StartedAt: time.Now()}
	e.mu.Unlock()

	// Mensaje de estado inicial
	statusMsg := tgbotapi.NewMessage(chatID, "*Iniciando descarga...*")
	statusMsg.ReplyToMessageID = replyTo
	statusMsg.ParseMode = "Markdown"
	sent, err := bot.Send(statusMsg)
	if err != nil {
		log.Printf("[%d] error enviando mensaje inicial: %v", chatID, err)
		e.failTask(chatID, "Error interno")
		e.mu.Lock()
		delete(e.tasks, chatID)
		e.mu.Unlock()
		return
	}
	statusMsgID := sent.MessageID

	var t *torrent.Torrent
	var addErr error

	if strings.HasPrefix(strings.TrimSpace(input), "magnet:?") {
		log.Printf("[%d] Agregando magnet link", chatID)
		// Limpiar parametros extra
		clean := e.magnetRe.ReplaceAllString(strings.TrimSpace(input), "")
		if !strings.HasPrefix(clean, "magnet:?xt=") {
			e.failTask(chatID, "Magnet link invalido")
			e.editMarkdown(bot, statusMsgID, chatID, "*Magnet link invalido.*")
			e.mu.Lock()
			delete(e.tasks, chatID)
			e.mu.Unlock()
			return
		}
		t, addErr = e.client.AddMagnet(clean)
	} else {
		log.Printf("[%d] Descargando .torrent URL: %s", chatID, input)
		bot.Send(tgbotapi.NewChatAction(chatID, tgbotapi.ChatTyping))

		data, fetchErr := fetchURL(input)
		if fetchErr != nil {
			e.failTask(chatID, fmt.Sprintf("Error fetching .torrent: %v", fetchErr))
			e.editMarkdown(bot, statusMsgID, chatID,
				fmt.Sprintf("*Error al descargar .torrent:* `%s`", fetchErr.Error()))
			e.mu.Lock()
			delete(e.tasks, chatID)
			e.mu.Unlock()
			return
		}
		t, addErr = e.client.AddTorrentFromData(data)
	}

	if addErr != nil {
		e.failTask(chatID, fmt.Sprintf("Error adding torrent: %v", addErr))
		e.editMarkdown(bot, statusMsgID, chatID,
			fmt.Sprintf("*Error al agregar torrent:* `%s`", addErr.Error()))
		e.mu.Lock()
		delete(e.tasks, chatID)
		e.mu.Unlock()
		return
	}

	// Esperar metadatos (con timeout)
	select {
	case <-t.GotInfo():
	case <-time.After(30 * time.Second):
		e.failTask(chatID, "Timeout obteniendo metadatos")
		e.editMarkdown(bot, statusMsgID, chatID, "*Timeout obteniendo metadatos del torrent.*")
		t.Drop()
		e.mu.Lock()
		delete(e.tasks, chatID)
		e.mu.Unlock()
		return
	}

	// Registrar info del torrent en el task
	name := t.Name()
	infoHash := t.InfoHash().HexString()
	totalLen := t.Length()

	e.mu.RLock()
	if task, ok := e.tasks[chatID]; ok {
		task.mu.Lock()
		task.InfoHash = infoHash
		task.Name = name
		task.TotalBytes = totalLen
		for _, f := range t.Files() {
			if path := f.Path(); path != "" {
				task.Files = append(task.Files, path)
			}
		}
		task.mu.Unlock()
	}
	e.mu.RUnlock()

	t.Download()

	e.editMarkdown(bot, statusMsgID, chatID,
		fmt.Sprintf("*Agregado:* `%s`\nTamano: `%s`\n*Buscando peers...*",
			name, formatBytes(totalLen)))

	// Lanzar goroutine de descarga
	go e.downloadLoop(bot, chatID, replyTo, t, statusMsgID)
}

// ─────────────────────────────────────────────────────────────────────────────
// downloadLoop — bucle principal de descarga con actualizacion de progreso
// ─────────────────────────────────────────────────────────────────────────────
func (e *Engine) downloadLoop(bot *tgbotapi.BotAPI, chatID int64, replyTo int, t *torrent.Torrent, statusMsgID int) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("[%d] PANIC en downloadLoop: %v", chatID, r)
			e.editMarkdown(bot, statusMsgID, chatID, "*Error interno (panic).*")
		}
	}()

	ticker := time.NewTicker(ProgressUpdateInterval)
	defer ticker.Stop()

	stallTicker := time.NewTicker(StallTimeout)
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
		// Verificar cancelacion
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

			var speed string
			if elapsed > 4 && completed > 0 {
				bps := int64(float64(completed) / elapsed)
				speed = formatBytes(bps) + "/s"
			} else {
				speed = "conectando..."
			}

			task.mu.Lock()
			task.Progress = pct
			task.Downloaded = completed
			task.mu.Unlock()

			bar := progressBar(int(pct), 20)
			e.editMarkdown(bot, statusMsgID, chatID,
				fmt.Sprintf("*Descargando:* `%s`\n%s `%.1f%%`\n%s | %s / %s",
					t.Name(), bar, pct, speed,
					formatBytes(completed), formatBytes(total)))

			if completed > lastBytes {
				lastBytes = completed
				lastTime = time.Now()
			}

		case <-stallTicker.C:
			if time.Since(lastTime) >= StallTimeout {
				log.Printf("[%d] Descarga estancada — sin actividad durante 90s", chatID)
				e.editMarkdown(bot, statusMsgID, chatID, "*Descarga estancada — sin peers.*")
				e.mu.Lock()
				delete(e.tasks, chatID)
				e.mu.Unlock()
				t.Drop()
				return
			}
		}
	}

	// Descarga completada
	task.mu.Lock()
	task.Done = true
	task.Progress = 100
	task.mu.Unlock()

	e.editMarkdown(bot, statusMsgID, chatID, "*Descarga completa — subiendo a Telegram...*")
	e.uploadFiles(bot, chatID, replyTo, t, statusMsgID)
}

// ─────────────────────────────────────────────────────────────────────────────
// uploadFiles — sube los archivos descargados a Telegram
// ─────────────────────────────────────────────────────────────────────────────
func (e *Engine) uploadFiles(bot *tgbotapi.BotAPI, chatID int64, replyTo int, t *torrent.Torrent, statusMsgID int) {
	e.mu.RLock()
	task := e.tasks[chatID]
	e.mu.RUnlock()
	if task == nil {
		return
	}

	task.mu.RLock()
	files := make([]string, len(task.Files))
	copy(files, task.Files)
	task.mu.RUnlock()

	uploaded := 0
	failed := 0

	for _, fPath := range files {
		fullPath := filepath.Join(e.storage, fPath)

		fi, err := os.Stat(fullPath)
		if err != nil {
			log.Printf("[%d] no se puede acceder a %s: %v", chatID, fPath, err)
			failed++
			continue
		}
		if fi.IsDir() || fi.Size() == 0 {
			continue
		}

		log.Printf("[%d] Subiendo %s (%s)", chatID, fPath, formatBytes(fi.Size()))
		bot.Send(tgbotapi.NewChatAction(chatID, tgbotapi.ChatUploadDocument))

		sent := false
		for attempt := 0; attempt < MaxRetries; attempt++ {
			doc := tgbotapi.NewDocumentUpload(chatID, fullPath)
			doc.ReplyToMessageID = replyTo
			doc.FileName = filepath.Base(fPath)

			if _, err := bot.Send(doc); err == nil {
				sent = true
				break
			} else {
				log.Printf("[%d] Intento %d fallo para %s: %v", chatID, attempt+1, fPath, err)
				if strings.Contains(err.Error(), "429") {
					time.Sleep(RetryDelay * time.Duration(attempt+1))
				} else {
					break
				}
			}
		}

		if sent {
			uploaded++
		} else {
			bot.Send(tgbotapi.NewMessage(chatID,
				fmt.Sprintf("*No pude subir:* `%s`", fPath)))
			failed++
		}

		time.Sleep(UploadPause)
	}

	// Limpiar tarea
	e.mu.Lock()
	delete(e.tasks, chatID)
	e.mu.Unlock()

	// Resumen
	var summary string
	if failed > 0 {
		summary = fmt.Sprintf("*Listo!* Subi %d archivo(s), %d fallaron.", uploaded, failed)
	} else {
		summary = fmt.Sprintf("*Todo listo!* Subi %d archivo(s).", uploaded)
	}
	bot.Send(tgbotapi.NewMessage(chatID, summary))

	// Eliminar mensaje de progreso
	deleteMsg := tgbotapi.NewDeleteMessage(chatID, statusMsgID)
	bot.Request(deleteMsg)
}

// ─────────────────────────────────────────────────────────────────────────────
// Helpers: mensajes
// ─────────────────────────────────────────────────────────────────────────────
func (e *Engine) sendMarkdown(bot *tgbotapi.BotAPI, chatID int64, replyTo int, text string) {
	msg := tgbotapi.NewMessage(chatID, text)
	msg.ReplyToMessageID = replyTo
	msg.ParseMode = "Markdown"
	if _, err := bot.Send(msg); err != nil {
		log.Printf("[%d] error sendMarkdown: %v", chatID, err)
	}
}

func (e *Engine) editMarkdown(bot *tgbotapi.BotAPI, msgID int, chatID int64, text string) {
	edit := tgbotapi.NewEditMessageText(chatID, msgID, text)
	edit.ParseMode = "Markdown"
	if _, err := bot.Request(edit); err != nil {
		if !strings.Contains(err.Error(), "400") {
			log.Printf("[%d] error editMarkdown: %v", chatID, err)
		}
	}
}

func (e *Engine) failTask(chatID int64, errMsg string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if task, ok := e.tasks[chatID]; ok {
		task.mu.Lock()
		task.Error = errMsg
		task.mu.Unlock()
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Helpers: formato y utilidades
// ─────────────────────────────────────────────────────────────────────────────
func isTorrentURL(s string) bool {
	s = strings.TrimSpace(s)
	return (strings.HasPrefix(s, "http://") || strings.HasPrefix(s, "https://")) &&
		strings.HasSuffix(s, ".torrent")
}

func fetchURL(rawURL string) ([]byte, error) {
	client := &http.Client{Timeout: 60 * time.Second}
	resp, err := client.Get(rawURL)
	if err != nil {
		return nil, fmt.Errorf("GET %s: %w", rawURL, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d al descargar %s", resp.StatusCode, rawURL)
	}

	return io.ReadAll(resp.Body)
}

func formatBytes(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.2f GB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.2f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.2f KB", float64(n)/(1<<10))
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
	return strings.Repeat("█", filled) + strings.Repeat("░", width-filled)
}

// ─────────────────────────────────────────────────────────────────────────────
// Polling loop
// ─────────────────────────────────────────────────────────────────────────────
func startPolling(bot *tgbotapi.BotAPI, engine *Engine) {
	u := tgbotapi.NewUpdate(0)
	u.Timeout = 60

	updates := bot.GetUpdatesChan(u)

	for update := range updates {
		if update.Message == nil {
			continue
		}

		chatID := update.Message.Chat.ID
		replyTo := update.Message.MessageID
		text := strings.TrimSpace(update.Message.Text)

		if text == "" {
			continue
		}

		log.Printf("[%d] << %s", chatID, text)
		engine.HandleMessage(bot, chatID, replyTo, text)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// main
// ─────────────────────────────────────────────────────────────────────────────
func main() {
	cfg := parseFlags()
	log.SetFlags(log.Ldate | log.Ltime | log.Lshortfile)

	log.Println("Iniciando TeleTorrent Bot v2.0")
	log.Printf("   Token: %s...", cfg.Token[:8])
	if cfg.ChannelID != 0 {
		log.Printf("   Channel: %d", cfg.ChannelID)
	}
	log.Printf("   Storage: %s", cfg.Storage)

	// Crear bot de Telegram
	bot, err := tgbotapi.NewBotAPI(cfg.Token)
	if err != nil {
		log.Fatalf("Error creando bot: %v", err)
	}
	log.Printf("Bot autorizado: @%s", bot.Self.UserName)

	// Si se especifico un channel, verificarlo
	if cfg.ChannelID != 0 {
		chat, err := bot.GetChat(tgbotapi.ChatInfoConfig{
			ChatConfig: tgbotapi.ChatConfig{ChatID: cfg.ChannelID},
		})
		if err != nil {
			log.Printf("No se pudo verificar el channel: %v (continuando de todas formas)", err)
		} else {
			log.Printf("Channel verificado: %s", chat.Title)
		}
	}

	// Crear motor de descargas
	engine, err := NewEngine(cfg)
	if err != nil {
		log.Fatalf("Error creando engine: %v", err)
	}
	defer engine.Close()

	// Capturar senales para cierre graceful
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		<-sigCh
		log.Println("Senal recibida, cerrando...")
		bot.StopReceivingUpdates()
		engine.Close()
		os.Exit(0)
	}()

	// Iniciar polling
	log.Println("Escuchando mensajes...")
	startPolling(bot, engine)
}
