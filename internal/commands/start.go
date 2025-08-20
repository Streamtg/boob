package commands

import (
	"EverythingSuckz/fsb/config"
	"EverythingSuckz/fsb/internal/utils"

	"github.com/celestix/gotgproto/dispatcher"
	"github.com/celestix/gotgproto/dispatcher/handlers"
	"github.com/celestix/gotgproto/ext"
	"github.com/celestix/gotgproto/storage"
	"go.uber.org/zap"
)

func (m *command) LoadStart(dispatcher dispatcher.Dispatcher) {
	log := m.log.Named("start")
	defer log.Sugar().Info("Loaded")
	dispatcher.AddHandler(handlers.NewCommand("start", start))
}

func start(ctx *ext.Context, u *ext.Update) error {
	chatId := u.EffectiveChat().GetID()
	log := utils.Logger.Named("start").With(zap.Int64("chatID", chatId))
	peerChatId := ctx.PeerStorage.GetPeerById(chatId)
	if peerChatId.Type != int(storage.TypeUser) {
		log.Debug("Ignoring non-user chat")
		return dispatcher.EndGroups
	}
	if len(config.ValueOf.AllowedUsers) != 0 && !utils.Contains(config.ValueOf.AllowedUsers, chatId) {
		_, err := ctx.Reply(u, "You are not allowed to use this bot.", nil)
		if err != nil {
			log.Error("Failed to send not allowed message", zap.Error(err))
		}
		return dispatcher.EndGroups
	}

	welcome := `Hey there! 👋 I’m your personal file streaming assistant.
Send me any file yes, any format 📂 and I’ll turn it into a direct download link or streaming link instantly! ⚡
What you can do:
✅ Upload files of any type
✅ Get a direct download link instantly
✅ Stream your media without hassle
✅ Share links with friends easily
How to start:
1️⃣ Send me a file
2️⃣ Wait a few seconds ⏱️
3️⃣ Receive your download & streaming link 🚀
Need help? Contact us at @yoelbots anytime!
💡 To see the bot statistics, just type /stats 📊`
	_, err := ctx.Reply(u, welcome, &ext.ReplyOpts{
		ReplyToMessageID: u.EffectiveMessage.ID,
	})
	if err != nil {
		log.Error("Failed to send welcome message", zap.Error(err))
	}
	return dispatcher.EndGroups
}
