// ... existing code ...
import (
    "EverythingSuckz/fsb/config"
    "EverythingSuckz/fsb/internal/utils"
    "github.com/celestix/gotgproto/dispatcher"
    "github.com/celestix/gotgproto/dispatcher/handlers"
    "github.com/celestix/gotgproto/ext"
    "github.com/celestix/gotgproto/storage"
    "github.com/gotd/td/tg" // Import the Telegram package
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

    // Force subscription check
    channelUsername := "@haris_garage" // Replace with your channel's username
    member, err := ctx.Client.API().ChannelsGetParticipant(ctx.Context(), &tg.ChannelsGetParticipantRequest{
        Channel: &tg.InputChannel{
            ChannelID: 1882519219, // Replace with your channel ID
        },
        Participant: &tg.InputPeerUser{
            UserID: chatId,
        },
    })
    if err != nil || member == nil {
        ctx.Reply(u, "Please join our channel to use this bot: "+channelUsername, nil)
        return dispatcher.EndGroups
    }

    ctx.Reply(u, "Need a direct streamable link to a file? Send it my way! 🤓\n\nJoin my Update Channel @haris_garage 🗿 for more updates.\n\nLink validity: 24 hours ⏳\n\nPro Tip: Use 1DM Browser for lightning-fast downloads! 🔥", nil)
    return dispatcher.EndGroups
}
// ... existing code ...
