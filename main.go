package main

import (
	"flag"
	"log"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"

	"github.com/anacrolix/torrent"
	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

type Config struct {
	Token     string
	ChannelID int64
	Storage   string
	Port      int
}

func parseFlags() Config {
	token := flag.String("token", "", "Telegram Bot Token (required)")
	channel := flag.String("channel", "", "Telegram Channel/Group ID (optional, numeric)")
	storage := flag.String("storage", "./downloads", "Download directory")
	port := flag.Int("port", 0, "DHT listen port (0 = random)")
	flag.Parse()

	if *token == "" {
		log.Fatal("ERROR: -token is required")
	}

	cfg := Config{Token: *token, Storage: *storage, Port: *port}
	if *channel != "" {
		id, err := strconv.ParseInt(*channel, 10, 64)
		if err != nil {
			log.Fatalf("Invalid channel ID: %v (must be numeric)", err)
		}
		cfg.ChannelID = id
	}
	return cfg
}

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
	log.Println("=== TeleTorrent Bot ===")
	log.Printf("Token: %s...%s", cfg.Token[:4], cfg.Token[len(cfg.Token)-4:])
	log.Printf("Storage: %s", cfg.Storage)

	bot, err := tgbotapi.NewBotAPI(cfg.Token)
	if err != nil {
		log.Fatalf("Bot init failed: %v", err)
	}
	log.Printf("Authorized as @%s (ID: %d)", bot.Self.UserName, bot.Self.ID)

	if cfg.ChannelID != 0 {
		if ch, err := bot.GetChat(tgbotapi.ChatInfoConfig{
			ChatConfig: tgbotapi.ChatConfig{ChatID: cfg.ChannelID},
		}); err != nil {
			log.Printf("Warning: channel %d check failed: %v", cfg.ChannelID, err)
		} else {
			log.Printf("Channel verified: %s (type: %s)", ch.Title, ch.Type)
		}
	}

	engine, err := NewEngine(cfg)
	if err != nil {
		log.Fatalf("Engine init failed: %v", err)
	}
	defer engine.Close()

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		s := <-sig
		log.Printf("Signal received: %v. Shutting down...", s)
		bot.StopReceivingUpdates()
		engine.Close()
		os.Exit(0)
	}()

	log.Println("Listening for messages...")
	startPolling(bot, engine)
}
