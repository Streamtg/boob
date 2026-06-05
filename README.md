# TeleTorrent Bot

Bot de Telegram escrito en **Go** que descarga archivos torrent (magnet links o archivos `.torrent` por URL) y los sube directamente a Telegram. No necesita permisos de root.

## Requisitos

- **Go 1.21+**
- Conexión a internet (para DHT/trackers)
- Espacio en disco suficiente para los archivos

## Instalación y ejecución

### 1. Obtener el token del bot

1. Abre Telegram y habla con [@BotFather](https://t.me/BotFather)
2. Envía `/newbot`
3. Sigue las instrucciones y copia el **token** (ejemplo: `123456789:ABCdefGHIjklMNOpqrSTUvwxYZ`)

### 2. Clonar y ejecutar

```bash
# Clonar / copiar los archivos del proyecto
# En el directorio del proyecto:

# Descargar dependencias
go mod tidy

# Ejecutar
go run main.go -token "AQUI_TU_TOKEN"
```

### 3. Uso

1. Abre tu bot en Telegram
2. Envía `/start` para ver la ayuda
3. Envía un magnet link, por ejemplo:
   ```
   magnet:?xt=urn:btih:c12fe1c06bba254a9dc9f519b335aa7c1367a88a
   ```
4. Envía una URL de archivo `.torrent`:
   ```
   https://example.com/mi-archivo.torrent
   ```
5. El bot descargará el torrent y subirá los archivos a Telegram
6. Usa `/cancel` para cancelar la descarga en curso
7. Usa `/status` para ver el progreso

## Estructura del proyecto

```
tele-torrent-bot/
├── main.go      # Punto de entrada, inicialización del cliente torrent y del bot
├── engine.go    # Lógica de descarga, subida y manejo de mensajes
├── go.mod       # Módulo Go con dependencias
├── README.md    # Este archivo
└── downloads/   # Directorio donde se guardan los archivos (se crea solo)
```

## Notas importantes

- **Solo una descarga por chat** a la vez. Cancela con `/cancel` antes de iniciar otra.
- **Archivo máximo en Telegram**: ~2 GB. Archivos más grandes se enviarán como documento.
- **Seeding**: El torrent sigue seedeando después de subirlo (hasta que el proceso se cierre).
- **Puerto DHT**: El cliente torrent abre un puerto UDP/TCP aleatorio para DHT. Asegúrate de que tu firewall lo permita (o no lo necesitarás si tienes peers por tracker).
- **No necesita root**: Todo funciona en espacio de usuario.

## Configuración avanzada (opcional)

Por defecto todo funciona con los valores por defecto. Si necesitas afinar:

```bash
# Cambiar el directorio de descarga
go run main.go -token "TU_TOKEN" -storage "/ruta/personalizada"

# El cliente torrent escuchará en 0.0.0.0:puerto-aleatorio
# Se conectará a hasta 100 peers simultáneos
```

## Solución de problemas

### "Failed to add torrent"
- Verifica que el magnet link esté completo (contenga `btih:` o `urn:btih:`)
- Verifica que la URL de .torrent devuelva HTTP 200

### "Failed to upload"
- Telegram limita a ~50 MB por mensaje en algunos bots; archivos grandes se envían como documento.
- Verifica que el bot tenga permisos para enviar archivos en el grupo/canal.

### El bot no responde
- Verifica que el token sea correcto
- Verifica que el bot esté iniciado (debe mostrar "✅ Telegram bot logged in as @TuBot")