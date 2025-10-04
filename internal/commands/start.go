package commands

import (
	"fmt"
	"strings"
	"sync"

	"EverythingSuckz/fsb/config"
	"EverythingSuckz/fsb/internal/utils"

	"github.com/celestix/gotgproto/dispatcher"
	"github.com/celestix/gotgproto/dispatcher/handlers"
	"github.com/celestix/gotgproto/ext"
	"github.com/celestix/gotgproto/storage"
)

type command struct {
	log ext.Logger
}

// ---- Blocklist en memoria ----
var blockedUsers = struct {
	sync.RWMutex
	m map[int64]struct{}
}{m: make(map[int64]struct{})}

// Comprueba si un usuario puede usar el bot
func isUserAllowed(chatId int64) bool {
	blockedUsers.RLock()
	_, blocked := blockedUsers.m[chatId]
	blockedUsers.RUnlock()
	if blocked {
		return false
	}

	if len(config.ValueOf.AllowedUsers) != 0 {
		return utils.Contains(config.ValueOf.AllowedUsers, chatId)
	}

	return true
}

// Comprobación de admin para /block y /unblock
func adminAllowed(chatId int64) bool {
	if len(config.ValueOf.AllowedUsers) != 0 {
		return utils.Contains(config.ValueOf.AllowedUsers, chatId)
	}
	return true
}

// ---- LoadStart ----
func (m *command) LoadStart(dispatcher dispatcher.Dispatcher) {
	log := m.log.Named("start")
	defer log.Sugar().Info("Loaded")

	dispatcher.AddHandler(handlers.NewCommand("start", start))
	dispatcher.AddHandler(handlers.NewCommand("block", blockCommand))
	dispatcher.AddHandler(handlers.NewCommand("unblock", unblockCommand))
	dispatcher.AddHandler(handlers.NewCommand("blocked", blockedListCommand))
}

// ---- /start ----
func start(ctx *ext.Context, u *ext.Update) error {
	chatId := u.EffectiveChat().GetID()
	peerChatId := ctx.PeerStorage.GetPeerById(chatId)
	if peerChatId.Type != int(storage.TypeUser) {
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

// ---- Comandos de blocklist ----
func blockCommand(ctx *ext.Context, u *ext.Update) error {
	chatId := u.EffectiveChat().GetID()
	if !adminAllowed(chatId) {
		ctx.Reply(u, "No tienes permisos para usar /block.", nil)
		return dispatcher.EndGroups
	}

	args := strings.TrimSpace(u.ArgsStr())
	if args == "" {
		ctx.Reply(u, "Uso: /block <user_id>", nil)
		return dispatcher.EndGroups
	}

	var target int64
	_, err := fmt.Sscan(args, &target)
	if err != nil || target == 0 {
		ctx.Reply(u, "ID inválida. Uso: /block <user_id>", nil)
		return dispatcher.EndGroups
	}

	blockedUsers.Lock()
	blockedUsers.m[target] = struct{}{}
	blockedUsers.Unlock()

	ctx.Reply(u, fmt.Sprintf("Usuario %d bloqueado ✅", target), nil)
	return dispatcher.EndGroups
}

func unblockCommand(ctx *ext.Context, u *ext.Update) error {
	chatId := u.EffectiveChat().GetID()
	if !adminAllowed(chatId) {
		ctx.Reply(u, "No tienes permisos para usar /unblock.", nil)
		return dispatcher.EndGroups
	}

	args := strings.TrimSpace(u.ArgsStr())
	if args == "" {
		ctx.Reply(u, "Uso: /unblock <user_id>", nil)
		return dispatcher.EndGroups
	}

	var target int64
	_, err := fmt.Sscan(args, &target)
	if err != nil || target == 0 {
		ctx.Reply(u, "ID inválida. Uso: /unblock <user_id>", nil)
		return dispatcher.EndGroups
	}

	blockedUsers.Lock()
	delete(blockedUsers.m, target)
	blockedUsers.Unlock()

	ctx.Reply(u, fmt.Sprintf("Usuario %d desbloqueado ✅", target), nil)
	return dispatcher.EndGroups
}

func blockedListCommand(ctx *ext.Context, u *ext.Update) error {
	chatId := u.EffectiveChat().GetID()
	if !adminAllowed(chatId) {
		ctx.Reply(u, "No tienes permisos para usar /blocked.", nil)
		return dispatcher.EndGroups
	}

	blockedUsers.RLock()
	if len(blockedUsers.m) == 0 {
		blockedUsers.RUnlock()
		ctx.Reply(u, "No hay usuarios bloqueados.", nil)
		return dispatcher.EndGroups
	}

	var b strings.Builder
	b.WriteString("Usuarios bloqueados:\n")
	for id := range blockedUsers.m {
		fmt.Fprintf(&b, "- %d\n", id)
	}
	blockedUsers.RUnlock()

	ctx.Reply(u, b.String(), nil)
	return dispatcher.EndGroups
}
