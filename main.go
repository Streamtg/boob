package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/anacrolix/torrent"
	"github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

// Config holds all the bot configuration.
type Config struct {
	BotToken       string
	TorrentStorage string // Directory for downloading torrents.
}

func main() {
	// ── Parse CLI flags ──────────────────────────────────────────────────────
	cfg := Config{}
	flag.StringVar(&cfg.BotToken, "token", "", "Telegram bot token from @BotFather")
	flag.StringVar(&cfg.TorrentStorage, "storage", "./downloads", "Directory to store downloaded files")
	flag.Parse()

	if cfg.BotToken == "" {
		fmt.Println("❌  Usage: go run main.go -token YOUR_BOT_TOKEN")
		fmt.Println("   Get your token from https://t.me/BotFather")
		os.Exit(1)
	}

	// ── Prepare storage directory ────────────────────────────────────────────
	if err := os.MkdirAll(cfg.TorrentStorage, 0755); err != nil {
		log.Fatalf("❌  Cannot create storage directory %q: %v", cfg.TorrentStorage, err)
	}

	// ── Create torrent client ────────────────────────────────────────────────
	tc, err := torrent.NewClient(&torrent.Config{
		DataDir:               cfg.TorrentStorage,
		NoUpload:              false,
		Seed:                  true,
		DisableTCP:            false,
		DisableUTP:            false,
		DisableIPv6:           false,
		NoDHT:                 false,
		DisableIPv4:           false,
		// AutoUpdate will use ~10MB RAM extra; set true only if your machine
		// supports it and you want faster tracker resolution.
		AutoUpdate:            false,
		// Keep the torrent data after download completes so seeding continues.
		// Set to 0 to disable auto-cleanup.
		DefaultStorage:        nil,
		// Limit how many peers we connect to (0 = unlimited).
		MaxPeers:              100,
		// Download rate limit in bytes/s (0 = unlimited).
		DownloadRateLimit:     0,
		// Upload rate limit in bytes/s (0 = unlimited).
		UploadRateLimit:       0,
		// Listen on all interfaces, random port. 0 means let OS pick one.
		ListenAddr:            "0.0.0.0:0",
		// HTTP trackers.
		Trackers:              []string{},
		// Set a DHT bootstrap node to speed up magnet resolution.
		DHTPeers:              10,
		// Allow private torrents to be downloaded (disable DHT for those).
		DisableAcceptRateLimit: false,
	})
	if err != nil {
		log.Fatalf("❌  Failed to create torrent client: %v", err)
	}
	defer tc.Close()
	log.Printf("✅  Torrent client started. Storage: %s", cfg.TorrentStorage)

	// ── Create Telegram bot ──────────────────────────────────────────────────
	bot, err := tgbotapi.NewBotAPI(cfg.BotToken)
	if err != nil {
		log.Fatalf("❌  Failed to authenticate with Telegram: %v", err)
	}
	log.Printf("✅  Telegram bot logged in as @%s", bot.Self.UserName)

	// ── Bootstrap the download engine ────────────────────────────────────────
	engine := NewEngine(tc, cfg.TorrentStorage)

	// ── Graceful shutdown ────────────────────────────────────────────────────
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		<-sigChan
		log.Println("⚡  Shutting down...")
		tc.Close()
		os.Exit(0)
	}()

	// ── Start update dispatcher ──────────────────────────────────────────────
	dispatch(bot, engine)
}

// dispatch receives updates from Telegram and routes them to handlers.
func dispatch(bot *tgbotapi.BotAPI, engine *Engine) {
	// Long-polling config: 60 s timeout, up to 100 updates per request.
	u := tgbotapi.NewUpdate(0)
	u.Timeout = 60

	updates := bot.GetUpdatesChan(u)

	for up := range updates {
		if up.Message == nil && (up.CallbackQuery == nil && up.ChannelPost == nil) {
			continue
		}

		var chatID int64
		var text string
		var msgID int

		if up.Message != nil {
			chatID = up.Message.Chat.ID
			text = up.Message.Text
			msgID = up.Message.MessageID
		} else if up.CallbackQuery != nil {
			chatID = up.CallbackQuery.Message.Chat.ID
			text = up.CallbackQuery.Data
			msgID = up.CallbackQuery.Message.MessageID
			// Answer the callback immediately so the button stops spinning.
			bot.Request(tgbotapi.CallbackConfig{CallbackQueryID: up.CallbackQuery.ID})
		} else {
			continue
		}

		go func(chat int64, body string, replyTo int) {
			engine.HandleMessage(bot, chat, replyTo, body)
		}(chatID, text, msgID)
	}
}