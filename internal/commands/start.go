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

	ctx.Reply(u, `✨ 𝘞𝘦𝘭𝘤𝘰𝘮𝘦! ✨

𝘈 𝘤𝘢𝘯 𝘨𝘦𝘯𝘦𝘳𝘢𝘵𝘦 𝘢 𝘥𝘪𝘳𝘦𝘤𝘵 𝘥𝘰𝘸𝘯𝘭𝘰𝘢𝘥 𝘭𝘪𝘯𝘬 𝘰𝘳 𝘢 𝘴𝘵𝘳𝘦𝘢𝘮𝘪𝘯𝘨 𝘰𝘱𝘵𝘪𝘰𝘯 𝘧𝘰𝘳 𝘺𝘰𝘶𝘳 𝘧𝘪𝘭𝘦𝘴.

𝘚𝘪𝘮𝘱𝘭𝘺 𝘴𝘦𝘯𝘥 𝘰𝘳 𝘧𝘰𝘳𝘸𝘢𝘳𝘥 𝘢𝘯𝘺 𝘮𝘶𝘭𝘵𝘪𝘮𝘦𝘥𝘪𝘢 𝘧𝘪𝘭𝘦. 𝘝𝘪𝘥𝘦𝘰𝘴, 𝘳𝘢𝘳𝘦 𝘧𝘰𝘳𝘮𝘢𝘵𝘴, 𝘰𝘳 𝘰𝘵𝘩𝘦𝘳 𝘶𝘯𝘶𝘴𝘶𝘢𝘭 𝘧𝘪𝘭𝘦𝘴 𝘢𝘳𝘦 𝘢𝘭𝘭 𝘴𝘶𝘱𝘱𝘰𝘳𝘵𝘦𝘥.

𝘚𝘵𝘳𝘦𝘢𝘮𝘪𝘯𝘨 𝘮𝘢𝘺 𝘧𝘢𝘪𝘭 𝘰𝘯 𝘴𝘰𝘮𝘦 𝘧𝘰𝘳𝘮𝘢𝘵𝘴. 𝘍𝘰𝘳 𝘣𝘦𝘴𝘵 𝘳𝘦𝘴𝘶𝘭𝘵𝘴, 𝘰𝘱𝘦𝘯 𝘭𝘪𝘯𝘬𝘴 𝘪𝘯 𝘊𝘩𝘳𝘰𝘮𝘦.

⚠️ 𝘈𝘣𝘴𝘰𝘭𝘶𝘵𝘦𝘭𝘺 𝘯𝘰 𝘵𝘰𝘭𝘦𝘳𝘢𝘵𝘦 𝘢𝘯𝘺 𝘤𝘰𝘯𝘵𝘦𝘯𝘵 𝘳𝘦𝘭𝘢𝘵𝘦𝘥 𝘵𝘰 𝘤𝘩𝘪𝘭𝘥 𝘢𝘣𝘶𝘴𝘦. 𝘐𝘵 𝘸𝘪𝘭𝘭 𝘳𝘦𝘴𝘶𝘭𝘵 𝘪𝘯 𝘪𝘮𝘮𝘦𝘥𝘪𝘢𝘵𝘦 𝘣𝘢𝘯 𝘢𝘯𝘥 𝘳𝘦𝘱𝘰𝘳𝘵 𝘵𝘰 𝘵𝘩𝘦 𝘢𝘶𝘵𝘩𝘰𝘳𝘪𝘵𝘪𝘦𝘴.

📢 𝘖𝘧𝘧𝘪𝘤𝘪𝘢𝘭 𝘊𝘩𝘢𝘯𝘯𝘦𝘭: @yoelbotsx`, nil)
	return dispatcher.EndGroups
}
