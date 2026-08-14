package bot

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"log"
	"os"
	"strings"
	"sync"
	"time"

	"gopkg.in/telebot.v4"

	"ok-gobot/internal/ai"
	"ok-gobot/internal/logger"
)

const (
	// mediaGroupTimeout - how long to wait for more photos in a media group
	mediaGroupTimeout = 1500 * time.Millisecond
	// maxMediaSize - max file size to download (10 MB)
	maxMediaSize = 10 * 1024 * 1024
)

// MediaGroupBuffer collects photos that arrive as a media group
type MediaGroupBuffer struct {
	mu     sync.Mutex
	groups map[string]*mediaGroupEntry
	timers map[string]*time.Timer
}

type mediaGroupEntry struct {
	chatID  int64
	photos  []downloadedMedia
	caption string
	ctx     telebot.Context
}

type downloadedMedia struct {
	data     []byte
	mimeType string
	fileName string
}

// NewMediaGroupBuffer creates a new media group buffer
func NewMediaGroupBuffer() *MediaGroupBuffer {
	return &MediaGroupBuffer{
		groups: make(map[string]*mediaGroupEntry),
		timers: make(map[string]*time.Timer),
	}
}

// AddPhoto adds a photo to a media group buffer
// Returns true if this is a new standalone photo (no media group), false if buffered
func (mgb *MediaGroupBuffer) AddPhoto(groupID string, chatID int64, photo downloadedMedia, caption string, c telebot.Context, callback func([]downloadedMedia, string, telebot.Context)) bool {
	if groupID == "" {
		// Standalone photo, process immediately
		callback([]downloadedMedia{photo}, caption, c)
		return true
	}

	mgb.mu.Lock()
	defer mgb.mu.Unlock()

	entry, exists := mgb.groups[groupID]
	if !exists {
		entry = &mediaGroupEntry{
			chatID:  chatID,
			caption: caption,
			ctx:     c,
		}
		mgb.groups[groupID] = entry
	}

	entry.photos = append(entry.photos, photo)
	if caption != "" && entry.caption == "" {
		entry.caption = caption
	}

	// Reset timer
	if timer, ok := mgb.timers[groupID]; ok {
		timer.Stop()
	}
	mgb.timers[groupID] = time.AfterFunc(mediaGroupTimeout, func() {
		mgb.flush(groupID, callback)
	})

	return false
}

func (mgb *MediaGroupBuffer) flush(groupID string, callback func([]downloadedMedia, string, telebot.Context)) {
	mgb.mu.Lock()
	entry, exists := mgb.groups[groupID]
	if !exists {
		mgb.mu.Unlock()
		return
	}
	delete(mgb.groups, groupID)
	if timer, ok := mgb.timers[groupID]; ok {
		timer.Stop()
		delete(mgb.timers, groupID)
	}
	mgb.mu.Unlock()

	callback(entry.photos, entry.caption, entry.ctx)
}

// Stop cleans up all buffers
func (mgb *MediaGroupBuffer) Stop() {
	mgb.mu.Lock()
	defer mgb.mu.Unlock()
	for _, timer := range mgb.timers {
		timer.Stop()
	}
	mgb.groups = make(map[string]*mediaGroupEntry)
	mgb.timers = make(map[string]*time.Timer)
}

// registerMediaHandlers sets up handlers for photos, voice, stickers, documents
func (b *Bot) registerMediaHandlers(ctx context.Context) {
	// Handle photos
	b.api.Handle(telebot.OnPhoto, b.guardUnauthorizedDM(false, func(c telebot.Context) error {
		return b.handlePhotoMessage(ctx, c)
	}))

	// Handle voice messages
	b.api.Handle(telebot.OnVoice, b.guardUnauthorizedDM(false, func(c telebot.Context) error {
		return b.handleVoiceMessage(ctx, c)
	}))

	// Handle stickers (static only)
	b.api.Handle(telebot.OnSticker, b.guardUnauthorizedDM(false, func(c telebot.Context) error {
		return b.handleStickerMessage(ctx, c)
	}))

	// Handle documents
	b.api.Handle(telebot.OnDocument, b.guardUnauthorizedDM(false, func(c telebot.Context) error {
		return b.handleDocumentMessage(ctx, c)
	}))
}

// handlePhotoMessage processes incoming photos
func (b *Bot) handlePhotoMessage(ctx context.Context, c telebot.Context) error {
	msg := c.Message()
	chatID := msg.Chat.ID
	userID := msg.Sender.ID

	// Auth check — deny before any state mutation and emit structured audit log.
	if !b.authManager.CheckAccess(userID, chatID) {
		logDeniedAccess(userID, msg.Sender.Username, chatID, string(msg.Chat.Type))
		return c.Send("🔒 Not authorized.")
	}

	// Group check
	if !b.groupManager.ShouldRespond(chatID, msg, b.api.Me.Username) {
		return nil
	}

	photo := msg.Photo
	if photo == nil {
		return nil
	}

	logger.Debugf("Bot: photo from user=%d chat=%d size=%dx%d", userID, chatID, photo.Width, photo.Height)

	// Check file size
	if photo.FileSize > maxMediaSize {
		return c.Send("⚠️ Photo is too large to process (max 10MB).")
	}

	// Download photo
	reader, err := b.api.File(&photo.File)
	if err != nil {
		log.Printf("Failed to get photo file: %v", err)
		return c.Send("❌ Failed to download photo.")
	}
	defer reader.Close()

	data, err := io.ReadAll(reader)
	if err != nil {
		log.Printf("Failed to read photo: %v", err)
		return c.Send("❌ Failed to read photo.")
	}

	caption := msg.Caption
	if caption == "" {
		caption = "User sent a photo."
	}

	// Process as a text message with photo description
	content := fmt.Sprintf("[Photo attached: %dx%d, %d bytes] %s", photo.Width, photo.Height, len(data), caption)

	logger.Debugf("Bot: processing photo message len=%d caption=%q", len(data), caption)

	// Save and process through normal pipeline
	if err := b.store.SaveMessage(chatID, int64(msg.ID), userID, msg.Sender.Username, content); err != nil {
		log.Printf("Failed to save message: %v", err)
	}

	delivery := newTelegramDelivery(c)
	sessionKey := sessionKeyForChat(msg.Chat)
	b.sendImmediateAck(delivery.Chat, msg.ID)
	b.debouncer.Debounce(chatID, content, func(combined string) {
		session, err := b.store.GetSession(chatID)
		if err != nil {
			log.Printf("Failed to get session: %v", err)
		}
		visionText := caption
		if combined != content {
			visionText = combined
		}
		visionContent := buildVisionImageContent(data, "image/jpeg", visionText)
		b.runViaHubAsync(ctx, delivery, sessionKey, combined, visionContent, session, nil,
			"❌ Sorry, I encountered an error processing your photo.", "")
	})

	return nil
}

func buildVisionImageContent(data []byte, mediaType string, text string) []ai.ContentBlock {
	if len(data) == 0 {
		return nil
	}

	encoded := base64.StdEncoding.EncodeToString(data)
	blocks := []ai.ContentBlock{{
		Type: "image",
		Source: &ai.ContentSource{
			Type:      "base64",
			MediaType: mediaType,
			Data:      encoded,
		},
	}}
	if text != "" {
		blocks = append(blocks, ai.ContentBlock{Type: "text", Text: text})
	}
	return blocks
}

// handleVoiceMessage processes incoming voice messages by transcribing them via
// the configured Whisper STT API and routing the text through the normal agent
// pipeline.
func (b *Bot) handleVoiceMessage(ctx context.Context, c telebot.Context) error {
	msg := c.Message()
	chatID := msg.Chat.ID
	userID := msg.Sender.ID

	if !b.authManager.CheckAccess(userID, chatID) {
		logDeniedAccess(userID, msg.Sender.Username, chatID, string(msg.Chat.Type))
		return c.Send("🔒 Not authorized.")
	}

	if !b.groupManager.ShouldRespond(chatID, msg, b.api.Me.Username) {
		return nil
	}

	voice := msg.Voice
	if voice == nil {
		return nil
	}

	logger.Debugf("Bot: voice from user=%d chat=%d duration=%ds", userID, chatID, voice.Duration)

	if b.voiceTranscriber == nil || !b.voiceTranscriber.IsAvailable() {
		content := fmt.Sprintf("[Voice message: %ds] (STT not configured)", voice.Duration)
		if err := b.store.SaveMessage(chatID, int64(msg.ID), userID, msg.Sender.Username, content); err != nil {
			log.Printf("Failed to save message: %v", err)
		}
		return c.Send("🎤 Voice message received. Speech-to-text is not configured (set stt.base_url in config).")
	}

	// Show typing while we download + transcribe
	stopTyping := NewTypingIndicator(b.api, msg.Chat)
	defer stopTyping()

	// Download voice file
	reader, err := b.api.File(&voice.File)
	if err != nil {
		log.Printf("[voice] failed to get file: %v", err)
		return c.Send("❌ Failed to download voice message.")
	}
	defer reader.Close()

	data, err := io.ReadAll(reader)
	if err != nil {
		log.Printf("[voice] failed to read file: %v", err)
		return c.Send("❌ Failed to read voice message.")
	}

	// Write to a temp file for the multipart upload
	tmp, err := os.CreateTemp("", "voice_*.ogg")
	if err != nil {
		log.Printf("[voice] failed to create temp file: %v", err)
		return c.Send("❌ Failed to process voice message.")
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		log.Printf("[voice] failed to write temp file: %v", err)
		return c.Send("❌ Failed to process voice message.")
	}
	tmp.Close()

	// Transcribe
	result, err := b.voiceTranscriber.Transcribe(ctx, tmpName)
	if err != nil {
		log.Printf("[voice] transcription failed: %v", err)
		return c.Send("❌ Failed to transcribe voice message. Please try again or type your message.")
	}

	text := strings.TrimSpace(result.Text)
	if text == "" {
		return c.Send("🎤 Could not understand voice message. Please try again.")
	}

	logger.Debugf("Bot: voice transcribed text=%q confidence=%.2f", text, result.Confidence)

	// If confidence is below threshold, warn the user but still process
	if !b.voiceTranscriber.IsHighConfidence(result.Confidence) {
		warning := fmt.Sprintf("🎤 _%s_\n\n⚠️ Low transcription confidence — please correct me if I misheard.", escapeMarkdownV1(text))
		if err := c.Send(warning, &telebot.SendOptions{ParseMode: telebot.ModeMarkdown}); err != nil {
			log.Printf("[voice] failed to send low-confidence warning: %v", err)
		}
	}

	// Process transcribed text through the normal agent pipeline
	if err := b.store.SaveMessage(chatID, int64(msg.ID), userID, msg.Sender.Username, text); err != nil {
		log.Printf("Failed to save message: %v", err)
	}

	delivery := newTelegramDelivery(c)
	sessionKey := sessionKeyForChat(msg.Chat)
	b.sendImmediateAck(delivery.Chat, msg.ID)
	b.debouncer.Debounce(chatID, text, func(combined string) {
		session, err := b.store.GetSession(chatID)
		if err != nil {
			log.Printf("Failed to get session: %v", err)
		}
		b.runViaHubAsync(ctx, delivery, sessionKey, combined, nil, session, nil,
			"❌ Sorry, I encountered an error processing your voice message.", "")
	})

	return nil
}

// escapeMarkdownV1 escapes characters that are special in Telegram's legacy
// Markdown (v1) parse mode.
func escapeMarkdownV1(s string) string {
	r := strings.NewReplacer(
		"_", "\\_",
		"*", "\\*",
		"`", "\\`",
		"[", "\\[",
	)
	return r.Replace(s)
}

// handleStickerMessage processes incoming stickers
func (b *Bot) handleStickerMessage(ctx context.Context, c telebot.Context) error {
	msg := c.Message()
	chatID := msg.Chat.ID
	userID := msg.Sender.ID

	if !b.authManager.CheckAccess(userID, chatID) {
		logDeniedAccess(userID, msg.Sender.Username, chatID, string(msg.Chat.Type))
		return c.Send("🔒 Not authorized.")
	}

	if !b.groupManager.ShouldRespond(chatID, msg, b.api.Me.Username) {
		return nil
	}

	sticker := msg.Sticker
	if sticker == nil {
		return nil
	}

	logger.Debugf("Bot: sticker from user=%d chat=%d emoji=%s", userID, chatID, sticker.Emoji)

	// Process sticker as emoji context
	content := fmt.Sprintf("[Sticker: %s]", sticker.Emoji)

	if err := b.store.SaveMessage(chatID, int64(msg.ID), userID, msg.Sender.Username, content); err != nil {
		log.Printf("Failed to save message: %v", err)
	}

	// Process through pipeline via hub
	delivery := newTelegramDelivery(c)
	sessionKey := sessionKeyForChat(msg.Chat)
	b.sendImmediateAck(delivery.Chat, msg.ID)
	b.debouncer.Debounce(chatID, content, func(combined string) {
		session, err := b.store.GetSession(chatID)
		if err != nil {
			log.Printf("Failed to get session: %v", err)
		}
		b.runViaHubAsync(ctx, delivery, sessionKey, combined, nil, session, nil,
			"❌ Sorry, I encountered an error processing your sticker.", "")
	})

	return nil
}

// handleDocumentMessage processes incoming documents
func (b *Bot) handleDocumentMessage(ctx context.Context, c telebot.Context) error {
	msg := c.Message()
	chatID := msg.Chat.ID
	userID := msg.Sender.ID

	if !b.authManager.CheckAccess(userID, chatID) {
		logDeniedAccess(userID, msg.Sender.Username, chatID, string(msg.Chat.Type))
		return c.Send("🔒 Not authorized.")
	}

	if !b.groupManager.ShouldRespond(chatID, msg, b.api.Me.Username) {
		return nil
	}

	doc := msg.Document
	if doc == nil {
		return nil
	}

	logger.Debugf("Bot: document from user=%d chat=%d name=%s size=%d", userID, chatID, doc.FileName, doc.FileSize)

	caption := msg.Caption
	if caption == "" {
		caption = "User sent a document."
	}

	content := fmt.Sprintf("[Document: %s, %d bytes] %s", doc.FileName, doc.FileSize, caption)

	if err := b.store.SaveMessage(chatID, int64(msg.ID), userID, msg.Sender.Username, content); err != nil {
		log.Printf("Failed to save message: %v", err)
	}

	delivery := newTelegramDelivery(c)
	sessionKey := sessionKeyForChat(msg.Chat)
	b.sendImmediateAck(delivery.Chat, msg.ID)
	b.debouncer.Debounce(chatID, content, func(combined string) {
		session, err := b.store.GetSession(chatID)
		if err != nil {
			log.Printf("Failed to get session: %v", err)
		}
		b.runViaHubAsync(ctx, delivery, sessionKey, combined, nil, session, nil,
			"❌ Sorry, I encountered an error processing your document.", "")
	})

	return nil
}
