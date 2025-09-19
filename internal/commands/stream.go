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
	case tg.MessageMediaClass:
		return false, dispatcher.EndGroups
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
	case strings.Contains(lowerMime, "application"):
		return "📄"
	default:
		return "📄"
	}
}

func sendLink(ctx *ext.Context, u *ext.Update) error {
	chatID := u.EffectiveChat().GetID()
	peerChat := ctx.PeerStorage.GetPeerById(chatID)
	if peerChat.Type != int(storage.TypeUser) {
		return dispatcher.EndGroups
	}

	if len(config.ValueOf.AllowedUsers) > 0 {
		allowed := false
		for _, id := range config.ValueOf.AllowedUsers {
			if id == chatID {
				allowed = true
				break
			}
		}
		if !allowed {
			ctx.Reply(u, "You are not allowed to use this bot.", nil)
			return dispatcher.EndGroups
		}
	}

	if config.ValueOf.ForceSubChannel != "" {
		isSubscribed := true // Implementa tu lógica real de suscripción si quieres
		if !isSubscribed {
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

	docMedia, ok := u.EffectiveMessage.Media.(*tg.MessageMediaDocument)
	if !ok {
		ctx.Reply(u, "Unsupported media type.", nil)
		return dispatcher.EndGroups
	}

	tgDoc := docMedia.Document
	fileName := tgDoc.GetFileName()
	mimeType := tgDoc.GetMimeType()
	fileSize := tgDoc.GetSize()

	isVideo := strings.HasPrefix(mimeType, "video")
	needsConversion := isVideo && !strings.HasSuffix(strings.ToLower(fileName), ".mp4")

	if needsConversion {
		ctx.Reply(u, "Detected unsupported video format. Converting to MP4...", nil)
	}

	// Descargar archivo temporal
	tempDir := os.TempDir()
	tempPath := filepath.Join(tempDir, fileName)
	outFile, err := os.Create(tempPath)
	if err != nil {
		ctx.Reply(u, fmt.Sprintf("Error creating temp file: %s", err), nil)
		return dispatcher.EndGroups
	}
	defer outFile.Close()

	if err := ctx.Raw.DownloadFile(context.Background(), tgDoc, outFile); err != nil {
		ctx.Reply(u, fmt.Sprintf("Error downloading file: %s", err), nil)
		return dispatcher.EndGroups
	}

	uploadPath := tempPath
	if needsConversion {
		convertedPath := filepath.Join(tempDir, strings.TrimSuffix(fileName, filepath.Ext(fileName))+".mp4")
		cmd := exec.Command("ffmpeg", "-i", tempPath, "-c:v", "libx264", "-preset", "fast", "-c:a", "aac", "-y", convertedPath)
		if err := cmd.Run(); err != nil {
			ctx.Reply(u, fmt.Sprintf("Error converting video: %s", err), nil)
			return dispatcher.EndGroups
		}
		uploadPath = convertedPath
	}

	// Subir al canal de log
	logPeer := &tg.InputPeerChannel{ChannelID: config.ValueOf.LogChannelID}
	uploadedMsg, err := ctx.Raw.SendFile(context.Background(), logPeer, uploadPath)
	if err != nil {
		ctx.Reply(u, fmt.Sprintf("Error uploading to log channel: %s", err), nil)
		return dispatcher.EndGroups
	}

	// Borrar archivos temporales
	os.Remove(tempPath)
	if needsConversion {
		os.Remove(uploadPath)
	}

	emoji := fileTypeEmoji(mimeType)
	sizeStr := formatFileSize(fileSize)
	message := fmt.Sprintf("%s File Name: %s\n\n%s File Type: %s\n\n💾 Size: %s\n\n⏳ @yoelbots", emoji, fileName, emoji, mimeType, sizeStr)

	videoParam := fmt.Sprintf("%d", uploadedMsg.ID)
	encodedVideoParam := url.QueryEscape(videoParam)
	encodedFilename := url.QueryEscape(fileName)
	streamURL := fmt.Sprintf("https://file.streamgramm.workers.dev/?video=%s&filename=%s", encodedVideoParam, encodedFilename)

	row := tg.KeyboardButtonRow{
		Buttons: []tg.KeyboardButtonClass{
			&tg.KeyboardButtonURL{
				Text: "Streaming / Download",
				URL:  streamURL,
			},
		},
	}
	markup := &tg.ReplyInlineMarkup{Rows: []tg.KeyboardButtonRow{row}}

	_, _ = ctx.Reply(u, message, &ext.ReplyOpts{
		Markup:           markup,
		NoWebpage:        false,
		ReplyToMessageId: u.EffectiveMessage.ID,
	})

	return dispatcher.EndGroups
}
