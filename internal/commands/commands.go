package commands

import (
	"fmt"

	"EverythingSuckz/fsb/config"
	"EverythingSuckz/fsb/internal/utils"

	"github.com/celestix/gotgproto/dispatcher"
	"github.com/celestix/gotgproto/dispatcher/handlers"
	"github.com/celestix/gotgproto/ext"
	"github.com/celestix/gotgproto/storage"
)

// Bloqueo de usuarios en memoria
var blockedUsers = make(map[int64]struct{})

// Comprueba si un usuario puede usar el bot
func isUserAllowed(chatId int64) bool {
	if _, blocked := blockedUsers[chatId]; blocked {
		return false
	}
	if len(config.ValueOf.AllowedUsers) != 0 {
		return utils.Contains(config.ValueOf.AllowedUsers, chatId)
	}
	return true
}

// ---- LoadStart ----
func (m *command) LoadStart(dispatcher dispatcher.Dispatcher) {
	dispatcher.AddHandler(handlers.NewCommand("start", start))
	dispatcher.AddHandler(handlers.NewCommand("block", blockCommand))
	dispatcher.AddHandler(handlers.NewCommand("unblock", unblockCommand))
}

// ---- /start ----
func start(ctx *ext.Context, u *ext.Update) error {
	chatId := u.EffectiveChat().GetID()
	peer := ctx.PeerStorage.GetPeerById(chatId)
	if peer.Type != int(storage.TypeUser) {
		return dispatcher.EndGroups
	}

	if !isUserAllowed(chatId) {
		ctx.Reply(u, "You are not allowed to use this bot.", nil)
		return dispatcher.EndGroups
	}

	msg := "Envía o reenvía cualquier tipo de archivo y recibirás un enlace de streaming y descarga.\n\n"
	msg += "Canal oficial: @yoelbotsx\n"
	msg += "Link válido por 24 horas ⏳\n"
	msg += "📊 Usa /stats para ver estadísticas del bot"

	ctx.Reply(u, msg, nil)
	return dispatcher.EndGroups
}

// ---- /block ----
func blockCommand(ctx *ext.Context, u *ext.Update) error {
	chatId := u.EffectiveChat().GetID()

	// El bot pedirá que se reenvíe un mensaje del usuario a bloquear
	ctx.Reply(u, "Por favor, reenvía un mensaje del usuario que quieres bloquear.", nil)

	// Guarda temporalmente que el siguiente mensaje reenviado será procesado
	ctx.Session().Set("next_block", true)
	ctx.Session().Set("requester", chatId)
	return dispatcher.EndGroups
}

// ---- /unblock ----
func unblockCommand(ctx *ext.Context, u *ext.Update) error {
	chatId := u.EffectiveChat().GetID()

	ctx.Reply(u, "Por favor, reenvía un mensaje del usuario que quieres desbloquear.", nil)
	ctx.Session().Set("next_unblock", true)
	ctx.Session().Set("requester", chatId)
	return dispatcher.EndGroups
}

// ---- Handler global para mensajes reenviados ----
func (m *command) HandleForwarded(ctx *ext.Context, u *ext.Update) error {
	chatId := u.EffectiveChat().GetID()
	s := ctx.Session()

	// Bloquear usuario
	if nextBlock, _ := s.Get("next_block").(bool); nextBlock {
		if u.EffectiveMessage.ForwardFrom != nil {
			target := u.EffectiveMessage.ForwardFrom.GetID()
			blockedUsers[target] = struct{}{}
			ctx.Reply(u, fmt.Sprintf("Usuario %d bloqueado ✅", target), nil)
			s.Delete("next_block")
			s.Delete("requester")
			return dispatcher.EndGroups
		}
		ctx.Reply(u, "Debes reenviar un mensaje válido del usuario a bloquear.", nil)
		return dispatcher.EndGroups
	}

	// Desbloquear usuario
	if nextUnblock, _ := s.Get("next_unblock").(bool); nextUnblock {
		if u.EffectiveMessage.ForwardFrom != nil {
			target := u.EffectiveMessage.ForwardFrom.GetID()
			delete(blockedUsers, target)
			ctx.Reply(u, fmt.Sprintf("Usuario %d desbloqueado ✅", target), nil)
			s.Delete("next_unblock")
			s.Delete("requester")
			return dispatcher.EndGroups
		}
		ctx.Reply(u, "Debes reenviar un mensaje válido del usuario a desbloquear.", nil)
		return dispatcher.EndGroups
	}

	return dispatcher.ContinueGroups
}
