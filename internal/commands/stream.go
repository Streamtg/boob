package commands

import (
	"fmt"
	"net/url"
	"strings"

	"EverythingSuckz/fsb/internal/utils"
	"EverythingSuckz/fsb/internal/types"
	"github.com/gotd/td/telegram/peers"
	"github.com/gotd/td/tg"
	"go.uber.org/zap"
	"golang.org/x/net/context"
)

func Stream(ctx context.Context, client *tg.Client, log *zap.Logger) error {
	api := tg.NewClient(client)

	self, err := peers.Self(api)
	if err != nil {
		log.Error("Error getting self info", zap.Error(err))
		return err
	}

	upd, err := api.UpdatesGetDifference(ctx, &tg.UpdatesGetDifferenceRequest{
		Pts:  0,
		Date: 0,
		Qts:  0,
	})
	if err != nil {
		log.Error("Error getting updates", zap.Error(err))
		return err
	}

	for _, update := range upd.OtherUpdates {
		switch u := update.(type) {
		case *tg.UpdateNewMessage:
			msg, ok := u.Message.(*tg.Message)
			if !ok || msg == nil {
				continue
			}

			if msg.Media == nil {
				continue
			}

			file := &types.File{}

			switch media := msg.Media.(type) {
			case *tg.MessageMediaDocument:
				doc := media.Document.AsNotEmpty()
				if doc == nil {
					continue
				}

				// Extraer nombre, tamaño y tipo MIME
				file.FileName = utils.GetDocumentName(doc)
				file.FileSize = doc.Size
				file.MimeType = doc.MimeType
				file.ID = doc.ID

			case *tg.MessageMediaPhoto:
				photo := media.Photo.AsNotEmpty()
				if photo == nil {
					continue
				}
				file.FileName = "photo.jpg"
				file.FileSize = utils.GetPhotoSize(photo)
				file.MimeType = "image/jpeg"
				file.ID = photo.ID

			default:
				continue
			}

			// Generar hash y enlace
			fullHash := utils.PackFile(file.FileName, file.FileSize, file.MimeType, file.ID)
			hash := utils.GetShortHash(fullHash)

			streamURL := fmt.Sprintf(
				"https://host.streamgramm.workers.dev/?video=%d&hash=%s&filename=%s",
				file.ID,
				hash,
				url.QueryEscape(file.FileName),
			)

			text := fmt.Sprintf("📁 *Archivo disponible:*\n\n`%s`\n\n🔗 [Abrir enlace](%s)", file.FileName, streamURL)
			_, err = api.MessagesSendMessage(ctx, &tg.MessagesSendMessageRequest{
				Peer:     self.InputPeer(),
				Message:  text,
				RandomID: utils.RandomID(),
				ParseMode: &tg.TextParseMode{
					Type: "Markdown",
				},
			})

			if err != nil {
				log.Error("Error sending message", zap.Error(err))
				continue
			}

			log.Info("Archivo procesado correctamente",
				zap.String("nombre", file.FileName),
				zap.Int64("id", file.ID),
				zap.String("enlace", streamURL),
			)
		}
	}

	return nil
}
