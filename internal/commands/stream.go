package commands

import (
	"fmt"
	"net/url"
	"strings"
	"time"

	"EverythingSuckz/fsb/config"
	"EverythingSuckz/fsb/internal/cache"
	"EverythingSuckz/fsb/internal/utils"

	"github.com/celestix/gotgproto/dispatcher"
	"github.com/celestix/gotgproto/dispatcher/handlers"
	"github.com/celestix/gotgproto/ext"
	"github.com/celestix/gotgproto/types"
	"github.com/gotd/td/tg"
)

// Canal para procesar logs
var logQueue = make(chan logMessage, 100)

type logMessage struct {
	ctx     *ext.Context
	u       *ext.Update
	content string
	opts    *ext.ReplyOpts
}

// Inicia el worker de logs
func StartLogWorker() {
	go func() {
		for msg := range logQueue {
			sendWithFloodWait(msg.ctx, msg.u, msg.content, msg.opts)
			time.Sleep(1500 * time.Millisecond) // Delay para no saturar Telegram
		}
	}()
}

// Función que maneja FLOOD_WAIT
func sendWithFloodWait(ctx *ext.Context, u *ext.Update, msg string, opts *ext.ReplyOpts) error {
	_, err := ctx.Reply(u, msg, opts)
	if err != nil {
		if strings.Contains(err.Error(), "FLOOD_WAIT") {
			var sec int
			fmt.Sscanf(err.Error(), "rpc error code 420: FLOOD_WAIT (%d)", &sec)
			time.Sleep(time.Duration(sec+1) * time.Second)
			_, _ = ctx.Reply(u, msg, opts)
			return nil
		}
	}
	return err
}

// LoadStream registers the handler for incoming messages
func (m *command) LoadStream(dispatcher dispatcher.Dispatcher) {
	defer m.log.Sugar().Info("Loaded Stream handler")
	dispatcher.AddHandler(
		handlers.NewMessage(nil, m.sendLink),
	)
}

// supportedMediaFilter checks if the message contains supported media
func supportedMediaFilter(m *types.Message) (bool, error) {
	if m.Media == nil {
		return false, dispatcher.EndGroups
	}
	switch media := m.Media.(type) {
	case *tg.MessageMediaDocument:
		doc := media.Document.(*tg.Document)
		if strings.HasPrefix(doc.MimeType, "video/") ||
			strings.HasPrefix(doc.MimeType, "audio/") ||
			strings.Contains(doc.MimeType, "pdf") ||
			strings.Contains(doc.MimeType, "zip") ||
			strings.Contains(doc.MimeType, "rar") ||
			strings.Contains(doc.MimeType, "apk") {
			return true, nil
		}
	case *tg.MessageMediaPhoto:
		return true, nil
	}
	return false, nil
}

// sendLink processes the message, forwards it, and sends the formatted output
func (m *command) sendLink(ctx *ext.Context, u *ext.Update) error {
	chatId := u.EffectiveChat().GetID()

	// Permisos
	if len(config.ValueOf.AllowedUsers) != 0 && !utils.Contains(config.ValueOf.AllowedUsers, chatId) {
		logQueue <- logMessage{ctx, u, "You are not allowed to use this bot.", nil}
		return dispatcher.EndGroups
	}

	// Force subscription
	if config.ValueOf.ForceSubChannel != "" {
		isSubscribed, err := utils.IsUserSubscribed(ctx, ctx.Raw, ctx.PeerStorage, chatId)
		if err != nil || !isSubscribed {
			row := tg.KeyboardButtonRow{
				Buttons: []tg.KeyboardButtonClass{
					&tg.KeyboardButtonURL{
						Text: "Join Channel",
						URL:  fmt.Sprintf("https://t.me/%s", config.ValueOf.ForceSubChannel),
					},
				},
			}
			markup := &tg.ReplyInlineMarkup{Rows: []tg.KeyboardButtonRow{row}}
			logQueue <- logMessage{ctx, u, "Please join our channel to get stream links.", &ext.ReplyOpts{Markup: markup}}
			return dispatcher.EndGroups
		}
	}

	// Verifica si es media soportada
	supported, err := supportedMediaFilter(u.EffectiveMessage)
	if err != nil || !supported {
		logQueue <- logMessage{ctx, u, "Sorry, this message type is unsupported.", nil}
		return dispatcher.EndGroups
	}

	// Forward a canal de log
	update, err := utils.ForwardMessages(ctx, chatId, config.ValueOf.LogChannelID, u.EffectiveMessage.ID)
	if err != nil {
		m.log.Sugar().Errorf("Forward failed: %v", err)
		logQueue <- logMessage{ctx, u, fmt.Sprintf("Error forwarding message: %s", err.Error()), nil}
		return dispatcher.EndGroups
	}

	// Extraer archivo
	messageID := update.Updates[0].(*tg.UpdateMessageID).ID
	var doc tg.MessageMediaClass
	switch m := update.Updates[1].(type) {
	case *tg.UpdateNewChannelMessage:
		if msg, ok := m.Message.(*tg.Message); ok {
			doc = msg.Media
		} else {
			logQueue <- logMessage{ctx, u, "Unable to extract message media", nil}
			return dispatcher.EndGroups
		}
	default:
		logQueue <- logMessage{ctx, u, "Unsupported update type", nil}
		return dispatcher.EndGroups
	}

	file, err := utils.FileFromMedia(doc)
	if err != nil {
		logQueue <- logMessage{ctx, u, fmt.Sprintf("Error extracting file: %s", err.Error()), nil}
		return dispatcher.EndGroups
	}

	// Nombre del archivo
	if file.FileName == "" || !strings.Contains(file.FileName, ".") {
		ext := getExtensionFromMIME(file.MimeType)
		if file.FileName == "" {
			file.FileName = fmt.Sprintf("%d%d%s", time.Now().UnixNano(), file.ID, ext)
		} else {
			file.FileName += ext
		}
	}

	// Generar hash y link
	fullHash := utils.PackFile(file.FileName, file.FileSize, file.MimeType, file.ID)
	hash := utils.GetShortHash(fullHash)
	streamURL := fmt.Sprintf("https://host.streamgramm.workers.dev/?video=%s&filename=%s",
		url.QueryEscape(fmt.Sprintf("%d?hash=%s", messageID, hash)),
		url.QueryEscape(file.FileName),
	)

	// Actualiza estadísticas
	statsCache := cache.GetStatsCache()
	if statsCache != nil {
		_ = statsCache.RecordFileProcessed(file.FileSize)
	}

	// Emoji
	fileEmoji := getFileEmoji(file.MimeType)

	// Construye mensaje
	message := fmt.Sprintf(
		"%s File: %s\n📂 Type: %s\n💽 Size: %s\n\n❗ WARNING:\n🚫 Illegal or explicit content = Ban + Report\n\n🔗 Follow: @yoelbotsx",
		fileEmoji,
		file.FileName,
		file.MimeType,
		formatFileSize(file.FileSize),
	)

	// Inline keyboard
	row := tg.KeyboardButtonRow{
		Buttons: []tg.KeyboardButtonClass{
			&tg.KeyboardButtonURL{Text: "▶️ Watch / Download", URL: streamURL},
		},
	}
	markup := &tg.ReplyInlineMarkup{Rows: []tg.KeyboardButtonRow{row}}

	// Envia mensaje al usuario
	logQueue <- logMessage{ctx, u, message, &ext.ReplyOpts{
		Markup:           markup,
		ReplyToMessageId: u.EffectiveMessage.ID,
	}}

	return dispatcher.EndGroups
}

// --- Helpers ---

func getExtensionFromMIME(mime string) string {
	mime = strings.ToLower(mime)
	switch {
	case strings.HasPrefix(mime, "video/"):
		return ".mp4"
	case strings.HasPrefix(mime, "image/"):
		return ".jpg"
	case strings.HasPrefix(mime, "audio/"):
		return ".mp3"
	case strings.Contains(mime, "pdf"):
		return ".pdf"
	case strings.Contains(mime, "zip"):
		return ".zip"
	case strings.Contains(mime, "rar"):
		return ".rar"
	case strings.Contains(mime, "apk"):
		return ".apk"
	default:
		return ".file"
	}
}

func getFileEmoji(mime string) string {
	lower := strings.ToLower(mime)
	switch {
	case strings.Contains(lower, "video"):
		return "🎬"
	case strings.Contains(lower, "image"):
		return "🖼️"
	case strings.Contains(lower, "audio"):
		return "🎵"
	case strings.Contains(lower, "pdf"):
		return "📄"
	case strings.Contains(lower, "zip"), strings.Contains(lower, "rar"):
		return "🗜️"
	case strings.Contains(lower, "apk"), strings.Contains(lower, "exe"):
		return "📦"
	default:
		return "📁"
	}
}

func formatFileSize(bytes int64) string {
	const (
		KB = 1024
		MB = 1024 * KB
		GB = 1024 * MB
	)
	switch {
	case bytes >= GB:
		return fmt.Sprintf("%.2f GB", float64(bytes)/float64(GB))
	case bytes >= MB:
		return fmt.Sprintf("%.2f MB", float64(bytes)/float64(MB))
	default:
		return fmt.Sprintf("%.2f KB", float64(bytes)/float64(KB))
	}
}
