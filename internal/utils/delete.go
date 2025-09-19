package utils

import (
	"fmt"

	"github.com/celestix/gotgproto/ext"
	"github.com/gotd/td/tg"
)

// DeleteMessage elimina un mensaje de un canal específico (e.g., original en LOG_CHANNEL)
func DeleteMessage(ctx *ext.Context, channelID int64, messageID int) error {
	_, err := ctx.Raw.ChannelsDeleteMessages(&tg.ChannelsDeleteMessagesRequest{
		Channel: &tg.InputChannel{ChannelID: channelID},
		ID:      []int{messageID},
	})
	if err != nil {
		return fmt.Errorf("error al eliminar mensaje %d del canal %d: %v", messageID, channelID, err)
	}
	return nil
}
