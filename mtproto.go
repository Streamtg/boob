package main

import (
	"bufio"
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gotd/td/telegram"
	"github.com/gotd/td/telegram/message"
	"github.com/gotd/td/telegram/uploader"
	"github.com/gotd/td/tg"
)

var (
	apiID   = 34280578
	apiHash = "b77ac49b31b12365b98f2333bd4c3eb0"
)

type MTProtoClient struct {
	apiID    int
	apiHash  string
	client   *telegram.Client
	sender   *message.Sender
	uploader *uploader.Uploader
	authed   bool
	peer     tg.InputPeerClass
	ready    chan struct{}
	ctx      context.Context
	cancel   context.CancelFunc
}

func NewMTProtoClient() *MTProtoClient {
	return &MTProtoClient{
		apiID:   apiID,
		apiHash: apiHash,
		ready:   make(chan struct{}),
	}
}

func (m *MTProtoClient) Start(chatID int64) error {
	ctx, cancel := context.WithCancel(context.Background())
	m.ctx = ctx
	m.cancel = cancel

	sessionDir := "./mtproto_session"
	os.MkdirAll(sessionDir, 0700)

	m.client = telegram.NewClient(m.apiID, m.apiHash, telegram.Options{
		SessionStorage: &sessionStorage{path: filepath.Join(sessionDir, "session.json")},
	})

	go func() {
		err := m.client.Run(ctx, func(runCtx context.Context) error {
			api := m.client.API()

			_, err := m.client.Self(runCtx)
			if err == nil {
				log.Println("MTProto: session found")
				m.authed = true
				m.sender = message.NewSender(api)
				m.uploader = uploader.NewUploader(api)
				peer, _ := m.resolvePeer(runCtx, chatID)
				m.peer = peer
				close(m.ready)
				log.Println("MTProto: ready!")
				<-runCtx.Done()
				return nil
			}

			fmt.Print("\n=== MTProto Authentication ===\n")
			reader := bufio.NewReader(os.Stdin)

			fmt.Print("Enter phone number (e.g., +1234567890): ")
			phone, _ := reader.ReadString('\n')
			phone = strings.TrimSpace(phone)
			if phone == "" {
				return fmt.Errorf("phone number is required")
			}

			sentCode, err := api.AuthSendCode(runCtx, &tg.AuthSendCodeRequest{
				PhoneNumber: phone,
				APIID:       m.apiID,
				APIHash:     m.apiHash,
				Settings:    tg.CodeSettings{},
			})
			if err != nil {
				return fmt.Errorf("send code: %w", err)
			}
			sent, ok := sentCode.(*tg.AuthSentCode)
			if !ok {
				return fmt.Errorf("unexpected: %T", sentCode)
			}

			fmt.Print("Enter verification code (sent to Telegram): ")
			code, _ := reader.ReadString('\n')
			code = strings.TrimSpace(code)
			if code == "" {
				return fmt.Errorf("code is required")
			}

			_, err = api.AuthSignIn(runCtx, &tg.AuthSignInRequest{
				PhoneNumber:   phone,
				PhoneCodeHash: sent.PhoneCodeHash,
				PhoneCode:     code,
			})
			if err != nil {
				if strings.Contains(err.Error(), "SESSION_PASSWORD_NEEDED") {
					fmt.Print("Enter 2FA password: ")
					pwd, _ := reader.ReadString('\n')
					pwd = strings.TrimSpace(pwd)
					if _, err := m.client.Auth().Password(runCtx, pwd); err != nil {
						return fmt.Errorf("password auth failed: %w", err)
					}
				} else {
					return fmt.Errorf("sign in failed: %w", err)
				}
			}

			self, err := m.client.Self(runCtx)
			if err != nil {
				return fmt.Errorf("get self: %w", err)
			}
			log.Printf("MTProto: authenticated as @%s (ID: %d)", self.Username, self.ID)

			m.authed = true
			m.sender = message.NewSender(api)
			m.uploader = uploader.NewUploader(api)
			peer, _ := m.resolvePeer(runCtx, chatID)
			m.peer = peer
			close(m.ready)
			log.Println("MTProto: ready!")
			<-runCtx.Done()
			return nil
		})
		if err != nil {
			log.Printf("MTProto: error: %v", err)
		}
	}()

	return nil
}

func (m *MTProtoClient) WaitReady() bool {
	<-m.ready
	return m.authed
}

func (m *MTProtoClient) resolvePeer(ctx context.Context, chatID int64) (tg.InputPeerClass, error) {
	api := m.client.API()
	// First try: get from dialogs
	dialogs, err := api.MessagesGetDialogs(ctx, &tg.MessagesGetDialogsRequest{Limit: 100})
	if err == nil {
		switch d := dialogs.(type) {
		case *tg.MessagesDialogs:
			for _, c := range d.Chats {
				if ch, ok := c.(*tg.Channel); ok && ch.ID == chatID {
					return &tg.InputPeerChannel{ChannelID: ch.ID, AccessHash: ch.AccessHash}, nil
				}
			}
		case *tg.MessagesDialogsSlice:
			for _, c := range d.Chats {
				if ch, ok := c.(*tg.Channel); ok && ch.ID == chatID {
					return &tg.InputPeerChannel{ChannelID: ch.ID, AccessHash: ch.AccessHash}, nil
				}
			}
		}
	}
	// Second try: ChannelsGetChannels (correct API)
	channels, err2 := api.ChannelsGetChannels(ctx, []tg.InputChannelClass{
		&tg.InputChannel{ChannelID: chatID, AccessHash: 0},
	})
	if err2 == nil {
		if chats, ok := channels.(*tg.MessagesChats); ok && len(chats.Chats) > 0 {
			if ch2, ok := chats.Chats[0].(*tg.Channel); ok {
				return &tg.InputPeerChannel{ChannelID: ch2.ID, AccessHash: ch2.AccessHash}, nil
			}
		}
	}
	return &tg.InputPeerChannel{ChannelID: chatID, AccessHash: 0}, nil
}

func (m *MTProtoClient) SendLargeFile(filePath, fileName string, replyTo int) error {
	if !m.authed {
		return fmt.Errorf("MTProto not authenticated")
	}
	log.Printf("MTProto: uploading %s...", fileName)

	cleanName := strings.NewReplacer(
		"[", "", "]", "", "{", "", "}", "",
		"(", "", ")", "", "|", "-", "\"", "",
	).Replace(fileName)

	upload, err := m.uploader.FromPath(m.ctx, filePath)
	if err != nil {
		return fmt.Errorf("upload: %w", err)
	}
	log.Printf("MTProto: upload complete, sending...")

	doc := &tg.InputMediaUploadedDocument{
		File: upload,
		Attributes: []tg.DocumentAttributeClass{
			&tg.DocumentAttributeFilename{FileName: cleanName},
		},
	}
	req := &tg.MessagesSendMediaRequest{
		Peer:     m.peer,
		Media:    doc,
		RandomID: time.Now().UnixNano(),
		Message:  "",
	}
	if replyTo > 0 {
		req.ReplyTo = &tg.InputReplyToMessage{ReplyToMsgID: replyTo}
	}

	api := m.client.API()
	_, err = api.MessagesSendMedia(m.ctx, req)
	return err
}

func (m *MTProtoClient) IsAuthed() bool { return m.authed }
func (m *MTProtoClient) Close() {
	if m.cancel != nil {
		m.cancel()
	}
}

type sessionStorage struct{ path string }

func (s *sessionStorage) LoadSession(ctx context.Context) ([]byte, error) {
	data, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	return data, nil
}

func (s *sessionStorage) StoreSession(ctx context.Context, data []byte) error {
	return os.WriteFile(s.path, data, 0600)
}
GOEOF
