package commands

import (
	"EverythingSuckz/fsb/config"
	"EverythingSuckz/fsb/internal/utils"

	"github.com/celestix/gotgproto/dispatcher"
	"github.com/celestix/gotgproto/dispatcher/handlers"
	"github.com/celestix/gotgproto/ext"
	"github.com/celestix/gotgproto/storage"
)

func (m *command) LoadStart(dispatcher dispatcher.Dispatcher) {
	log := m.log.Named("start")
	defer log.Sugar().Info("Loaded")
	dispatcher.AddHandler(handlers.NewCommand("start", start))
}

func start(ctx *ext.Context, u *ext.Update) error {
	chatID := u.EffectiveChat().GetID()
	peer := ctx.PeerStorage.GetPeerById(chatID)

	// Solo permitir usuarios tipo "user"
	if peer.Type != int(storage.TypeUser) {
		return dispatcher.EndGroups
	}

	// Filtrado de usuarios permitidos
	if len(config.ValueOf.AllowedUsers) != 0 && !utils.Contains(config.ValueOf.AllowedUsers, chatID) {
		ctx.Reply(u, "You are not allowed to use this bot.", nil)
		return dispatcher.EndGroups
	}

	// Mensaje de bienvenida en inglés, con diseño tipo bloque y emojis
	welcomeMessage := `
┌─────────────────────────────┐
│ 📤 SEND OR FORWARD ANY FILE │
└─────────────────────────────┘

➡️ I WILL GENERATE A LINK FOR:
   • DIRECT DOWNLOAD
   • STREAMING (if multimedia)

┌─────────── SUPPORTED FILES ──────────┐
│ 🎬 Videos                           │
│ 🖼️ Images                           │
│ 📄 Documents                        │
│ 🗜️ RAR/ZIP & other uncommon formats │
└──────────────────────────────────────┘

⚠️ NOTE:
• Playback may fail on some formats
• Recommended: open links in Chrome

🔔 OFFICIAL UPDATE CHANNEL: @yoelbotsx

📊 Use /stats to view bot statistics
`

	ctx.Reply(u, welcomeMessage, nil)
	return dispatcher.EndGroups
}
