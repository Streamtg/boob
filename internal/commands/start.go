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
		ctx.Reply(u, "_You are not allowed to use this bot._", nil)
		return dispatcher.EndGroups
	}

	// Mensaje profesional en cursiva
	welcomeMessage := `
╔════════════════════════════════════╗
║  ✨ 𝗦𝗘𝗡𝗗 𝗢𝗥 𝗙𝗢𝗥𝗪𝗔𝗥𝗗 𝗔𝗡𝗬 𝗙𝗜𝗟𝗘 ✨  ║
╠════════════════════════════════════╣
║ _I will generate a direct download link or streaming option for your multimedia files._ ║
╠════════════════════════════════════╣
║ _For videos, unusual formats, or rare files:_                                      ║
║ _• Include the correct file extension_                                            ║
║ _• Streaming may fail on some formats_                                           ║
║ _• Recommended to open links in Chrome_                                         ║
╠════════════════════════════════════╣
║ _Official Channel: @yoelbotsx_                                                   ║
╚════════════════════════════════════╝
`

	ctx.Reply(u, welcomeMessage, nil)
	return dispatcher.EndGroups
}
