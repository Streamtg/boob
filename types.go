package main

import (
	"sync"
	"time"

	"github.com/anacrolix/torrent"
	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
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

type QueuedTask struct {
	ChatID  int64
	ReplyTo int
	Input   string
	Bot     *tgbotapi.BotAPI
}

type TaskManager struct {
	mu     sync.RWMutex
	tasks  map[int64]*TaskStatus
	queue  chan QueuedTask
	active map[int64]bool
}

func NewTaskManager() *TaskManager {
	return &TaskManager{
		tasks:  make(map[int64]*TaskStatus),
		queue:  make(chan QueuedTask, 100),
		active: make(map[int64]bool),
	}
}

func (tm *TaskManager) Add(chatID int64, task *TaskStatus) {
	tm.mu.Lock()
	defer tm.mu.Unlock()
	tm.tasks[chatID] = task
	tm.active[chatID] = true
}

func (tm *TaskManager) Get(chatID int64) *TaskStatus {
	tm.mu.RLock()
	defer tm.mu.RUnlock()
	return tm.tasks[chatID]
}

func (tm *TaskManager) Remove(chatID int64) {
	tm.mu.Lock()
	defer tm.mu.Unlock()
	delete(tm.tasks, chatID)
	delete(tm.active, chatID)
}

func (tm *TaskManager) IsActive(chatID int64) bool {
	tm.mu.RLock()
	defer tm.mu.RUnlock()
	return tm.active[chatID]
}

func (tm *TaskManager) TorrentClient() *torrent.Client {
	return nil
}
