package commands

import (
	"fmt"
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
	"go.uber.org/zap"
)

func (m *command) LoadStream(dispatcher dispatcher.Dispatcher) {
	log := m.log.Named("start")
	defer log.Sugar().Info("Loaded")
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

// Universal file type detection function
func getUniversalFileTypeInfo(fileName, mimeType string) (icon, typeName, ext string) {
	ext = strings.ToUpper(strings.TrimPrefix(filepath.Ext(fileName), "."))
	lowerExt := strings.ToLower(ext)

	switch {
	case strings.Contains(mimeType, "video") || lowerExt == "mp4" || lowerExt == "mkv" || lowerExt == "mov" || lowerExt == "avi" || lowerExt == "flv":
		return "🎬", "Video", ext
	case strings.Contains(mimeType, "audio") || lowerExt == "mp3" || lowerExt == "wav" || lowerExt == "flac" || lowerExt == "aac" || lowerExt == "ogg":
		return "🎵", "Audio", ext
	case strings.Contains(mimeType, "image") || lowerExt == "png" || lowerExt == "jpg" || lowerExt == "jpeg" || lowerExt == "gif" || lowerExt == "bmp" || lowerExt == "tiff" || lowerExt == "webp":
		return "🖼️", "Image", ext
	case lowerExt == "pdf" || lowerExt == "doc" || lowerExt == "docx" || lowerExt == "txt" || lowerExt == "ppt" || lowerExt == "pptx" || lowerExt == "xls" || lowerExt == "xlsx":
		return "📄", "Document", ext
	case lowerExt == "zip" || lowerExt == "rar" || lowerExt == "7z" || lowerExt == "tar" || lowerExt == "gz" || lowerExt == "bz2":
		return "🗂️", "Compressed", ext
	case lowerExt == "py" || lowerExt == "js" || lowerExt == "go" || lowerExt == "java" || lowerExt == "c" || lowerExt == "cpp" || lowerExt == "cs" || lowerExt == "ts" || lowerExt == "rb" || lowerExt == "php":
		return "💻", "Code", ext
	case lowerExt == "exe" || lowerExt == "msi" || lowerExt == "apk" || lowerExt == "bat" || lowerExt == "sh":
		return "⚙️", "Installer", ext
	case lowerExt == "ttf" || lowerExt == "otf" || lowerExt == "woff" || lowerExt == "woff2":
		return "🔤", "Font", ext
	case lowerExt == "csv" || lowerExt == "json" || lowerExt == "xml" || lowerExt == "db" || lowerExt == "sql":
		return "🗃️", "Data", ext
	default:
		return "🧩", "Other", ext
	}
}

func sendLink(ctx *ext.Context, u *ext.Update) error {
	chatId := u.EffectiveChat().GetID()
	peerChatId := ctx.PeerStorage.GetPeerById(chatId)
	if peerChatId.Type != int(storage.TypeUser) {
		return dispatcher.EndGroups
	}

	// Welcome message for /start command
	if u.EffectiveMessage.Text != "" && strings.HasPrefix(u.EffectiveMessage.Text, "/start") {
		welcome := `Hey there! 👋 I’m your personal file streaming assistant.
Send me any file yes, any format 📂 and I’ll turn it into a direct download link or streaming link instantly! ⚡
What you can do:
✅ Upload files of any type
✅ Get a direct download link instantly
✅ Stream your media without hassle
✅ Share links with friends easily
How to start:
1️⃣ Send me a file
2️⃣ Wait a few seconds ⏱️
3️⃣ Receive your download & streaming link 🚀
Need help? Contact us at @yoelbots anytime!
💡 To see the bot statistics, just type /stats 📊`
		ctx.Reply(u, welcome, nil)
		return dispatcher.EndGroups
	}

	// Check for allowed users
	if len(config.ValueOf.AllowedUsers) != 0 && !utils.Contains(config.ValueOf.AllowedUsers, chatId) {
		ctx.Reply(u, "You are not allowed to use this bot.", nil)
		return dispatcher.EndGroups
	}

	// Check for forced channel subscription
	if config.ValueOf.ForceSubChannel != "" {
		isSubscribed, err := utils.IsUserSubscribed(ctx, ctx.Raw, ctx.PeerStorage, chatId)
		if err != nil {
			utils.Logger.Error("Error checking subscription status",
				zap.Error(err),
				zap.Int64("userID", chatId),
				zap.String("channel", config.ValueOf.ForceSubChannel))
			row := tg.KeyboardButtonRow{
				Buttons: []tg.KeyboardButtonClass{
					&tg.KeyboardButtonURL{
						Text: "Join Channel",
						URL:  fmt.Sprintf("https://t.me/%s", config.ValueOf.ForceSubChannel),
					},
				},
			}
			markup := &tg.ReplyInlineMarkup{
				Rows: []tg.KeyboardButtonRow{row},
			}
			ctx.Reply(u, "Please join our channel to get stream links.", &ext.ReplyOpts{
				Markup: markup,
			})
			return dispatcher.EndGroups
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
			markup := &tg.ReplyInlineMarkup{
				Rows: []tg.KeyboardButtonRow{row},
			}
			ctx.Reply(u, "Please join our channel to get stream links.", &ext.ReplyOpts{
				Markup: markup,
			})
			return dispatcher.EndGroups
		}
	}

	// Check for supported media
	supported, err := supportedMediaFilter(u.EffectiveMessage)
	if err != nil {
		return err
	}
	if !supported {
		ctx.Reply(u, "⚠️ Sorry, this message type is unsupported.", nil)
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

	// Generate hash
	fullHash := utils.PackFile(file.FileName, file.FileSize, file.MimeType, file.ID)
	hash := utils.GetShortHash(fullHash)

	// Record file statistics
	statsCache := cache.GetStatsCache()
	if statsCache != nil {
		err := statsCache.RecordFileProcessed(file.FileSize)
		if err != nil {
			utils.Logger.Error("Failed to record file statistics", zap.Error(err))
		}
	}

	// Get file type information
	icon, typeName, ext := getUniversalFileTypeInfo(file.FileName, file.MimeType)

	// Create response message with file details
	message := fmt.Sprintf("%s %s • %s • %.2f MB\n\n⏳ Link validity is 24 hours", icon, typeName, ext, float64(file.FileSize)/(1024*1024))

	// Create Stream/Download button for videos or binary files
	row := tg.KeyboardButtonRow{}
	if strings.Contains(file.MimeType, "video") || strings.Contains(file.MimeType, "application/octet-stream") {
		videoParam := fmt.Sprintf("%d?hash=%s", messageID, hash)
		streamURL := fmt.Sprintf("https://file.streamgramm.workers.dev/?video=%s", videoParam)
		row.Buttons = append(row.Buttons, &tg.KeyboardButtonURL{
			Text: "Stream / Download",
			URL:  streamURL,
		})
	}

	markup := &tg.ReplyInlineMarkup{
		Rows: []tg.KeyboardButtonRow{row},
	}

	// Send reply
	if strings.Contains(config.ValueOf.Host, "http://localhost") {
		_, err = ctx.Reply(u, message, &ext.ReplyOpts{
			NoWebpage:        false,
			ReplyToMessageId: u.EffectiveMessage.ID,
		})
	} else {
		_, err = ctx.Reply(u, message, &ext.ReplyOpts{
			Markup:           markup,
			NoWebpage:        false,
			ReplyToMessageId: u.EffectiveMessage.ID,
		})
	}

	if err != nil {
		utils.Logger.Sugar().Error(err)
		ctx.Reply(u, fmt.Sprintf("Error - %s", err.Error()), nil)
	}

	return dispatcher.EndGroups
}
