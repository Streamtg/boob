package main

import (
	"flag"
	"log"
	"strconv"
)

type Config struct {
	Token      string
	ChannelID  int64
	Storage    string
	Port       int
	MaxFileSize int64
}

var appConfig Config

func parseFlags() Config {
	token := flag.String("token", "", "Telegram Bot Token (required)")
	channel := flag.String("channel", "", "Canal ID (opcional)")
	storage := flag.String("storage", "./downloads", "Directorio de descargas")
	port := flag.Int("port", 0, "Puerto DHT (0=aleatorio)")
	maxFileSize := flag.Int("max-file-size", 1990, "Tamano maximo en MB")
	flag.Parse()

	if *token == "" {
		log.Fatal("ERROR: -token es requerido")
	}

	cfg := Config{
		Token:       *token,
		Storage:     *storage,
		Port:        *port,
		MaxFileSize: int64(*maxFileSize),
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
