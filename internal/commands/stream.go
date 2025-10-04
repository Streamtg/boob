package commands

import (
	"fmt"
	"net/url"
	"strings"

	"EverythingSuckz/fsb/config"
	"EverythingSuckz/fsb/internal/cache"
	"EverythingSuckz/fsb/internal/utils"

	"github.com/celestix/gotgproto/dispatcher"
	"github.com/celestix/gotgproto/dispatcher/handlers"
	"github.com/celestix/gotgproto/ext"
	"github.com/celestix/gotgproto/storage"
	"github.com/celestix/gotgproto/types"
	"github.com/gotd/td/tg"
)

// ---------------------------
// Función principal existente
// ---------------------------
func (m *command) LoadStream(dispatcher dispatcher.Dispatcher) {
	defer m.log.Sugar().Info("Loaded")
	dispatcher.AddHandler(
		handlers.NewMessage(nil, sendLink),
	)
}

// ---------------------------
// Filtrado de medios soportados
// ---------------------------
func supportedMediaFilter(m *types.Message) (bool, error) {
	if m.Media == nil {
		return false, dispatcher.EndGroups
	}
	switch m.Media.(type) {
	case *tg.MessageMediaDocument:
		return true, nil
	case *tg.MessageMediaPhoto:
		return true, nil
	case tg.MessageMediaClass:
		return false, dispatcher.EndGroups
	default:
		return false, nil
	}
}

// ---------------------------
// Conversión de tamaño de bytes
// ---------------------------
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

// ---------------------------
// Emoji según tipo de archivo
// ---------------------------
func fileTypeEmoji(mime string) string {
	lowerMime := strings.ToLower(mime)
	switch {
	case strings.Contains(lowerMime, "video"):
		return "🎬"
	case strings.Contains(lowerMime, "image"):
		return "🖼️"
	case strings.Contains(lowerMime, "audio"):
		return "🎵"
	case strings.Contains(lowerMime, "pdf"):
		return "📕"
	case strings.Contains(lowerMime, "zip"), strings.Contains(lowerMime, "rar"):
		return "🗜️"
	case strings.Contains(lowerMime, "text"):
		return "📝"
	case strings.Contains(lowerMime, "word"), strings.Contains(lowerMime, "excel"), strings.Contains(lowerMime, "powerpoint"):
		return "📄"
	case strings.Contains(lowerMime, "epub"):
		return "📚"
	default:
		return "📄"
	}
}

// ---------------------------
// Sanitización de nombres de archivo
// ---------------------------
func sanitizeFileName(name string) string {
	name = strings.ReplaceAll(name, " ", "_")
	name = strings.Map(func(r rune) rune {
		if strings.ContainsRune(`\/:*?"<>|`, r) {
			return -1
		}
		return r
	}, name)
	return name
}

// ---------------------------
// Previsualización placeholder
// ---------------------------
func generatePreview(file interface{}) string {
	// Solo placeholder: podrías mejorar con thumbnails reales
	switch f := file.(type) {
	case *utils.FileFromMediaReturnType: // Reemplaza con tu tipo real de retorno
		lowerMime := strings.ToLower(f.MimeType)
		if strings.Contains(lowerMime, "image") {
			return "🖼️ Preview: [Image]"
		} else if strings.Contains(lowerMime, "video") {
			return "🎬 Preview: [Video]"
		}
	}
	return ""
}

// ---------------------------
// Seguridad placeholder
// ---------------------------
func isFileSafe(file interface{}) bool {
	return true
}

// ---------------------------
// Función principal de envío de link
// ---------------------------
func sendLink(ctx *ext.Context, u *ext.Update) error {
	chatId := u.EffectiveChat().GetID()
	peerChatId := ctx.PeerStorage.GetPeerById(chatId)
	if peerChatId.Type != int(storage.TypeUser) {
		return dispatcher.EndGroups
	}

	if len(config.ValueOf.AllowedUsers) != 0 && !utils.Contains(config.ValueOf.AllowedUsers, chatId) {
		ctx.Reply(u, "You are not allowed to use this bot.", nil)
		return dispatcher.EndGroups
	}

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
			ctx.Reply(u, "Please join our channel to get stream links.", &ext.ReplyOpts{Markup: markup})
			return dispatcher.EndGroups
		}
	}

	supported, err := supportedMediaFilter(u.EffectiveMessage)
	if err != nil {
		return err
	}
	if !supported {
		ctx.Reply(u, "Sorry, this message type is unsupported.", nil)
		return dispatcher.EndGroups
	}

	update, err := utils.ForwardMessages(ctx, chatId, config.ValueOf.LogChannelID, u.EffectiveMessage.ID)
	if err != nil {
		ctx.Reply(u, fmt.Sprintf("Error - %s", err.Error()), nil)
		return dispatcher.EndGroups
	}

	messageID := update.Updates[0].(*tg.UpdateMessageID).ID
	doc := update.Updates[1].(*tg.UpdateNewChannelMessage).Message.(*tg.Message).Media
	file, err := utils.FileFromMedia(doc)
	if err != nil {
		ctx.Reply(u, fmt.Sprintf("Error - %s", err.Error()), nil)
		return dispatcher.EndGroups
	}

	// Detectar nombre y formato si no está presente
	if file.FileName == "" {
		var ext string
		lowerMime := strings.ToLower(file.MimeType)
		switch {
		case strings.Contains(lowerMime, "image/jpeg"):
			ext = ".jpg"
			file.FileName = "photo" + ext
		case strings.Contains(lowerMime, "image/png"):
			ext = ".png"
			file.FileName = "photo" + ext
		case strings.Contains(lowerMime, "image/gif"):
			ext = ".gif"
			file.FileName = "animation" + ext
		case strings.Contains(lowerMime, "video"):
			ext = ".mp4"
			file.FileName = "video" + ext
		case strings.Contains(lowerMime, "audio"):
			ext = ".mp3"
			file.FileName = "audio" + ext
		case strings.Contains(lowerMime, "pdf"):
			ext = ".pdf"
			file.FileName = "document" + ext
		case strings.Contains(lowerMime, "zip"):
			ext = ".zip"
			file.FileName = "archive" + ext
		case strings.Contains(lowerMime, "rar"):
			ext = ".rar"
			file.FileName = "archive" + ext
		case strings.Contains(lowerMime, "text"):
			ext = ".txt"
			file.FileName = "text" + ext
		case strings.Contains(lowerMime, "application"):
			ext = ".bin"
			file.FileName = "file" + ext
		default:
			ext = ""
			file.FileName = "unknown"
		}
	}

	file.FileName = sanitizeFileName(file.FileName)

	emoji := fileTypeEmoji(file.MimeType)
	size := formatFileSize(file.FileSize)
	preview := generatePreview(file)
	safe := isFileSafe(file)
	safeMessage := ""
	if !safe {
		safeMessage = "\n⚠️ Warning: File may be unsafe!"
	}

	message := fmt.Sprintf(
		"%s File Name: %s\n\n%s File Type: %s\n\n💾 Size: %s\n%s\n\n⏳ @yoelbots",
		emoji, file.FileName,
		emoji, file.MimeType,
		size,
		preview+safeMessage,
	)

	fullHash := utils.PackFile(file.FileName, file.FileSize, file.MimeType, file.ID)
	hash := utils.GetShortHash(fullHash)

	statsCache := cache.GetStatsCache()
	if statsCache != nil {
		_ = statsCache.RecordFileProcessed(file.FileSize)
	}

	row := tg.KeyboardButtonRow{}
	videoParam := fmt.Sprintf("%d?hash=%s", messageID, hash)
	encodedVideoParam := url.QueryEscape(videoParam)
	encodedFilename := url.QueryEscape(file.FileName)
	streamURL := fmt.Sprintf("https://file.streamgramm.workers.dev/?video=%s&filename=%s", encodedVideoParam, encodedFilename)
	row.Buttons = append(row.Buttons, &tg.KeyboardButtonURL{
		Text: "Streaming / Download",
		URL:  streamURL,
	})
	markup := &tg.ReplyInlineMarkup{Rows: []tg.KeyboardButtonRow{row}}

	_, err = ctx.Reply(u, message, &ext.ReplyOpts{
		Markup:           markup,
		NoWebpage:        false,
		ReplyToMessageId: u.EffectiveMessage.ID,
	})
	if err != nil {
		ctx.Reply(u, fmt.Sprintf("Error - %s", err.Error()), nil)
	}

	return dispatcher.EndGroups
}
