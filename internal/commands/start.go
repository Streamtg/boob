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

	// Mensaje de bienvenida en inglés con MarkdownV2 (negrita y cursiva)
	welcomeMessage := "*📢 Send or Forward any file*\n\n" +
		"_I will generate a link for direct download or streaming if it's multimedia._\n\n" +
		"_Supports videos, documents, images, rar/zip files, and other uncommon formats._\n" +
		"_Playback may fail on some formats, so it is recommended to open links in Chrome._\n\n" +
		"*Official Update Channel:* @yoelbotsx\n\n" +
		"*Use /stats to view bot statistics.*"

	ctx.Reply(u, welcomeMessage, &ext.ReplyOpts{
		ParseMode: ext.ParseModeMarkdownV2,
	})

	return dispatcher.EndGroups
}
