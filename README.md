# TeleTorrent Bot

Bot de Telegram en **Go** que descarga archivos torrent (magnet links o archivos `.torrent` por URL) y los sube directamente a Telegram. No necesita permisos de root.

## Requisitos

- **Go 1.24+**
- Conexión a internet (para DHT/trackers)
- Espacio en disco para los archivos

## Instalación

```bash
# 1. Obtén tu token de @BotFather (https://t.me/BotFather)

# 2. En el directorio del proyecto:
go mod tidy
go build -o tele-torrent-bot .

# 3. Ejecutar:
./tele-torrent-bot -token "TU_TOKEN_AQUI"
```

## Uso

1. Abre tu bot en Telegram
2. Envía `/start` para ver la ayuda
3. Envía un magnet link:
   ```
   magnet:?xt=urn:btih:c12fe1c06bba254a9dc9f519b335aa7c1367a88a
   ```
4. O una URL de archivo `.torrent`:
   ```
   https://example.com/mi-archivo.torrent
   ```
5. El bot descarga el torrent y sube los archivos a Telegram
6. Usa `/cancel` para cancelar la descarga en curso
7. Usa `/status` para ver el progreso

## Estructura

```
├── main.go   — Todo el código (bot Telegram + motor torrent)
├── go.mod    — Módulo y dependencias
├── go.sum    — Checksums de dependencias
└── README.md — Este archivo
```

## Notas

- Solo una descarga por chat a la vez
- Seeding continúa después de subir (hasta cerrar el proceso)
- Puerto DHT abierto automáticamente (sin root)
- No necesita root ni permisos especiales