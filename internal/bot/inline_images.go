package bot

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"

	"gopkg.in/telebot.v4"

	"ok-gobot/internal/ai"
)

// maxInlineImageBytes bounds one decoded inline image. Telegram refuses photos
// past roughly 10 MB, so anything larger could not be delivered anyway and is
// rejected before it is written to disk.
const maxInlineImageBytes = 10 << 20

// deliverInlineImages writes images the model returned in its own message to
// disk and sends them to the chat.
//
// Delivery failures are logged and never abort the turn: the text half of the
// reply, if any, still has to reach the user. Each image is reported
// individually so a silent loss is impossible — the journal always says
// whether a picture that arrived on the wire also reached the chat.
//
// It returns how many images actually reached the chat. The caller needs the
// count rather than len(images): a picture-only answer has no text, and only a
// delivered picture earns the right to suppress the empty-response warning.
func (b *Bot) deliverInlineImages(chat *telebot.Chat, images []ai.InlineImage) int {
	if chat == nil || len(images) == 0 {
		return 0
	}
	dir := filepath.Join(os.TempDir(), "okgobot-inline-images")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		log.Printf("[inline_image] cannot create %s: %v", dir, err)
		return 0
	}
	delivered := 0

	for i, img := range images {
		mediaType, raw, err := img.Decode()
		if err != nil {
			log.Printf("[inline_image] %d/%d undecodable: %v", i+1, len(images), err)
			continue
		}
		if len(raw) > maxInlineImageBytes {
			log.Printf("[inline_image] %d/%d too large to send: %d bytes", i+1, len(images), len(raw))
			continue
		}
		ext := ".png"
		if mediaType == "image/jpeg" {
			ext = ".jpg"
		}
		path := filepath.Join(dir, fmt.Sprintf("inline_%d_%d%s", time.Now().UnixNano(), i, ext))
		if err := os.WriteFile(path, raw, 0o600); err != nil {
			log.Printf("[inline_image] %d/%d write failed: %v", i+1, len(images), err)
			continue
		}
		if _, err := b.api.Send(chat, &telebot.Photo{File: telebot.FromDisk(path)}); err != nil {
			log.Printf("[inline_image] %d/%d send failed: %v", i+1, len(images), err)
			_ = os.Remove(path)
			continue
		}
		delivered++
		log.Printf("[inline_image] %d/%d delivered chat=%d bytes=%d type=%s", i+1, len(images), chat.ID, len(raw), mediaType)
		_ = os.Remove(path)
	}
	return delivered
}
