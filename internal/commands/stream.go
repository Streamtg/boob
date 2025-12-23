package commands

import (
	"fmt"
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

// command defines the base structure for command handlers
type command struct {
	log interface {
		Sugar() interface {
			Info(args ...interface{})
		}
	}
}

// LoadStream registers the primary message handler for media content
func (m *command) LoadStream(dispatcher dispatcher.Dispatcher) {
	defer m.log.Sugar().Info("Streaming module loaded successfully")
	
	dispatcher.AddHandler(
		handlers.NewMessage(nil, sendLink),
	)
}

// supportedMediaFilter validates if the message contains processable media (Document or Photo)
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

// formatFileSize converts raw bytes into a human-readable string (KB, MB, GB)
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

// fileTypeEmoji returns a context-aware emoji based on the file MIME type
func fileTypeEmoji(mime string) string {
	mime = strings.ToLower(mime)
	switch {
	case strings.Contains(mime, "video"):
		return "🎬"
	case strings.Contains(mime, "image"):
		return "🖼️"
	case strings.Contains(mime, "audio"):
		return "🎵"
	case strings.Contains(mime, "pdf"):
		return "📕"
	case strings.Contains(mime, "zip"), strings.Contains(mime, "rar"), strings.Contains(mime, "compressed"):
		return "🗜️"
	default:
		return "📄"
	}
}

// sendLink processes incoming media and replies with a formatted stream/download URL
func sendLink(ctx *ext.Context, u *ext.Update) error {
	chatId := u.EffectiveChat().GetID()
	
	// Ensure the interaction is occurring within a private chat
	peer := ctx.PeerStorage.GetPeerById(chatId)
	if peer.Type != int(storage.TypeUser) {
		return dispatcher.EndGroups
	}

	// ACL: Access Control List check
	if len(config.ValueOf.AllowedUsers) != 0 && !utils.Contains(config.ValueOf.AllowedUsers, chatId) {
		_, _ = ctx.Reply(u, "🚫 **Access Denied:** You are not authorized to use this bot.", nil)
		return dispatcher.EndGroups
	}

	// Force Subscription Logic
	if config.ValueOf.ForceSubChannel != "" {
		isSubscribed, err := utils.IsUserSubscribed(ctx, ctx.Raw, ctx.PeerStorage, chatId)
		if err != nil || !isSubscribed {
			joinURL := fmt.Sprintf("https://t.me/%s", config.ValueOf.ForceSubChannel)
			msg := fmt.Sprintf("⚠️ **Subscription Required:**\n\nPlease join our channel to generate links:\n\n👉 %s", joinURL)
			_, _ = ctx.Reply(u, msg, &ext.ReplyOpts{NoWebpage: true})
			return dispatcher.EndGroups
		}
	}

	// Filter supported media types
	supported, err := supportedMediaFilter(u.EffectiveMessage)
	if err != nil || !supported {
		// Silently ignore unsupported media to avoid spamming the user
		return dispatcher.EndGroups
	}

	// Forward message to the Log Channel to obtain a persistent FileID and MessageID
	update, err := utils.ForwardMessages(ctx, chatId, config.ValueOf.LogChannelID, u.EffectiveMessage.ID)
	if err != nil {
		_, _ = ctx.Reply(u, fmt.Sprintf("❌ **Forwarding Error:** %s", err.Error()), nil)
		return dispatcher.EndGroups
	}

	// Assertions to extract MessageID and Media data from the update sequence
	msgID := update.Updates[0].(*tg.UpdateMessageID).ID
	channelMsg, ok := update.Updates[1].(*tg.UpdateNewChannelMessage).Message.(*tg.Message)
	if !ok {
		_, _ = ctx.Reply(u, "❌ **Data Error:** Could not process media object.", nil)
		return dispatcher.EndGroups
	}

	file, err := utils.FileFromMedia(channelMsg.Media)
	if err != nil {
		_, _ = ctx.Reply(u, fmt.Sprintf("❌ **Media Error:** %s", err.Error()), nil)
		return dispatcher.EndGroups
	}

	// URL Construction: https://{domain}/{message_id}/{hash}
	fullHash := utils.PackFile(file.FileName, file.FileSize, file.MimeType, file.ID)
	shortHash := utils.GetShortHash(fullHash)
	
	baseWorkerURL := strings.TrimSuffix(config.ValueOf.WorkerURL, "/")
	finalStreamURL := fmt.Sprintf("%s/%d/%s", baseWorkerURL, msgID, shortHash)

	// Record metrics in cache
	statsCache := cache.GetStatsCache()
	if statsCache != nil {
		_ = statsCache.RecordFileProcessed(file.FileSize)
	}

	// Metadata Formatting
	emoji := fileTypeEmoji(file.MimeType)
	sizeStr := formatFileSize(file.FileSize)
	
	// Building the response text (No Inline Buttons as requested)
	caption := fmt.Sprintf(
		"%s **File Name:** `%s`\n\n"+
			"📂 **Type:** `%s`\n"+
			"💾 **Size:** `%s`\n\n"+
			"🚀 **Direct Link:**\n`%s`\n\n"+
			"⚡ *Powered by @Hostwave_bot*",
		emoji, file.FileName, file.MimeType, sizeStr, finalStreamURL,
	)

	// Final execution of the reply
	_, err = ctx.Reply(u, caption, &ext.ReplyOpts{
		NoWebpage:        false,
		ReplyToMessageId: u.EffectiveMessage.ID,
		ParseMode:        "Markdown",
	})
	
	return dispatcher.EndGroups
}
