package commands

import (
	"fmt"
	"net/url"
	"strings"
	"time"

	"EverythingSuckz/fsb/config"
	"EverythingSuckz/fsb/internal/cache"
	"EverythingSuckz/fsb/internal/database"
	"EverythingSuckz/fsb/internal/types"
	"EverythingSuckz/fsb/internal/utils"

	"github.com/celestix/gotgproto/dispatcher"
	"github.com/celestix/gotgproto/dispatcher/handlers"
	"github.com/celestix/gotgproto/dispatcher/handlers/filters"
	"github.com/celestix/gotgproto/ext"
	tgtypes "github.com/celestix/gotgproto/types"
	"github.com/gotd/td/tg"
	"crypto/sha256"
	"encoding/hex"
)

// Filtro personalizado para ignorar comandos
func nonCommandFilter(m *tgtypes.Message) bool {
	if m.Text == "" {
		return true
	}
	return !strings.HasPrefix(m.Text, "/")
}

func (m *command) LoadStream(dispatcher dispatcher.Dispatcher) {
	defer m.log.Sugar().Info("Loaded Stream handler")
	// Usar filtro personalizado para ignorar comandos
	dispatcher.AddHandler(
		handlers.NewMessage(filters.MessageFilter(nonCommandFilter), m.sendLink),
	)
	dispatcher.AddHandler(
		handlers.NewCommand("broadcast", m.broadcastMessage),
	)
	// Handler para /series
	dispatcher.AddHandler(
		handlers.NewCommand("series", m.handleSeries),
	)
	// Inicializar mapas si no existen
	m.mutex.Lock()
	if m.seriesModes == nil {
		m.seriesModes = make(map[int64]bool)
	}
	if m.seriesURLs == nil {
		m.seriesURLs = make(map[int64][]string)
	}
	m.mutex.Unlock()
}

func (m *command) broadcastMessage(ctx *ext.Context, u *ext.Update) error {
	userId := u.EffectiveUser().ID

	// Verifica si el usuario es admin
	isAdmin := len(config.ValueOf.AdminIDs) == 0 || utils.Contains(config.ValueOf.AdminIDs, userId)
	if !isAdmin {
		ctx.Reply(u, "You are not authorized to use /broadcast.", nil)
		return dispatcher.EndGroups
	}

	// Extrae el mensaje después de /broadcast
	messageText := strings.TrimSpace(strings.TrimPrefix(u.EffectiveMessage.Text, "/broadcast"))
	if messageText == "" || messageText == "/broadcast" {
		ctx.Reply(u, "Please provide a message to broadcast. Usage: /broadcast <message>", nil)
		return dispatcher.EndGroups
	}

	// Obtener todos los usuarios de la tabla User
	var users []types.User
	if err := database.GetDB().Find(&users).Error; err != nil {
		m.log.Sugar().Errorf("Failed to fetch users from database: %v", err)
		ctx.Reply(u, "Error fetching users from database.", nil)
		return dispatcher.EndGroups
	}
	if len(users) == 0 {
		ctx.Reply(u, "No users found in the database for broadcast.", nil)
		return dispatcher.EndGroups
	}

	// Enviar mensaje a cada usuario
	successCount := 0
	failureCount := 0
	for _, user := range users {
		_, err := ctx.Raw.MessagesSendMessage(ctx, &tg.MessagesSendMessageRequest{
			Peer:     &tg.InputPeerUser{UserID: user.UserID},
			Message:  messageText,
			RandomID: time.Now().UnixNano(),
		})
		if err != nil {
			failureCount++
			m.log.Sugar().Errorf("Failed to send broadcast to user %d: %v", user.UserID, err)
			continue
		}
		successCount++
		// Solo aplicar delay para no admins
		if !isAdmin {
			time.Sleep(2 * time.Second) // Anti-flood para no admins
		}
	}

	response := fmt.Sprintf("Broadcast completed:\n- Sent to %d users\n- Failed for %d users", successCount, failureCount)
	ctx.Reply(u, response, nil)
	return dispatcher.EndGroups
}

func (m *command) sendLink(ctx *ext.Context, u *ext.Update) error {
	chatId := u.EffectiveChat().GetID()
	peerChat := ctx.PeerStorage.GetPeerById(chatId)
	if peerChat == nil {
		m.log.Sugar().Errorf("Failed to get peer for chat %d", chatId)
		return dispatcher.EndGroups
	}

	// Registro de usuarios en la base de datos
	userId := u.EffectiveUser().ID
	user := types.User{UserID: userId}
	if err := database.GetDB().FirstOrCreate(&user, types.User{UserID: userId}).Error; err != nil {
		m.log.Sugar().Errorf("Failed to save user %d to database: %v", userId, err)
	}

	if len(config.ValueOf.AllowedUsers) != 0 && !utils.Contains(config.ValueOf.AllowedUsers, userId) {
		ctx.Reply(u, "You are not allowed to use this bot.", nil)
		return dispatcher.EndGroups
	}

	if config.ValueOf.ForceSubChannel != "" {
		isSubscribed, err := utils.IsUserSubscribed(ctx, ctx.Raw, ctx.PeerStorage, userId)
		if err != nil {
			m.log.Sugar().Errorf("Failed to check subscription for user %d: %v", userId, err)
			ctx.Reply(u, "Error checking subscription status.", nil)
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

	update, err := utils.ForwardMessages(ctx, chatId, config.ValueOf.LogChannelID, u.EffectiveMessage.ID)
	if err != nil {
		m.log.Sugar().Errorf("Failed to forward message from chat %d: %v", chatId, err)
		ctx.Reply(u, fmt.Sprintf("Error forwarding message: %s", err.Error()), nil)
		return dispatcher.EndGroups
	}

	messageID := update.Updates[0].(*tg.UpdateMessageID).ID
	doc := update.Updates[1].(*tg.UpdateNewChannelMessage).Message.(*tg.Message).Media
	file, err := utils.FileFromMedia(doc)
	if err != nil {
		m.log.Sugar().Errorf("Failed to extract file from media: %v", err)
		ctx.Reply(u, fmt.Sprintf("Error extracting file: %s", err.Error()), nil)
		return dispatcher.EndGroups
	}
	if file.FileName == "" {
		hash := sha256.Sum256([]byte(fmt.Sprintf("%d", file.ID)))
		file.FileName = hex.EncodeToString(hash[:])[:12] + "_file"
	}

	message := fmt.Sprintf(
		"%s 𝑁𝑎𝑚𝑒: %s\n%s 𝑇𝑦𝑝𝑒: %s\n%s 𝑆𝑖𝑧𝑒: %s\n\n⚠️ 𝑆𝑒𝑛𝑑𝑖𝑛𝑔 𝑜𝑟 𝑓𝑜𝑟𝑤𝑎𝑟𝑑𝑖𝑛𝑔 𝑐ℎ𝑖𝑙𝑑 𝑎𝑏𝑢𝑠𝑒 𝑐𝑜𝑛𝑡𝑒𝑛𝑡 𝑤𝑖𝑙𝑙 𝑟𝑒𝑠𝑢𝑙𝑡 𝑖𝑛 𝑏𝑎𝑛 𝑎𝑛𝑑 𝑟𝑒𝑝𝑜𝑟𝑡\n\n⏳ @yoelbotsx",
		fileTypeEmoji(file.MimeType), toItalicUnicode(file.FileName),
		fileTypeEmoji(file.MimeType), toItalicUnicode(file.MimeType),
		fileTypeEmoji(file.MimeType), toItalicUnicode(formatFileSize(file.FileSize)),
	)

	fullHash := utils.PackFile(file.FileName, file.FileSize, file.MimeType, file.ID)
	hash := utils.GetShortHash(fullHash)

	statsCache := cache.GetStatsCache()
	if statsCache != nil {
		_ = statsCache.RecordFileProcessed(file.FileSize)
	}

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
		m.log.Sugar().Errorf("Failed to send reply: %v", err)
		ctx.Reply(u, fmt.Sprintf("Error sending reply: %s", err.Error()), nil)
	}

	// Si el modo series está activo, agregar el URL a la lista
	m.mutex.Lock()
	if m.seriesModes[userId] {
		m.seriesURLs[userId] = append(m.seriesURLs[userId], streamURL)
	}
	m.mutex.Unlock()

	return dispatcher.EndGroups
}

func (m *command) handleSeries(ctx *ext.Context, u *ext.Update) error {
	userId := u.EffectiveUser().ID

	// Verifica si el usuario es admin
	if len(config.ValueOf.AdminIDs) != 0 && !utils.Contains(config.ValueOf.AdminIDs, userId) {
		ctx.Reply(u, "You are not authorized to use /series.", nil)
		return dispatcher.EndGroups
	}

	// Togglea el modo
	m.mutex.Lock()
	if m.seriesModes[userId] {
		// Modo activo: Desactivar y enviar la lista
		urls := m.seriesURLs[userId]
		if len(urls) == 0 {
			ctx.Reply(u, "No files were processed during this series mode.", nil)
		} else {
			const maxMessageLength = 4000
			list := strings.Builder{}
			list.WriteString("Processed series URLs:\n")
			for i, url := range urls {
				line := fmt.Sprintf("%d. %s\n", i+1, url)
				if list.Len()+len(line) > maxMessageLength {
					_, err := ctx.Reply(u, list.String(), nil)
					if err != nil {
						m.log.Sugar().Errorf("Failed to send partial series URLs: %v", err)
						ctx.Reply(u, "Error sending series URLs.", nil)
						m.mutex.Unlock()
						return dispatcher.EndGroups
					}
					list.Reset()
					list.WriteString("Processed series URLs (continued):\n")
				}
				list.WriteString(line)
			}
			if list.Len() > len("Processed series URLs (continued):\n") {
				_, err := ctx.Reply(u, list.String(), nil)
				if err != nil {
					m.log.Sugar().Errorf("Failed to send final series URLs: %v", err)
					ctx.Reply(u, "Error sending series URLs.", nil)
					m.mutex.Unlock()
					return dispatcher.EndGroups
				}
			}
		}
		// Limpiar la lista y desactivar modo
		m.seriesURLs[userId] = nil
		m.seriesModes[userId] = false
	} else {
		// Modo inactivo: Activar
		m.seriesModes[userId] = true
		m.seriesURLs[userId] = []string{}
		ctx.Reply(u, "Series mode activated. Send files to process. Use /series again to get the list and deactivate.", nil)
	}
	m.mutex.Unlock()

	return dispatcher.EndGroups
}

func supportedMediaFilter(m *tgtypes.Message) (bool, error) {
	if m.Media == nil {
		return false, dispatcher.EndGroups
	}
	switch media := m.Media.(type) {
	case *tg.MessageMediaDocument:
		doc := media.Document.(*tg.Document)
		// Acepta videos (MIME "video/*") y documentos (RAR, EXE, APK son "application/*")
		if strings.HasPrefix(doc.MimeType, "video/") || strings.HasPrefix(doc.MimeType, "application/") {
			return true, nil
		}
		return false, dispatcher.EndGroups
	case *tg.MessageMediaPhoto:
		return true, nil // Acepta fotos
	default:
		return false, dispatcher.EndGroups // Excluye stickers y otros
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
	case strings.Contains(lowerMime, "application/x-msdos-program"), strings.Contains(lowerMime, "application/octet-stream"):
		return "💻" // Para EXE/APK
	default:
		return "📄"
	}
}

func toItalicUnicode(text string) string {
	var result strings.Builder
	for _, r := range text {
		switch {
		case r >= 'A' && r <= 'Z':
			result.WriteRune(rune(0x1D434 + (r - 'A')))
		case r >= 'a' && r <= 'z':
			result.WriteRune(rune(0x1D44E + (r - 'a')))
		case r >= '0' && r <= '9':
			result.WriteRune(rune(0x1D7CE + (r - '0')))
		default:
			result.WriteRune(r)
		}
	}
	return result.String()
}
