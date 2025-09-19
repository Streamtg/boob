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

// Convert bytes to human-readable size
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

// Emoji based on file type
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

// Simple hash function to replace utils.PackFile and utils.GetShortHash
func generateHash(fileName string, fileSize int64, mimeType string, fileID int64) string {
	hashInput := fmt.Sprintf("%s%d%s%d", fileName, fileSize, mimeType, fileID)
	hash := fmt.Sprintf("%x", []byte(hashInput)) // Simple hex string from input
	return hash[:8]                             // Truncate to 8 chars for short hash
}

// Get file size from path
func getFileSize(path string) (int64, error) {
	fileInfo, err := os.Stat(path)
	if err != nil {
		return 0, err
	}
	return fileInfo.Size(), nil
}

// Extract file info from media
func fileFromMedia(media tg.MessageMediaClass) (*struct {
	ID       int64
	FileName string
	MimeType string
	FileSize int64
	DcID     int
}, error) {
	switch m := media.(type) {
	case *tg.MessageMediaDocument:
		doc := m.Document.(*tg.Document)
		fileName := ""
		for _, attr := range doc.Attributes {
			if attr, ok := attr.(*tg.DocumentAttributeFilename); ok {
				fileName = attr.FileName
				break
			}
		}
		return &struct {
			ID       int64
			FileName string
			MimeType string
			FileSize int64
			DcID     int
		}{
			ID:       doc.ID,
			FileName: fileName,
			MimeType: doc.MimeType,
			FileSize: doc.Size,
			DcID:     doc.DCID,
		}, nil
	case *tg.MessageMediaPhoto:
		photo := m.Photo.(*tg.Photo)
		return &struct {
			ID       int64
			FileName string
			MimeType string
			FileSize int64
			DcID     int
		}{
			ID:       photo.ID,
			FileName: "photo.jpg",
			MimeType: "image/jpeg",
			FileSize: photo.Sizes[len(photo.Sizes)-1].(*tg.PhotoSize).Size,
			DcID:     photo.DCID,
		}, nil
	default:
		return nil, fmt.Errorf("unsupported media type")
	}
}

// Forward message to log channel
func forwardMessages(ctx *ext.Context, fromChatID, toChatID int64, messageID int) (*tg.Updates, error) {
	return ctx.Raw.MessagesForwardMessages(&tg.MessagesForwardMessagesRequest{
		FromPeer: &tg.InputPeerUser{UserID: fromChatID},
		ToPeer:   &tg.InputPeerChannel{ChannelID: toChatID},
		ID:       []int{messageID},
	})
}

func sendLink(ctx *ext.Context, u *ext.Update) error {
	chatId := u.EffectiveChat().GetID()
	peerChatId := ctx.PeerStorage.GetPeerById(chatId)
	if peerChatId.Type != int(storage.TypeUser) {
		return dispatcher.EndGroups
	}

	if len(config.ValueOf.AllowedUsers) != 0 {
		allowed := false
		for _, userID := range config.ValueOf.AllowedUsers {
			if userID == chatId {
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
		// Check subscription (simplified, assumes PeerStorage handles it)
		peer, err := ctx.PeerStorage.GetPeerByUsername(config.ValueOf.ForceSubChannel)
		if err != nil || peer.ID == 0 {
			ctx.Reply(u, "Error checking subscription.", nil)
			return dispatcher.EndGroups
		}
		isSubscribed := false
		// This is a simplified check; you may need to use ctx.Raw to verify
		// Replace with proper subscription check if available in your setup
		_, err = ctx.Raw.ChannelsGetParticipant(&tg.ChannelsGetParticipantRequest{
			Channel:    &tg.InputChannel{ChannelID: peer.ID},
			Participant: &tg.InputPeerUser{UserID: chatId},
		})
		if err == nil {
			isSubscribed = true
		}
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
	if err != nil {
		return err
	}
	if !supported {
		ctx.Reply(u, "Sorry, this message type is unsupported.", nil)
		return dispatcher.EndGroups
	}

	// Get original media
	media := u.EffectiveMessage.Media
	file, err := fileFromMedia(media)
	if err != nil {
		ctx.Reply(u, fmt.Sprintf("Error - %s", err.Error()), nil)
		return dispatcher.EndGroups
	}

	// Detect if it's a video and needs conversion (non-MP4 formats like MKV, AVI)
	isVideo := strings.HasPrefix(strings.ToLower(file.MimeType), "video/")
	needsConversion := isVideo && !strings.HasSuffix(strings.ToLower(file.FileName), ".mp4")

	var messageID int
	var processingMsg *types.Message

	if needsConversion {
		// Send processing message
		procMsg, err := ctx.Reply(u, "Processing video: downloading, converting to MP4, and uploading... This may take a while.", nil)
		if err != nil {
			return err
		}
		processingMsg = procMsg

		// Create temp directory
		tempDir, err := os.MkdirTemp("", "fsb-*")
		if err != nil {
			ctx.Reply(u, fmt.Sprintf("Error creating temp directory - %s", err.Error()), nil)
			return dispatcher.EndGroups
		}
		defer os.RemoveAll(tempDir)

		inputPath := filepath.Join(tempDir, file.FileName)
		outputPath := filepath.Join(tempDir, strings.TrimSuffix(file.FileName, filepath.Ext(file.FileName))+".mp4")

		// Download media
		downloadOpts := &ext.DownloadOpts{
			DCId: file.DcID,
		}
		err = ctx.DownloadMedia(media, inputPath, downloadOpts)
		if err != nil {
			ctx.Reply(u, fmt.Sprintf("Error downloading file - %s", err.Error()), nil)
			return dispatcher.EndGroups
		}

		// Convert with FFmpeg
		cmd := exec.Command("ffmpeg", "-i", inputPath, "-c:v", "copy", "-c:a", "copy", "-movflags", "+faststart", outputPath)
		err = cmd.Run()
		if err != nil {
			// Fallback to re-encoding if stream copy fails
			cmd = exec.Command("ffmpeg", "-i", inputPath, "-c:v", "libx264", "-preset", "fast", "-c:a", "aac", "-movflags", "+faststart", outputPath)
			err = cmd.Run()
			if err != nil {
				ctx.Reply(u, fmt.Sprintf("Error converting video with FFmpeg - %s", err.Error()), nil)
				return dispatcher.EndGroups
			}
		}

		// Upload converted MP4 to log channel
		uploaded, err := ctx.UploadDocument(config.ValueOf.LogChannelID, outputPath, file.FileName+".mp4", nil)
		if err != nil {
			ctx.Reply(u, fmt.Sprintf("Error uploading converted video - %s", err.Error()), nil)
			return dispatcher.EndGroups
		}

		// Update file details for MP4
		messageID = uploaded.ID
		file.FileName = strings.TrimSuffix(file.FileName, filepath.Ext(file.FileName)) + ".mp4"
		file.MimeType = "video/mp4"
		file.FileSize, _ = getFileSize(outputPath)
		file.ID = uploaded.Media.(*tg.MessageMediaDocument).Document.(*tg.Document).ID
	} else {
		// Forward message for MP4 or other files
		update, err := forwardMessages(ctx, chatId, config.ValueOf.LogChannelID, u.EffectiveMessage.ID)
		if err != nil {
			ctx.Reply(u, fmt.Sprintf("Error - %s", err.Error()), nil)
			return dispatcher.EndGroups
		}

		messageID = update.Updates[0].(*tg.UpdateMessageID).ID
		doc := update.Updates[1].(*tg.UpdateNewChannelMessage).Message.(*tg.Message).Media
		file, err = fileFromMedia(doc)
		if err != nil {
			ctx.Reply(u, fmt.Sprintf("Error - %s", err.Error()), nil)
			return dispatcher.EndGroups
		}
	}

	// Set default file name if empty
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

	// Create message with emoji, type, and size
	emoji := fileTypeEmoji(file.MimeType)
	size := formatFileSize(file.FileSize)
	message := fmt.Sprintf(
		"%s File Name: %s\n\n%s File Type: %s\n\n💾 Size: %s\n\n⏳ @yoelbots",
		emoji, file.FileName,
		emoji, file.MimeType,
		size,
	)

	// Generate hash
	hash := generateHash(file.FileName, file.FileSize, file.MimeType, file.ID)

	// Update stats cache
	statsCache := cache.GetStatsCache()
	if statsCache != nil {
		_ = statsCache.RecordFileProcessed(file.FileSize)
	}

	// Create stream/download button
	var markup *tg.ReplyInlineMarkup
	row := tg.KeyboardButtonRow{}
	videoParam := fmt.Sprintf("%d?hash=%s", messageID, hash)
	encodedVideoParam := url.QueryEscape(videoParam)
	encodedFilename := url.QueryEscape(file.FileName)
	streamURL := fmt.Sprintf("https://file.streamgramm.workers.dev/?video=%s&filename=%s", encodedVideoParam, encodedFilename)
	row.Buttons = append(row.Buttons, &tg.KeyboardButtonURL{
		Text: "Streaming / Download",
		URL:  streamURL,
	})
	markup = &tg.ReplyInlineMarkup{Rows: []tg.KeyboardButtonRow{row}}

	// Send or edit final message
	replyOpts := &ext.ReplyOpts{
		Markup:           markup,
		NoWebpage:        false,
		ReplyToMessageId: u.EffectiveMessage.ID,
	}
	if needsConversion && processingMsg != nil {
		// Edit processing message with final result
		_, err = ctx.EditMessage(chatId, processingMsg.ID, message, replyOpts)
		if err != nil {
			ctx.Reply(u, fmt.Sprintf("Error editing message - %s", err.Error()), nil)
		}
		// Notify user
		ctx.Reply(u, "Stream link is now available!", nil)
	} else {
		// Send directly for MP4 or others
		_, err = ctx.Reply(u, message, replyOpts)
		if err != nil {
			ctx.Reply(u, fmt.Sprintf("Error - %s", err.Error()), nil)
		}
		// Notify for videos
		if isVideo {
			ctx.Reply(u, "Stream link is now available!", nil)
		}
	}

	return dispatcher.EndGroups
}
