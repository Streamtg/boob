func sendLink(ctx *ext.Context, u *ext.Update) error {
	chatId := u.EffectiveChat().GetID()
	peerChatId := ctx.PeerStorage.GetPeerById(chatId)
	if peerChatId.Type != int(storage.TypeUser) {
		return dispatcher.EndGroups
	}

	// Validación de usuarios permitidos
	if len(config.ValueOf.AllowedUsers) != 0 && !utils.Contains(config.ValueOf.AllowedUsers, chatId) {
		ctx.Reply(u, "You are not allowed to use this bot.", nil)
		return dispatcher.EndGroups
	}

	// Validación de suscripción forzada
	if config.ValueOf.ForceSubChannel != "" {
		isSubscribed, err := utils.IsUserSubscribed(ctx, ctx.Raw, ctx.PeerStorage, chatId)
		if err != nil || !isSubscribed {
			row := tg.KeyboardButtonRow{
				Buttons: []tg.KeyboardButtonClass{
					&tg.KeyboardButtonURL{
						Text: "Join Channel",
						URL:  fmt.Sprintf("https://t.me/%s", config.ValueOf.ForceSubChannel),
					},
				},
			}
			markup := &tg.ReplyInlineMarkup{Rows: []tg.KeyboardButtonRow{row}}
			ctx.Reply(u, "Please join our channel to get stream links.", &ext.ReplyOpts{Markup: markup})
			return dispatcher.EndGroups
		}
	}

	// Filtrado de medios soportados
	supported, err := supportedMediaFilter(u.EffectiveMessage)
	if err != nil {
		return err
	}
	if !supported {
		ctx.Reply(u, "Sorry, this message type is unsupported.", nil)
		return dispatcher.EndGroups
	}

	// Forward al canal de logs
	update, err := utils.ForwardMessages(ctx, chatId, config.ValueOf.LogChannelID, u.EffectiveMessage.ID)
	if err != nil {
		ctx.Reply(u, fmt.Sprintf("Error - %s", err.Error()), nil)
		return dispatcher.EndGroups
	}

	messageID := update.Updates[0].(*tg.UpdateMessageID).ID
	doc := update.Updates[1].(*tg.UpdateNewChannelMessage).Message.(*tg.Message).Media
	file, err := utils.FileFromMedia(doc)
	if err != nil {
		ctx.Reply(u, fmt.Sprintf("Error - %s", err.Error()), nil)
		return dispatcher.EndGroups
	}

	// Detectar nombre y formato si no está presente
	if file.FileName == "" {
		var ext string
		lowerMime := strings.ToLower(file.MimeType)
		switch {
		case strings.Contains(lowerMime, "image/jpeg"):
			ext = ".jpg"
			file.FileName = "photo" + ext
		case strings.Contains(lowerMime, "image/png"):
			ext = ".png"
			file.FileName = "photo" + ext
		case strings.Contains(lowerMime, "image/gif"):
			ext = ".gif"
			file.FileName = "animation" + ext
		case strings.Contains(lowerMime, "video"):
			ext = ".mp4"
			file.FileName = "video" + ext
		case strings.Contains(lowerMime, "audio"):
			ext = ".mp3"
			file.FileName = "audio" + ext
		case strings.Contains(lowerMime, "pdf"):
			ext = ".pdf"
			file.FileName = "document" + ext
		case strings.Contains(lowerMime, "zip"):
			ext = ".zip"
			file.FileName = "archive" + ext
		case strings.Contains(lowerMime, "rar"):
			ext = ".rar"
			file.FileName = "archive" + ext
		case strings.Contains(lowerMime, "text"):
			ext = ".txt"
			file.FileName = "text" + ext
		case strings.Contains(lowerMime, "application"):
			ext = ".bin"
			file.FileName = "file" + ext
		default:
			ext = ""
			file.FileName = "unknown"
		}
	}

	// Sanitizar nombre
	file.FileName = sanitizeFileName(file.FileName)

	// Emoji y tamaño
	emoji := fileTypeEmoji(file.MimeType)
	size := formatFileSize(file.FileSize)

	// Duración si es multimedia
	durationMsg := ""
	if file.Duration > 0 {
		hours := file.Duration / 3600
		minutes := (file.Duration % 3600) / 60
		seconds := file.Duration % 60

		if hours > 0 {
			durationMsg = fmt.Sprintf("⏱ Duration: %02d:%02d:%02d", hours, minutes, seconds)
		} else {
			durationMsg = fmt.Sprintf("⏱ Duration: %02d:%02d", minutes, seconds)
		}
	}

	// Previsualización y seguridad placeholders
	preview := generatePreview(file)
	safe := isFileSafe(file)
	safeMessage := ""
	if !safe {
		safeMessage = "\n⚠️ Warning: File may be unsafe!"
	}

	// Construir mensaje final
	message := fmt.Sprintf(
		"%s File Name: %s\n\n%s File Type: %s\n\n💾 Size: %s\n%s\n%s\n\n⏳ @yoelbots",
		emoji, file.FileName,
		emoji, file.MimeType,
		size,
		durationMsg,
		preview+safeMessage,
	)

	// Hash para streaming
	fullHash := utils.PackFile(file.FileName, file.FileSize, file.MimeType, file.ID)
	hash := utils.GetShortHash(fullHash)

	// Registrar estadísticas
	statsCache := cache.GetStatsCache()
	if statsCache != nil {
		_ = statsCache.RecordFileProcessed(file.FileSize)
	}

	// Crear botón de streaming/download
	row := tg.KeyboardButtonRow{}
	videoParam := fmt.Sprintf("%d?hash=%s", messageID, hash)
	encodedVideoParam := url.QueryEscape(videoParam)
	encodedFilename := url.QueryEscape(file.FileName)
	streamURL := fmt.Sprintf("https://file.streamgramm.workers.dev/?video=%s&filename=%s", encodedVideoParam, encodedFilename)
	row.Buttons = append(row.Buttons, &tg.KeyboardButtonURL{
		Text: "Streaming / Download",
		URL:  streamURL,
	})
	markup := &tg.ReplyInlineMarkup{Rows: []tg.KeyboardButtonRow{row}}

	// Enviar mensaje
	_, err = ctx.Reply(u, message, &ext.ReplyOpts{
		Markup:           markup,
		NoWebpage:        false,
		ReplyToMessageId: u.EffectiveMessage.ID,
	})
	if err != nil {
		ctx.Reply(u, fmt.Sprintf("Error - %s", err.Error()), nil)
	}

	return dispatcher.EndGroups
}
