package commands

import (
	"fmt"
	"strings"

	"EverythingSuckz/fsb/config"
	"EverythingSuckz/fsb/internal/cache"
	"EverythingSuckz/fsb/internal/utils"

	"github.com/celestix/gotgproto/dispatcher"
	"github.com/celestix/gotgproto/dispatcher/handlers"
	"github.com/celestix/gotgproto/dispatcher/handlers/styling"
	"github.com/celestix/gotgproto/ext"
	"github.com/celestix/gotgproto/storage"
	"github.com/celestix/gotgproto/types"
	"github.com/gotd/td/tg"
	"go.uber.org/zap"
)

// LoadStream registers the handler for media messages.
// Note: 'command' struct is already defined in commands.go
func (m *command) LoadStream(dispatcher dispatcher.Dispatcher) {
	m.log.Named("stream").Info("Initializing media streaming handler...")
	
	dispatcher.AddHandler(
		handlers.NewMessage(m.supportedMediaFilter, m.sendLink),
	)
}

// supportedMediaFilter ensures only valid media (Documents/Photos) are processed.
func (m *command) supportedMediaFilter(ctx *ext.Context, u *ext.Update) error {
	msg := u.EffectiveMessage
	if msg.Media == nil {
		return dispatcher.EndGroups
	}
	switch msg.Media.(type) {
	case *tg.MessageMediaDocument, *tg.MessageMediaPhoto:
		return nil
	default:
		return dispatcher.EndGroups
	}
}

// formatFileSize converts bytes to a human-readable string.
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

// getEmojiByMime returns a visual indicator for the file type.
func getEmojiByMime(mime string) string {
	mime = strings.ToLower(mime)
	switch {
	case strings.Contains(mime, "video"):
		return "🎬"
	case strings.Contains(mime, "image"):
		return "🖼️"
	case strings.Contains(mime, "audio"):
		return "🎵"
	default:
		return "📄"
	}
}

// sendLink handles the logic of generating and sending the RESTful link.
func (m *command) sendLink(ctx *ext.Context, u *ext.Update) error {
	chatId := u.EffectiveChat().GetID()
	
	// Enforce Private Chat only
	peer := ctx.PeerStorage.GetPeerById(chatId)
	if peer.Type != int(storage.TypeUser) {
		return dispatcher.EndGroups
	}

	// 1. Subscription Check (Join Logic)
	if config.ValueOf.ForceSubChannel != "" {
		isSubscribed, err := utils.IsUserSubscribed(ctx, ctx.Raw, ctx.PeerStorage, chatId)
		if err != nil || !isSubscribed {
			joinURL := fmt.Sprintf("https://t.me/%s", config.ValueOf.ForceSubChannel)
			msg := fmt.Sprintf("⚠️ **Subscription Required**\n\nPlease join our channel to use this service:\n%s", joinURL)
			_, _ = ctx.Reply(u, styling.Markdown(msg), &ext.ReplyOpts{NoWebpage: true})
			return dispatcher.EndGroups
		}
	}

	// 2. Forward to Log Channel for persistent FileID
	m.log.Debug("Forwarding message to log channel", zap.Int64("chat_id", chatId))
	update, err := utils.ForwardMessages(ctx, chatId, config.ValueOf.LogChannelID, u.EffectiveMessage.ID)
	if err != nil {
		m.log.Error("Forwarding failed", zap.Error(err))
		_, _ = ctx.Reply(u, "❌ **Error:** Failed to process file in log channel.", nil)
		return dispatcher.EndGroups
	}

	// 3. Extract Message ID and Media Metadata
	msgID := update.Updates[0].(*tg.UpdateMessageID).ID
	channelMsg, ok := update.Updates[1].(*tg.UpdateNewChannelMessage).Message.(*tg.Message)
	if !ok {
		return dispatcher.EndGroups
	}

	file, err := utils.FileFromMedia(channelMsg.Media)
	if err != nil {
		m.log.Error("Metadata extraction failed", zap.Error(err))
		return dispatcher.EndGroups
	}

	// 4. Construct the RESTful Link: domain/msgID/hash
	fullHash := utils.PackFile(file.FileName, file.FileSize, file.MimeType, file.ID)
	shortHash := utils.GetShortHash(fullHash)
	
	baseUrl := strings.TrimSuffix(config.ValueOf.WorkerURL, "/")
	finalURL := fmt.Sprintf("%s/%d/%s", baseUrl, msgID, shortHash)

	// 5. Build and Send Response
	emoji := getEmojiByMime(file.MimeType)
	sizeStr := formatFileSize(file.FileSize)
	
	caption := fmt.Sprintf(
		"%s **File:** `%s`\n"+
			"💾 **Size:** `%s`\n\n"+
			"🚀 **Stream/Download Link:**\n`%s`\n\n"+
			"⚡ *Powered by @yoelbots*",
		emoji, file.FileName, sizeStr, finalURL,
	)

	// Update stats in cache
	stats := cache.GetStatsCache()
	if stats != nil {
		_ = stats.RecordFileProcessed(file.FileSize)
	}

	_, err = ctx.Reply(u, styling.Markdown(caption), &ext.ReplyOpts{
		NoWebpage:        false,
		ReplyToMessageId: u.EffectiveMessage.ID,
	})
	
	return dispatcher.EndGroups
}
