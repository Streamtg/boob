package main

import (
	"flag"
	"log"
	"os"
	"strconv"
)

type Config struct {
	Token      string
	ChannelID  int64
	Storage    string
	Port       int
	DownloadLimit int64
	MaxFileSize   int64
	MaxTasks      int
}

var appConfig Config

func parseFlags() Config {
	token := flag.String("token", "", "Telegram Bot Token (required)")
	channel := flag.String("channel", "", "Canal ID (opcional)")
	storage := flag.String("storage", "./downloads", "Directorio de descargas")
	port := flag.Int("port", 0, "Puerto DHT")
	downloadLimit := flag.Int("download-limit", 0, "Límite de descarga KB/s (0 = ilimitado)")
	maxFileSize := flag.Int("max-file-size", 1990, "Tamaño máximo de archivo en MB")
	flag.Parse()

	if *token == "" {
		log.Fatal("ERROR: -token es requerido")
	}

	cfg := Config{
		Token:      *token,
		Storage:    *storage,
		Port:       *port,
		DownloadLimit: int64(*downloadLimit),
		MaxFileSize:   int64(*maxFileSize),
		MaxTasks:      1,
	}
	if *channel != "" {
		id, err := strconv.ParseInt(*channel, 10, 64)
		if err != nil {
			log.Fatalf("channel ID invalido: %v", err)
		}
		cfg.ChannelID = id
	}

	appConfig = cfg
	return cfg
}
