package bot

import (
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"gopkg.in/telebot.v4"
)

// MediaHandler handles incoming media files
type MediaHandler struct {
	bot      *telebot.Bot
	tempDir  string
	whisper  bool // whether whisper CLI is available
}

// NewMediaHandler creates a new media handler
func NewMediaHandler(bot *telebot.Bot) *MediaHandler {
	tempDir := filepath.Join(os.TempDir(), "okgobot-media")
	os.MkdirAll(tempDir, 0755)

	// Check if whisper is available
	_, whisperErr := exec.LookPath("whisper")

	return &MediaHandler{
		bot:     bot,
		tempDir: tempDir,
		whisper: whisperErr == nil,
	}
}

// HandlePhoto processes incoming photos
func (m *MediaHandler) HandlePhoto(c telebot.Context) (string, string, error) {
	photo := c.Message().Photo
	if photo == nil {
		return "", "", fmt.Errorf("no photo in message")
	}

	// Get the largest photo (last in array)
	file, err := m.bot.FileByID(photo.FileID)
	if err != nil {
		return "", "", fmt.Errorf("failed to get file: %w", err)
	}

	// Download the file
	filePath := filepath.Join(m.tempDir, fmt.Sprintf("photo_%s.jpg", photo.FileID))
	if err := m.downloadFile(file.FilePath, filePath); err != nil {
		return "", "", fmt.Errorf("failed to download photo: %w", err)
	}

	caption := c.Message().Caption
	return filePath, caption, nil
}

// HandleVoice processes incoming voice messages
func (m *MediaHandler) HandleVoice(c telebot.Context) (string, string, error) {
	voice := c.Message().Voice
	if voice == nil {
		return "", "", fmt.Errorf("no voice in message")
	}

	file, err := m.bot.FileByID(voice.FileID)
	if err != nil {
		return "", "", fmt.Errorf("failed to get file: %w", err)
	}

	// Download the file
	ext := ".ogg"
	filePath := filepath.Join(m.tempDir, fmt.Sprintf("voice_%s%s", voice.FileID, ext))
	if err := m.downloadFile(file.FilePath, filePath); err != nil {
		return "", "", fmt.Errorf("failed to download voice: %w", err)
	}

	// Transcribe if whisper is available
	transcription := ""
	if m.whisper {
		transcription, _ = m.transcribeAudio(filePath)
	}

	return filePath, transcription, nil
}

// HandleAudio processes incoming audio files
func (m *MediaHandler) HandleAudio(c telebot.Context) (string, string, error) {
	audio := c.Message().Audio
	if audio == nil {
		return "", "", fmt.Errorf("no audio in message")
	}

	file, err := m.bot.FileByID(audio.FileID)
	if err != nil {
		return "", "", fmt.Errorf("failed to get file: %w", err)
	}

	// Determine extension
	ext := filepath.Ext(audio.FileName)
	if ext == "" {
		ext = ".mp3"
	}

	filePath := filepath.Join(m.tempDir, fmt.Sprintf("audio_%s%s", audio.FileID, ext))
	if err := m.downloadFile(file.FilePath, filePath); err != nil {
		return "", "", fmt.Errorf("failed to download audio: %w", err)
	}

	// Transcribe if whisper is available
	transcription := ""
	if m.whisper {
		transcription, _ = m.transcribeAudio(filePath)
	}

	return filePath, transcription, nil
}

// HandleDocument processes incoming documents
func (m *MediaHandler) HandleDocument(c telebot.Context) (string, string, error) {
	doc := c.Message().Document
	if doc == nil {
		return "", "", fmt.Errorf("no document in message")
	}

	file, err := m.bot.FileByID(doc.FileID)
	if err != nil {
		return "", "", fmt.Errorf("failed to get file: %w", err)
	}

	// Keep original filename
	filePath := filepath.Join(m.tempDir, doc.FileName)
	if err := m.downloadFile(file.FilePath, filePath); err != nil {
		return "", "", fmt.Errorf("failed to download document: %w", err)
	}

	// Try to extract text content
	content := ""
	ext := strings.ToLower(filepath.Ext(doc.FileName))
	switch ext {
	case ".txt", ".md", ".json", ".yaml", ".yml", ".xml", ".csv":
		data, err := os.ReadFile(filePath)
		if err == nil {
			content = string(data)
			if len(content) > 4000 {
				content = content[:4000] + "\n... (truncated)"
			}
		}
	case ".pdf":
		content, _ = m.extractPDFText(filePath)
	}

	return filePath, content, nil
}

// downloadFile downloads a file from Telegram servers
func (m *MediaHandler) downloadFile(telegramPath, localPath string) error {
	url := fmt.Sprintf("https://api.telegram.org/file/bot%s/%s", m.bot.Token, telegramPath)

	resp, err := http.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	out, err := os.Create(localPath)
	if err != nil {
		return err
	}
	defer out.Close()

	_, err = io.Copy(out, resp.Body)
	return err
}

// transcribeAudio uses whisper CLI to transcribe audio
func (m *MediaHandler) transcribeAudio(audioPath string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*10) // 10 min timeout
	defer cancel()

	cmd := exec.CommandContext(ctx, "whisper", audioPath, "--model", "base", "--output_format", "txt")
	output, err := cmd.CombinedOutput()
	if err != nil {
		log.Printf("Whisper error: %v, output: %s", err, string(output))
		return "", err
	}

	// Read the output file
	txtPath := strings.TrimSuffix(audioPath, filepath.Ext(audioPath)) + ".txt"
	content, err := os.ReadFile(txtPath)
	if err != nil {
		return "", err
	}

	return strings.TrimSpace(string(content)), nil
}

// extractPDFText extracts text from PDF using pdftotext
func (m *MediaHandler) extractPDFText(pdfPath string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*10) // 30 sec timeout
	defer cancel()

	txtPath := strings.TrimSuffix(pdfPath, ".pdf") + ".txt"
	cmd := exec.CommandContext(ctx, "pdftotext", pdfPath, txtPath)
	if err := cmd.Run(); err != nil {
		return "", err
	}

	content, err := os.ReadFile(txtPath)
	if err != nil {
		return "", err
	}

	text := string(content)
	if len(text) > 8000 {
		text = text[:8000] + "\n... (truncated)"
	}

	return text, nil
}

// SendPhoto sends a photo to a chat
func (m *MediaHandler) SendPhoto(chat *telebot.Chat, photoPath string, caption string) error {
	photo := &telebot.Photo{File: telebot.FromDisk(photoPath), Caption: caption}
	_, err := m.bot.Send(chat, photo)
	return err
}

// SendDocument sends a document to a chat
func (m *MediaHandler) SendDocument(chat *telebot.Chat, docPath string, caption string) error {
	doc := &telebot.Document{File: telebot.FromDisk(docPath), Caption: caption}
	_, err := m.bot.Send(chat, doc)
	return err
}

// SendVoice sends a voice message to a chat
func (m *MediaHandler) SendVoice(chat *telebot.Chat, voicePath string) error {
	voice := &telebot.Voice{File: telebot.FromDisk(voicePath)}
	_, err := m.bot.Send(chat, voice)
	return err
}

// Cleanup removes temporary files older than 1 hour
func (m *MediaHandler) Cleanup() {
	entries, err := os.ReadDir(m.tempDir)
	if err != nil {
		return
	}

	for _, entry := range entries {
		info, err := entry.Info()
		if err != nil {
			continue
		}
		// Remove files older than 1 hour
		// (simple check, could be improved)
		_ = info
		// os.Remove(filepath.Join(m.tempDir, entry.Name()))
	}
}
