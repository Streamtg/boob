package commands

import (
	"fmt"
	"strings"

	"EverythingSuckz/fsb/config"
	"EverythingSuckz/fsb/internal/cache" // Ahora sí se usa
	"EverythingSuckz/fsb/internal/utils"

	"github.com/celestix/gotgproto/dispatcher"
	"github.com/celestix/gotgproto/dispatcher/handlers"
	"github.com/celestix/gotgproto/ext"
	"github.com/celestix/gotgproto/storage"
	"github.com/gotd/td/tg"
)

// LoadStream registra el handler.
// Pasamos 'nil' como filtro inicial y validamos dentro de sendLink para máximo control.
func (m *command) LoadStream(dispatcher dispatcher.Dispatcher) {
	m.log.Named("stream").Info("Streaming handler initialized")
	dispatcher.AddHandler(handlers.NewMessage(nil, m.sendLink))
}

// formatFileSize convierte bytes a texto legible.
func formatFileSize(bytes int64) string {
	const (KB, MB, GB = 1024, 1024 * 1024, 1024 * 1024 * 1024)
	switch {
	case bytes >= GB:
		return fmt.Sprintf("%.2f GB", float64(bytes)/float64(GB))
	case bytes >= MB:
		return fmt.Sprintf("%.2f MB", float64(bytes)/float64(MB))
	default:
		return fmt.Sprintf("%.2f KB", float64(bytes)/float64(KB))
	}
}

// sendLink es el handler principal que valida, procesa y responde.
func (m *command) sendLink(ctx *ext.Context, u *ext.Update) error {
	// 1. Validación de Chat Privado
	chatId := u.EffectiveChat().GetID()
	if ctx.PeerStorage.GetPeerById(chatId).Type != int(storage.TypeUser) {
		return dispatcher.EndGroups
	}

	// 2. Validación de Tipo de Media (Reemplaza al filtro anterior)
	// Si no tiene media o no es documento/foto, ignoramos silenciosamente.
	if u.EffectiveMessage.Media == nil {
		return dispatcher.EndGroups
	}
	switch u.EffectiveMessage.Media.(type) {
	case *tg.MessageMediaDocument, *tg.MessageMediaPhoto:
		// Es válido, continuamos
	default:
		return dispatcher.EndGroups
	}

	// 3. Validación de Suscripción (Force Sub)
	if config.ValueOf.ForceSubChannel != "" {
		isSubscribed, err := utils.IsUserSubscribed(ctx, ctx.Raw, ctx.PeerStorage, chatId)
		if err != nil || !isSubscribed {
			joinURL := fmt.Sprintf("https://t.me/%s", config.ValueOf.ForceSubChannel)
			_, _ = ctx.Reply(u, "⚠️ **Subscription Required**\n\nPlease join our channel:\n"+joinURL, &ext.ReplyOpts{NoWebpage: true})
			return dispatcher.EndGroups
		}
	}

	// 4. Reenvío al Canal de Logs (Persistencia)
	update, err := utils.ForwardMessages(ctx, chatId, config.ValueOf.LogChannelID, u.EffectiveMessage.ID)
	if err != nil {
		// Logueamos usando la interfaz interna del logger si falla
		_, _ = ctx.Reply(u, "❌ Error: Could not forward file to log channel.", nil)
		return dispatcher.EndGroups
	}

	// Extracción segura de datos
	msgID := update.Updates[0].(*tg.UpdateMessageID).ID
	channelMsg, ok := update.Updates[1].(*tg.UpdateNewChannelMessage).Message.(*tg.Message)
	if !ok {
		return dispatcher.EndGroups
	}

	file, err := utils.FileFromMedia(channelMsg.Media)
	if err != nil {
		return dispatcher.EndGroups
	}

	// 5. Generación del Enlace RESTful
	fullHash := utils.PackFile(file.FileName, file.FileSize, file.MimeType, file.ID)
	shortHash := utils.GetShortHash(fullHash)
	baseUrl := strings.TrimSuffix(config.ValueOf.WorkerURL, "/")
	
	finalURL := fmt.Sprintf("%s/%d/%s", baseUrl, msgID, shortHash)

	// 6. Registro de Estadísticas (Uso correcto del paquete cache)
	if stats := cache.GetStatsCache(); stats != nil {
		// Ignoramos el error de registro para no detener el flujo principal
		_ = stats.RecordFileProcessed(file.FileSize)
	}

	// 7. Respuesta al Usuario
	caption := fmt.Sprintf(
		"🎬 **File:** `%s`\n"+
			"💾 **Size:** `%s`\n\n"+
			"🚀 **Direct Link:**\n`%s`\n\n"+
			"⚡ *By @yoelbots*",
		file.FileName, formatFileSize(file.FileSize), finalURL,
	)

	// Usamos markdown style parse mode explícitamente si ReplyOpts lo permite, 
	// o confiamos en que gotgproto detecta las entidades.
	// Nota: Si usas la última versión de gotgproto, 'styling' package es preferido,
	// pero aquí usamos strings planos con formato markdown para evitar deps complejas.
	_, _ = ctx.Reply(u, caption, &ext.ReplyOpts{
		NoWebpage:        true,
		ReplyToMessageId: u.EffectiveMessage.ID,
		// ParseMode es manejado a veces automáticamente o requiere setup en main. 
		// Si falla el formato, considera usar utils.Format o styling.
	})
	
	return dispatcher.EndGroups
}
