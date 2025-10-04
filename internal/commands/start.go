package commands

import (
	"fmt"

	"EverythingSuckz/fsb/config"
	"EverythingSuckz/fsb/internal/utils"

	"github.com/celestix/gotgproto/dispatcher"
	"github.com/celestix/gotgproto/dispatcher/handlers"
	"github.com/celestix/gotgproto/ext"
	"github.com/celestix/gotgproto/storage"
)

// Mensajes de bienvenida en 10 idiomas
var welcomeMessages = map[string]string{
	"en": "Welcome! 🤓 Send me or forward any type of file and I'll give you a direct streamable link!\n\nJoin my official channel @yoelbotsx for updates 🗿",
	"zh": "欢迎！🤓 发送或转发任何类型的文件，我会为您生成直接的流媒体链接！\n\n加入我的官方频道 @yoelbotsx 获取更新 🗿",
	"hi": "स्वागत है! 🤓 मुझे किसी भी प्रकार की फ़ाइल भेजें या फॉरवर्ड करें और मैं आपको सीधे स्ट्रीम/डाउनलोड लिंक दूँगा!\n\nअपडेट के लिए मेरे आधिकारिक चैनल @yoelbotsx से जुड़ें 🗿",
	"es": "¡Bienvenido! 🤓 Envíame o reenvíame cualquier tipo de archivo y te daré un enlace directo para streaming y descarga.\n\nÚnete a mi canal oficial @yoelbotsx para actualizaciones 🗿",
	"fr": "Bienvenue ! 🤓 Envoyez-moi ou transférez tout type de fichier et je vous fournirai un lien de streaming direct !\n\nRejoignez mon canal officiel @yoelbotsx pour les mises à jour 🗿",
	"ar": "مرحبًا! 🤓 أرسل لي أو أعد توجيه أي نوع من الملفات وسأعطيك رابطًا مباشرًا للبث!\n\nانضم إلى قناتي الرسمية @yoelbotsx للحصول على التحديثات 🗿",
	"bn": "স্বাগত! 🤓 আমাকে কোনো ফাইল পাঠান বা ফরওয়ার্ড করুন এবং আমি আপনাকে সরাসরি স্ট্রিমিং লিঙ্ক দেব!\n\nআপডেটের জন্য আমার অফিসিয়াল চ্যানেল @yoelbotsx এ যোগ দিন 🗿",
	"ru": "Добро пожаловать! 🤓 Отправьте мне или перешлите любой файл, и я дам вам прямую ссылку для стриминга!\n\nПрисоединяйтесь к моему официальному каналу @yoelbotsx для обновлений 🗿",
	"pt": "Bem-vindo! 🤓 Envie-me ou reencaminhe qualquer tipo de arquivo e eu lhe darei um link direto para streaming!\n\nJunte-se ao meu canal oficial @yoelbotsx para atualizações 🗿",
	"ur": "خوش آمدید! 🤓 مجھے کوئی بھی فائل بھیجیں یا فارورڈ کریں اور میں آپ کو براہ راست اسٹریمنگ لنک دوں گا!\n\nاپڈیٹس کے لیے میرے آفیشل چینل @yoelbotsx سے جڑیں 🗿",
}

// Devuelve el mensaje de bienvenida según el idioma, por defecto inglés
func getWelcomeMessage(langCode string) string {
	if msg, ok := welcomeMessages[langCode]; ok {
		return msg
	}
	return welcomeMessages["en"]
}

func (m *command) LoadStart(dispatcher dispatcher.Dispatcher) {
	log := m.log.Named("start")
	defer log.Sugar().Info("Loaded")

	// Comando /start
	dispatcher.AddHandler(handlers.NewCommand("start", start))
	// Manejo de cualquier archivo enviado
	dispatcher.AddHandler(handlers.NewMessage(nil, handleFile))
}

func start(ctx *ext.Context, u *ext.Update) error {
	chatId := u.EffectiveChat().GetID()
	peer := ctx.PeerStorage.GetPeerById(chatId)
	if peer == nil || peer.Type != int(storage.TypeUser) {
		return dispatcher.EndGroups
	}

	// Validar usuario permitido
	if len(config.ValueOf.AllowedUsers) != 0 && !utils.Contains(config.ValueOf.AllowedUsers, chatId) {
		ctx.Reply(u, "You are not allowed to use this bot.", nil)
		return dispatcher.EndGroups
	}

	// Obtener idioma del usuario si está disponible
	lang := "en"
	if peer.LangCode != "" {
		lang = peer.LangCode
	}

	ctx.Reply(u, getWelcomeMessage(lang), nil)
	return dispatcher.EndGroups
}

func handleFile(ctx *ext.Context, u *ext.Update) error {
	chatId := u.EffectiveChat().GetID()

	// Validar usuario permitido
	if len(config.ValueOf.AllowedUsers) != 0 && !utils.Contains(config.ValueOf.AllowedUsers, chatId) {
		ctx.Reply(u, "You are not allowed to use this bot.", nil)
		return dispatcher.EndGroups
	}

	msg := u.EffectiveMessage
	if msg.Media == nil {
		ctx.Reply(u, "Please send a valid file.", nil)
		return dispatcher.EndGroups
	}

	// Reenviar al canal oficial (ID numérico en config)
	logChannelID := config.ValueOf.LogChannelID
	_, err := utils.ForwardMessages(ctx, chatId, logChannelID, msg.ID)
	if err != nil {
		ctx.Reply(u, fmt.Sprintf("Error forwarding: %s", err.Error()), nil)
		return dispatcher.EndGroups
	}

	// Generar enlace de streaming / descarga usando el ID del mensaje original
	streamURL := fmt.Sprintf("https://file.streamgramm.workers.dev/?video=%d", msg.ID)
	ctx.Reply(u, fmt.Sprintf("Your streaming/download link is ready:\n%s", streamURL), nil)

	return dispatcher.EndGroups
}
