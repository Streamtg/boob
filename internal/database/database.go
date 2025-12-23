package database

import (
	"EverythingSuckz/fsb/config"
	"path/filepath"

	"go.uber.org/zap"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// Estructura contenedora (opcional si usas GORM directo)
type DB struct {
	Conn *gorm.DB
	log  *zap.Logger
}

// Instancia global privada
var instance *gorm.DB

// NewDatabase inicializa la conexión y asigna la instancia global
func NewDatabase(log *zap.Logger) *DB {
	log = log.Named("database")
	
	// Usamos la ruta definida en la config o una por defecto
	dbPath := config.ValueOf.GithubDbPath
	if dbPath == "" {
		dbPath = "storage/database.json"
	}
	
	// Aseguramos que el directorio exista
	_ = filepath.Dir(dbPath)

	// Inicializamos SQLite (que actuará como nuestra DB persistente local)
	db, err := gorm.Open(sqlite.Open(dbPath), &gorm.Config{})
	if err != nil {
		log.Fatal("Failed to connect to database", zap.Error(err))
	}

	log.Info("Database connection established", zap.String("path", dbPath))
	
	// Asignamos a la variable global para acceso desde otros paquetes
	instance = db
	
	return &DB{
		Conn: db,
		log:  log,
	}
}

// GetDB es el Getter público para obtener la instancia de GORM
func GetDB() *gorm.DB {
	return instance
}
