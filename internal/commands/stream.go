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

// humanFileSize formats bytes into KB, MB, GB
func humanFileSize(size int64) string {
	const (
		KB = 1 << (10 * 1)
		MB = 1 << (10 * 2)
		GB = 1 << (10 * 3)
	)
	switch {
	case size >= GB:
		return fmt.Sprintf("%.2f GB", float64(size)/GB)
	case size >= MB:
		return fmt.Sprintf("%.2f MB", float64(size)/MB)
	case size >= KB:
		return fmt.Sprintf("%.2f KB", float64(size)/KB)
	default:
		return fmt.Sprintf("%d B", size)
	}
}

// friendlyFileType returns a human-friendly label with emoji
func friendlyFileType(mime string) string {
	switch {
	case strings.Contains(mime, "video"):
		return "🎬 Video"
	case strings.Contains(mime, "image"):
		return "🖼️ Image"
	case strings.Contains(mime, "audio"):
		return "🎵 Audio"
	case strings.Contains(mime, "pdf"):
		return "📄 PDF Document"
	case strings.Contains(mime, "zip"), strings.Contains(mime, "rar"), strings.Contains(mime, "tar"):
		return "📦 Archive"
	default:
		return "📁 File"
	}
}

// LoadStream registers the file handler
func (m *command) LoadStream(dispatcher dispatcher.Dispatcher) {
	log := m.log.Named("start")
	defer log.Sugar().Info("Loaded")
	dispatcher.AddHandler(
		handlers.NewMessage(nil, sendLink),
	)
}

// supportedMediaFilter returns true if the message contains supported media
func supportedMediaFilter(m *types.Message) (bool, error) {
	if m.Media == nil {
		return false, dispatcher.EndGroups
	}
	switch m.Media.(type) {
	case *tg.MessageMediaDocument, *tg.MessageMediaPhoto:
		return true, nil
	default:
		return false, nil
	}
}

// sendLink processes the received file and sends name, size, type, and streaming link
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

	// Check forced subscription
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

	// Check if media is supported
	supported, err := supportedMediaFilter(u.EffectiveMessage)
	if err != nil || !supported {
		ctx.Reply(u, "Sorry, this message type is unsupported.", nil)
		return dispatcher.EndGroups
	}

	// Forward message to log channel
	update, err := utils.ForwardMessages(ctx, chatId, config.ValueOf.LogChannelID, u.EffectiveMessage.ID)
	if err != nil {
		utils.Logger.Sugar().Error(err)
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

	fullHash := utils.PackFile(file.FileName, file.FileSize, file.MimeType, file.ID)
	hash := utils.GetShortHash(fullHash)
	link := fmt.Sprintf("%s/stream/%d?hash=%s", config.ValueOf.Host, messageID, hash)

	// Update statistics
	statsCache := cache.GetStatsCache()
	if statsCache != nil {
		_ = statsCache.RecordFileProcessed(file.FileSize)
	}

	// Build message with friendly file type
	message := fmt.Sprintf(
		"📄 File Name: %s\n📦 File Size: %s\n📝 File Type: %s",
		file.FileName,
		humanFileSize(file.FileSize),
		friendlyFileType(file.MimeType),
	)

	// Build streaming button
	row := tg.KeyboardButtonRow{}
	if strings.Contains(file.MimeType, "video") || strings.Contains(file.MimeType, "application/octet-stream") {
		videoParam := fmt.Sprintf("%d?hash=%s", messageID, hash)
		streamURL := fmt.Sprintf("https://file.streamgramm.workers.dev/?video=%s", videoParam)
		row.Buttons = append(row.Buttons, &tg.KeyboardButtonURL{
			Text: "Stream / Download",
			URL:  streamURL,
		})
	}

	markup := &tg.ReplyInlineMarkup{Rows: []tg.KeyboardButtonRow{row}}

	if strings.Contains(link, "http://localhost") {
		_, err = ctx.Reply(u, message, &ext.ReplyOpts{ReplyToMessageId: u.EffectiveMessage.ID})
	} else {
		_, err = ctx.Reply(u, message, &ext.ReplyOpts{Markup: markup, ReplyToMessageId: u.EffectiveMessage.ID})
	}

	if err != nil {
		utils.Logger.Sugar().Error(err)
		ctx.Reply(u, fmt.Sprintf("Error - %s", err.Error()), nil)
	}

	return dispatcher.EndGroups
}
