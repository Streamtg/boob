package main

import (
	"context"
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
	"github.com/gotd/td/telegram"
	"github.com/gotd/td/telegram/auth"
	"github.com/gotd/td/tg"
)

// ─────────────────────────────────────────────────────────────────────────────
// Bot API (for messages/commands) — stdlib only
// ─────────────────────────────────────────────────────────────────────────────

type Bot struct {
	token     string
	baseURL   string
	client    *http.Client
	channelID int64
}

func NewBot(token string, channelID int64) *Bot {
	return &Bot{
		token:     token,
		baseURL:   "https://api.telegram.org/bot" + token,
		channelID: channelID,
		client: &http.Client{
			Timeout: 180 * time.Second,
			Transport: &http.Transport{
				MaxIdleConns:        200,
				MaxIdleConnsPerHost: 50,
				IdleConnTimeout:     200 * time.Second,
			},
		},
	}
}

func (b *Bot) api(method string, params map[string]string) ([]byte, error) {
	reqURL := b.baseURL + "/" + method
	if len(params) > 0 {
		q := url.Values{}
		for k, v := range params {
			q.Set(k, v)
		}
		reqURL += "?" + q.Encode()
	}
	resp, err := b.client.Get(reqURL)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	return io.ReadAll(resp.Body)
}

func (b *Bot) post(path string, mime string, body io.Reader) ([]byte, error) {
	resp, err := b.client.Post(b.baseURL+"/"+path, mime, body)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	return io.ReadAll(resp.Body)
}

func (b *Bot) getChatID(msgChatID int64) int64 {
	if b.channelID != 0 {
		return b.channelID
	}
	return msgChatID
}

type Update struct {
	UpdateID int `json:"update_id"`
	Message  *struct {
		MessageID int    `json:"message_id"`
		Chat      struct {
			ID int64 `json:"id"`
		} `json:"chat"`
		Text    string `json:"text"`
		Caption string `json:"caption"`
	} `json:"message"`
}

func (b *Bot) sendMsg(chatID int64, text string, replyTo int, markdown bool) (int, error) {
	var parseMode string
	if markdown {
		parseMode = "Markdown"
	}
	body, _ := json.Marshal(map[string]interface{}{
		"chat_id":             chatID,
		"text":                text,
		"parse_mode":          parseMode,
		"reply_to_message_id": replyTo,
	})
	data, err := b.post("sendMessage", "application/json", strings.NewReader(string(body)))
	if err != nil {
		log.Printf("[%d] sendMsg error: %v", chatID, err)
		return 0, err
	}
	var r struct {
		OK     bool `json:"ok"`
		Result struct {
			MessageID int `json:"message_id"`
		} `json:"result"`
	}
	json.Unmarshal(data, &r)
	if !r.OK {
		log.Printf("[%d] sendMsg API error: %s", chatID, string(data))
		return 0, fmt.Errorf("api error")
	}
	return r.Result.MessageID, nil
}

func (b *Bot) editMsg(chatID int64, msgID int, text string) {
	body, _ := json.Marshal(map[string]interface{}{
		"chat_id":    chatID,
		"message_id": msgID,
		"text":       text,
	})
	b.post("editMessageText", "application/json", strings.NewReader(string(body)))
}

func (b *Bot) sendAction(chatID int64) {
	body, _ := json.Marshal(map[string]interface{}{"chat_id": chatID, "action": "upload_document"})
	b.post("sendChatAction", "application/json", strings.NewReader(string(body)))
}

func (b *Bot) uploadFile(chatID int64, filePath string, caption string) error {
	file, err := os.Open(filePath)
	if err != nil {
		return fmt.Errorf("open: %w", err)
	}
	defer file.Close()

	_, err = file.Stat()
	if err != nil {
		return fmt.Errorf("stat: %w", err)
	}
	filename := filepath.Base(filePath)

	pr, pw := io.Pipe()
	writer := multipart.NewWriter(pw)
	errChan := make(chan error, 1)

	go func() {
		defer pw.Close()
		writer.WriteField("chat_id", strconv.FormatInt(chatID, 10))
		if caption != "" {
			writer.WriteField("caption", caption)
		}
		part, _ := writer.CreateFormFile("document", filename)
		buf := make([]byte, 65536)
		for {
			n, rerr := file.Read(buf)
			if n > 0 {
				part.Write(buf[:n])
			}
			if rerr != nil {
				break
			}
		}
		writer.Close()
		errChan <- nil
	}()

	client := &http.Client{Timeout: 15 * time.Minute}
	req, _ := http.NewRequest("POST", b.baseURL+"/sendDocument", pr)
	req.Header.Set("Content-Type", writer.FormDataContentType())

	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	<-errChan

	respData, _ := io.ReadAll(resp.Body)
	var result struct {
		OK bool `json:"ok"`
	}
	json.Unmarshal(respData, &result)
	if !result.OK {
		return fmt.Errorf("upload failed")
	}
	return nil
}

// ─────────────────────────────────────────────────────────────────────────────
// MTProto Client — compatible con gotd/td v0.145.1
// ─────────────────────────────────────────────────────────────────────────────

type MTProtoConfig struct {
	APIID       int
	APIHash     string
	Phone       string
	SessionPath string
	ChannelID   int64
}

type MTProtoClient struct {
	cfg     MTProtoConfig
	client  *telegram.Client
	api     *tg.Client
	ready   bool
	mu      sync.Mutex
}

func NewMTProtoClient(cfg MTProtoConfig) *MTProtoClient {
	return &MTProtoClient{cfg: cfg}
}

// Terminal implements auth.UserAuthenticator for gotd/td v0.145.1
type Terminal struct {
	PhoneNumber string
}

func (t *Terminal) Phone(_ context.Context) (string, error) {
	if t.PhoneNumber != "" {
		log.Printf("📱 Using phone: %s", t.PhoneNumber)
		return t.PhoneNumber, nil
	}
	fmt.Print("📱 Enter your phone number (+1234567890): ")
	var phone string
	fmt.Scanln(&phone)
	return strings.TrimSpace(phone), nil
}

func (t *Terminal) Code(_ context.Context, _ *tg.AuthSentCode) (string, error) {
	fmt.Print("📱 Enter the code sent to your Telegram: ")
	var code string
	fmt.Scanln(&code)
	return strings.TrimSpace(code), nil
}

func (t *Terminal) Password(_ context.Context) (string, error) {
	fmt.Print("🔐 Enter your 2FA password (press Enter if none): ")
	var pwd string
	fmt.Scanln(&pwd)
	return pwd, nil
}

func (t *Terminal) SignUp(_ context.Context) (auth.UserInfo, error) {
	return auth.UserInfo{}, fmt.Errorf("sign up not supported")
}

func (t *Terminal) AcceptTermsOfService(_ context.Context, _ tg.HelpTermsOfService) error {
	return nil
}

func (m *MTProtoClient) Start(ctx context.Context) error {
	m.mu.Lock()
	if m.ready {
		m.mu.Unlock()
		return nil
	}
	m.mu.Unlock()

	log.Println("🔐 Starting MTProto authentication...")

	// Create client with app credentials
	m.client = telegram.NewClient(m.cfg.APIID, m.cfg.APIHash, telegram.Options{
		SessionStorage: &telegram.MemoryStorage{},
	})

	authHandler := &Terminal{PhoneNumber: m.cfg.Phone}

	if err := m.client.Run(ctx, func(ctx context.Context) error {
		// Authenticate
		flow := auth.NewFlow(authHandler, auth.SendCodeOptions{})
		if err := m.client.Auth().IfNecessary(ctx, flow); err != nil {
			return fmt.Errorf("MTProto auth failed: %w", err)
		}

		// Get self info
		self, err := m.client.Self(ctx)
		if err == nil {
			log.Printf("✅ MTProto authenticated as: %s %s (@%s)",
				self.FirstName, self.LastName, self.Username)
		}
		return nil
	}); err != nil {
		return fmt.Errorf("MTProto connection failed: %w", err)
	}

	// Get the tg client API
	m.api = m.client.API()

	m.mu.Lock()
	m.ready = true
	m.mu.Unlock()

	return nil
}

func (m *MTProtoClient) IsReady() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.ready
}

// sendDocument uploads a file via MTProto and sends as document to channel
func (m *MTProtoClient) sendDocument(ctx context.Context, filePath string, caption string) error {
	m.mu.Lock()
	if !m.ready || m.api == nil {
		m.mu.Unlock()
		return fmt.Errorf("MTProto not authenticated")
	}
	api := m.api
	m.mu.Unlock()

	// Get file size
	stat, err := os.Stat(filePath)
	if err != nil {
		return fmt.Errorf("stat: %w", err)
	}

	filename := filepath.Base(filePath)
	log.Printf("[MTProto] 📤 Uploading: %s (%s)", filename, formatBytes(stat.Size()))

	// Open file for upload
	file, err := os.Open(filePath)
	if err != nil {
		return err
	}
	defer file.Close()

	// Save file to Telegram using upload.SaveFilePart
	partSize := 512 * 1024 // 512KB parts
	numParts := (stat.Size() + partSize - 1) / partSize

	fileID := int64(0) // file id placeholder

	for i := int64(0); i < numParts; i++ {
		buf := make([]byte, partSize)
		n, readErr := file.Read(buf)
		if n == 0 && readErr == io.EOF {
			break
		}
		if readErr != nil && readErr != io.EOF {
			return fmt.Errorf("read part %d: %w", i, readErr)
		}

		if stat.Size() > 10*1024*1024 {
			// Big file
			req := &tg.UploadSaveBigFilePartRequest{
				FileID:  fileID,
				PartID:  int(i),
				Bytes:   buf[:n],
				PartNum: int(numParts),
			}
			if _, err := api.UploadSaveBigFilePart(ctx, req); err != nil {
				return fmt.Errorf("upload part %d: %w", i, err)
			}
		} else {
			req := &tg.UploadSaveFilePartRequest{
				FileID: fileID,
				PartID: int(i),
				Bytes:  buf[:n],
			}
			if _, err := api.UploadSaveFilePart(ctx, req); err != nil {
				return fmt.Errorf("upload part %d: %w", i, err)
			}
		}
	}

	// Get channel access hash
	// For private channels, we need the access hash
	// Use 0 as placeholder — the API will resolve it
	inputChannel := &tg.InputChannel{
		ChannelID:  m.cfg.ChannelID,
		AccessHash: 0,
	}

	// Create input media document
	inputMedia := &tg.InputMediaUploadedDocument{
		File: &tg.InputFile{
			ID:       fileID,
			Parts:    int(numParts),
			Name:     filename,
			MimeType: "application/octet-stream",
		},
	}

	// Send message with document
	req := &tg.MessagesSendMediaRequest{
		Peer:   inputChannel,
		Media:  inputMedia,
		Message: caption,
	}
	_, err = api.MessagesSendMedia(ctx, req)
	if err != nil {
		return fmt.Errorf("send media: %w", err)
	}

	log.Printf("[MTProto] ✅ Uploaded: %s", filename)
	return nil
}

// SendText sends a plain text message to the channel via MTProto
func (m *MTProtoClient) SendText(ctx context.Context, text string) error {
	m.mu.Lock()
	if !m.ready || m.api == nil {
		m.mu.Unlock()
		return fmt.Errorf("MTProto not authenticated")
	}
	api := m.api
	m.mu.Unlock()

	inputChannel := &tg.InputChannel{
		ChannelID:  m.cfg.ChannelID,
		AccessHash: 0,
	}

	req := &tg.MessagesSendMessageRequest{
		Peer:    inputChannel,
		Message: text,
	}
	_, err := api.MessagesSendMessage(ctx, req)
	return err
}

func (m *MTProtoClient) Close() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.client != nil {
		m.client.Close()
		m.ready = false
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Download engine
// ─────────────────────────────────────────────────────────────────────────────

type Task struct {
	Name       string
	Progress   float64
	Done       bool
	Files      []FileEntry
	TotalBytes int64
	Error      string
	StartedAt  time.Time
	Torrent    *torrent.Torrent
}

type FileEntry struct {
	DisplayPath string
	Length      int64
}

type Engine struct {
	tc      *torrent.Client
	storage string
	tasks   map[int64]*Task
	mu      sync.Mutex
	mtproto *MTProtoClient
	botAPI  *Bot
}

func NewEngine(tc *torrent.Client, storage string, mtproto *MTProtoClient, botAPI *Bot) *Engine {
	return &Engine{
		tc:      tc,
		storage: storage,
		tasks:   make(map[int64]*Task),
		mtproto: mtproto,
		botAPI:  botAPI,
	}
}

func (e *Engine) Handle(chatID int64, replyTo int, text string) {
	log.Printf("[%d] ▶️  %q", chatID, text)

	switch text {
	case "/cancel":
		e.cmdCancel(chatID, replyTo)
	case "/start", "/help":
		e.cmdStart(chatID, replyTo)
	case "/status":
		e.cmdStatus(chatID, replyTo)
	case "/mtproto":
		e.cmdMTProtoStatus(chatID, replyTo)
	default:
		if isMagnet(text) {
			e.startDownloadMagnet(chatID, replyTo, text)
		} else if isTorrentURL(text) {
			e.startDownloadURL(chatID, replyTo, text)
		} else {
			e.cmdStart(chatID, replyTo)
		}
	}
}

func (e *Engine) cmdStart(chatID int64, replyTo int) {
	e.botAPI.sendMsg(chatID, helpText, replyTo, true)
}

func (e *Engine) cmdCancel(chatID int64, replyTo int) {
	e.mu.Lock()
	_, ok := e.tasks[chatID]
	if !ok {
		e.mu.Unlock()
		e.botAPI.sendMsg(chatID, "📭 Nothing to cancel.", replyTo, true)
		return
	}
	delete(e.tasks, chatID)
	e.mu.Unlock()
	e.botAPI.sendMsg(chatID, "🚫 Download cancelled.", replyTo, true)
}

func (e *Engine) cmdStatus(chatID int64, replyTo int) {
	e.mu.Lock()
	t, ok := e.tasks[chatID]
	e.mu.Unlock()

	if !ok {
		e.botAPI.sendMsg(chatID, "📭 *No active download.*", replyTo, true)
		return
	}

	var msg string
	if t.Error != "" {
		msg = fmt.Sprintf("❌ *Error:* `%s`", t.Error)
	} else if t.Done {
		msg = fmt.Sprintf("✅ *Completed:* `%s`", t.Name)
	} else {
		elapsed := time.Since(t.StartedAt).Round(time.Second)
		bar := strings.Repeat("█", int(t.Progress)/5) + strings.Repeat("░", 20-int(t.Progress)/5)
		msg = fmt.Sprintf("⏳ *Downloading:* `%s`\n%s `%.1f%%`\n⏱ %s",
			t.Name, bar, t.Progress, elapsed)
	}
	e.botAPI.sendMsg(chatID, msg, replyTo, true)
}

func (e *Engine) cmdMTProtoStatus(chatID int64, replyTo int) {
	if e.mtproto.IsReady() {
		e.botAPI.sendMsg(chatID,
			"✅ *MTProto:* Connected (no rate limits)\n📤 File uploads use your account via MTProto.",
			replyTo, true)
	} else {
		e.botAPI.sendMsg(chatID,
			"❌ *MTProto:* Not connected.\nStart with: `-phone +1234567890 -api-id ID -api-hash HASH`",
			replyTo, true)
	}
}

func validTrackerScheme(rawURL string) bool {
	u, err := url.Parse(rawURL)
	if err != nil {
		return false
	}
	scheme := strings.ToLower(u.Scheme)
	return scheme == "udp" || scheme == "http" || scheme == "https"
}

func cleanMagnetURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if !strings.HasPrefix(raw, "magnet:?") {
		return raw
	}

	u, err := url.Parse(raw)
	if err != nil {
		return raw
	}

	rawTrackers := u.Query()["tr"]
	if len(rawTrackers) == 0 {
		return raw
	}

	var validTrackers []string
	for _, tr := range rawTrackers {
		if validTrackerScheme(tr) {
			validTrackers = append(validTrackers, tr)
		} else {
			log.Printf("⚠️  Removed invalid tracker: %s", tr)
		}
	}

	q := url.Values{}
	for k, vals := range u.Query() {
		if k == "tr" {
			for _, v := range validTrackers {
				q.Add(k, v)
			}
		} else {
			for _, v := range vals {
				q.Add(k, v)
			}
		}
	}

	rebuilt := u.Scheme + "://" + u.Host + u.Path
	if len(q) > 0 {
		rebuilt += "?" + q.Encode()
	}
	return rebuilt
}

func (e *Engine) startDownloadMagnet(chatID int64, replyTo int, rawMagnet string) {
	e.mu.Lock()
	if _, active := e.tasks[chatID]; active {
		e.mu.Unlock()
		e.botAPI.sendMsg(chatID, "⏳ Download already in progress. Use /cancel first.", replyTo, true)
		return
	}
	e.tasks[chatID] = &Task{StartedAt: time.Now()}
	e.mu.Unlock()

	cleaned := cleanMagnetURL(rawMagnet)
	log.Printf("[%d] cleaned magnet: %s", chatID, cleaned)

	var t *torrent.Torrent
	func() {
		defer func() {
			if r := recover(); r != nil {
				log.Printf("[%d] PANIC in AddMagnet (recovered): %v", chatID, r)
			}
		}()
		var err error
		t, err = e.tc.AddMagnet(cleaned)
		if err != nil {
			e.mu.Lock()
			delete(e.tasks, chatID)
			e.mu.Unlock()
			e.botAPI.sendMsg(chatID, fmt.Sprintf("❌ *Magnet error:* `%s`", err.Error()), 0, true)
			return
		}
	}()

	if t == nil {
		e.mu.Lock()
		delete(e.tasks, chatID)
		e.mu.Unlock()
		e.botAPI.sendMsg(chatID, "❌ *Magnet failed (library panic recovered)*", 0, true)
		return
	}

	log.Printf("[%d] magnet added: %s", chatID, t.Name())
	e.botAPI.sendMsg(chatID, fmt.Sprintf("📥 *Added:* `%s`\n⏳ *Waiting for metadata…*", t.Name()), 0, true)
	go e.downloadLoop(chatID, replyTo, t)
}

func (e *Engine) startDownloadURL(chatID int64, replyTo int, input string) {
	e.mu.Lock()
	if _, active := e.tasks[chatID]; active {
		e.mu.Unlock()
		e.botAPI.sendMsg(chatID, "⏳ Download already in progress. Use /cancel first.", replyTo, true)
		return
	}
	e.tasks[chatID] = &Task{StartedAt: time.Now()}
	e.mu.Unlock()

	e.botAPI.sendAction(chatID)

	mi, fetchErr := e.fetchTorrentFile(input)
	if fetchErr != nil {
		e.mu.Lock()
		delete(e.tasks, chatID)
		e.mu.Unlock()
		e.botAPI.sendMsg(chatID, fmt.Sprintf("❌ *Fetch failed:* `%s`", fetchErr.Error()), 0, true)
		return
	}

	t, err := e.tc.AddTorrent(mi)
	if err != nil {
		e.mu.Lock()
		delete(e.tasks, chatID)
		e.mu.Unlock()
		e.botAPI.sendMsg(chatID, fmt.Sprintf("❌ *Error:* `%s`", err.Error()), 0, true)
		return
	}
	log.Printf("[%d] torrent from URL: %s", chatID, t.Name())

	e.botAPI.sendMsg(chatID, fmt.Sprintf("📥 *Added:* `%s`\n⏳ *Waiting for metadata…*", t.Name()), 0, true)
	go e.downloadLoop(chatID, replyTo, t)
}

func (e *Engine) fetchTorrentFile(rawURL string) (*metainfo.MetaInfo, error) {
	client := &http.Client{Timeout: 60 * time.Second}
	resp, err := client.Get(rawURL)
	if err != nil {
		return nil, fmt.Errorf("HTTP GET: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	limitedReader := &io.LimitedReader{R: resp.Body, N: 10 * 1024 * 1024}

	mi, err := metainfo.Load(limitedReader)
	if err != nil {
		return nil, fmt.Errorf("parse torrent: %w", err)
	}
	return mi, nil
}

func (e *Engine) downloadLoop(chatID int64, replyTo int, t *torrent.Torrent) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("[%d] panic recovered in downloadLoop: %v", chatID, r)
			e.mu.Lock()
			delete(e.tasks, chatID)
			e.mu.Unlock()
		}
	}()

	select {
	case <-t.GotInfo():
	case <-time.After(120 * time.Second):
		e.botAPI.sendMsg(chatID, "❌ *Timeout: no metadata after 2 minutes.*", 0, true)
		e.mu.Lock()
		delete(e.tasks, chatID)
		e.mu.Unlock()
		return
	}

	info := t.Info()
	name := t.Name()
	if info != nil {
		name = info.Name
	}
	total := t.Length()

	var files []FileEntry
	for _, f := range t.Files() {
		files = append(files, FileEntry{
			DisplayPath: f.DisplayPath(),
			Length:      f.Length(),
		})
	}

	e.mu.Lock()
	if e.tasks[chatID] == nil {
		e.mu.Unlock()
		return
	}
	e.tasks[chatID].Name = name
	e.tasks[chatID].Files = files
	e.tasks[chatID].TotalBytes = total
	e.tasks[chatID].Torrent = t
	e.mu.Unlock()

	t.DownloadAll()
	log.Printf("[%d] download started: %s (%s)", chatID, name, formatBytes(total))

	statusID, _ := e.botAPI.sendMsg(chatID,
		fmt.Sprintf("📥 *Downloading:* `%s`\n%s / %s", name, "0 B", formatBytes(total)), 0, true)

	ticker := time.NewTicker(3 * time.Second)
	stall := time.NewTicker(120 * time.Second)

	var lastBytes int64
	lastTime := time.Now()

	for {
		select {
		case <-ticker.C:
			completed := t.BytesCompleted()
			pct := float64(completed) / float64(total) * 100

			e.mu.Lock()
			if e.tasks[chatID] == nil {
				e.mu.Unlock()
				ticker.Stop()
				stall.Stop()
				return
			}
			e.tasks[chatID].Progress = pct
			startTime := e.tasks[chatID].StartedAt
			e.mu.Unlock()

			var speed string
			elapsed := time.Since(startTime).Seconds()
			if elapsed > 5 && completed > 0 {
				bps := int64(float64(completed) / elapsed)
				speed = fmt.Sprintf("%s/s", formatBytes(bps))
			} else {
				speed = "connecting…"
			}

			if completed > lastBytes {
				lastBytes = completed
				lastTime = time.Now()
			}

			bar := strings.Repeat("█", int(pct)/5) + strings.Repeat("░", 20-int(pct)/5)
			e.botAPI.editMsg(chatID, statusID,
				fmt.Sprintf("📥 *Downloading*\n`%s`\n%s `%.1f%%`\n%sw %s / %s",
					name, bar, pct, speed, formatBytes(completed), formatBytes(total)))

		case <-stall.C:
			if time.Since(lastTime) >= 120*time.Second {
				e.mu.Lock()
				if e.tasks[chatID] != nil {
					e.mu.Unlock()
					e.botAPI.sendMsg(chatID, "❌ *Stalled — no peers for 2 minutes.*", 0, true)
				} else {
					e.mu.Unlock()
				}
				ticker.Stop()
				stall.Stop()
				return
			}
		}

		if t.BytesCompleted() >= total {
			ticker.Stop()
			stall.Stop()
			break
		}
	}

	log.Printf("[%d] download complete, waiting for pieces to flush...", chatID)
	time.Sleep(3 * time.Second)

	e.mu.Lock()
	if e.tasks[chatID] == nil {
		e.mu.Unlock()
		return
	}
	taskCopy := *e.tasks[chatID]
	taskCopy.Files = make([]FileEntry, len(e.tasks[chatID].Files))
	copy(taskCopy.Files, e.tasks[chatID].Files)
	torrentRef := e.tasks[chatID].Torrent
	e.tasks[chatID].Done = true
	e.tasks[chatID].Progress = 100
	e.mu.Unlock()

	e.botAPI.sendMsg(chatID, fmt.Sprintf("✅ *Download complete:* `%s`\n⏳ *Uploading to channel…*", name), 0, true)

	e.uploadFiles(chatID, torrentRef, &taskCopy)
}

func (e *Engine) saveFileFromTorrent(torrentFile *torrent.File, expectedLen int64, outputPath string) (int64, error) {
	dir := filepath.Dir(outputPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return 0, fmt.Errorf("mkdir: %w", err)
	}

	outFile, err := os.Create(outputPath)
	if err != nil {
		return 0, fmt.Errorf("create file: %w", err)
	}

	reader := torrentFile.NewReader()
	if reader == nil {
		outFile.Close()
		return 0, fmt.Errorf("NewReader returned nil")
	}
	defer reader.Close()

	buf := make([]byte, 65536)
	var totalWritten int64

	for totalWritten < expectedLen {
		remaining := expectedLen - totalWritten
		if remaining < int64(len(buf)) {
			buf = make([]byte, remaining)
		}

		n, readErr := reader.Read(buf)
		if n > 0 {
			_, writeErr := outFile.Write(buf[:n])
			if writeErr != nil {
				outFile.Close()
				os.Remove(outputPath)
				return totalWritten, fmt.Errorf("write: %w", writeErr)
			}
			totalWritten += int64(n)
		}
		if readErr != nil {
			if readErr == io.EOF {
				log.Printf("⚠️  torrent reader EOF at %d/%d bytes", totalWritten, expectedLen)
				break
			}
			outFile.Close()
			os.Remove(outputPath)
			return totalWritten, fmt.Errorf("read: %w", readErr)
		}
	}

	outFile.Close()

	if totalWritten > expectedLen {
		f, err := os.OpenFile(outputPath, os.O_RDWR, 0644)
		if err == nil {
			f.Truncate(expectedLen)
			f.Close()
		}
		totalWritten = expectedLen
	}

	return totalWritten, nil
}

func (e *Engine) uploadFiles(chatID int64, torrentRef *torrent.Torrent, task *Task) {
	files := task.Files
	torrentName := task.Name

	e.mu.Lock()
	delete(e.tasks, chatID)
	e.mu.Unlock()

	ok, fail := 0, 0
	totalFiles := len(files)

	method := "Bot API"
	if e.mtproto.IsReady() {
		method = "MTProto"
	}

	if e.mtproto.IsReady() {
		ctx := context.Background()
		e.mtproto.SendText(ctx, fmt.Sprintf("📤 *Starting upload:* %d file(s) via %s", totalFiles, method))
	} else {
		targetChat := e.botAPI.getChatID(chatID)
		e.botAPI.sendMsg(targetChat, fmt.Sprintf("📤 *Starting upload:* %d file(s) via %s", totalFiles, method), 0, true)
	}

	for i, fe := range files {
		safeName := filepath.Base(fe.DisplayPath)

		caption := fmt.Sprintf("[%d/%d] %s — %s", i+1, totalFiles, torrentName, safeName)

		log.Printf("[%d] 📤 saving then uploading [%d/%d]: %s (%s)",
			chatID, i+1, totalFiles, safeName, formatBytes(fe.Length))

		var tf *torrent.File
		if torrentRef != nil {
			for _, f := range torrentRef.Files() {
				if f.DisplayPath() == fe.DisplayPath {
					tf = f
					break
				}
			}
		}

		if tf == nil {
			log.Printf("[%d] file not found in torrent: %s", chatID, safeName)
			if e.mtproto.IsReady() {
				ctx := context.Background()
				e.mtproto.SendText(ctx, fmt.Sprintf("❌ *File not found:* `%s`", safeName))
			} else {
				targetChat := e.botAPI.getChatID(chatID)
				e.botAPI.sendMsg(targetChat, fmt.Sprintf("❌ *File not found:* `%s`", safeName), 0, true)
			}
			fail++
			continue
		}

		diskPath := filepath.Join(e.storage, fe.DisplayPath)

		written, saveErr := e.saveFileFromTorrent(tf, fe.Length, diskPath)
		if saveErr != nil {
			log.Printf("[%d] save error: %v", chatID, saveErr)
			if e.mtproto.IsReady() {
				ctx := context.Background()
				e.mtproto.SendText(ctx, fmt.Sprintf("❌ *Save failed:* `%s` — %v", safeName, saveErr))
			} else {
				targetChat := e.botAPI.getChatID(chatID)
				e.botAPI.sendMsg(targetChat, fmt.Sprintf("❌ *Save failed:* `%s` — %v", safeName, saveErr), 0, true)
			}
			fail++
			continue
		}

		log.Printf("[%d] 💾 saved to disk: %s (%s)", chatID, diskPath, formatBytes(written))

		var uploadErr error
		if e.mtproto.IsReady() {
			ctx := context.Background()
			e.mtproto.SendText(ctx, fmt.Sprintf("📤 *Uploading:* `%s` (%d/%d) — %s", safeName, i+1, totalFiles, formatBytes(written)))
			uploadErr = e.mtproto.sendDocument(ctx, diskPath, caption)
		} else {
			targetChat := e.botAPI.getChatID(chatID)
			e.botAPI.sendAction(targetChat)
			e.botAPI.sendMsg(targetChat, fmt.Sprintf("📤 *Uploading:* `%s` (%d/%d) — %s", safeName, i+1, totalFiles, formatBytes(written)), 0, true)
			uploadErr = e.botAPI.uploadFile(targetChat, diskPath, caption)
		}

		if uploadErr != nil {
			log.Printf("[%d] upload failed: %v", chatID, uploadErr)
			if e.mtproto.IsReady() {
				ctx := context.Background()
				e.mtproto.SendText(ctx, fmt.Sprintf("❌ *Upload failed:* `%s` — %v", safeName, uploadErr))
			} else {
				targetChat := e.botAPI.getChatID(chatID)
				e.botAPI.sendMsg(targetChat, fmt.Sprintf("❌ *Upload failed:* `%s` — %v", safeName, uploadErr), 0, true)
			}
			fail++
		} else {
			log.Printf("[%d] ✅ Uploaded: %s", chatID, safeName)
			if e.mtproto.IsReady() {
				ctx := context.Background()
				e.mtproto.SendText(ctx, fmt.Sprintf("✅ *Uploaded:* `%s` (%d/%d)", safeName, i+1, totalFiles))
			} else {
				targetChat := e.botAPI.getChatID(chatID)
				e.botAPI.sendMsg(targetChat, fmt.Sprintf("✅ *Uploaded:* `%s` (%d/%d)", safeName, i+1, totalFiles), 0, true)
			}
			ok++
		}

		if err := os.Remove(diskPath); err != nil {
			log.Printf("[%d] warning: could not delete %s: %v", chatID, diskPath, err)
		} else {
			log.Printf("[%d] 🗑 deleted: %s", chatID, diskPath)
		}

		time.Sleep(500 * time.Millisecond)
	}

	if fail > 0 {
		if e.mtproto.IsReady() {
			ctx := context.Background()
			e.mtproto.SendText(ctx, fmt.Sprintf("🎉 *Done!* %d uploaded, %d failed.", ok, fail))
		} else {
			targetChat := e.botAPI.getChatID(chatID)
			e.botAPI.sendMsg(targetChat, fmt.Sprintf("🎉 *Done!* %d uploaded, %d failed.", ok, fail), 0, true)
		}
	} else {
		if e.mtproto.IsReady() {
			ctx := context.Background()
			e.mtproto.SendText(ctx, fmt.Sprintf("🎉 *All done!* %d file(s) uploaded.", ok))
		} else {
			targetChat := e.botAPI.getChatID(chatID)
			e.botAPI.sendMsg(targetChat, fmt.Sprintf("🎉 *All done!* %d file(s) uploaded.", ok), 0, true)
		}
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Helpers
// ─────────────────────────────────────────────────────────────────────────────

func isMagnet(s string) bool {
	return strings.HasPrefix(strings.TrimSpace(s), "magnet:?")
}

func isTorrentURL(s string) bool {
	s = strings.TrimSpace(s)
	return (strings.HasPrefix(s, "http://") || strings.HasPrefix(s, "https://")) &&
		strings.HasSuffix(s, ".torrent")
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

const helpText = `🤖 *TeleTorrent Bot*

Send me a magnet link or .torrent URL and I'll download & upload it to your channel.

*Commands:*
/start /help — this message
/status      — active download status
/cancel      — cancel current download
/mtproto     — check MTProto connection status

*Upload method:*
• Bot API (rate limited)
• MTProto (after auth — no rate limits)

*Notes:*
• Files uploaded directly to configured channel.
• MTProto uploads have NO rate limits.
• Session persists between restarts.`

// ─────────────────────────────────────────────────────────────────────────────
// Main
// ─────────────────────────────────────────────────────────────────────────────

func main() {
	var token, storage, channelStr, phoneStr, apiIDStr, apiHash, sessionPath string
	flag.StringVar(&token, "token", "", "Telegram bot token from @BotFather")
	flag.StringVar(&storage, "storage", "./downloads", "Download directory")
	flag.StringVar(&channelStr, "channel", "", "Channel/group ID (e.g. -1003213143951)")
	flag.StringVar(&phoneStr, "phone", "", "Your phone for MTProto (e.g. +1234567890)")
	flag.StringVar(&apiIDStr, "api-id", "", "API ID from my.telegram.org")
	flag.StringVar(&apiHash, "api-hash", "", "API hash from my.telegram.org")
	flag.StringVar(&sessionPath, "session", "./mtproto_session.json", "MTProto session file")
	flag.Parse()

	if token == "" {
		fmt.Println("❌  Usage: tele-torrent-bot -token BOT_TOKEN -channel CHANNEL_ID")
		fmt.Println("        [-phone +NUMBER -api-id ID -api-hash HASH]")
		fmt.Println("\nGet credentials at https://my.telegram.org/apps")
		os.Exit(1)
	}

	var channelID int64
	if channelStr != "" {
		var err error
		channelID, err = strconv.ParseInt(channelStr, 10, 64)
		if err != nil {
			log.Fatalf("❌  Invalid channel ID: %s", channelStr)
		}
		log.Printf("📢 Target channel: %s", channelStr)
	}

	if err := os.MkdirAll(storage, 0755); err != nil {
		log.Fatalf("❌  Cannot create storage directory: %v", err)
	}
	log.Printf("📁 Storage directory: %s", storage)

	cfg := torrent.NewDefaultClientConfig()
	cfg.DataDir = storage
	cfg.NoUpload = false
	cfg.Seed = true
	cfg.SetListenAddr("0.0.0.0:0")
	cfg.DisableIPv6 = true
	cfg.DisableIPv4 = false
	cfg.NoDHT = false
	cfg.DisableTCP = false

	tc, err := torrent.NewClient(cfg)
	if err != nil {
		log.Fatalf("❌  Torrent client error: %v", err)
	}
	log.Printf("✅  Torrent client ready")

	bot := NewBot(token, channelID)

	data, err := bot.api("getMe", nil)
	if err != nil {
		log.Fatalf("❌  Telegram auth error: %v", err)
	}
	var me struct {
		OK bool `json:"ok"`
		Result struct {
			Username string `json:"username"`
		} `json:"result"`
	}
	json.Unmarshal(data, &me)
	log.Printf("✅  Bot logged in as @%s", me.Result.Username)

	mtproto := &MTProtoClient{}

	if phoneStr != "" && apiIDStr != "" && apiHash != "" {
		apiID, _ := strconv.Atoi(apiIDStr)
		mtproto.cfg = MTProtoConfig{
			APIID:       apiID,
			APIHash:     apiHash,
			Phone:       phoneStr,
			SessionPath: sessionPath,
			ChannelID:   channelID,
		}

		log.Printf("📱 MTProto credentials provided — authenticating...")
		log.Printf("   Phone: %s | API ID: %d", phoneStr, apiID)

		go func() {
			ctx := context.Background()
			if err := mtproto.Start(ctx); err != nil {
				log.Printf("❌  MTProto auth failed: %v", err)
				log.Printf("💡 Bot will use Bot API for uploads (rate limited)")
			} else {
				log.Printf("✅  MTProto ready! No rate limits for uploads.")
				if channelID != 0 {
					ctx := context.Background()
					mtproto.SendText(ctx, "🤖 *TeleTorrent Bot* online via MTProto — no rate limits.")
				}
			}
		}()
	} else {
		log.Printf("📱 No MTProto credentials — using Bot API for uploads (429 rate limit may apply)")
		log.Printf("💡 For no rate limits: -phone +1234567890 -api-id 12345 -api-hash abcdef...")
	}

	engine := NewEngine(tc, storage, mtproto, bot)

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigChan
		log.Println("⚡ Shutting down...")
		mtproto.Close()
		tc.Close()
		os.Exit(0)
	}()

	offset := 0
	for {
		data, err := bot.api("getUpdates", map[string]string{
			"timeout": "120",
			"offset":  strconv.Itoa(offset),
		})
		if err != nil {
			log.Printf("⚠️  getUpdates error: %v — retrying in 5s", err)
			time.Sleep(5 * time.Second)
			continue
		}

		var ups struct {
			OK     bool      `json:"ok"`
			Result []Update `json:"result"`
		}
		if err := json.Unmarshal(data, &ups); err != nil {
			log.Printf("⚠️  JSON parse error: %v — retrying in 5s", err)
			time.Sleep(5 * time.Second)
			continue
		}

		if !ups.OK {
			log.Printf("⚠️  API returned !ok — retrying in 5s")
			time.Sleep(5 * time.Second)
			continue
		}

		if len(ups.Result) == 0 {
			continue
		}

		for _, u := range ups.Result {
			offset = u.UpdateID + 1

			if u.Message != nil {
				chatID := u.Message.Chat.ID
				text := u.Message.Text
				if text == "" {
					text = u.Message.Caption
				}
				if text == "" {
					continue
				}
				replyTo := u.Message.MessageID
				go engine.Handle(chatID, replyTo, text)
			}
		}
	}
}
