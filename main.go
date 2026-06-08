package main

import (
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

func startPolling(bot *tgbotapi.BotAPI, engine *Engine) {
	u := tgbotapi.NewUpdate(0)
	u.Timeout = 60
	updates := bot.GetUpdatesChan(u)
	for update := range updates {
		if update.Message == nil || strings.TrimSpace(update.Message.Text) == "" {
			continue
		}
		chatID := update.Message.Chat.ID
		text := strings.TrimSpace(update.Message.Text)
		log.Printf("[%d] << %s", chatID, text)
		engine.HandleMessage(bot, chatID, update.Message.MessageID, text)
	}
}

func main() {
	cfg := parseFlags()
	log.SetFlags(log.Ldate | log.Ltime | log.Lshortfile)
	log.Println("=== TeleTorrent Bot - Versión Reconstruida ===")

	bot, err := tgbotapi.NewBotAPI(cfg.Token)
	if err != nil {
		log.Fatalf("bot init: %v", err)
	}
	log.Printf("autorizado como @%s (id: %d)", bot.Self.UserName, bot.Self.ID)

	if cfg.ChannelID != 0 {
		if ch, err := bot.GetChat(tgbotapi.ChatInfoConfig{
			ChatConfig: tgbotapi.ChatConfig{ChatID: cfg.ChannelID},
		}); err != nil {
			log.Printf("warning: channel %d: %v", cfg.ChannelID, err)
		} else {
			log.Printf("canal verificado: %s (%s)", ch.Title, ch.Type)
		}
	}

	engine, err := NewEngine(cfg)
	if err != nil {
		log.Fatalf("engine init: %v", err)
	}
	defer engine.Close()

	// MTProto se inicia automáticamente en background
	if cfg.ChannelID != 0 {
		go engine.InitMTProto(cfg.ChannelID)
	}

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sig
		log.Println("cerrando...")
		bot.StopReceivingUpdates()
		engine.Close()
		os.Exit(0)
	}()
	log.Println("escuchando mensajes...")
	startPolling(bot, engine)
}
