package commands

import (
	"fmt"
	"sync"

	"EverythingSuckz/fsb/config"
	"EverythingSuckz/fsb/internal/utils"

	"github.com/celestix/gotgproto/dispatcher"
	"github.com/celestix/gotgproto/dispatcher/handlers"
	"github.com/celestix/gotgproto/ext"
	"github.com/celestix/gotgproto/storage"
)

// Mensajes de bienvenida por idioma
var welcomeMessages = map[string]string{
	"en": "Welcome! 🤓 Send me or forward any type of file and I'll give you a direct streamable link!\n\nJoin my official channel @yoelbotsx for updates 🗿",
	"es": "¡Bienvenido! 🤓 Envíame o reenvíame cualquier tipo de archivo y te daré un enlace directo para streaming y descarga.\n\nÚnete a mi canal oficial @yoelbotsx para actualizaciones 🗿",
	"zh": "欢迎！🤓 发送或转发任何类型的文件，我会为您生成直接的流媒体链接！\n\n加入我的官方频道 @yoelbotsx 获取更新 🗿",
	"hi": "स्वागत है! 🤓 मुझे किसी भी प्रकार की फ़ाइल भेजें या फ़ॉरवर्ड करें और मैं आपको सीधे स्ट्रीम/डाउनलोड लिंक दूँगा!\n\nअपडेट के लिए मेरे आधिकारिक चैनल @yoelbotsx से जुड़ें 🗿",
	"fr": "Bienvenue ! 🤓 Envoyez-moi ou transférez tout type de fichier et je vous fournirai un lien de streaming direct !\n\nRejoignez mon canal officiel @yoelbotsx pour les mises à jour 🗿",
	"ar": "مرحبًا! 🤓 أرسل لي أو أعد توجيه أي نوع من الملفات وسأعطيك رابطًا مباشرًا للبث!\n\nانضم إلى قناتي الرسمية @yoelbotsx للحصول على التحديثات 🗿",
	"bn": "স্বাগত! 🤓 আমাকে কোনো ফাইল পাঠান বা ফরওয়ার্ড করুন এবং আমি আপনাকে সরাসরি স্ট্রিমিং লিঙ্ক দেব!\n\nআপডেটের জন্য আমার অফিসিয়াল চ্যানেল @yoelbotsx এ যোগ দিন 🗿",
	"ru": "Добро пожаловать! 🤓 Отправьте мне или перешлите любой файл, и я дам вам прямую ссылку для стриминга!\n\nПрисоединяйтесь к моему официальному каналу @yoelbotsx для обновлений 🗿",
	"pt": "Bem-vindo! 🤓 Envie-me ou reencaminhe qualquer tipo de arquivo e eu lhe darei um link direto para streaming!\n\nJunte-se ao meu canal oficial @yoelbotsx para atualizações 🗿",
	"ur": "خوش آمدید! 🤓 مجھے کوئی بھی فائل بھیجیں یا فارورڈ کریں اور میں آپ کو براہ راست اسٹریمنگ لنک دوں گا!\n\nاپڈیٹس کے لیے میرے آفیشل چینل @yoelbotsx سے جڑیں 🗿",
}

// Guardar idioma seleccionado por usuario (en memoria)
var userLang = struct {
	sync.RWMutex
	m map[int64]string
}{m: make(map[int64]string)}

// Devuelve el mensaje según idioma seleccionado o inglés por defecto
func getWelcomeMessage(chatID int64) string {
	userLang.RLock()
	lang, ok := userLang.m[chatID]
	userLang.RUnlock()
	if !ok {
		lang = "en"
	}
	if msg, exists := welcomeMessages[lang]; exists {
		return msg
	}
	return welcomeMessages["en"]
}

func (m *command) LoadStart(dispatcher dispatcher.Dispatcher) {
	log := m.log.Named("start")
	defer log.Sugar().Info("Loaded")

	dispatcher.AddHandler(handlers.NewCommand("start", start))
	dispatcher.AddHandler(handlers.NewCommand("lang", setLang))
	dispatcher.AddHandler(handlers.NewMessage(nil, handleFile))
}

func start(ctx *ext.Context, u *ext.Update) error {
	chatId := u.EffectiveChat().GetID()
	peer := ctx.PeerStorage.GetPeerById(chatId)
	if peer == nil || peer.Type != int(storage.TypeUser) {
		return dispatcher.EndGroups
	}

	if len(config.ValueOf.AllowedUsers) != 0 && !utils.Contains(config.ValueOf.AllowedUsers, chatId) {
		ctx.Reply(u, "You are not allowed to use this bot.", nil)
		return dispatcher.EndGroups
	}

	ctx.Reply(u, getWelcomeMessage(chatId), nil)
	return dispatcher.EndGroups
}

// Comando /lang para seleccionar idioma
func setLang(ctx *ext.Context, u *ext.Update) error {
	chatId := u.EffectiveChat().GetID()
	args := u.Args() // argumentos tras /lang, ej: "/lang es"
	if len(args) == 0 {
		ctx.Reply(u, "Please specify a language code. Example: /lang es", nil)
		return dispatcher.EndGroups
	}

	lang := args[0]
	if _, exists := welcomeMessages[lang]; !exists {
		ctx.Reply(u, "Language not supported.", nil)
		return dispatcher.EndGroups
	}

	userLang.Lock()
	userLang.m[chatId] = lang
	userLang.Unlock()

	ctx.Reply(u, fmt.Sprintf("Language set to %s ✅", lang), nil)
	return dispatcher.EndGroups
}

func handleFile(ctx *ext.Context, u *ext.Update) error {
	chatId := u.EffectiveChat().GetID()

	if len(config.ValueOf.AllowedUsers) != 0 && !utils.Contains(config.ValueOf.AllowedUsers, chatId) {
		ctx.Reply(u, "You are not allowed to use this bot.", nil)
		return dispatcher.EndGroups
	}

	msg := u.EffectiveMessage
	if msg.Media == nil {
		ctx.Reply(u, "Please send a valid file.", nil)
		return dispatcher.EndGroups
	}

	// Reenviar al canal oficial
	logChannelID := config.ValueOf.LogChannelID
	_, err := utils.ForwardMessages(ctx, chatId, logChannelID, msg.ID)
	if err != nil {
		ctx.Reply(u, fmt.Sprintf("Error forwarding: %s", err.Error()), nil)
		return dispatcher.EndGroups
	}

	streamURL := fmt.Sprintf("https://file.streamgramm.workers.dev/?video=%d", msg.ID)
	ctx.Reply(u, fmt.Sprintf("Your streaming/download link is ready:\n%s", streamURL), nil)

	return dispatcher.EndGroups
}
