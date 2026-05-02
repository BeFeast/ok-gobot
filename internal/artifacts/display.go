package artifacts

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

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
	Metadata     *Metadata       `json:"metadata,omitempty"`
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

// Metadata stores verification fields that make a persisted artifact auditable
// without trusting a raw URI by itself.
type Metadata struct {
	Kind           string `json:"kind,omitempty"`
	NormalizedPath string `json:"normalized_path,omitempty"`
	SizeBytes      *int64 `json:"size_bytes,omitempty"`
	SHA256         string `json:"sha256,omitempty"`
	Producer       string `json:"producer,omitempty"`
	CreatedAt      string `json:"created_at,omitempty"`
}

type localPathValidation struct {
	Path   string
	Info   os.FileInfo
	Reason string
	Err    error
	Safe   bool
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
		Roots:            ConfiguredRoots(roots),
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

// ConfiguredRoots returns absolute root values without resolving symlinks when
// at least one can be canonicalized. Keeping the configured symlink spelling
// lets path validation accept artifacts under a symlinked root before verifying
// their symlink-resolved path against canonical roots.
func ConfiguredRoots(roots []string) []string {
	out := make([]string, 0, len(roots))
	for _, root := range roots {
		root = strings.TrimSpace(root)
		if root == "" {
			continue
		}
		path, err := absoluteCleanPath(root)
		if err != nil {
			continue
		}
		out = append(out, path)
	}
	if len(NormalizeRoots(out)) == 0 {
		return nil
	}
	return out
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
	metadata := ParseMetadata(artifact.Metadata)
	info := Info{
		ID:           artifact.ID,
		JobID:        artifact.JobID,
		Type:         artifact.ArtifactType,
		ArtifactType: artifact.ArtifactType,
		Label:        artifact.Name,
		Name:         artifact.Name,
		MimeType:     artifact.MimeType,
		CreatedAt:    artifact.CreatedAt,
		Display: DisplayMetadata{
			Kind: kind,
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
		if artifact.Content == "" {
			hideArtifact(&info, "unsupported artifact kind")
			break
		}
		if reason := verifyContentMetadata(artifact.Content, metadata); reason != "" {
			hideArtifact(&info, reason)
			break
		}
		info.Content = artifact.Content
		info.Display.Safe = true
		info.Display.Inline = true
		info.Metadata = displayMetadata(metadata, artifact, kind, "", nil)
	case isSafeRemoteURL(rawURI) || isSafeImageDataURL(rawURI):
		info.URL = rawURI
		info.URI = rawURI
		info.Display.Safe = true
		info.Display.Href = rawURI
		if kind == KindArtifact {
			info.Display.Kind = KindURL
		}
		info.Display.Preview = info.Display.Kind == KindImage || info.Display.Kind == KindURL
		info.Metadata = displayMetadata(metadata, artifact, info.Display.Kind, "", nil)
	case isLocalURI(rawURI):
		local := validateLocalPath(rawURI, s.Roots)
		if !local.Safe {
			hideArtifact(&info, local.Reason)
			break
		}
		if !supportedLocalArtifactKind(artifact, kind) {
			hideArtifact(&info, "unsupported artifact kind")
			break
		}
		if reason := verifyLocalMetadata(metadata, local.Path, local.Info); reason != "" {
			hideArtifact(&info, reason)
			break
		}
		info.Path = local.Path
		info.URI = local.Path
		info.Display.Safe = true
		info.Display.Href = s.contentHref(artifact.ID)
		info.Display.Preview = info.Display.Kind == KindImage
		info.Display.Inline = info.Display.Kind == KindTextReport
		info.Metadata = displayMetadata(metadata, artifact, kind, local.Path, local.Info)
	default:
		hideArtifact(&info, "unsupported artifact URI scheme")
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

func hideArtifact(info *Info, reason string) {
	if info == nil {
		return
	}
	info.Path = ""
	info.URL = ""
	info.URI = ""
	info.Content = ""
	info.Metadata = nil
	info.Display.Safe = false
	info.Display.Preview = false
	info.Display.Inline = false
	info.Display.Href = ""
	info.Display.Reason = strings.TrimSpace(reason)
	if info.Display.Reason == "" {
		info.Display.Reason = "not safe to display"
	}
}

func displayMetadata(stored *Metadata, artifact storage.JobArtifact, kind, normalizedPath string, fileInfo os.FileInfo) *Metadata {
	meta := cloneMetadata(stored)
	if meta == nil {
		meta = &Metadata{}
	}
	if strings.TrimSpace(kind) != "" {
		meta.Kind = strings.TrimSpace(kind)
	}
	if normalizedPath != "" {
		meta.NormalizedPath = normalizedPath
	}
	if meta.CreatedAt == "" {
		meta.CreatedAt = strings.TrimSpace(artifact.CreatedAt)
	}
	if meta.SizeBytes == nil {
		switch {
		case fileInfo != nil:
			setMetadataSize(meta, fileInfo.Size())
		case artifact.Content != "":
			setMetadataSize(meta, int64(len([]byte(artifact.Content))))
		}
	}
	if meta.SHA256 == "" && artifact.Content != "" && normalizedPath == "" {
		meta.SHA256 = sha256HexString(artifact.Content)
	}
	if metadataEmpty(meta) {
		return nil
	}
	return meta
}

func cloneMetadata(meta *Metadata) *Metadata {
	if meta == nil {
		return nil
	}
	clone := *meta
	if meta.SizeBytes != nil {
		size := *meta.SizeBytes
		clone.SizeBytes = &size
	}
	return &clone
}

func setMetadataSize(meta *Metadata, size int64) {
	if meta == nil {
		return
	}
	meta.SizeBytes = new(int64)
	*meta.SizeBytes = size
}

func metadataEmpty(meta *Metadata) bool {
	if meta == nil {
		return true
	}
	return meta.Kind == "" && meta.NormalizedPath == "" && meta.SizeBytes == nil && meta.SHA256 == "" && meta.Producer == "" && meta.CreatedAt == ""
}

// ParseMetadata extracts known verification metadata fields from a persisted
// artifact metadata JSON object. Unknown fields are intentionally ignored so
// callers do not expose arbitrary producer-provided metadata.
func ParseMetadata(raw string) *Metadata {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	var meta Metadata
	if err := json.Unmarshal([]byte(raw), &meta); err != nil {
		return nil
	}
	meta.Kind = strings.TrimSpace(meta.Kind)
	meta.NormalizedPath = strings.TrimSpace(meta.NormalizedPath)
	meta.SHA256 = strings.ToLower(strings.TrimSpace(meta.SHA256))
	meta.Producer = strings.TrimSpace(meta.Producer)
	meta.CreatedAt = strings.TrimSpace(meta.CreatedAt)
	if metadataEmpty(&meta) {
		return nil
	}
	return &meta
}

// BuildMetadata derives verification metadata for an artifact at persistence
// time. It hashes inline content only; callers that have configured artifact
// roots should use BuildMetadataForRoots for local files.
func BuildMetadata(artifact storage.JobArtifact, producer string, createdAt time.Time) Metadata {
	return buildMetadata(artifact, producer, createdAt, nil)
}

// BuildMetadataForRoots derives verification metadata for an artifact at
// persistence time. Local files are hashed only after they resolve inside one
// of the configured artifact roots.
func BuildMetadataForRoots(artifact storage.JobArtifact, producer string, createdAt time.Time, roots []string) Metadata {
	return buildMetadata(artifact, producer, createdAt, effectiveRoots(roots))
}

func buildMetadata(artifact storage.JobArtifact, producer string, createdAt time.Time, roots []string) Metadata {
	meta := Metadata{
		Kind:      displayKind(artifact),
		Producer:  strings.TrimSpace(producer),
		CreatedAt: createdAt.UTC().Format(time.RFC3339Nano),
	}

	if isLocalURI(artifact.URI) {
		if len(roots) == 0 {
			return meta
		}
		local := validateLocalPath(artifact.URI, roots)
		if local.Safe {
			meta.NormalizedPath = local.Path
			info := local.Info
			setMetadataSize(&meta, info.Size())
			if sum, err := fileSHA256(local.Path); err == nil {
				meta.SHA256 = sum
			}
		}
		return meta
	}

	if artifact.Content != "" {
		setMetadataSize(&meta, int64(len([]byte(artifact.Content))))
		meta.SHA256 = sha256HexString(artifact.Content)
	}
	return meta
}

func effectiveRoots(roots []string) []string {
	configured := ConfiguredRoots(roots)
	if len(configured) > 0 {
		return configured
	}
	return DefaultRoots()
}

func verifyContentMetadata(content string, meta *Metadata) string {
	if meta == nil {
		return ""
	}
	size := int64(len([]byte(content)))
	if meta.SizeBytes != nil && *meta.SizeBytes != size {
		return "artifact metadata size does not match content"
	}
	if meta.SHA256 != "" && !strings.EqualFold(meta.SHA256, sha256HexString(content)) {
		return "artifact metadata hash does not match content"
	}
	return ""
}

func verifyLocalMetadata(meta *Metadata, path string, info os.FileInfo) string {
	if meta == nil {
		return ""
	}
	if meta.NormalizedPath != "" {
		normalized, err := existingLocalPath(meta.NormalizedPath)
		if err != nil {
			return "artifact metadata path cannot be resolved"
		}
		if normalized != path {
			return "artifact metadata path does not match file"
		}
	}
	if info != nil && meta.SizeBytes != nil && *meta.SizeBytes != info.Size() {
		return "artifact metadata size does not match file"
	}
	if meta.SHA256 != "" {
		sum, err := fileSHA256(path)
		if err != nil {
			return "artifact metadata hash cannot be verified"
		}
		if !strings.EqualFold(meta.SHA256, sum) {
			return "artifact metadata hash does not match file"
		}
	}
	return ""
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
	result := validateLocalPath(rawURI, roots)
	if !result.Safe {
		return "", false
	}
	return result.Path, true
}

// ContentPath returns the safe local file path for an artifact content endpoint.
func ContentPath(artifact storage.JobArtifact, roots []string) (string, error) {
	result := validateLocalPath(artifact.URI, roots)
	if !result.Safe {
		if result.Err != nil {
			return "", result.Err
		}
		return "", errors.New(result.Reason)
	}
	if !supportedLocalArtifactKind(artifact, displayKind(artifact)) {
		return "", errors.New("unsupported artifact kind")
	}
	if reason := verifyLocalMetadata(ParseMetadata(artifact.Metadata), result.Path, result.Info); reason != "" {
		return "", errors.New(reason)
	}
	return result.Path, nil
}

// GeneratedLocalPath returns a path for a new local artifact file under the
// first configured safe root. The filename must be a plain basename so callers
// cannot accidentally escape the artifact root with path traversal.
func GeneratedLocalPath(roots []string, filename string) (string, error) {
	filename = strings.TrimSpace(filename)
	if filename == "" || filename != filepath.Base(filename) || filename == "." || filename == string(os.PathSeparator) {
		return "", fmt.Errorf("artifact filename must be a plain filename")
	}

	normalizedRoots := NormalizeRoots(roots)
	if len(normalizedRoots) == 0 {
		normalizedRoots = DefaultRoots()
	}
	if len(normalizedRoots) == 0 {
		return "", fmt.Errorf("no configured artifact root is available")
	}

	root := normalizedRoots[0]
	path := filepath.Clean(filepath.Join(root, filename))
	if !pathInsideRoot(path, root) {
		return "", fmt.Errorf("generated artifact path escapes configured artifact root")
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

func validateLocalPath(rawURI string, roots []string) localPathValidation {
	local, ok := localPathFromURI(rawURI)
	if !ok {
		return localPathValidation{Reason: "unsupported artifact URI scheme"}
	}
	lexicalRoots := cleanRootPaths(roots)
	normalizedRoots := NormalizeRoots(roots)
	path, err := absoluteCleanPath(local)
	if err != nil {
		return localPathValidation{Reason: "artifact path cannot be resolved", Err: fmt.Errorf("artifact path cannot be resolved: %w", err)}
	}
	if !pathInsideAnyRoot(path, lexicalRoots) && !pathInsideAnyRoot(path, normalizedRoots) {
		return localPathValidation{Reason: "local artifact is outside configured artifact roots", Err: fmt.Errorf("local artifact is outside configured artifact roots")}
	}
	evaluated, err := filepath.EvalSymlinks(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return localPathValidation{Reason: "artifact file not found", Err: fmt.Errorf("artifact file not found: %w", err)}
		}
		return localPathValidation{Reason: "artifact path cannot be resolved", Err: fmt.Errorf("artifact path cannot be resolved: %w", err)}
	}
	path = filepath.Clean(evaluated)
	if !pathInsideAnyRoot(path, normalizedRoots) {
		return localPathValidation{Reason: "local artifact is outside configured artifact roots", Err: fmt.Errorf("local artifact is outside configured artifact roots")}
	}
	info, err := os.Stat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return localPathValidation{Reason: "artifact file not found", Err: fmt.Errorf("artifact file not found: %w", err)}
		}
		return localPathValidation{Reason: "artifact path cannot be resolved", Err: fmt.Errorf("artifact path cannot be resolved: %w", err)}
	}
	if info.IsDir() {
		return localPathValidation{Reason: "artifact path is a directory", Err: fmt.Errorf("artifact path is a directory")}
	}
	if !info.Mode().IsRegular() {
		return localPathValidation{Reason: "artifact path is not a regular file", Err: fmt.Errorf("artifact path is not a regular file")}
	}
	return localPathValidation{Path: path, Info: info, Safe: true}
}

func existingLocalPath(path string) (string, error) {
	path, err := absoluteCleanPath(path)
	if err != nil {
		return "", err
	}
	evaluated, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", err
	}
	return filepath.Clean(evaluated), nil
}

func supportedLocalArtifactKind(artifact storage.JobArtifact, kind string) bool {
	typeName := strings.ToLower(strings.TrimSpace(artifact.ArtifactType))
	switch strings.TrimSpace(kind) {
	case KindImage, KindTextReport, KindURL:
		return true
	case KindArtifact:
		return typeName == "" || typeName == KindArtifact || typeName == "file"
	default:
		return false
	}
}

func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close() //nolint:errcheck
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func sha256HexString(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func absoluteCleanPath(path string) (string, error) {
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
	return filepath.Clean(abs), nil
}

func canonicalPath(path string) (string, error) {
	abs, err := absoluteCleanPath(path)
	if err != nil {
		return "", err
	}
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

func cleanRootPaths(roots []string) []string {
	seen := make(map[string]struct{}, len(roots))
	out := make([]string, 0, len(roots))
	for _, root := range roots {
		root = strings.TrimSpace(root)
		if root == "" {
			continue
		}
		path, err := absoluteCleanPath(root)
		if err != nil || path == "" {
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

func pathInsideAnyRoot(path string, roots []string) bool {
	for _, root := range roots {
		if pathInsideRoot(path, root) {
			return true
		}
	}
	return false
}
