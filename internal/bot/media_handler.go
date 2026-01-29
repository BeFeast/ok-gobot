package bot

import (
	"context"
	"fmt"
	"io"
	"log"
	"sync"
	"time"

	"gopkg.in/telebot.v4"

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
	b.api.Handle(telebot.OnPhoto, func(c telebot.Context) error {
		return b.handlePhotoMessage(ctx, c)
	})

	// Handle voice messages
	b.api.Handle(telebot.OnVoice, func(c telebot.Context) error {
		return b.handleVoiceMessage(ctx, c)
	})

	// Handle stickers (static only)
	b.api.Handle(telebot.OnSticker, func(c telebot.Context) error {
		return b.handleStickerMessage(ctx, c)
	})

	// Handle documents
	b.api.Handle(telebot.OnDocument, func(c telebot.Context) error {
		return b.handleDocumentMessage(ctx, c)
	})
}

// handlePhotoMessage processes incoming photos
func (b *Bot) handlePhotoMessage(ctx context.Context, c telebot.Context) error {
	msg := c.Message()
	chatID := msg.Chat.ID
	userID := msg.Sender.ID

	// Auth check
	if !b.authManager.CheckAccess(userID, chatID) {
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

	b.debouncer.Debounce(chatID, content, func(combined string) {
		session, err := b.store.GetSession(chatID)
		if err != nil {
			log.Printf("Failed to get session: %v", err)
		}
		if err := b.handleAgentRequest(ctx, c, combined, session); err != nil {
			log.Printf("Failed to handle photo request: %v", err)
			c.Send("❌ Sorry, I encountered an error processing your photo.")
		}
	})

	return nil
}

// handleVoiceMessage processes incoming voice messages
func (b *Bot) handleVoiceMessage(ctx context.Context, c telebot.Context) error {
	msg := c.Message()
	chatID := msg.Chat.ID
	userID := msg.Sender.ID

	if !b.authManager.CheckAccess(userID, chatID) {
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

	// For now, inform the user that voice is received but transcription is pending
	content := fmt.Sprintf("[Voice message: %ds duration] (transcription not yet implemented)", voice.Duration)

	if err := b.store.SaveMessage(chatID, int64(msg.ID), userID, msg.Sender.Username, content); err != nil {
		log.Printf("Failed to save message: %v", err)
	}

	return c.Send("🎤 Voice message received. Transcription is not yet implemented.")
}

// handleStickerMessage processes incoming stickers
func (b *Bot) handleStickerMessage(ctx context.Context, c telebot.Context) error {
	msg := c.Message()
	chatID := msg.Chat.ID
	userID := msg.Sender.ID

	if !b.authManager.CheckAccess(userID, chatID) {
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

	// Process through pipeline
	b.debouncer.Debounce(chatID, content, func(combined string) {
		session, err := b.store.GetSession(chatID)
		if err != nil {
			log.Printf("Failed to get session: %v", err)
		}
		if err := b.handleAgentRequest(ctx, c, combined, session); err != nil {
			log.Printf("Failed to handle sticker request: %v", err)
		}
	})

	return nil
}

// handleDocumentMessage processes incoming documents
func (b *Bot) handleDocumentMessage(ctx context.Context, c telebot.Context) error {
	msg := c.Message()
	chatID := msg.Chat.ID
	userID := msg.Sender.ID

	if !b.authManager.CheckAccess(userID, chatID) {
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

	b.debouncer.Debounce(chatID, content, func(combined string) {
		session, err := b.store.GetSession(chatID)
		if err != nil {
			log.Printf("Failed to get session: %v", err)
		}
		if err := b.handleAgentRequest(ctx, c, combined, session); err != nil {
			log.Printf("Failed to handle document request: %v", err)
		}
	})

	return nil
}
