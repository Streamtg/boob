package main

import (
	"flag"
	"log"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

// Config holds command-line flags.
type Config struct {
	Token     string
	ChannelID int64
	Storage   string
	Port      int
}

func parseFlags() Config {
	token := flag.String("token", "", "Telegram Bot Token (required)")
	channel := flag.String("channel", "", "Telegram channel/group ID (optional, numeric)")
	storage := flag.String("storage", "./downloads", "download directory")
	port := flag.Int("port", 0, "DHT listen port (0 = random)")
	flag.Parse()

	if *token == "" {
		log.Fatal("ERROR: -token is required")
	}

	cfg := Config{
		Token:   *token,
		Storage: *storage,
		Port:    *port,
	}
	if *channel != "" {
		id, err := strconv.ParseInt(*channel, 10, 64)
		if err != nil {
			log.Fatalf("invalid channel ID %q: %v", *channel, err)
		}
		cfg.ChannelID = id
	}
	return cfg
}

// startPolling runs the long-poll loop for Telegram updates.
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
	log.Printf("token: %s...%s", cfg.Token[:4], cfg.Token[len(cfg.Token)-4:])
	log.Printf("storage: %s", cfg.Storage)

	bot, err := tgbotapi.NewBotAPI(cfg.Token)
	if err != nil {
		log.Fatalf("bot init: %v", err)
	}
	log.Printf("authorized as @%s (id: %d)", bot.Self.UserName, bot.Self.ID)

	if cfg.ChannelID != 0 {
		ch, err := bot.GetChat(tgbotapi.ChatInfoConfig{
			ChatConfig: tgbotapi.ChatConfig{ChatID: cfg.ChannelID},
		})
		if err != nil {
			log.Printf("warning: channel %d check failed: %v", cfg.ChannelID, err)
		} else {
			log.Printf("channel verified: %s (%s)", ch.Title, ch.Type)
		}
	}

	engine, err := NewEngine(cfg)
	if err != nil {
		log.Fatalf("engine init: %v", err)
	}
	defer engine.Close()

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		s := <-sig
		log.Printf("signal %v received, shutting down ...", s)
		bot.StopReceivingUpdates()
		engine.Close()
		os.Exit(0)
	}()

	log.Println("listening for messages ...")
	startPolling(bot, engine)
}
