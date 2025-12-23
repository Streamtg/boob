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
	"go.uber.org/zap"
)

// LoadStream registers the handler. 'command' struct is in commands.go
func (m *command) LoadStream(dispatcher dispatcher.Dispatcher) {
	m.log.Named("stream").Info("Streaming handler initialized")
	dispatcher.AddHandler(handlers.NewMessage(m.supportedMediaFilter, m.sendLink))
}

func (m *command) supportedMediaFilter(ctx *ext.Context, u *ext.Update) error {
	if u.EffectiveMessage.Media == nil {
		return dispatcher.EndGroups
	}
	switch u.EffectiveMessage.Media.(type) {
	case *tg.MessageMediaDocument, *tg.MessageMediaPhoto:
		return nil
	default:
		return dispatcher.EndGroups
	}
}

func formatFileSize(bytes int64) string {
	const (KB, MB, GB = 1024, 1024 * 1024, 1024 * 1024 * 1024)
	switch {
	case bytes >= GB: return fmt.Sprintf("%.2f GB", float64(bytes)/float64(GB))
	case bytes >= MB: return fmt.Sprintf("%.2f MB", float64(bytes)/float64(MB))
	default: return fmt.Sprintf("%.2f KB", float64(bytes)/float64(KB))
	}
}

func (m *command) sendLink(ctx *ext.Context, u *ext.Update) error {
	chatId := u.EffectiveChat().GetID()
	if ctx.PeerStorage.GetPeerById(chatId).Type != int(storage.TypeUser) {
		return dispatcher.EndGroups
	}

	// Subscription Check
	if config.ValueOf.ForceSubChannel != "" {
		isSubscribed, err := utils.IsUserSubscribed(ctx, ctx.Raw, ctx.PeerStorage, chatId)
		if err != nil || !isSubscribed {
			joinURL := fmt.Sprintf("https://t.me/%s", config.ValueOf.ForceSubChannel)
			_, _ = ctx.Reply(u, "⚠️ Join our channel to use this bot:\n"+joinURL, &ext.ReplyOpts{NoWebpage: true})
			return dispatcher.EndGroups
		}
	}

	// Forward to Logs
	update, err := utils.ForwardMessages(ctx, chatId, config.ValueOf.LogChannelID, u.EffectiveMessage.ID)
	if err != nil {
		_, _ = ctx.Reply(u, "❌ Forwarding Error", nil)
		return dispatcher.EndGroups
	}

	msgID := update.Updates[0].(*tg.UpdateMessageID).ID
	channelMsg := update.Updates[1].(*tg.UpdateNewChannelMessage).Message.(*tg.Message)
	file, _ := utils.FileFromMedia(channelMsg.Media)

	// URL Generation
	fullHash := utils.PackFile(file.FileName, file.FileSize, file.MimeType, file.ID)
	shortHash := utils.GetShortHash(fullHash)
	baseUrl := strings.TrimSuffix(config.ValueOf.WorkerURL, "/")
	
	// Format: domain/msgID/shortHash
	finalURL := fmt.Sprintf("%s/%d/%s", baseUrl, msgID, shortHash)

	caption := fmt.Sprintf(
		"🎬 **File:** `%s`\n"+
			"💾 **Size:** `%s`\n\n"+
			"🚀 **Direct Link:**\n`%s`\n\n"+
			"⚡ *By @yoelbots*",
		file.FileName, formatFileSize(file.FileSize), finalURL,
	)

	// We use the helper to parse entities automatically
	_, err = ctx.Reply(u, caption, &ext.ReplyOpts{
		NoWebpage: true,
		// gotgproto often uses ParseMode: "markdown" inside ReplyOpts if available, 
		// or automatically detects if you use the styling wrapper.
	})
	
	return dispatcher.EndGroups
}
