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
	"github.com/celestix/gotgproto/ext/handlers/filters"
)

// RegisterFileHandler sets the handler for processing documents (files)
func RegisterFileHandler(d *dispatcher.Dispatcher) {
	d.AddHandler(handlers.NewMessage(filters.Document, fileHandler))
}

// fileHandler processes any type of document sent to the bot
func fileHandler(c *ext.Context, u *ext.Update) error {
	doc := u.EffectiveMessage.Document

	// Extract file details
	fileName := doc.FileName
	fileSize := utils.FormatFileSize(doc.Size)
	mimeType := doc.MimeType
	if mimeType == "" {
		mimeType = "Unknown type"
	}

	// Save file in cache
	fileID := utils.GenerateRandomString(12)
	cache.Set(fileID, doc)

	// Build response message
	response := fmt.Sprintf(
		"📂 *File received successfully!*\n\n"+
			"**Name:** `%s`\n\n"+
			"**Size:** %s\n\n"+
			"**Type:** %s\n\n"+
			"🔗 Your direct link: %s/%s",
		fileName, fileSize, mimeType, config.AppUrl, fileID,
	)

	// Send the message to the user
	_, err := u.EffectiveMessage.ReplyText(response, &ext.SendOptions{
		ParseMode: "markdown",
	})
	return err
}
