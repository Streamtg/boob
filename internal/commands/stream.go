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

// LoadStream registers the handler for incoming messages
func (m *command) LoadStream(dispatcher dispatcher.Dispatcher) {
	log := m.log.Named("start")
	defer log.Sugar().Info("Loaded Stream handler")
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

	// Permission check
	if len(config.ValueOf.AllowedUsers) != 0 && !utils.Contains(config.ValueOf.AllowedUsers, chatId) {
		return sendWithFloodWait(ctx, u, "You are not allowed to use this bot.")
	}

	// Force subscription check
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
			ctx.Reply(u, "Please join our channel to get stream links.", &ext.ReplyOpts{
				Markup: markup,
			})
			return dispatcher.EndGroups
		}
	}

	// Check if message has supported media
	supported, err := supportedMediaFilter(u.EffectiveMessage)
	if err != nil || !supported {
		return sendWithFloodWait(ctx, u, "Sorry, this message type is unsupported.")
	}

	// Forward message to log channel
	update, err := utils.ForwardMessages(ctx, chatId, config.ValueOf.LogChannelID, u.EffectiveMessage.ID)
	if err != nil {
		m.log.Sugar().Errorf("Forward failed: %v", err)
		return sendWithFloodWait(ctx, u, fmt.Sprintf("Error forwarding message: %s", err.Error()))
	}

	// Extract file
	messageID := update.Updates[0].(*tg.UpdateMessageID).ID
	doc := update.Updates[1].(*tg.UpdateNewChannelMessage).Message.(*tg.Message).Media
	file, err := utils.FileFromMedia(doc)
	if err != nil {
		return sendWithFloodWait(ctx, u, fmt.Sprintf("Error extracting file: %s", err.Error()))
	}

	// Assign numeric-only filename if missing
	if file.FileName == "" || !strings.Contains(file.FileName, ".") {
		ext := getExtensionFromMIME(file.MimeType)
		if file.FileName == "" {
			file.FileName = fmt.Sprintf("%d%d%s", time.Now().UnixNano(), file.ID, ext)
		} else {
			file.FileName += ext
		}
	}

	// Build file hash & stream link
	fullHash := utils.PackFile(file.FileName, file.FileSize, file.MimeType, file.ID)
	hash := utils.GetShortHash(fullHash)
	streamURL := fmt.Sprintf("https://host.streamgramm.workers.dev/?video=%s&filename=%s",
		url.QueryEscape(fmt.Sprintf("%d?hash=%s", messageID, hash)),
		url.QueryEscape(file.FileName),
	)

	// Update stats cache
	statsCache := cache.GetStatsCache()
	if statsCache != nil {
		_ = statsCache.RecordFileProcessed(file.FileSize)
	}

	// Determine emoji based on file type
	fileEmoji := getFileEmoji(file.MimeType)

	// Construct message
	message := fmt.Sprintf(
		"%s File: %s\n📂 Type: %s\n💽 Size: %s\n\n❗ WARNING:\n🚫 Illegal or explicit content = Ban + Report\n\n🔗 Follow: @yoelbotsx",
		fileEmoji,
		file.FileName,
		file.MimeType,
		formatFileSize(file.FileSize),
	)

	// Inline keyboard with download/stream
	row := tg.KeyboardButtonRow{
		Buttons: []tg.KeyboardButtonClass{
			&tg.KeyboardButtonURL{Text: "▶️ Watch / Download", URL: streamURL},
		},
	}
	markup := &tg.ReplyInlineMarkup{Rows: []tg.KeyboardButtonRow{row}}

	// Send message con manejo de FLOOD_WAIT
	return sendWithFloodWait(ctx, u, message, &ext.ReplyOpts{
		Markup:           markup,
		ReplyToMessageId: u.EffectiveMessage.ID,
	})
}

// sendWithFloodWait retries automatically si Telegram retorna FLOOD_WAIT
func sendWithFloodWait(ctx *ext.Context, u *ext.Update, msg string, opts ...*ext.ReplyOpts) error {
	for {
		_, err := ctx.Reply(u, msg, opts...)
		if err != nil {
			if floodErr, ok := err.(*tg.ErrFlood); ok {
				wait := time.Duration(floodErr.Timeout) * time.Second
				time.Sleep(wait + time.Second) // agrega 1s extra por seguridad
				continue
			}
			return err
		}
		break
	}
	return nil
}

// getExtensionFromMIME returns file extension based on MIME type
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

// getFileEmoji returns an emoji depending on file type
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

// formatFileSize formats bytes into KB, MB, GB
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
