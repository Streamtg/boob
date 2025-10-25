package main

import (
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/celestix/gotgproto/dispatcher"
	"github.com/celestix/gotgproto/dispatcher/handlers"
	"github.com/celestix/gotgproto/ext"
	"github.com/celestix/gotgproto/types"
	"github.com/gotd/td/tg"
)

type command struct {
	log ext.Logger
}

// LoadStream registers the handler for incoming messages
func (m *command) LoadStream(dispatcher dispatcher.Dispatcher) {
	defer m.log.Sugar().Info("Loaded Stream handler")
	dispatcher.AddHandler(handlers.NewMessage(nil, m.sendLink))
}

// simple File struct interno
type File struct {
	ID       int64
	FileName string
	FileSize int64
	MimeType string
}

// Check supported media
func supportedMediaFilter(m *types.Message) bool {
	if m.Media == nil {
		return false
	}
	switch media := m.Media.(type) {
	case *tg.MessageMediaDocument:
		doc := media.Document.(*tg.Document)
		if strings.HasPrefix(doc.MimeType, "video/") ||
			strings.HasPrefix(doc.MimeType, "audio/") ||
			strings.Contains(doc.MimeType, "pdf") ||
			strings.Contains(doc.MimeType, "zip") ||
			strings.Contains(doc.MimeType, "rar") ||
			strings.Contains(doc.MimeType, "apk") {
			return true
		}
	case *tg.MessageMediaPhoto:
		return true
	}
	return false
}

// Convierte media a File
func fileFromMedia(media tg.MessageMediaClass) (*File, error) {
	switch m := media.(type) {
	case *tg.MessageMediaDocument:
		doc := m.Document.(*tg.Document)
		name := doc.FileName
		if name == "" {
			name = fmt.Sprintf("%d.bin", time.Now().UnixNano())
		}
		return &File{
			ID:       doc.ID,
			FileName: name,
			FileSize: doc.Size,
			MimeType: doc.MimeType,
		}, nil
	case *tg.MessageMediaPhoto:
		photo := m.Photo.(*tg.Photo)
		size := int64(0)
		if len(photo.Sizes) > 0 {
			size = photo.Sizes[len(photo.Sizes)-1].Size
		}
		return &File{
			ID:       photo.ID,
			FileName: fmt.Sprintf("%d.jpg", time.Now().UnixNano()),
			FileSize: size,
			MimeType: "image/jpeg",
		}, nil
	default:
		return nil, fmt.Errorf("unsupported media")
	}
}

// Build file hash placeholder
func packFile(name string, size int64, mime string, id int64) string {
	return fmt.Sprintf("%s-%d-%s-%d", name, size, mime, id)
}

// Short hash placeholder
func getShortHash(full string) string {
	if len(full) < 8 {
		return full
	}
	return full[:8]
}

// Emoji según tipo de archivo
func getFileEmoji(mime string) string {
	lower := strings.ToLower(mime)
	switch {
	case strings.Contains(lower, "video"):
		return "🎬"
	case strings.Contains(lower, "image"):
		return "🖼️"
	case strings.Contains(lower, "audio"):
		return "🎵"
	case strings.Contains(lower, "pdf"):
		return "📄"
	case strings.Contains(lower, "zip"), strings.Contains(lower, "rar"):
		return "🗜️"
	case strings.Contains(lower, "apk"), strings.Contains(lower, "exe"):
		return "📦"
	default:
		return "📁"
	}
}

// Formato de tamaño
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

// sendLink
func (m *command) sendLink(ctx *ext.Context, u *ext.Update) error {
	chatId := u.EffectiveChat().GetID()
	msg := u.EffectiveMessage

	if !supportedMediaFilter(msg) {
		ctx.Reply(u, "Unsupported message type", nil)
		return dispatcher.EndGroups
	}

	file, err := fileFromMedia(msg.Media)
	if err != nil {
		ctx.Reply(u, fmt.Sprintf("Error extracting file: %v", err), nil)
		return dispatcher.EndGroups
	}

	fullHash := packFile(file.FileName, file.FileSize, file.MimeType, file.ID)
	hash := getShortHash(fullHash)

	streamURL := fmt.Sprintf("https://host.streamgramm.workers.dev/?video=%d&hash=%s&filename=%s",
		file.ID,
		url.QueryEscape(hash),
		url.QueryEscape(file.FileName),
	)

	// Construir mensaje
	message := fmt.Sprintf("%s File: %s\n📂 Type: %s\n💽 Size: %s",
		getFileEmoji(file.MimeType),
		file.FileName,
		file.MimeType,
		formatFileSize(file.FileSize),
	)

	row := tg.KeyboardButtonRow{
		Buttons: []tg.KeyboardButtonClass{
			&tg.KeyboardButtonURL{Text: "▶️ Watch / Download", URL: streamURL},
		},
	}
	markup := &tg.ReplyInlineMarkup{Rows: []tg.KeyboardButtonRow{row}}

	_, err = ctx.Reply(u, message, &ext.ReplyOpts{
		Markup:           markup,
		ReplyToMessageId: msg.ID,
	})
	if err != nil {
		m.log.Sugar().Errorf("Failed to send reply: %v", err)
		ctx.Reply(u, fmt.Sprintf("Error sending reply: %s", err.Error()), nil)
	}

	return dispatcher.EndGroups
}
