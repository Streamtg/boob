package types

import (
	"crypto/md5"
	"encoding/hex"
	"reflect"
	"strconv"

	"github.com/gotd/td/tg"
)

// File representa un archivo en Telegram, ya sea documento, foto, audio o video.
type File struct {
	Location tg.InputFileLocationClass
	FileSize int64
	FileName string
	MimeType string
	ID       int64
	Duration int64 // Nueva propiedad: duración en segundos para audio/video
}

// HashableFileStruct contiene los campos que se usarán para generar un hash único
type HashableFileStruct struct {
	FileName string
	FileSize int64
	MimeType string
	FileID   int64
	Duration int64 // Incluimos duración en el hash
}

// Pack genera un hash MD5 único del archivo
func (f *HashableFileStruct) Pack() string {
	hasher := md5.New()
	val := reflect.ValueOf(*f)
	for i := 0; i < val.NumField(); i++ {
		field := val.Field(i)

		var fieldValue []byte
		switch field.Kind() {
		case reflect.String:
			fieldValue = []byte(field.String())
		case reflect.Int64:
			fieldValue = []byte(strconv.FormatInt(field.Int(), 10))
		}

		hasher.Write(fieldValue)
	}
	return hex.EncodeToString(hasher.Sum(nil))
}

// Nuevo helper que crea un HashableFileStruct desde un File
func (f *File) ToHashable() *HashableFileStruct {
	return &HashableFileStruct{
		FileName: f.FileName,
		FileSize: f.FileSize,
		MimeType: f.MimeType,
		FileID:   f.ID,
		Duration: f.Duration, // Incluimos duración
	}
}
