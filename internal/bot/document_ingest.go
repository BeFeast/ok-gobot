package bot

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"gopkg.in/telebot.v4"
)

// maxInlineDocumentBytes bounds how much of a text document is pasted into the
// model's turn. Larger files are only saved; the model reads them with the
// file tool.
const maxInlineDocumentBytes = 48 * 1024

// documentInboxDirName is the directory under the soul workspace where
// incoming Telegram documents are kept.
const documentInboxDirName = "inbox"

type savedDocument struct {
	path   string
	size   int64
	inline string
}

// saveIncomingDocument downloads a Telegram document into the soul inbox and
// returns where it landed. Text documents small enough to inline are also
// returned as a string.
func (b *Bot) saveIncomingDocument(doc *telebot.Document, msgID int) (savedDocument, error) {
	if doc == nil {
		return savedDocument{}, fmt.Errorf("no document")
	}
	if doc.FileSize > maxMediaSize {
		return savedDocument{}, fmt.Errorf("document is too large (%d bytes, max %d)", doc.FileSize, maxMediaSize)
	}
	reader, err := b.api.File(&doc.File)
	if err != nil {
		return savedDocument{}, fmt.Errorf("download: %w", err)
	}
	defer reader.Close()
	data, err := io.ReadAll(io.LimitReader(reader, maxMediaSize+1))
	if err != nil {
		return savedDocument{}, fmt.Errorf("read: %w", err)
	}
	if len(data) > maxMediaSize {
		return savedDocument{}, fmt.Errorf("document is too large (max %d bytes)", maxMediaSize)
	}

	dir := documentInboxDir(b.skillSuggestionSoulPath())
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return savedDocument{}, fmt.Errorf("inbox: %w", err)
	}
	path := filepath.Join(dir, fmt.Sprintf("%d-%s", msgID, sanitizeDocumentName(doc.FileName)))
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return savedDocument{}, fmt.Errorf("write: %w", err)
	}

	saved := savedDocument{path: path, size: int64(len(data))}
	if isTextDocument(doc.FileName, doc.MIME) && len(data) <= maxInlineDocumentBytes && utf8.Valid(data) {
		saved.inline = string(data)
	}
	return saved, nil
}

// documentInboxDir is <soul>/inbox, or a temp directory when no soul path is
// configured.
func documentInboxDir(soulPath string) string {
	if strings.TrimSpace(soulPath) != "" {
		return filepath.Join(soulPath, documentInboxDirName)
	}
	return filepath.Join(os.TempDir(), "okgobot-media", documentInboxDirName)
}

// describeSavedDocument builds the user turn the model sees for a saved
// document: where the file is, the caption, and the text itself when it was
// small enough to inline.
func describeSavedDocument(name, caption string, saved savedDocument) string {
	if strings.TrimSpace(name) == "" {
		name = filepath.Base(saved.path)
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "[Document: %s, %d bytes] saved to %s", name, saved.size, saved.path)
	if saved.inline == "" {
		sb.WriteString(" — read it with the file tool.")
	}
	if strings.TrimSpace(caption) != "" {
		sb.WriteString("\n")
		sb.WriteString(caption)
	}
	if saved.inline != "" {
		fmt.Fprintf(&sb, "\n\n--- contents of %s ---\n%s\n--- end of %s ---", name, strings.TrimRight(saved.inline, "\n"), name)
	}
	return sb.String()
}

var textDocumentExtensions = map[string]bool{
	".md": true, ".markdown": true, ".txt": true, ".text": true, ".rst": true, ".org": true,
	".json": true, ".yaml": true, ".yml": true, ".toml": true, ".ini": true, ".cfg": true, ".conf": true,
	".csv": true, ".tsv": true, ".log": true, ".xml": true, ".html": true, ".htm": true, ".svg": true,
	".go": true, ".py": true, ".js": true, ".ts": true, ".tsx": true, ".jsx": true, ".sh": true, ".bash": true,
	".rb": true, ".rs": true, ".c": true, ".h": true, ".cpp": true, ".hpp": true, ".java": true, ".kt": true,
	".sql": true, ".env": true, ".gitignore": true, ".dockerfile": true,
}

// isTextDocument reports whether a document can be handed to the model as
// text, judged by extension first and MIME type second.
func isTextDocument(name, mime string) bool {
	ext := strings.ToLower(filepath.Ext(name))
	if textDocumentExtensions[ext] {
		return true
	}
	mime = strings.ToLower(strings.TrimSpace(mime))
	if strings.HasPrefix(mime, "text/") {
		return true
	}
	switch mime {
	case "application/json", "application/xml", "application/x-yaml", "application/yaml", "application/toml", "application/x-sh":
		return true
	}
	return false
}

// sanitizeDocumentName keeps a Telegram file name safe to use as a single
// path element.
func sanitizeDocumentName(name string) string {
	name = filepath.Base(strings.TrimSpace(strings.ReplaceAll(name, "\\", "/")))
	if name == "." || name == "/" || name == "" {
		name = "document"
	}
	var sb strings.Builder
	for _, r := range name {
		switch {
		case r < 0x20, r == 0x7f, r == '/', r == '\\', r == ':':
			sb.WriteRune('_')
		default:
			sb.WriteRune(r)
		}
	}
	out := strings.Trim(sb.String(), ". ")
	if out == "" {
		out = "document"
	}
	if len(out) > 120 {
		out = out[:120]
	}
	return out
}
