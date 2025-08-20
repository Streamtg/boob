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

func (m *command) LoadStream(dispatcher dispatcher.Dispatcher) {
	log := m.log.Named("start")
	defer log.Sugar().Info("Loaded")
	dispatcher.AddHandler(
		handlers.NewMessage(nil, sendLink),
	)
}

func supportedMediaFilter(m *types.Message) (bool, error) {
	if not := m.Media == nil; not {
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

// Función auxiliar para convertir el tamaño del archivo a un formato legible
func formatFileSize(bytes int64) string {
	const (
		KB = 1024
		MB = KB * 1024
		GB = MB * 1024
		TB = GB * 1024
	)
	switch {
	case bytes >= TB:
		return fmt.Sprintf("%.2f TB", float64(bytes)/float64(TB))
	case bytes >= GB:
		return fmt.Sprintf("%.2f GB", float64(bytes)/float64(GB))
	case bytes >= MB:
		return fmt.Sprintf("%.2f MB", float64(bytes)/float64(MB))
	case bytes >= KB:
		return fmt.Sprintf("%.2f KB", float64(bytes)/float64(KB))
	default:
		return fmt.Sprintf("%d bytes", bytes)
	}
}

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

	supported, err := supportedMediaFilter(u.EffectiveMessage)
	if err != nil {
		return err
	}
	if !supported {
		ctx.Reply(u, "Sorry, this message type is unsupported.", nil)
		return dispatcher.EndGroups
	}

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

	fullHash := utils.PackFile(
		file.FileName,
		file.FileSize,
		file.MimeType,
		file.ID,
	)
	hash := utils.GetShortHash(fullHash)

	// Obtener la extensión del archivo desde el nombre o el MIME type
	extension := ""
	if lastDotIndex := strings.LastIndex(file.FileName, "."); lastDotIndex != -1 && lastDotIndex < len(file.FileName)-1 {
		extension = file.FileName[lastDotIndex+1:]
	} else if file.MimeType != "" {
		// Si no hay extensión en el nombre, intentar inferirla del MIME type
		switch file.MimeType {
		case "video/mp4":
			extension = "mp4"
		case "video/x-matroska":
			extension = "mkv"
		case "image/jpeg":
			extension = "jpg"
		case "image/png":
			extension = "png"
		case "application/pdf":
			extension = "pdf"
		default:
			extension = "unknown"
		}
	} else {
		extension = "unknown"
	}

	// Formatear el tamaño del archivo
	fileSize := formatFileSize(file.FileSize)

	// Mensaje con nombre, tamaño y tipo de archivo
	message := fmt.Sprintf(
		"📄 File Name: %s\n📏 File Size: %s\n📋 File Type: %s",
		file.FileName,
		fileSize,
		extension,
	)

	row := tg.KeyboardButtonRow{}

	// Botón Stream/Download apunta al Worker, para videos y archivos binarios
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

	statsCache := cache.GetStatsCache()
	if statsCache != nil {
		err := statsCache.RecordFileProcessed(file.FileSize)
		if err != nil {
			utils.Logger.Error("Failed to record file statistics", zap.Error(err))
		}
	}

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
