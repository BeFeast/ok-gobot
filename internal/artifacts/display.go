package artifacts

import (
	"fmt"
	"mime"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"ok-gobot/internal/storage"
)

const (
	KindArtifact   = "artifact"
	KindImage      = "image"
	KindTextReport = "text_report"
	KindURL        = "url"
)

// Info is the API-safe representation of a persisted job artifact.
type Info struct {
	ID           int64           `json:"id"`
	JobID        string          `json:"job_id"`
	Type         string          `json:"type"`
	ArtifactType string          `json:"artifact_type,omitempty"`
	Label        string          `json:"label"`
	Name         string          `json:"name,omitempty"`
	MimeType     string          `json:"mime_type,omitempty"`
	Content      string          `json:"content,omitempty"`
	Path         string          `json:"path,omitempty"`
	URL          string          `json:"url,omitempty"`
	URI          string          `json:"uri,omitempty"`
	CreatedAt    string          `json:"created_at"`
	Display      DisplayMetadata `json:"display"`
}

// DisplayMetadata tells Mission Control how an artifact can be displayed
// without trusting arbitrary persisted paths or URI schemes.
type DisplayMetadata struct {
	Kind    string `json:"kind"`
	Safe    bool   `json:"safe"`
	Preview bool   `json:"preview,omitempty"`
	Inline  bool   `json:"inline,omitempty"`
	Href    string `json:"href,omitempty"`
	Reason  string `json:"reason,omitempty"`
}

// Serializer converts persisted artifact rows into display-safe API objects.
type Serializer struct {
	Roots            []string
	ContentURLPrefix string
}

// NewSerializer builds a serializer using the supplied artifact roots and URL
// prefix for safe local artifact content. The prefix should not include a
// trailing slash, for example "/api/artifacts".
func NewSerializer(roots []string, contentURLPrefix string) Serializer {
	return Serializer{
		Roots:            NormalizeRoots(roots),
		ContentURLPrefix: strings.TrimRight(strings.TrimSpace(contentURLPrefix), "/"),
	}
}

// DefaultRoots returns the built-in artifact roots used when config does not
// provide explicit roots. OK_GOBOT_ARTIFACT_ROOTS may add roots using the OS
// path-list separator.
func DefaultRoots() []string {
	var roots []string
	if home, err := os.UserHomeDir(); err == nil && strings.TrimSpace(home) != "" {
		roots = append(roots,
			filepath.Join(home, ".ok-gobot", "screenshots"),
			filepath.Join(home, ".ok-gobot", "artifacts"),
		)
	}
	if env := strings.TrimSpace(os.Getenv("OK_GOBOT_ARTIFACT_ROOTS")); env != "" {
		roots = append(roots, filepath.SplitList(env)...)
	}
	return NormalizeRoots(roots)
}

// NormalizeRoots expands and canonicalizes artifact root paths.
func NormalizeRoots(roots []string) []string {
	seen := make(map[string]struct{}, len(roots))
	out := make([]string, 0, len(roots))
	for _, root := range roots {
		root = strings.TrimSpace(root)
		if root == "" {
			continue
		}
		path, err := canonicalPath(root)
		if err != nil {
			continue
		}
		if _, ok := seen[path]; ok {
			continue
		}
		seen[path] = struct{}{}
		out = append(out, path)
	}
	return out
}

// Serialize converts one persisted artifact to a display-safe API object.
func (s Serializer) Serialize(artifact storage.JobArtifact) Info {
	if len(s.Roots) == 0 {
		s.Roots = DefaultRoots()
	}

	kind := displayKind(artifact)
	info := Info{
		ID:           artifact.ID,
		JobID:        artifact.JobID,
		Type:         artifact.ArtifactType,
		ArtifactType: artifact.ArtifactType,
		Label:        artifact.Name,
		Name:         artifact.Name,
		MimeType:     artifact.MimeType,
		Content:      artifact.Content,
		CreatedAt:    artifact.CreatedAt,
		Display: DisplayMetadata{
			Kind: kind,
			Safe: artifact.URI == "",
		},
	}

	if info.Type == "" {
		info.Type = KindArtifact
	}
	if info.Label == "" {
		info.Label = info.Type
	}

	rawURI := strings.TrimSpace(artifact.URI)
	switch {
	case rawURI == "":
		if artifact.Content != "" {
			info.Display.Safe = true
			info.Display.Inline = true
		}
	case isSafeRemoteURL(rawURI) || isSafeImageDataURL(rawURI):
		info.URL = rawURI
		info.URI = rawURI
		info.Display.Safe = true
		info.Display.Href = rawURI
		if kind == KindArtifact {
			info.Display.Kind = KindURL
		}
		info.Display.Preview = info.Display.Kind == KindImage || info.Display.Kind == KindURL
	case isLocalURI(rawURI):
		path, ok := SafeLocalPath(rawURI, s.Roots)
		if !ok {
			info.Display.Safe = false
			info.Display.Reason = "local artifact is outside configured artifact roots"
			break
		}
		info.Path = path
		info.URI = path
		info.Display.Safe = true
		info.Display.Href = s.contentHref(artifact.ID)
		info.Display.Preview = info.Display.Kind == KindImage
		info.Display.Inline = info.Display.Kind == KindTextReport
	default:
		info.Display.Safe = false
		info.Display.Reason = "unsupported artifact URI scheme"
	}

	if artifact.Content != "" && info.Display.Kind == KindTextReport && info.Display.Safe {
		info.Display.Inline = true
	}

	return info
}

// SerializeAll converts persisted artifact rows to display-safe API objects.
func (s Serializer) SerializeAll(rows []storage.JobArtifact) []Info {
	if rows == nil {
		return []Info{}
	}
	out := make([]Info, len(rows))
	for i, row := range rows {
		out[i] = s.Serialize(row)
	}
	return out
}

// FormatProofHints renders artifact references that are safe to show in chat
// surfaces. It intentionally never includes local filesystem paths.
func FormatProofHints(infos []Info, max int) []string {
	if len(infos) == 0 {
		return nil
	}
	if max <= 0 || max > len(infos) {
		max = len(infos)
	}

	hints := make([]string, 0, max+1)
	for i := 0; i < max; i++ {
		hints = append(hints, FormatProofHint(infos[i]))
	}
	if remaining := len(infos) - max; remaining > 0 {
		hints = append(hints, fmt.Sprintf("...and %d more artifact(s)", remaining))
	}
	return hints
}

// FormatProofHint renders one artifact reference without exposing unsafe local
// paths. Safe remote URLs may be shown directly; local files are referenced by
// durable artifact ID instead.
func FormatProofHint(info Info) string {
	label := artifactHintLabel(info)
	if info.ID > 0 {
		label = fmt.Sprintf("%s (#%d)", label, info.ID)
	}

	switch {
	case strings.TrimSpace(info.URL) != "":
		return fmt.Sprintf("%s: %s", label, info.URL)
	case info.Display.Safe && info.Display.Kind == KindImage && info.Path != "":
		return fmt.Sprintf("%s: safe local image artifact", label)
	case info.Display.Safe && strings.TrimSpace(info.Display.Href) != "":
		return fmt.Sprintf("%s: %s", label, info.Display.Href)
	case info.Display.Safe && info.Display.Inline:
		return fmt.Sprintf("%s: inline %s artifact", label, displayKindLabel(info.Display.Kind))
	case info.Display.Safe:
		return fmt.Sprintf("%s: stored %s artifact", label, displayKindLabel(info.Display.Kind))
	default:
		reason := strings.TrimSpace(info.Display.Reason)
		if reason == "" {
			reason = "not safe to display"
		}
		return fmt.Sprintf("%s: hidden (%s)", label, reason)
	}
}

// FirstSafeLocalImage returns the first image artifact backed by a safe local
// path. Callers may upload the path but should not include it in user-visible
// text.
func FirstSafeLocalImage(infos []Info) (Info, bool) {
	for _, info := range infos {
		if info.Display.Safe && info.Display.Kind == KindImage && strings.TrimSpace(info.Path) != "" {
			return info, true
		}
	}
	return Info{}, false
}

func artifactHintLabel(info Info) string {
	for _, candidate := range []string{info.Label, info.Name, info.Type, info.ArtifactType} {
		candidate = strings.TrimSpace(candidate)
		if candidate != "" {
			return truncateRunes(candidate, 80)
		}
	}
	return "artifact"
}

func displayKindLabel(kind string) string {
	if strings.TrimSpace(kind) == "" {
		return KindArtifact
	}
	return strings.TrimSpace(kind)
}

func truncateRunes(s string, max int) string {
	if max <= 0 {
		return ""
	}
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	if max <= 3 {
		return string(runes[:max])
	}
	return string(runes[:max-3]) + "..."
}

// SafeLocalPath returns a canonical local path only when rawURI points inside
// one of the configured roots.
func SafeLocalPath(rawURI string, roots []string) (string, bool) {
	local, ok := localPathFromURI(rawURI)
	if !ok {
		return "", false
	}
	path, err := canonicalPath(local)
	if err != nil {
		return "", false
	}
	for _, root := range NormalizeRoots(roots) {
		if pathInsideRoot(path, root) {
			return path, true
		}
	}
	return "", false
}

// ContentPath returns the safe local file path for an artifact content endpoint.
func ContentPath(artifact storage.JobArtifact, roots []string) (string, error) {
	path, ok := SafeLocalPath(artifact.URI, roots)
	if !ok {
		return "", fmt.Errorf("artifact file is outside configured artifact roots")
	}
	stat, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if stat.IsDir() {
		return "", fmt.Errorf("artifact path is a directory")
	}
	return path, nil
}

func (s Serializer) contentHref(id int64) string {
	if s.ContentURLPrefix == "" || id <= 0 {
		return ""
	}
	return s.ContentURLPrefix + "/" + strconv.FormatInt(id, 10) + "/content"
}

func displayKind(artifact storage.JobArtifact) string {
	typeName := strings.ToLower(strings.TrimSpace(artifact.ArtifactType))
	mimeType := strings.ToLower(strings.TrimSpace(artifact.MimeType))
	uri := strings.ToLower(strings.TrimSpace(artifact.URI))
	name := strings.ToLower(strings.TrimSpace(artifact.Name))

	if isImageType(typeName, mimeType, uri, name) {
		return KindImage
	}
	if typeName == KindTextReport || typeName == "report" || strings.Contains(typeName, "report") || strings.HasPrefix(mimeType, "text/") {
		return KindTextReport
	}
	if artifact.Content != "" {
		return KindTextReport
	}
	if isSafeRemoteURL(artifact.URI) {
		return KindURL
	}
	return KindArtifact
}

func isImageType(typeName, mimeType, uri, name string) bool {
	if typeName == "screenshot" || typeName == "image" || strings.HasPrefix(typeName, "image_") {
		return true
	}
	if strings.HasPrefix(mimeType, "image/") {
		return true
	}
	if isImagePath(uri) || isImagePath(name) {
		return true
	}
	return false
}

func isImagePath(path string) bool {
	ext := strings.ToLower(filepath.Ext(path))
	if ext == "" {
		return false
	}
	if mt := mime.TypeByExtension(ext); strings.HasPrefix(mt, "image/") {
		return true
	}
	switch ext {
	case ".png", ".jpg", ".jpeg", ".gif", ".webp":
		return true
	}
	return false
}

func isSafeRemoteURL(raw string) bool {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u == nil {
		return false
	}
	scheme := strings.ToLower(u.Scheme)
	return (scheme == "http" || scheme == "https") && u.Host != ""
}

func isSafeImageDataURL(raw string) bool {
	lower := strings.ToLower(strings.TrimSpace(raw))
	allowed := []string{
		"data:image/png;base64,",
		"data:image/jpeg;base64,",
		"data:image/jpg;base64,",
		"data:image/gif;base64,",
		"data:image/webp;base64,",
	}
	for _, prefix := range allowed {
		if strings.HasPrefix(lower, prefix) {
			return true
		}
	}
	return false
}

func isLocalURI(raw string) bool {
	_, ok := localPathFromURI(raw)
	return ok
}

func localPathFromURI(raw string) (string, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", false
	}
	if strings.HasPrefix(raw, "~/") {
		return raw, true
	}
	if filepath.IsAbs(raw) {
		return raw, true
	}
	u, err := url.Parse(raw)
	if err != nil || u == nil {
		return "", false
	}
	if strings.ToLower(u.Scheme) != "file" {
		return "", false
	}
	if u.Host != "" && u.Host != "localhost" {
		return "", false
	}
	return u.Path, u.Path != ""
}

func canonicalPath(path string) (string, error) {
	path = strings.TrimSpace(path)
	if strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		path = filepath.Join(home, path[2:])
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	abs = filepath.Clean(abs)
	if evaluated, err := filepath.EvalSymlinks(abs); err == nil {
		abs = filepath.Clean(evaluated)
	}
	return abs, nil
}

func pathInsideRoot(path, root string) bool {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator)))
}
