package main

import (
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/anacrolix/torrent"
	"github.com/anacrolix/torrent/metainfo"
	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

const (
	progressInterval = 2 * time.Second
	stallTimeout     = 90 * time.Second
	uploadPause      = 500 * time.Millisecond
	maxRetries       = 5
	retryDelay       = 2 * time.Second
)

type TaskStatus struct {
	InfoHash   string
	Name       string
	Progress   float64
	Done       bool
	Files      []string
	TotalBytes int64
	Downloaded int64
	Error      string
	StartedAt  time.Time
	mu         sync.RWMutex
}

type Engine struct {
	client   *torrent.Client
	storage  string
	tasks    map[int64]*TaskStatus
	mu       sync.RWMutex
	magnetRe *regexp.Regexp
}

func NewEngine(cfg Config) (*Engine, error) {
	if err := os.MkdirAll(cfg.Storage, 0755); err != nil {
		return nil, fmt.Errorf("mkdir storage: %w", err)
	}

	tc := torrent.NewDefaultClientConfig()
	tc.DataDir = cfg.Storage
	tc.Seed = true
	tc.Debug = false
	tc.ListenPort = cfg.Port
	tc.NoDHT = false

	client, err := torrent.NewClient(tc)
	if err != nil {
		return nil, fmt.Errorf("torrent client: %w", err)
	}

	return &Engine{
		client:   client,
		storage:  cfg.Storage,
		tasks:    make(map[int64]*TaskStatus),
		magnetRe: regexp.MustCompile(`[&?].*$`),
	}, nil
}

func (e *Engine) Close() { e.client.Close() }

func (e *Engine) HandleMessage(bot *tgbotapi.BotAPI, chatID int64, replyTo int, text string) {
	switch {
	case text == "/start" || text == "/help":
		e.cmdHelp(bot, chatID, replyTo)
	case text == "/status":
		e.cmdStatus(bot, chatID, replyTo)
	case text == "/cancel":
		e.cmdCancel(bot, chatID, replyTo)
	case strings.HasPrefix(strings.TrimSpace(text), "magnet:?"):
		e.startDownload(bot, chatID, replyTo, text)
	case isTorrentURL(text):
		e.startDownload(bot, chatID, replyTo, text)
	default:
		e.send(bot, chatID, replyTo, "*Unknown command.* Use /help")
	}
}

func (e *Engine) cmdHelp(bot *tgbotapi.BotAPI, chatID int64, replyTo int) {
	e.send(bot, chatID, replyTo,
		"*TeleTorrent Bot*\n\n"+
			"Send me a magnet link or .torrent URL and I'll download and send files back.\n\n"+
			"*Commands:*\n"+
			"/start /help - Show this\n"+
			"/status - Download progress\n"+
			"/cancel - Cancel current download\n\n"+
			"*Supported:*\n"+
			"`magnet:?xt=urn:btih:...`\n"+
			"`https://.../file.torrent`\n\n"+
			"One download at a time. 90s stall timeout.")
}

func (e *Engine) cmdStatus(bot *tgbotapi.BotAPI, chatID int64, replyTo int) {
	e.mu.RLock()
	task, ok := e.tasks[chatID]
	e.mu.RUnlock()
	if !ok {
		e.send(bot, chatID, replyTo, "*No active download.*")
		return
	}

	task.mu.RLock()
	pct, name, errMsg := task.Progress, task.Name, task.Error
	started, done := task.StartedAt, task.Done
	down, total := task.Downloaded, task.TotalBytes
	task.mu.RUnlock()

	switch {
	case errMsg != "":
		e.send(bot, chatID, replyTo, fmt.Sprintf("*Error:* `%s`", errMsg))
	case done:
		e.send(bot, chatID, replyTo, fmt.Sprintf("*Completed:* `%s`", name))
	default:
		elapsed := time.Since(started).Round(time.Second)
		speed := ""
		if elapsed.Seconds() > 4 && down > 0 {
			speed = " | " + formatBytes(int64(float64(down)/elapsed.Seconds())) + "/s"
		}
		e.send(bot, chatID, replyTo,
			fmt.Sprintf("*Downloading:* `%s`\n`%.1f%%` | %s / %s%s\n%s",
				name, pct, formatBytes(down), formatBytes(total), speed, elapsed))
	}
}

func (e *Engine) cmdCancel(bot *tgbotapi.BotAPI, chatID int64, replyTo int) {
	e.mu.Lock()
	if _, ok := e.tasks[chatID]; !ok {
		e.mu.Unlock()
		e.send(bot, chatID, replyTo, "*Nothing to cancel.*")
		return
	}
	delete(e.tasks, chatID)
	e.mu.Unlock()
	e.send(bot, chatID, replyTo, "*Download cancelled.*")
}

func (e *Engine) startDownload(bot *tgbotapi.BotAPI, chatID int64, replyTo int, input string) {
	e.mu.Lock()
	if _, active := e.tasks[chatID]; active {
		e.mu.Unlock()
		e.send(bot, chatID, replyTo, "*Download already in progress.* Use /cancel first.")
		return
	}
	e.tasks[chatID] = &TaskStatus{StartedAt: time.Now()}
	e.mu.Unlock()

	msg := tgbotapi.NewMessage(chatID, "*Starting download...*")
	msg.ReplyToMessageID = replyTo
	msg.ParseMode = "Markdown"
	sent, err := bot.Send(msg)
	if err != nil {
		log.Printf("[%d] send error: %v", chatID, err)
		e.mu.Lock()
		delete(e.tasks, chatID)
		e.mu.Unlock()
		return
	}
	statusMsgID := sent.MessageID

	var t *torrent.Torrent
	var addErr error

	if strings.HasPrefix(strings.TrimSpace(input), "magnet:?") {
		log.Printf("[%d] magnet: %s...", chatID, input[:min(len(input), 60)])
		clean := e.magnetRe.ReplaceAllString(strings.TrimSpace(input), "")
		if !strings.HasPrefix(clean, "magnet:?xt=") {
			e.edit(bot, statusMsgID, chatID, "*Invalid magnet link.*")
			cleanup(e, chatID)
			return
		}
		t, addErr = e.client.AddMagnet(clean)
	} else {
		log.Printf("[%d] torrent URL: %s", chatID, input)
		bot.Send(tgbotapi.NewChatAction(chatID, tgbotapi.ChatTyping))
		data, fetchErr := fetchURL(input)
		if fetchErr != nil {
			e.edit(bot, statusMsgID, chatID, fmt.Sprintf("*Fetch error:* `%s`", fetchErr.Error()))
			cleanup(e, chatID)
			return
		}
		mi, parseErr := metainfo.Load(data)
		if parseErr != nil {
			e.edit(bot, statusMsgID, chatID, fmt.Sprintf("*Parse error:* `%s`", parseErr.Error()))
			cleanup(e, chatID)
			return
		}
		t, addErr = e.client.AddTorrent(mi)
	}
	if addErr != nil {
		e.edit(bot, statusMsgID, chatID, fmt.Sprintf("*Add error:* `%s`", addErr.Error()))
		cleanup(e, chatID)
		return
	}

	select {
	case <-t.GotInfo():
	case <-time.After(30 * time.Second):
		e.edit(bot, statusMsgID, chatID, "*Timeout getting metadata.*")
		t.Drop()
		cleanup(e, chatID)
		return
	}

	name := t.Name()
	totalLen := t.Length()

	e.mu.RLock()
	if task, ok := e.tasks[chatID]; ok {
		task.mu.Lock()
		task.InfoHash = t.InfoHash().HexString()
		task.Name = name
		task.TotalBytes = totalLen
		for _, f := range t.Files() {
			if p := f.Path(); p != "" {
				task.Files = append(task.Files, p)
			}
		}
		task.mu.Unlock()
	}
	e.mu.RUnlock()

	t.DownloadAll()
	e.edit(bot, statusMsgID, chatID,
		fmt.Sprintf("*Added:* `%s`\nSize: `%s`\n*Looking for peers...*", name, formatBytes(totalLen)))

	go e.downloadLoop(bot, chatID, replyTo, t, statusMsgID)
}

func (e *Engine) downloadLoop(bot *tgbotapi.BotAPI, chatID int64, replyTo int, t *torrent.Torrent, statusMsgID int) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("[%d] PANIC: %v", chatID, r)
		}
	}()

	ticker := time.NewTicker(progressInterval)
	defer ticker.Stop()
	stallTicker := time.NewTicker(stallTimeout)
	defer stallTicker.Stop()

	var lastBytes int64
	lastTime := time.Now()

	e.mu.RLock()
	task := e.tasks[chatID]
	e.mu.RUnlock()
	if task == nil {
		return
	}

	for {
		e.mu.RLock()
		exists := e.tasks[chatID] != nil
		e.mu.RUnlock()
		if !exists {
			t.Drop()
			return
		}

		completed := t.BytesCompleted()
		total := t.Length()
		if completed >= total && total > 0 {
			break
		}

		select {
		case <-ticker.C:
			pct := float64(completed) / float64(total) * 100
			elapsed := time.Since(task.StartedAt).Seconds()
			speed := "connecting..."
			if elapsed > 4 && completed > 0 {
				speed = formatBytes(int64(float64(completed)/elapsed)) + "/s"
			}

			task.mu.Lock()
			task.Progress = pct
			task.Downloaded = completed
			task.mu.Unlock()

			bar := progressBar(int(pct), 20)
			e.edit(bot, statusMsgID, chatID,
				fmt.Sprintf("*Downloading:* `%s`\n%s `%.1f%%`\n%s | %s / %s",
					t.Name(), bar, pct, speed, formatBytes(completed), formatBytes(total)))

			if completed > lastBytes {
				lastBytes = completed
				lastTime = time.Now()
			}

		case <-stallTicker.C:
			if time.Since(lastTime) >= stallTimeout {
				log.Printf("[%d] stalled", chatID)
				e.edit(bot, statusMsgID, chatID, "*Stalled - no peers.*")
				e.mu.Lock()
				delete(e.tasks, chatID)
				e.mu.Unlock()
				t.Drop()
				return
			}
		}
	}

	task.mu.Lock()
	task.Done = true
	task.Progress = 100
	task.mu.Unlock()

	e.edit(bot, statusMsgID, chatID, "*Download complete - uploading to Telegram...*")
	e.uploadFiles(bot, chatID, replyTo, t, statusMsgID)
}

func (e *Engine) uploadFiles(bot *tgbotapi.BotAPI, chatID int64, replyTo int, t *torrent.Torrent, statusMsgID int) {
	e.mu.RLock()
	task := e.tasks[chatID]
	e.mu.RUnlock()
	if task == nil {
		return
	}

	task.mu.RLock()
	files := make([]string, len(task.Files))
	copy(files, task.Files)
	task.mu.RUnlock()

	uploaded, failed := 0, 0

	for _, fPath := range files {
		fullPath := filepath.Join(e.storage, fPath)
		fi, err := os.Stat(fullPath)
		if err != nil {
			log.Printf("[%d] stat %s: %v", chatID, fPath, err)
			failed++
			continue
		}
		if fi.IsDir() || fi.Size() == 0 {
			continue
		}

		log.Printf("[%d] uploading %s (%s)", chatID, fPath, formatBytes(fi.Size()))
		bot.Send(tgbotapi.NewChatAction(chatID, tgbotapi.ChatUploadDocument))

		sent := false
		for attempt := 0; attempt < maxRetries; attempt++ {
			doc := tgbotapi.NewDocument(chatID, tgbotapi.FilePath(fullPath))
			doc.ReplyToMessageID = replyTo
			if _, sendErr := bot.Send(doc); sendErr == nil {
				sent = true
				break
			} else {
				log.Printf("[%d] attempt %d failed: %v", chatID, attempt+1, sendErr)
				if strings.Contains(sendErr.Error(), "429") {
					time.Sleep(retryDelay * time.Duration(attempt+1))
				} else {
					break
				}
			}
		}
		if sent {
			uploaded++
		} else {
			bot.Send(tgbotapi.NewMessage(chatID, fmt.Sprintf("*Upload failed:* `%s`", fPath)))
			failed++
		}
		time.Sleep(uploadPause)
	}

	e.mu.Lock()
	delete(e.tasks, chatID)
	e.mu.Unlock()

	summary := fmt.Sprintf("*Done!* Uploaded %d file(s).", uploaded)
	if failed > 0 {
		summary = fmt.Sprintf("*Done!* Uploaded %d, %d failed.", uploaded, failed)
	}
	bot.Send(tgbotapi.NewMessage(chatID, summary))
	bot.Request(tgbotapi.NewDeleteMessage(chatID, statusMsgID))
}

func (e *Engine) send(bot *tgbotapi.BotAPI, chatID int64, replyTo int, text string) {
	msg := tgbotapi.NewMessage(chatID, text)
	msg.ReplyToMessageID = replyTo
	msg.ParseMode = "Markdown"
	if _, err := bot.Send(msg); err != nil {
		log.Printf("[%d] send err: %v", chatID, err)
	}
}

func (e *Engine) edit(bot *tgbotapi.BotAPI, msgID int, chatID int64, text string) {
	edit := tgbotapi.NewEditMessageText(chatID, msgID, text)
	edit.ParseMode = "Markdown"
	if _, err := bot.Request(edit); err != nil {
		if !strings.Contains(err.Error(), "400") {
			log.Printf("[%d] edit err: %v", chatID, err)
		}
	}
}

func cleanup(e *Engine, chatID int64) {
	e.mu.Lock()
	delete(e.tasks, chatID)
	e.mu.Unlock()
}

func isTorrentURL(s string) bool {
	s = strings.TrimSpace(s)
	return (strings.HasPrefix(s, "http://") || strings.HasPrefix(s, "https://")) &&
		strings.HasSuffix(s, ".torrent")
}

func fetchURL(rawURL string) ([]byte, error) {
	c := &http.Client{Timeout: 60 * time.Second}
	resp, err := c.Get(rawURL)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return io.ReadAll(resp.Body)
}

func formatBytes(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.2f GB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KB", float64(n)/(1<<10))
	default:
		return strconv.FormatInt(n, 10) + " B"
	}
}

func progressBar(percent, width int) string {
	if percent < 0 {
		percent = 0
	}
	if percent > 100 {
		percent = 100
	}
	filled := percent * width / 100
	return strings.Repeat("=", filled) + strings.Repeat("-", width-filled)
}
