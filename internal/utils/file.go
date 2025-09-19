package utils

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/celestix/gotgproto/ext"
	"github.com/gotd/td/tg"
)

// File representa la metadata de un archivo
type File struct {
	FileName  string
	FileSize  int64
	MimeType  string
	ID        int64
	MessageID int
}

// ConvertAndUploadVideo convierte un video en formato raro a MP4 y lo sube al canal de logs
func ConvertAndUploadVideo(ctx *ext.Context, logChannelID int64, file *File) (*File, error) {
	// Validar que el archivo es un video
	if !strings.Contains(strings.ToLower(file.MimeType), "video") {
		return nil, fmt.Errorf("el archivo no es un video")
	}

	// Crear un directorio temporal para procesar el archivo
	tempDir, err := os.MkdirTemp("", "video_conversion_")
	if err != nil {
		return nil, fmt.Errorf("error al crear directorio temporal: %v", err)
	}
	defer os.RemoveAll(tempDir)

	// Descargar el archivo original desde Telegram
	originalPath := filepath.Join(tempDir, file.FileName)
	err = downloadFileFromTelegram(ctx, file.ID, originalPath)
	if err != nil {
		return nil, fmt.Errorf("error al descargar el archivo: %v", err)
	}

	// Generar nombre para el archivo convertido
	outputFileName := strings.TrimSuffix(file.FileName, filepath.Ext(file.FileName)) + ".mp4"
	outputPath := filepath.Join(tempDir, outputFileName)

	// Ejecutar conversión a MP4 usando HandBrakeCLI
	err = convertToMP4(originalPath, outputPath)
	if err != nil {
		return nil, fmt.Errorf("error al convertir el video a MP4: %v", err)
	}

	// Obtener tamaño del archivo convertido
	fileInfo, err := os.Stat(outputPath)
	if err != nil {
		return nil, fmt.Errorf("error al obtener información del archivo convertido: %v", err)
	}
	convertedFileSize := fileInfo.Size()

	// Subir el archivo convertido al canal de logs
	messageID, fileID, err := uploadFileToChannel(ctx, logChannelID, outputPath)
	if err != nil {
		return nil, fmt.Errorf("error al subir el archivo convertido: %v", err)
	}

	// Crear nueva estructura File con la metadata actualizada
	convertedFile := &File{
		FileName:  outputFileName,
		FileSize:  convertedFileSize,
		MimeType:  "video/mp4",
		ID:        fileID,
		MessageID: messageID,
	}

	return convertedFile, nil
}

// downloadFileFromTelegram descarga un archivo desde Telegram usando su ID
func downloadFileFromTelegram(ctx *ext.Context, fileID int64, outputPath string) error {
	fileRequest := &tg.InputDocumentFileLocation{
		ID: fileID,
	}
	file, err := ctx.Raw.UploadGetFile(&tg.UploadGetFileRequest{
		Location: fileRequest,
	})
	if err != nil {
		return fmt.Errorf("error al obtener archivo de Telegram: %v", err)
	}

	// Guardar el archivo en el disco
	err = os.WriteFile(outputPath, file.Bytes, 0644)
	if err != nil {
		return fmt.Errorf("error al guardar archivo: %v", err)
	}
	return nil
}

// convertToMP4 convierte un video a formato MP4 usando HandBrakeCLI
func convertToMP4(inputPath, outputPath string) error {
	// Comando HandBrakeCLI: -i input -o output --preset "Fast 1080p30" (preset para calidad rápida y buena)
	// Puedes ajustar el preset (e.g., "Very Fast 1080p30" para más velocidad, o "HQ 1080p30 Surround" para mejor calidad)
	cmd := exec.Command("HandBrakeCLI", "-i", inputPath, "-o", outputPath, "--preset", "Fast 1080p30")
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("error en HandBrakeCLI: %v, salida: %s", err, string(output))
	}
	return nil
}

// uploadFileToChannel sube un archivo al canal de logs y retorna el ID del mensaje y del archivo
func uploadFileToChannel(ctx *ext.Context, logChannelID int64, filePath string) (int, int64, error) {
	fileBytes, err := os.ReadFile(filePath)
	if err != nil {
		return 0, 0, fmt.Errorf("error al leer archivo convertido: %v", err)
	}

	document := &tg.InputMediaUploadedDocument{
		File: &tg.InputFile{
			Name:   filepath.Base(filePath),
			Length: int64(len(fileBytes)),
		},
		MimeType: "video/mp4",
	}

	update, err := ctx.Raw.MessagesSendMedia(&tg.MessagesSendMediaRequest{
		Peer:  &tg.InputPeerChannel{ChannelID: logChannelID},
		Media: document,
	})
	if err != nil {
		return 0, 0, fmt.Errorf("error al subir archivo al canal: %v", err)
	}

	var messageID int
	var fileID int64
	for _, u := range update.Updates {
		switch updateType := u.(type) {
		case *tg.UpdateMessageID:
			messageID = updateType.ID
		case *tg.UpdateNewChannelMessage:
			if msg, ok := updateType.Message.(*tg.Message); ok {
				if doc, ok := msg.Media.(*tg.MessageMediaDocument); ok {
					if doc.Document != nil {
						fileID = doc.Document.ID
					}
				}
			}
		}
	}

	if messageID == 0 || fileID == 0 {
		return 0, 0, fmt.Errorf("no se pudo obtener ID del mensaje o archivo")
	}

	return messageID, fileID, nil
}
