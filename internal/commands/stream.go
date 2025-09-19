package commands

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
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

func (m *command) LoadStream(dispatcher dispatcher.Dispatcher) {
	defer m.log.Sugar().Info("Loaded")
	dispatcher.AddHandler(
		handlers.NewMessage(nil, sendLink),
	)
}

func supportedMediaFilter(m *types.Message) (bool, error) {
	if m.Media == nil {
		return false, dispatcher.EndGroups
	}
	switch m.Media.(type) {
	case *tg.MessageMediaDocument:
		return true, nil
	case *tg.MessageMediaPhoto:
		return true, nil
	default:
		return false, nil
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
	default:
		return "📄"
	}
}

func sendLink(ctx *ext.Context, u *ext.Update) error {
	chatID := u.EffectiveChat().GetID()
	peer := ctx.PeerStorage.GetPeerById(chatID)
	if peer.Type != int(storage.TypeUser) {
		return dispatcher.EndGroups
	}

	if len(config.ValueOf.AllowedUsers) > 0 && !utils.Contains(config.ValueOf.AllowedUsers, chatID) {
		ctx.Reply(u, "You are not allowed to use this bot.", nil)
		return dispatcher.EndGroups
	}

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
			ctx.Reply(u, "Please join our channel to get stream links.", &ext.ReplyOpts{Markup: markup})
			return dispatcher.EndGroups
		}
	}

	supported, err := supportedMediaFilter(u.EffectiveMessage)
	if err != nil || !supported {
		ctx.Reply(u, "Sorry, this message type is unsupported.", nil)
		return dispatcher.EndGroups
	}

	// Forward to log channel
	update, err := utils.ForwardMessages(ctx, chatID, config.ValueOf.LogChannelID, u.EffectiveMessage.ID)
	if err != nil {
		ctx.Reply(u, fmt.Sprintf("Error forwarding message: %s", err), nil)
		return dispatcher.EndGroups
	}

	messageID := update.Updates[0].(*tg.UpdateMessageID).ID
	docMedia := update.Updates[1].(*tg.UpdateNewChannelMessage).Message.(*tg.Message).Media
	tgDoc := docMedia.Document

	// Obtener info del archivo
	fileName := tgDoc.FileName
	mimeType := tgDoc.MimeType
	fileSize := tgDoc.Size

	if fileName == "" {
		fileName = "unknown_file"
		if strings.Contains(strings.ToLower(mimeType), "video") {
			fileName += ".mp4"
		} else {
			fileName += ".bin"
		}
	}

	// Descargar archivo temporalmente
	tempPath := filepath.Join(os.TempDir(), fileName)
	err = utils.DownloadFileFromDocument(ctx.Raw, tgDoc, tempPath)
	if err != nil {
		ctx.Reply(u, fmt.Sprintf("Error downloading file: %s", err), nil)
		return dispatcher.EndGroups
	}

	// Detectar si video raro y convertir a MP4
	if !strings.HasSuffix(strings.ToLower(fileName), ".mp4") && strings.Contains(strings.ToLower(mimeType), "video") {
		ctx.Reply(u, "Detected unusual video format, converting to MP4...", nil)

		convertedPath := tempPath + "_converted.mp4"
		cmd := exec.Command("ffmpeg", "-i", tempPath, "-c:v", "libx264", "-preset", "fast", "-c:a", "aac", "-b:a", "128k", convertedPath)
		if err := cmd.Run(); err != nil {
			ctx.Reply(u, fmt.Sprintf("Error converting video: %s", err), nil)
			os.Remove(tempPath)
			return dispatcher.EndGroups
		}
		os.Remove(tempPath)
		fileName = filepath.Base(convertedPath)
		tempPath = convertedPath
	}

	// Subir al canal de log
	logMsg, err := utils.UploadDocumentToChannel(ctx.Raw, config.ValueOf.LogChannelID, tempPath)
	if err != nil {
		ctx.Reply(u, fmt.Sprintf("Error uploading to log channel: %s", err), nil)
		os.Remove(tempPath)
		return dispatcher.EndGroups
	}

	// Borrar archivo temporal
	os.Remove(tempPath)

	streamParam := fmt.Sprintf("%d", logMsg.ID)
	encodedVideoParam := url.QueryEscape(streamParam)
	encodedFilename := url.QueryEscape(fileName)
	streamURL := fmt.Sprintf("https://file.streamgramm.workers.dev/?video=%s&filename=%s", encodedVideoParam, encodedFilename)

	message := fmt.Sprintf(
		"🎬 File Name: %s\n💾 Size: %s\n⏳ @yoelbots",
		fileName,
		formatFileSize(fileSize),
	)

	row := tg.KeyboardButtonRow{
		Buttons: []tg.KeyboardButtonClass{
			&tg.KeyboardButtonURL{
				Text: "Streaming / Download",
				URL:  streamURL,
			},
		},
	}
	markup := &tg.ReplyInlineMarkup{Rows: []tg.KeyboardButtonRow{row}}

	ctx.Reply(u, message, &ext.ReplyOpts{
		Markup:           markup,
		NoWebpage:        false,
		ReplyToMessageId: u.EffectiveMessage.ID,
	})

	statsCache := cache.GetStatsCache()
	if statsCache != nil {
		_ = statsCache.RecordFileProcessed(fileSize)
	}

	return dispatcher.EndGroups
}
