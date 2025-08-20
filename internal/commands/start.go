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
	chatId := u.EffectiveChat().GetID()
	peerChatId := ctx.PeerStorage.GetPeerById(chatId)
	if peerChatId.Type != int(storage.TypeUser) {
		return dispatcher.EndGroups
	}
	if len(config.ValueOf.AllowedUsers) != 0 && !utils.Contains(config.ValueOf.AllowedUsers, chatId) {
		ctx.Reply(u, "You are not allowed to use this bot.", nil)
		return dispatcher.EndGroups
	}

	ctx.Reply(u, "Hey there! 👋 I’m your personal file streaming assistant.\n\nSend me any file yes, any format 📂 and I’ll turn it into a direct download link or streaming link instantly! ⚡\n\nWhat you can do:\n✅ Upload files of any type\n✅ Get a direct download link instantly\n✅ Stream your media without hassle\n✅ Share links with friends easily\n\nHow to start:\n1️⃣ Send me a file\n2️⃣ Wait a few seconds ⏱️\n3️⃣ Receive your download & streaming link 🚀\n\nNeed help? Contact us at @yoelbots anytime!\n\n💡 To see the bot statistics, just type /stats 📊", nil)
	return dispatcher.EndGroups
}
