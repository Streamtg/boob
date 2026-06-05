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
	"github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

// ── Progress tracker ──────────────────────────────────────────────────────────

type TaskStatus struct {
	InfoHash   string
	Name       string
	Progress   float64 // 0.0 – 100.0
	Done       bool
	Files      []string
	TotalBytes int64
	Downloaded int64
	Error      string
	StartedAt  time.Time
	mu         sync.RWMutex
}

// ── Download engine ───────────────────────────────────────────────────────────

type Engine struct {
	client  *torrent.Client
	storage string
	tasks   map[int64]*TaskStatus // keyed by chatID
	mu      sync.RWMutex
}

// Strip trailing parameters from a magnet URI so the parser doesn't confuse
// the info-hash with query parameters.
var magnetCleaner = regexp.MustCompile(`&.*|\?.*`)

func NewEngine(tc *torrent.Client, storage string) *Engine {
	return &Engine{
		client:  tc,
		storage: storage,
		tasks:   make(map[int64]*TaskStatus),
	}
}

// HandleMessage routes a message text to the appropriate handler.
func (e *Engine) HandleMessage(bot *tgbotapi.BotAPI, chatID int64, replyTo int, text string) {
	switch text {
	case "/cancel":
		e.cancelTask(bot, chatID, replyTo)
	case "/start", "/help":
		e.cmdHelp(bot, chatID, replyTo)
	case "/status":
		e.cmdStatus(bot, chatID, replyTo)
	default:
		if isMagnet(text) || isTorrentURL(text) {
			e.startDownload(bot, chatID, replyTo, text)
		} else {
			e.cmdHelp(bot, chatID, replyTo)
		}
	}
}

// ── /start & /help ────────────────────────────────────────────────────────────

func (e *Engine) cmdHelp(bot *tgbotapi.BotAPI, chatID int64, replyTo int) {
	e.sendMarkdown(bot, chatID, replyTo, `🤖 *TeleTorrent Bot*

Send me a magnet link or a direct .torrent URL and I will download it and send the files back here.

*Commands:*
• /start /help — Show this message
• /status   — Show active download status
• /cancel   — Cancel current download

*Supported:*
• Magnet links (magnet:?xt=…)
• Direct .torrent file URLs (https://…/file.torrent)

*Notes:*
• Files larger than ~2 GB are sent as document.
• Seeding continues after the upload finishes.
• Runs in user-space — no root needed.`)
}

// ── /status ───────────────────────────────────────────────────────────────────

func (e *Engine) cmdStatus(bot *tgbotapi.BotAPI, chatID int64, replyTo int) {
	e.mu.RLock()
	task, ok := e.tasks[chatID]
	e.mu.RUnlock()

	if !ok {
		e.sendMarkdown(bot, chatID, replyTo, "📭 *No active download.*")
		return
	}

	task.mu.RLock()
	pct := task.Progress
	name := task.Name
	errMsg := task.Error
	started := task.StartedAt
	task.mu.RUnlock()

	switch {
	case errMsg != "":
		e.sendMarkdown(bot, chatID, replyTo, fmt.Sprintf("❌ *Error:* `%s`", errMsg))
	case task.Done:
		e.sendMarkdown(bot, chatID, replyTo, fmt.Sprintf("✅ *Completed:* `%s`", name))
	default:
		elapsed := time.Since(started).Round(time.Second)
		e.sendMarkdown(bot, chatID, replyTo,
			fmt.Sprintf("⏳ *Downloading:* `%s`\n📊 `%.1f%%` | ⏱ %s", name, pct, elapsed))
	}
}

// ── /cancel ───────────────────────────────────────────────────────────────────

func (e *Engine) cancelTask(bot *tgbotapi.BotAPI, chatID int64, replyTo int) {
	e.mu.Lock()
	if _, ok := e.tasks[chatID]; !ok {
		e.mu.Unlock()
		e.sendMarkdown(bot, chatID, replyTo, "📭 Nothing to cancel.")
		return
	}
	delete(e.tasks, chatID)
	e.mu.Unlock()
	e.sendMarkdown(bot, chatID, replyTo, "🚫 Download cancelled.")
}

// ── Download starter ──────────────────────────────────────────────────────────

func (e *Engine) startDownload(bot *tgbotapi.BotAPI, chatID int64, replyTo int, input string) {
	// Only one active download per chat.
	e.mu.Lock()
	if _, active := e.tasks[chatID]; active {
		e.mu.Unlock()
		e.sendMarkdown(bot, chatID, replyTo, "⏳ Download already in progress. Use /cancel first.")
		return
	}

	e.tasks[chatID] = &TaskStatus{StartedAt: time.Now()}
	e.mu.Unlock()

	statusMsg := tgbotapi.NewMessage(chatID, "⏳ *Starting download…*")
	statusMsg.ReplyToMessageID = replyTo
	statusMsg.ParseMode = "Markdown"
	sent, _ := bot.Send(statusMsg)
	statusMsgID := sent.MessageID

	var t *torrent.Torrent
	var err error

	if isMagnet(input) {
		log.Printf("[%d] Adding magnet", chatID)
		clean := magnetCleaner.ReplaceAllString(strings.TrimSpace(input), "")
		t, err = e.client.AddMagnet(clean)
	} else {
		// .torrent URL — fetch the file first.
		log.Printf("[%d] Fetching .torrent URL: %s", chatID, input)
		bot.Send(tgbotapi.NewChatAction(chatID, tgbotapi.ChatTyping))

		data, fetchErr := fetchURL(input)
		if fetchErr != nil {
			e.failTask(chatID, fmt.Sprintf("fetch error: %v", fetchErr))
			e.editMarkdown(bot, statusMsgID, chatID, fmt.Sprintf("❌ *Failed to fetch .torrent:* `%s`", fetchErr.Error()))
			return
		}
		t, err = e.client.AddTorrentFromData(data)
		if err == nil {
			t.SetInfoURL(input)
		}
	}

	if err != nil {
		e.failTask(chatID, fmt.Sprintf("add error: %v", err))
		e.editMarkdown(bot, statusMsgID, chatID, fmt.Sprintf("❌ *Error:* `%s`", err.Error()))
		return
	}

	// Register metadata with the task.
	e.mu.RLock()
	e.tasks[chatID].InfoHash = t.InfoHash().HexString()
	e.tasks[chatID].Name = t.Name()
	e.mu.RUnlock()

	e.editMarkdown(bot, statusMsgID, chatID,
		fmt.Sprintf("📥 *Added:* `%s`\n⏳ *Waiting for peers…*", t.Name()))

	// ── Main download loop ──────────────────────────────────────────────────
	go e.downloadLoop(bot, chatID, replyTo, t, statusMsgID)
}

// downloadLoop blocks until the torrent is fully downloaded, then uploads.
func (e *Engine) downloadLoop(bot *tgbotapi.BotAPI, chatID int64, replyTo int, t *torrent.Torrent, statusMsgID int) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("[%d] panic: %v", chatID, r)
		}
	}()

	// Block until the torrent metadata is resolved.
	<-t.GotInfo()

	// Build the file list for the task.
	var files []string
	for _, f := range t.Files() {
		path := f.Path()
		if path != "" {
			files = append(files, path)
		}
	}

	e.mu.RLock()
	task := e.tasks[chatID]
	e.mu.RUnlock()

	task.mu.Lock()
	task.Files = files
	task.TotalBytes = t.Length()
	task.mu.Unlock()

	t.Download()

	// Report progress every 2 seconds.
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	// Stall safety: if no new bytes are received for 90 s, abort.
	stallTick := time.NewTicker(90 * time.Second)
	defer stallTick.Stop()

	var lastBytes int64
	lastTime := time.Now()

	for {
		select {
		case <-ticker.C:
			completed := t.BytesCompleted()
			total := t.Length()
			pct := float64(completed) / float64(total) * 100

			// Check if task was cancelled.
			e.mu.RLock()
			exists := e.tasks[chatID] != nil
			e.mu.RUnlock()
			if !exists {
				return
			}

			// Calculate download speed.
			elapsed := time.Since(task.StartedAt).Seconds()
			var speed string
			if elapsed > 4 && completed > 0 {
				bps := int64(float64(completed) / elapsed)
				speed = formatBytes(bps) + "/s"
			} else {
				speed = "connecting…"
			}

			task.mu.Lock()
			task.Progress = pct
			task.Downloaded = completed
			task.mu.Unlock()

			bar := progressBar(int(pct), 20)
			e.editMarkdown(bot, statusMsgID, chatID,
				fmt.Sprintf("📥 *Downloading*\n`%s`\n%s `%.1f%%`\n%sw %s / %s",
					t.Name(), bar, pct, speed,
					formatBytes(completed), formatBytes(total)))

			// Update stall tracker.
			if completed > lastBytes {
				lastBytes = completed
				lastTime = time.Now()
			}

		case <-stallTick.C:
			// No bytes received for 90 seconds — abort.
			if time.Since(lastTime) >= 90*time.Second {
				e.mu.RLock()
				exists := e.tasks[chatID] != nil
				e.mu.RUnlock()
				if exists {
					e.editMarkdown(bot, statusMsgID, chatID, "❌ *Stalled — no peers.*")
				}
				return
			}
		}

		// Exit when fully downloaded.
		if t.BytesCompleted() >= t.Length() {
			break
		}
	}

	// ── Download complete ───────────────────────────────────────────────────
	task.mu.Lock()
	task.Done = true
	task.Progress = 100
	task.mu.Unlock()

	e.editMarkdown(bot, statusMsgID, chatID, "✅ *Download complete — uploading to Telegram…*")
	e.uploadFiles(bot, chatID, replyTo, t)
}

// ── Upload completed files to Telegram ───────────────────────────────────────

func (e *Engine) uploadFiles(bot *tgbotapi.BotAPI, chatID int64, replyTo int, t *torrent.Torrent) {
	e.mu.RLock()
	task := e.tasks[chatID]
	e.mu.RUnlock()

	task.mu.RLock()
	files := task.Files
	task.mu.RUnlock()

	uploaded := 0
	failed := 0

	for _, fPath := range files {
		fullPath := filepath.Join(e.storage, fPath)

		fi, err := os.Stat(fullPath)
		if err != nil {
			log.Printf("[%d] cannot stat %s: %v", chatID, fPath, err)
			continue
		}
		if fi.IsDir() || fi.Size() == 0 {
			continue
		}

		log.Printf("[%d] uploading %s (%s)", chatID, fPath, formatBytes(fi.Size()))

		bot.Send(tgbotapi.NewChatAction(chatID, tgbotapi.ChatUploadDocument))

		doc := tgbotapi.NewDocumentUpload(chatID, fullPath)
		doc.ReplyToMessageID = replyTo
		doc.FileName = filepath.Base(fPath)

		if _, err := bot.Send(doc); err != nil {
			log.Printf("[%d] upload failed %s: %v", chatID, fPath, err)
			bot.Send(tgbotapi.NewMessage(chatID,
				fmt.Sprintf("⚠️ Could not upload `%s`: %v", fPath, err)))
			failed++
		} else {
			uploaded++
		}

		// Small pause between files to avoid Telegram rate limits.
		time.Sleep(500 * time.Millisecond)
	}

	// Clean up.
	e.mu.Lock()
	delete(e.tasks, chatID)
	e.mu.Unlock()

	var summary string
	if failed > 0 {
		summary = fmt.Sprintf("🎉 *Done!* Uploaded %d file(s), %d failed.", uploaded, failed)
	} else {
		summary = fmt.Sprintf("🎉 *All done!* Uploaded %d file(s).", uploaded)
	}
	bot.Send(tgbotapi.NewMessage(chatID, summary))
}

// ── Helpers ───────────────────────────────────────────────────────────────────

func (e *Engine) failTask(chatID int64, errMsg string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if task, ok := e.tasks[chatID]; ok {
		task.mu.Lock()
		task.Error = errMsg
		task.mu.Unlock()
	}
}

func (e *Engine) sendMarkdown(bot *tgbotapi.BotAPI, chatID int64, replyTo int, text string) {
	msg := tgbotapi.NewMessage(chatID, text)
	msg.ReplyToMessageID = replyTo
	msg.ParseMode = "Markdown"
	bot.Send(msg)
}

func (e *Engine) editMarkdown(bot *tgbotapi.BotAPI, msgID int, chatID int64, text string) {
	edit := tgbotapi.NewEditMessageText(chatID, msgID, text)
	edit.ParseMode = "Markdown"
	bot.Request(edit)
}

func isMagnet(s string) bool {
	return strings.HasPrefix(strings.TrimSpace(s), "magnet:?")
}

func isTorrentURL(s string) bool {
	s = strings.TrimSpace(s)
	return (strings.HasPrefix(s, "http://") || strings.HasPrefix(s, "https://")) &&
		strings.HasSuffix(s, ".torrent")
}

func fetchURL(rawURL string) ([]byte, error) {
	client := &http.Client{Timeout: 60 * time.Second}
	resp, err := client.Get(rawURL)
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
		return fmt.Sprintf("%.1f GB", float64(n)/(1<<30))
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
	return strings.Repeat("█", filled) + strings.Repeat("░", width-filled)
}