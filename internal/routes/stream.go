package routes

import (
	"EverythingSuckz/fsb/internal/bot"
	"EverythingSuckz/fsb/internal/utils"
	"context"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	range_parser "github.com/quantumsheep/range-parser"
	"github.com/gotd/td/tg"
	"go.uber.org/zap"

	"github.com/gin-gonic/gin"
)

var log *zap.Logger

func (e *allRoutes) LoadHome(r *Route) {
	log = e.log.Named("Stream")
	defer log.Info("Loaded stream route")
	r.Engine.GET("/stream/:messageID", getStreamRoute)
}

func getStreamRoute(ctx *gin.Context) {
	// Timeout aumentado a 600 segundos para archivos grandes
	c, cancel := context.WithTimeout(ctx.Request.Context(), 600*time.Second)
	defer cancel()

	w := ctx.Writer
	r := ctx.Request

	messageIDParm := ctx.Param("messageID")
	messageID, err := strconv.Atoi(messageIDParm)
	if err != nil {
		log.Error("Invalid message ID", zap.String("messageID", messageIDParm), zap.Error(err))
		http.Error(w, "Invalid message ID", http.StatusBadRequest)
		return
	}

	authHash := ctx.Query("hash")
	if authHash == "" {
		log.Error("Missing hash param")
		http.Error(w, "Missing hash param", http.StatusBadRequest)
		return
	}

	worker := bot.GetNextWorker()
	file, err := utils.FileFromMessage(c, worker.Client, messageID)
	if err != nil {
		log.Error("Failed to fetch file", zap.Int("messageID", messageID), zap.Error(err))
		http.Error(w, fmt.Sprintf("File not found: %v", err), http.StatusBadRequest)
		return
	}

	// Sincronizar lógica de nombre de archivo por defecto
	if file.FileName == "" {
		file.FileName = generateDefaultFilename(file.MimeType)
	}

	// Validar hash
	expectedHash := utils.PackFile(file.FileName, file.FileSize, file.MimeType, file.ID)
	if !utils.CheckHash(authHash, expectedHash) {
		log.Error("Invalid hash",
			zap.String("received", authHash),
			zap.String("expected", utils.GetShortHash(expectedHash)),
			zap.String("fileName", file.FileName),
			zap.Int64("fileSize", file.FileSize),
			zap.String("mimeType", file.MimeType),
			zap.Int64("fileID", file.ID))
		http.Error(w, "Invalid hash", http.StatusBadRequest)
		return
	}

	// Manejo de fotos (file.FileSize == 0)
	if file.FileSize == 0 {
		res, err := worker.Client.API().UploadGetFile(c, &tg.UploadGetFileRequest{
			Location: file.Location,
			Offset:   0,
			Limit:    1024 * 1024,
		})
		if err != nil {
			log.Error("Failed to get file", zap.Error(err))
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		result, ok := res.(*tg.UploadFile)
		if !ok {
			log.Error("Unexpected response type")
			http.Error(w, "Unexpected response", http.StatusInternalServerError)
			return
		}
		fileBytes := result.GetBytes()
		ctx.Header("Content-Disposition", fmt.Sprintf("inline; filename=\"%s\"", file.FileName))
		if r.Method != "HEAD" {
			ctx.Data(http.StatusOK, file.MimeType, fileBytes)
		}
		return
	}

	ctx.Header("Accept-Ranges", "bytes")
	var start, end int64
	rangeHeader := r.Header.Get("Range")

	if rangeHeader == "" {
		start = 0
		end = file.FileSize - 1
		ctx.Header("Content-Type", file.MimeType)
		ctx.Header("Content-Length", strconv.FormatInt(file.FileSize, 10))
		ctx.Header("Connection", "keep-alive")
		ctx.Status(http.StatusOK)
	} else {
		ranges, err := range_parser.Parse(file.FileSize, rangeHeader)
		if err != nil {
			log.Error("Invalid range header", zap.String("range", rangeHeader), zap.Error(err))
			http.Error(w, err.Error(), http.StatusRequestedRangeNotSatisfiable)
			return
		}
		start = ranges[0].Start
		end = ranges[0].End
		ctx.Header("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, file.FileSize))
		log.Info("Content-Range", zap.Int64("start", start), zap.Int64("end", end), zap.Int64("fileSize", file.FileSize))
		ctx.Header("Content-Type", file.MimeType)
		ctx.Header("Content-Length", strconv.FormatInt(end-start+1, 10))
		ctx.Header("Connection", "keep-alive")
		ctx.Status(http.StatusPartialContent)
	}

	mimeType := file.MimeType
	if mimeType == "" {
		mimeType = "application/octet-stream"
	}

	disposition := "inline"
	if ctx.Query("d") == "true" {
		disposition = "attachment"
	}
	ctx.Header("Content-Disposition", fmt.Sprintf("%s; filename=\"%s\"", disposition, file.FileName))

	if r.Method != "HEAD" {
		lr, err := utils.NewTelegramReader(c, worker.Client, file.Location, start, end, end-start+1)
		if err != nil {
			log.Error("Failed to create Telegram reader", zap.Error(err))
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		defer lr.Close()

		_, err = io.CopyN(w, lr, end-start+1)
		if err != nil {
			if err == io.ErrUnexpectedEOF ||
				strings.Contains(err.Error(), "broken pipe") ||
				strings.Contains(err.Error(), "connection reset by peer") ||
				strings.Contains(err.Error(), "context canceled") ||
				strings.Contains(err.Error(), "context deadline exceeded") {
				log.Warn("Client disconnected or Telegram context issue", zap.Error(err))
				return
			}
			log.Error("Error while copying stream", zap.Error(err))
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}
}

// generateDefaultFilename sincroniza con commands/stream.go
func generateDefaultFilename(mimeType string) string {
	lower := strings.ToLower(mimeType)
	switch {
	case strings.Contains(lower, "image/jpeg"):
		return "photo.jpg"
	case strings.Contains(lower, "image/png"):
		return "photo.png"
	case strings.Contains(lower, "image/gif"):
		return "animation.gif"
	case strings.Contains(lower, "video"):
		return "video.mp4"
	case strings.Contains(lower, "audio"):
		return "audio.mp3"
	case strings.Contains(lower, "pdf"):
		return "document.pdf"
	case strings.Contains(lower, "zip"):
		return "archive.zip"
	case strings.Contains(lower, "rar"):
		return "archive.rar"
	case strings.Contains(lower, "text"):
		return "text.txt"
	case strings.Contains(lower, "application"):
		return "file.bin"
	default:
		return "unknown"
	}
}
