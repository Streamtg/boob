package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/anacrolix/torrent"
	"github.com/anacrolix/torrent/metainfo"
)

// ─────────────────────────────────────────────────────────────────────────────
// Configuración
// ─────────────────────────────────────────────────────────────────────────────

var (
	token      string
	channelID  int64
	storageDir string
	httpClient *http.Client
)

// ─────────────────────────────────────────────────────────────────────────────
// Telegram Bot API (stdlib only)
// ─────────────────────────────────────────────────────────────────────────────

func apiURL(method string) string {
	return "https://api.telegram.org/bot" + token + "/" + method
}

func get(method string, params map[string]string) ([]byte, error) {
	u := apiURL(method)
	if len(params) > 0 {
		q := url.Values{}
		for k, v := range params {
			q.Set(k, v)
		}
		u += "?" + q.Encode()
	}
	resp, err := httpClient.Get(u)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	return io.ReadAll(resp.Body)
}

func postJSON(method string, data interface{}) ([]byte, error) {
	body, _ := json.Marshal(data)
	resp, err := httpClient.Post(apiURL(method), "application/json", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	return io.ReadAll(resp.Body)
}

// sendMsg con retry automático para 429
func sendMsg(chatID int64, text string, replyTo int, markdown bool) (int, error) {
	parseMode := ""
	if markdown {
		parseMode = "Markdown"
	}
	data := map[string]interface{}{
		"chat_id": chatID,
		"text":    text,
		"parse_mode": parseMode,
	}
	if replyTo > 0 {
		data["reply_to_message_id"] = replyTo
}
