package commands

import (
	"EverythingSuckz/fsb/config"
	"EverythingSuckz/fsb/internal/utils"

	"github.com/celestix/gotgproto/dispatcher"
	"github.com/celestix/gotgproto/dispatcher/handlers"
	"github.com/celestix/gotgproto/ext"
	"github.com/celestix/gotgproto/storage"
	"github.com/celestix/gotgproto/types"
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

	// Mensaje principal
	welcomeMessage := `
╔══════════════════════════════════╗
║          📤 WELCOME TO FS-BOT          ║
╠══════════════════════════════════╣
║ SEND OR FORWARD ANY FILE          ║
║ I WILL GENERATE A LINK FOR:      ║
║  • Direct Download               ║
║  • Streaming (if multimedia)     ║
╠════════════ SUPPORTED FILES ══════╣
║ 🎬 Videos                         ║
║ 🖼️ Images                         ║
║ 📄 Documents                      ║
║ 🗜️ RAR/ZIP & other formats        ║
╠══════════════════════════════════╣
║ ⚠️ NOTE:                            ║
║ • Playback may fail on some files ║
║ • Recommended: open links in Chrome ║
╚══════════════════════════════════╝
`

	// Botones inline
	inlineKeyboard := [][]types.KeyboardButtonClass{
		{
			types.KeyboardButton{
				Text: "📺 Channel Updates",
				URL:  "@yoelbotsx",
			},
			types.KeyboardButton{
				Text: "📊 Bot Stats",
				URL:  "https://t.me/yoelbotsx?start=stats",
			},
		},
		{
			types.KeyboardButton{
				Text: "💬 Support",
				URL:  "https://t.me/yoelbotsx",
			},
		},
	}

	ctx.Reply(u, welcomeMessage, &ext.ReplyOpts{
		InlineKeyboard: inlineKeyboard,
	})

	return dispatcher.EndGroups
}
