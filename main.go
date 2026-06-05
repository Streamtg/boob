package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/anacrolix/torrent"
	"github.com/anacrolix/torrent/metainfo"
)

// ─────────────────────────────────────────────────────────────────────────────
// Telegram Bot API — stdlib only, no external deps
// ─────────────────────────────────────────────────────────────────────────────

type Bot struct {
	token   string
	baseURL string
	client  *http.Client
}

func NewBot(token string) *Bot {
	return &Bot{
		token:   token,
		baseURL: "https://api.telegram.org/bot" + token,
		client:  &http.Client{Timeout: 30 * time.Second},
}
