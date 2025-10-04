package commands

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"

	"EverythingSuckz/fsb/config"
	"EverythingSuckz/fsb/internal/utils"

	"github.com/celestix/gotgproto/dispatcher"
	"github.com/celestix/gotgproto/dispatcher/handlers"
	"github.com/celestix/gotgproto/ext"
	"github.com/celestix/gotgproto/storage"
)

const blockedFile = "blocked_users.json"

var blockedUsers = make(map[int64]struct{})

// Load blocked users from JSON
func loadBlockedUsers() {
	file, err := os.ReadFile(blockedFile)
	if err != nil {
		return
	}
	var ids []int64
	if err := json.Unmarshal(file, &ids); err != nil {
		return
	}
	for _, id := range ids {
		blockedUsers[id] = struct{}{}
	}
}

// Save blocked users to JSON
func saveBlockedUsers() {
	var ids []int64
	for id := range blockedUsers {
		ids = append(ids, id)
	}
	data, _ := json.Marshal(ids)
	_ = os.WriteFile(blockedFile, data, 0644)
}

// Check if user is allowed
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
	loadBlockedUsers()
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

	msg := "Send or forward any type of file and you will get a streaming and download link.\n\n"
	msg += "Official channel: @yoelbotsx\n"
	msg += "Link valid for 24 hours ⏳\n"
	msg += "📊 Use /stats to view bot statistics"

	ctx.Reply(u, msg, nil)
	return dispatcher.EndGroups
}

// ---- /block <user_id> ----
func blockCommand(ctx *ext.Context, u *ext.Update) error {
	chatId := u.EffectiveChat().GetID()
	if !utils.Contains(config.ValueOf.AdminUsers, chatId) {
		ctx.Reply(u, "You are not allowed to block users.", nil)
		return dispatcher.EndGroups
	}

	args := strings.Fields(u.MessageText())
	if len(args) < 2 {
		ctx.Reply(u, "Usage: /block <user_id>", nil)
		return dispatcher.EndGroups
	}

	id, err := strconv.ParseInt(args[1], 10, 64)
	if err != nil {
		ctx.Reply(u, "Invalid user ID.", nil)
		return dispatcher.EndGroups
	}

	blockedUsers[id] = struct{}{}
	saveBlockedUsers()
	ctx.Reply(u, fmt.Sprintf("User %d blocked ✅", id), nil)
	return dispatcher.EndGroups
}

// ---- /unblock <user_id> ----
func unblockCommand(ctx *ext.Context, u *ext.Update) error {
	chatId := u.EffectiveChat().GetID()
	if !utils.Contains(config.ValueOf.AdminUsers, chatId) {
		ctx.Reply(u, "You are not allowed to unblock users.", nil)
		return dispatcher.EndGroups
	}

	args := strings.Fields(u.MessageText())
	if len(args) < 2 {
		ctx.Reply(u, "Usage: /unblock <user_id>", nil)
		return dispatcher.EndGroups
	}

	id, err := strconv.ParseInt(args[1], 10, 64)
	if err != nil {
		ctx.Reply(u, "Invalid user ID.", nil)
		return dispatcher.EndGroups
	}

	delete(blockedUsers, id)
	saveBlockedUsers()
	ctx.Reply(u, fmt.Sprintf("User %d unblocked ✅", id), nil)
	return dispatcher.EndGroups
}
