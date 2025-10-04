package commands

import (
	"encoding/json"
	"fmt"
	"os"

	"EverythingSuckz/fsb/config"
	"EverythingSuckz/fsb/internal/utils"

	"github.com/celestix/gotgproto/dispatcher"
	"github.com/celestix/gotgproto/dispatcher/handlers"
	"github.com/celestix/gotgproto/ext"
	"github.com/celestix/gotgproto/storage"
)

const blockedFile = "blocked_users.json"

// Persistent block list
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
	loadBlockedUsers() // load blocked users on start

	dispatcher.AddHandler(handlers.NewCommand("start", start))
	dispatcher.AddHandler(handlers.NewCommand("block", blockCommand))
	dispatcher.AddHandler(handlers.NewCommand("unblock", unblockCommand))
	dispatcher.AddHandler(handlers.NewMessage(nil, handleForwarded))
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

// ---- /block ----
func blockCommand(ctx *ext.Context, u *ext.Update) error {
	chatId := u.EffectiveChat().GetID()
	ctx.Reply(u, "Please forward a message from the user you want to block.", nil)
	ctx.Session().Set("next_block", true)
	ctx.Session().Set("requester", chatId)
	return dispatcher.EndGroups
}

// ---- /unblock ----
func unblockCommand(ctx *ext.Context, u *ext.Update) error {
	chatId := u.EffectiveChat().GetID()
	ctx.Reply(u, "Please forward a message from the user you want to unblock.", nil)
	ctx.Session().Set("next_unblock", true)
	ctx.Session().Set("requester", chatId)
	return dispatcher.EndGroups
}

// ---- Handler for forwarded messages ----
func handleForwarded(ctx *ext.Context, u *ext.Update) error {
	s := ctx.Session()

	// Block user
	if nextBlock, _ := s.Get("next_block").(bool); nextBlock {
		if u.EffectiveMessage.ForwardFrom != nil {
			target := u.EffectiveMessage.ForwardFrom.GetID()
			blockedUsers[target] = struct{}{}
			saveBlockedUsers()
			ctx.Reply(u, fmt.Sprintf("User %d blocked ✅", target), nil)
			s.Delete("next_block")
			s.Delete("requester")
			return dispatcher.EndGroups
		}
		ctx.Reply(u, "You must forward a valid message from the user to block.", nil)
		return dispatcher.EndGroups
	}

	// Unblock user
	if nextUnblock, _ := s.Get("next_unblock").(bool); nextUnblock {
		if u.EffectiveMessage.ForwardFrom != nil {
			target := u.EffectiveMessage.ForwardFrom.GetID()
			delete(blockedUsers, target)
			saveBlockedUsers()
			ctx.Reply(u, fmt.Sprintf("User %d unblocked ✅", target), nil)
			s.Delete("next_unblock")
			s.Delete("requester")
			return dispatcher.EndGroups
		}
		ctx.Reply(u, "You must forward a valid message from the user to unblock.", nil)
		return dispatcher.EndGroups
	}

	return dispatcher.ContinueGroups
}
