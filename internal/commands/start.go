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

	// Mensaje principal en inglés (solo texto, negrita y cursiva usando MarkdownV2)
	welcomeMessage := `*📤 WELCOME TO FS-BOT*
_Send or forward any file and I will generate a link for:_
- Direct Download
- Streaming (if multimedia)

*Supported files:*
🎬 Videos
🖼️ Images
📄 Documents
🗜️ RAR/ZIP & other formats

⚠️ *Note:*
- Playback may fail on some files
- Recommended: open links in Chrome

_Channel updates: @yoelbotsx_`

	ctx.Reply(u, welcomeMessage, nil)

	return dispatcher.EndGroups
}
