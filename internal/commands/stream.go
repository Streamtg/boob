package commands

import (
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

// Extensiones consideradas "raras" para navegador
var rareVideoExts = []string{".mkv", ".avi", ".mov", ".flv", ".wmv", ".webm"}

func isRareFormat(filename string) bool {
	ext := strings.ToLower(filepath.Ext(filename))
	for _, e := range rareVideoExts {
		if ext == e {
			return true
		}
	}
	return false
}

func sendLink(ctx *ext.Context, u *ext.Update) error {
	chatID := u.EffectiveChat().GetID()
	peerChatId := ctx.PeerStorage.GetPeerById(chatID)
	if peerChatId.Type != int(storage.TypeUser) {
		return dispatcher.EndGroups
	}

	if len(config.ValueOf.AllowedUsers) != 0 && !utils.Contains(config.ValueOf.AllowedUsers, chatID) {
		ctx.Reply(u, "You are not allowed to use this bot.", nil)
		return dispatcher.EndGroups
	}

	if config.ValueOf.ForceSubChannel != "" {
		isSubscribed, err := utils.IsUserSubscribed(ctx, ctx.Raw, ctx.PeerStorage, chatID)
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

	update, err := utils.ForwardMessages(ctx, chatID, config.ValueOf.LogChannelID, u.EffectiveMessage.ID)
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

	if file.FileName == "" {
		file.FileName = "file" + filepath.Ext(file.MimeType)
	}

	// Lanzar goroutine para conversión y subida si formato raro
	if isRareFormat(file.FileName) {
		go func(file *utils.File) {
			ctx.Reply(u, "⚠️ The video format is not supported by browsers. Converting to MP4...", nil)

			tmpFile := filepath.Join(os.TempDir(), file.FileName)
			outFile := strings.TrimSuffix(tmpFile, filepath.Ext(tmpFile)) + ".mp4"

			if err := utils.DownloadFile(ctx, file, tmpFile); err != nil {
				ctx.Reply(u, fmt.Sprintf("Error downloading file: %s", err.Error()), nil)
				return
			}

			cmd := exec.Command("ffmpeg", "-y", "-i", tmpFile, "-c:v", "libx264", "-preset", "fast", "-c:a", "aac", outFile)
			if err := cmd.Run(); err != nil {
				ctx.Reply(u, fmt.Sprintf("Error converting video: %s", err.Error()), nil)
				os.Remove(tmpFile)
				return
			}

			uploaded, err := utils.UploadFileToChannel(ctx, config.ValueOf.LogChannelID, outFile)
			if err != nil {
				ctx.Reply(u, fmt.Sprintf("Error uploading converted video: %s", err.Error()), nil)
				os.Remove(tmpFile)
				os.Remove(outFile)
				return
			}

			os.Remove(tmpFile)
			os.Remove(outFile)

			// Construir mensaje final para streaming
			emoji := fileTypeEmoji(uploaded.MimeType)
			size := formatFileSize(uploaded.FileSize)
			message := fmt.Sprintf("%s File Name: %s\n\n%s File Type: %s\n\n💾 Size: %s\n\n⏳ @yoelbots",
				emoji, uploaded.FileName, emoji, uploaded.MimeType, size)

			fullHash := utils.PackFile(uploaded.FileName, uploaded.FileSize, uploaded.MimeType, uploaded.ID)
			hash := utils.GetShortHash(fullHash)

			row := tg.KeyboardButtonRow{}
			videoParam := fmt.Sprintf("%d?hash=%s", messageID, hash)
			encodedVideoParam := url.QueryEscape(videoParam)
			encodedFilename := url.QueryEscape(uploaded.FileName)
			streamURL := fmt.Sprintf("https://file.streamgramm.workers.dev/?video=%s&filename=%s", encodedVideoParam, encodedFilename)
			row.Buttons = append(row.Buttons, &tg.KeyboardButtonURL{
				Text: "Streaming / Download",
				URL:  streamURL,
			})
			markup := &tg.ReplyInlineMarkup{Rows: []tg.KeyboardButtonRow{row}}

			ctx.Reply(u, "✅ Video is ready for streaming!", &ext.ReplyOpts{Markup: markup})

			statsCache := cache.GetStatsCache()
			if statsCache != nil {
				_ = statsCache.RecordFileProcessed(uploaded.FileSize)
			}
		}(file)

		return dispatcher.EndGroups
	}

	// Caso normal (no raro)
	emoji := fileTypeEmoji(file.MimeType)
	size := formatFileSize(file.FileSize)
	message := fmt.Sprintf("%s File Name: %s\n\n%s File Type: %s\n\n💾 Size: %s\n\n⏳ @yoelbots",
		emoji, file.FileName, emoji, file.MimeType, size)

	fullHash := utils.PackFile(file.FileName, file.FileSize, file.MimeType, file.ID)
	hash := utils.GetShortHash(fullHash)

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
