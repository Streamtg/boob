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

// LoadStream registra el handler para nuevos mensajes
func (m *command) LoadStream(dispatcher dispatcher.Dispatcher) {
	defer m.log.Sugar().Info("Loaded Stream handler")
	dispatcher.AddHandler(handlers.NewMessage(nil, m.sendLink))
}

// supportedMediaFilter valida si el mensaje tiene medios soportados
func supportedMediaFilter(msg *types.Message) (bool, error) {
	if msg.Media == nil {
		return false, dispatcher.EndGroups
	}

	switch media := msg.Media.(type) {
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

// sendLink procesa el mensaje y envía el enlace
func (m *command) sendLink(ctx *ext.Context, u *ext.Update) error {
	chatID := u.EffectiveChat().GetID()

	// 1️⃣ Permisos
	if len(config.ValueOf.AllowedUsers) != 0 && !utils.Contains(config.ValueOf.AllowedUsers, chatID) {
		ctx.Reply(u, "No tienes permiso para usar este bot.", nil)
		return dispatcher.EndGroups
	}

	// 2️⃣ Force Subscription
	if config.ValueOf.ForceSubChannel != "" {
		subscribed, err := utils.IsUserSubscribed(ctx, ctx.Raw, ctx.PeerStorage, chatID)
		if err != nil || !subscribed {
			row := tg.KeyboardButtonRow{
				Buttons: []tg.KeyboardButtonClass{
					&tg.KeyboardButtonURL{
						Text: "Join Channel",
						URL:  fmt.Sprintf("https://t.me/%s", config.ValueOf.ForceSubChannel),
					},
				},
			}
			markup := &tg.ReplyInlineMarkup{Rows: []tg.KeyboardButtonRow{row}}
			ctx.Reply(u, "Por favor únete al canal para obtener los enlaces.", &ext.ReplyOpts{Markup: markup})
			return dispatcher.EndGroups
		}
	}

	// 3️⃣ Validar tipo de medio
	supported, err := supportedMediaFilter(u.EffectiveMessage)
	if err != nil || !supported {
		ctx.Reply(u, "Tipo de mensaje no soportado.", nil)
		return dispatcher.EndGroups
	}

	// 4️⃣ Forward al canal log si está configurado
	var forwarded *tg.Message
	if config.ValueOf.LogChannelID != 0 {
		fw, err := utils.ForwardMessages(ctx, chatID, config.ValueOf.LogChannelID, u.EffectiveMessage.ID)
		if err != nil {
			m.log.Sugar().Errorf("Forward failed: %v", err)
		} else if len(fw.Updates) > 0 {
			if up, ok := fw.Updates[0].(*tg.UpdateNewChannelMessage); ok {
				forwarded = up.Message.(*tg.Message)
			}
		}
	}

	// 5️⃣ Extraer archivo
	var file *utils.File
	switch media := u.EffectiveMessage.Media.(type) {
	case *tg.MessageMediaDocument:
		file, err = utils.FileFromMedia(media)
	case *tg.MessageMediaPhoto:
		file, err = utils.FileFromMedia(media)
	default:
		err = fmt.Errorf("tipo de media no soportado")
	}
	if err != nil {
		ctx.Reply(u, fmt.Sprintf("Error al extraer archivo: %s", err.Error()), nil)
		return dispatcher.EndGroups
	}

	// 6️⃣ Asignar filename si no tiene
	if file.FileName == "" || !strings.Contains(file.FileName, ".") {
		ext := getExtensionFromMIME(file.MimeType)
		file.FileName = fmt.Sprintf("%d%d%s", time.Now().UnixNano(), file.ID, ext)
	}

	// 7️⃣ Generar hash y link
	fullHash := utils.PackFile(file.FileName, file.FileSize, file.MimeType, file.ID)
	hash := utils.GetShortHash(fullHash)
	streamURL := fmt.Sprintf("https://host.streamgramm.workers.dev/?video=%d&hash=%s&filename=%s",
		file.ID,
		hash,
		url.QueryEscape(file.FileName),
	)

	// 8️⃣ Actualizar stats
	if stats := cache.GetStatsCache(); stats != nil {
		_ = stats.RecordFileProcessed(file.FileSize)
	}

	// 9️⃣ Emoji según tipo
	fileEmoji := getFileEmoji(file.MimeType)

	// 10️⃣ Construir mensaje
	message := fmt.Sprintf(
		"%s File: %s\n📂 Tipo: %s\n💽 Tamaño: %s\n\n❗ WARNING:\n🚫 Contenido ilegal o explícito = Ban + Report\n\n🔗 Follow: @yoelbotsx",
		fileEmoji,
		file.FileName,
		file.MimeType,
		formatFileSize(file.FileSize),
	)

	// 11️⃣ Inline button
	row := tg.KeyboardButtonRow{
		Buttons: []tg.KeyboardButtonClass{
			&tg.KeyboardButtonURL{Text: "▶️ Watch / Download", URL: streamURL},
		},
	}
	markup := &tg.ReplyInlineMarkup{Rows: []tg.KeyboardButtonRow{row}}

	// 12️⃣ Enviar mensaje con manejo básico de FLOOD_WAIT
	_, err = ctx.Reply(u, message, &ext.ReplyOpts{
		Markup:           markup,
		ReplyToMessageId: u.EffectiveMessage.ID,
	})
	if err != nil {
		if strings.Contains(err.Error(), "FLOOD_WAIT") {
			m.log.Sugar().Warnf("FLOOD_WAIT detected, skipping reply: %v", err)
		} else {
			m.log.Sugar().Errorf("Error enviando mensaje: %v", err)
		}
	}

	return dispatcher.EndGroups
}

// getExtensionFromMIME retorna extensión por MIME
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

// getFileEmoji retorna emoji según MIME
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

// formatFileSize formatea bytes a KB, MB, GB
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
