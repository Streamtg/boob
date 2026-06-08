package main

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// SplitLargeFile divide un archivo en partes de maxSize MB usando ffmpeg
// Esto permite enviar archivos que excedan el límite de Telegram (~2 GB)
func SplitLargeFile(filePath string, maxSizeMB int64) ([]string, error) {
	fi, err := os.Stat(filePath)
	if err != nil {
		return nil, fmt.Errorf("stat: %w", err)
	}

	maxBytes := maxSizeMB * 1024 * 1024
	if fi.Size() <= maxBytes {
		return []string{filePath}, nil
	}

	log.Printf("Archivo muy grande (%s), dividiendo en partes de %d MB...",
		formatBytes(fi.Size()), maxSizeMB)

	ext := filepath.Ext(filePath)
	baseName := strings.TrimSuffix(filePath, ext)
	outputPattern := baseName + "_part_%03d" + ext

	// Usar ffmpeg para dividir: segmentación por tamaño
	// -f segment: formato segmentado
	// -segment_format mp4: cada segmento en MP4
	// -segment_time: duración de cada segmento (alternativa)
	// -fs: max file size (límite por archivo)
	cmd := exec.Command("ffmpeg",
		"-i", filePath,
		"-c", "copy",
		"-map", "0",
		"-f", "segment",
		"-segment_format", "mp4",
		"-reset_timestamps", "1",
		"-fs", fmt.Sprintf("%d", maxBytes),
		outputPattern,
	)

	output, err := cmd.CombinedOutput()
	if err != nil {
		log.Printf("ffmpeg split error: %v\nOutput: %s", err, string(output))
		// Si ffmpeg falla, devolver el archivo original
		return []string{filePath}, nil
	}

	// Recoger los archivos generados
	var parts []string
	for i := 0; ; i++ {
		partPath := fmt.Sprintf("%s_part_%03d%s", baseName, i, ext)
		if _, err := os.Stat(partPath); os.IsNotExist(err) {
			break
		}
		parts = append(parts, partPath)
	}

	if len(parts) == 0 {
		return []string{filePath}, nil
	}

	log.Printf("Archivo dividido en %d partes", len(parts))
	return parts, nil
}
