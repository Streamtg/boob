package main

import (
	"crypto/md5"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"
)

type CacheEntry struct {
	Caption     string `json:"caption"`
	FilePath    string `json:"file_path"`
	FileMD5     string `json:"file_md5"`
	TgFileID    string `json:"tg_file_id"`
	TgFileSize  int    `json:"tg_file_size"`
	DateCreate  string `json:"date_create"`
	TorrentName string `json:"torrent_name"`
}

type FileCache struct {
	mu       sync.RWMutex
	entries  []CacheEntry
	filePath string
}

func NewFileCache(storagePath string) *FileCache {
	fc := &FileCache{
		filePath: filepath.Join(storagePath, "cache.json"),
	}
	fc.load()
	return fc
}

func (fc *FileCache) load() {
	data, err := os.ReadFile(fc.filePath)
	if err != nil {
		if os.IsNotExist(err) {
			fc.entries = []CacheEntry{}
			return
		}
		log.Printf("cache load error: %v", err)
		fc.entries = []CacheEntry{}
		return
	}
	if err := json.Unmarshal(data, &fc.entries); err != nil {
		log.Printf("cache unmarshal error: %v", err)
		fc.entries = []CacheEntry{}
	}
}

func (fc *FileCache) save() {
	data, err := json.MarshalIndent(fc.entries, "", "  ")
	if err != nil {
		log.Printf("cache save error: %v", err)
		return
	}
	if err := os.WriteFile(fc.filePath, data, 0644); err != nil {
		log.Printf("cache write error: %v", err)
	}
}

func (fc *FileCache) Add(tgFileID string, tgFileSize int, filePath, caption, torrentName string) {
	// Calculate MD5
	var md5Sum string
	if data, err := os.ReadFile(filePath); err == nil {
		md5Sum = fmt.Sprintf("%x", md5.Sum(data))
	}

	fc.mu.Lock()
	defer fc.mu.Unlock()

	entry := CacheEntry{
		Caption:     caption,
		FilePath:    filePath,
		FileMD5:     md5Sum,
		TgFileID:    tgFileID,
		TgFileSize:  tgFileSize,
		DateCreate:  time.Now().Format(time.RFC3339),
		TorrentName: torrentName,
	}
	fc.entries = append(fc.entries, entry)
	fc.save()
}

func (fc *FileCache) FindByMD5(filePath string) *CacheEntry {
	var md5Sum string
	if data, err := os.ReadFile(filePath); err == nil {
		md5Sum = fmt.Sprintf("%x", md5.Sum(data))
	}
	if md5Sum == "" {
		return nil
	}

	fc.mu.RLock()
	defer fc.mu.RUnlock()

	for i := len(fc.entries) - 1; i >= 0; i-- {
		if fc.entries[i].FileMD5 == md5Sum {
			return &fc.entries[i]
		}
	}
	return nil
}

func (fc *FileCache) FindByTorrentName(name string) []CacheEntry {
	fc.mu.RLock()
	defer fc.mu.RUnlock()

	var results []CacheEntry
	for _, e := range fc.entries {
		if e.TorrentName == name {
			results = append(results, e)
		}
	}
	return results
}
