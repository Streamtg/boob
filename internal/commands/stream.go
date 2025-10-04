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

// LoadStream registers the main message handler
func (m *command) LoadStream(dispatcher dispatcher.Dispatcher) {
	defer m.log.Sugar().Info("Stream handler loaded")
	dispatcher.AddHandler(handlers.NewMessage(nil, sendLink))
}

// supportedMediaFilter checks if the message contains supported media
func supportedMediaFilter(m *types.Message) (bool, error) {
	if m.Media == nil {
		return false, dispatcher.EndGroups
	}
	switch m.Media.(type) {
	case *tg.MessageMediaDocument, *tg.MessageMediaPhoto:
		return true, nil
	default:
		return false, dispatcher.EndGroups
	}
}

// formatFileSize converts bytes to human-readable string
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

// fileTypeEmoji returns an emoji representing the file type
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

// buildStreamURL constructs the streaming/download link
func buildStreamURL(messageID int, hash, filename string) string {
	videoParam := fmt.Sprintf("%d?hash=%s", messageID, hash)
	return fmt.Sprintf(
		"https://file.streamgramm.workers.dev/?video=%s&filename=%s",
		url.QueryEscape(videoParam),
		url.QueryEscape(filename),
	)
}

// replyError centralizes error responses
func replyError(ctx *ext.Context, u *ext.Update, err error) dispatcher.Control {
	ctx.Reply(u, fmt.Sprintf("⚠️ Error: %s", err.Error()), nil)
	return dispatcher.EndGroups
}

// sendLink handles messages with media and generates streaming/download links
func sendLink(ctx *ext.Context, u *ext.Update) error {
	chatId := u.EffectiveChat().GetID()
	peerChat := ctx.PeerStorage.GetPeerById(chatId)
	if peerChat == nil || peerChat.Type != int(storage.TypeUser) {
		return dispatcher.EndGroups
	}

	// Check allowed users
	if len(config.ValueOf.AllowedUsers) != 0 && !utils.Contains(config.ValueOf.AllowedUsers, chatId) {
		ctx.Reply(u, "⚠️ You are not allowed to use this bot.", nil)
		return dispatcher.EndGroups
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
			ctx.Reply(u, "⚠️ Please join our channel to access streaming/download links.", &ext.ReplyOpts{Markup: markup})
			return dispatcher.EndGroups
		}
	}

	// Validate media type
	supported, err := supportedMediaFilter(u.EffectiveMessage)
	if err != nil {
		return replyError(ctx, u, err)
	}
	if !supported {
		ctx.Reply(u, "⚠️ Sorry, this message type is unsupported.", nil)
		return dispatcher.EndGroups
	}

	// Forward message to log channel
	update, err := utils.ForwardMessages(ctx, chatId, config.ValueOf.LogChannelID, u.EffectiveMessage.ID)
	if err != nil {
		return replyError(ctx, u, err)
	}

	messageID := update.Updates[0].(*tg.UpdateMessageID).ID
	doc := update.Updates[1].(*tg.UpdateNewChannelMessage).Message.(*tg.Message).Media
	file, err := utils.FileFromMedia(doc)
	if err != nil {
		return replyError(ctx, u, err)
	}

	if file.FileName == "" {
		file.FileName = "unknown"
	}

	emoji := fileTypeEmoji(file.MimeType)
	message := fmt.Sprintf(
		"🎬 *File Name:* %s\n🎬 *File Type:* %s\n🎬 *File Size:* %s\n\n⚠️ *No child abuse content allowed. Violators will be banned and reported.*\n⏳ @yoelbots",
		file.FileName,
		file.MimeType,
		formatFileSize(file.FileSize),
	)

	// Generate short hash for link
	fullHash := utils.PackFile(file.FileName, file.FileSize, file.MimeType, file.ID)
	hash := utils.GetShortHash(fullHash)

	// Record stats
	statsCache := cache.GetStatsCache()
	if statsCache != nil {
		_ = statsCache.RecordFileProcessed(file.FileSize)
	}

	// Build streaming/download button
	row := tg.KeyboardButtonRow{}
	streamURL := buildStreamURL(messageID, hash, file.FileName)
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
		return replyError(ctx, u, err)
	}

	m.log.Sugar().Infof("User %d processed file '%s' (%s), link: %s", chatId, file.FileName, file.MimeType, streamURL)

	return dispatcher.EndGroups
}
