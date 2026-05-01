package runtime

import (
	"crypto/sha256"
	"encoding/hex"
	"mime"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"

	artifactview "ok-gobot/internal/artifacts"
	"ok-gobot/internal/storage"
)

const (
	JobArtifactTypeScreenshot = "screenshot"
	JobArtifactTypeURL        = "url"
	JobArtifactTypeFile       = "file"
	JobArtifactTypeTextReport = "text_report"
)

var (
	roleURLRE     = regexp.MustCompile(`https?://[^\s<>"']+`)
	roleFileURIRE = regexp.MustCompile(`file:///[^\s<>"']+`)
	rolePathRE    = regexp.MustCompile(`(?i)(^|[\s"'(\[])(/[A-Za-z0-9._~!$&+,;=:@%/-]+\.(png|jpe?g|gif|webp|html?|md|json|txt|log|pdf|csv))`)
)

func isRoleJob(job *storage.Job) bool {
	if job == nil {
		return false
	}
	return strings.TrimSpace(job.Kind) == "role" || strings.TrimSpace(job.RoleName) != ""
}

func roleProofArtifacts(result JobRunResult, roots []string) []JobArtifactSpec {
	raw := append([]JobArtifactSpec(nil), result.Artifacts...)
	if !hasRoleTextReport(raw) {
		report := strings.TrimSpace(result.Summary)
		if report == "" {
			report = "role job completed with no final output"
		}
		raw = append([]JobArtifactSpec{{
			Name:     "final-report",
			Type:     JobArtifactTypeTextReport,
			MimeType: "text/markdown",
			Content:  report,
		}}, raw...)
	}

	raw = append(raw, extractArtifactsFromRoleText(result.Summary)...)
	for _, artifact := range result.Artifacts {
		if standardRoleArtifactType(artifact) == JobArtifactTypeTextReport {
			raw = append(raw, extractArtifactsFromRoleText(artifact.Content)...)
		}
	}

	return sanitizeRoleArtifactSpecs(raw, roots)
}

func hasRoleTextReport(artifacts []JobArtifactSpec) bool {
	for _, artifact := range artifacts {
		if standardRoleArtifactType(artifact) == JobArtifactTypeTextReport && strings.TrimSpace(artifact.Content) != "" {
			return true
		}
	}
	return false
}

func extractArtifactsFromRoleText(text string) []JobArtifactSpec {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}

	var artifacts []JobArtifactSpec
	for _, match := range roleURLRE.FindAllString(text, -1) {
		raw := cleanRoleArtifactMatch(match)
		if raw == "" {
			continue
		}
		artifacts = append(artifacts, JobArtifactSpec{
			Name: "url",
			Type: JobArtifactTypeURL,
			URI:  raw,
		})
	}

	for _, match := range roleFileURIRE.FindAllString(text, -1) {
		raw := cleanRoleArtifactMatch(match)
		if raw == "" {
			continue
		}
		artifacts = append(artifacts, JobArtifactSpec{
			Name: artifactNameForPath(artifactPathValue(raw)),
			Type: localArtifactType(raw),
			URI:  raw,
		})
	}

	for _, match := range rolePathRE.FindAllStringSubmatch(text, -1) {
		if len(match) < 3 {
			continue
		}
		raw := cleanRoleArtifactMatch(match[2])
		if raw == "" {
			continue
		}
		artifacts = append(artifacts, JobArtifactSpec{
			Name: artifactNameForPath(raw),
			Type: localArtifactType(raw),
			URI:  raw,
		})
	}

	return artifacts
}

func sanitizeRoleArtifactSpecs(raw []JobArtifactSpec, roots []string) []JobArtifactSpec {
	if len(raw) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(raw))
	out := make([]JobArtifactSpec, 0, len(raw))
	for _, artifact := range raw {
		safe, ok := sanitizeRoleArtifactSpec(artifact, roots)
		if !ok {
			continue
		}
		key := roleArtifactKey(safe)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, safe)
	}
	return out
}

func sanitizeRoleArtifactSpec(artifact JobArtifactSpec, roots []string) (JobArtifactSpec, bool) {
	artifactType := standardRoleArtifactType(artifact)
	if artifactType == "" {
		return JobArtifactSpec{}, false
	}

	name := strings.TrimSpace(artifact.Name)
	mimeType := strings.TrimSpace(artifact.MimeType)
	safe := JobArtifactSpec{
		Name:     name,
		Type:     artifactType,
		MimeType: mimeType,
		Metadata: artifact.Metadata,
	}

	switch artifactType {
	case JobArtifactTypeTextReport:
		content := strings.TrimSpace(artifact.Content)
		if content == "" {
			return JobArtifactSpec{}, false
		}
		if safe.Name == "" {
			safe.Name = "final-report"
		}
		if safe.MimeType == "" {
			safe.MimeType = "text/markdown"
		}
		safe.Content = content
		return safe, true
	case JobArtifactTypeURL:
		raw := firstArtifactValue(artifact)
		normalized, ok := safeRemoteURL(raw)
		if !ok {
			return JobArtifactSpec{}, false
		}
		if safe.Name == "" {
			safe.Name = "url"
		}
		safe.URI = normalized
		return safe, true
	case JobArtifactTypeScreenshot, JobArtifactTypeFile:
		raw := firstArtifactValue(artifact)
		path, ok := safeLocalArtifactPath(raw, roots)
		if !ok {
			return JobArtifactSpec{}, false
		}
		if artifactType == JobArtifactTypeScreenshot && !isImagePath(path) && !strings.HasPrefix(strings.ToLower(safe.MimeType), "image/") {
			return JobArtifactSpec{}, false
		}
		if safe.Name == "" {
			safe.Name = artifactNameForPath(path)
		}
		if safe.MimeType == "" {
			safe.MimeType = mimeTypeForPath(path)
		}
		safe.URI = path
		return safe, true
	default:
		return JobArtifactSpec{}, false
	}
}

func standardRoleArtifactType(artifact JobArtifactSpec) string {
	typeName := strings.ToLower(strings.TrimSpace(artifact.Type))
	mimeType := strings.ToLower(strings.TrimSpace(artifact.MimeType))
	value := firstArtifactValue(artifact)

	switch {
	case typeName == JobArtifactTypeScreenshot || typeName == "image" || strings.HasPrefix(typeName, "image_") || strings.HasPrefix(mimeType, "image/"):
		return JobArtifactTypeScreenshot
	case typeName == JobArtifactTypeURL || typeName == "link":
		return JobArtifactTypeURL
	case typeName == JobArtifactTypeFile || typeName == "artifact":
		return JobArtifactTypeFile
	case typeName == JobArtifactTypeTextReport || typeName == "text" || typeName == "markdown" || typeName == "report" || strings.Contains(typeName, "report") || strings.HasPrefix(mimeType, "text/"):
		return JobArtifactTypeTextReport
	}

	if _, ok := safeRemoteURL(value); ok {
		return JobArtifactTypeURL
	}
	if isImagePath(value) || isImagePath(artifact.Name) {
		return JobArtifactTypeScreenshot
	}
	if looksLikeLocalArtifactPath(value) {
		return JobArtifactTypeFile
	}
	if strings.TrimSpace(artifact.Content) != "" {
		return JobArtifactTypeTextReport
	}
	return ""
}

func firstArtifactValue(artifact JobArtifactSpec) string {
	if raw := strings.TrimSpace(artifact.URI); raw != "" {
		return raw
	}
	return strings.TrimSpace(artifact.Content)
}

func roleArtifactKey(artifact JobArtifactSpec) string {
	switch artifact.Type {
	case JobArtifactTypeTextReport:
		sum := sha256.Sum256([]byte(artifact.Content))
		return artifact.Type + ":" + artifact.Name + ":" + hex.EncodeToString(sum[:])
	case JobArtifactTypeURL, JobArtifactTypeScreenshot, JobArtifactTypeFile:
		return artifact.Type + ":" + artifact.URI
	default:
		return artifact.Type + ":" + artifact.Name
	}
}

func safeRemoteURL(raw string) (string, bool) {
	raw = cleanRoleArtifactMatch(raw)
	if raw == "" {
		return "", false
	}
	u, err := url.Parse(raw)
	if err != nil || u == nil {
		return "", false
	}
	scheme := strings.ToLower(u.Scheme)
	if (scheme != "http" && scheme != "https") || u.Host == "" {
		return "", false
	}
	return raw, true
}

func safeLocalArtifactPath(raw string, roots []string) (string, bool) {
	raw = cleanRoleArtifactMatch(raw)
	if raw == "" {
		return "", false
	}
	artifactRoots := artifactview.ConfiguredRoots(roots)
	if len(artifactRoots) == 0 {
		artifactRoots = artifactview.DefaultRoots()
	}
	path, err := artifactview.ContentPath(storage.JobArtifact{URI: raw}, artifactRoots)
	if err != nil {
		return "", false
	}
	return path, true
}

func localArtifactType(raw string) string {
	if isImagePath(raw) {
		return JobArtifactTypeScreenshot
	}
	return JobArtifactTypeFile
}

func looksLikeLocalArtifactPath(raw string) bool {
	raw = strings.TrimSpace(raw)
	return strings.HasPrefix(strings.ToLower(raw), "file://") || filepath.IsAbs(raw)
}

func artifactPathValue(raw string) string {
	if strings.HasPrefix(strings.ToLower(raw), "file://") {
		if u, err := url.Parse(raw); err == nil && u != nil && u.Path != "" {
			return u.Path
		}
	}
	return raw
}

func artifactNameForPath(path string) string {
	base := filepath.Base(path)
	if base == "." || base == "/" || base == "" {
		return "artifact"
	}
	return base
}

func isImagePath(path string) bool {
	ext := strings.ToLower(filepath.Ext(artifactPathValue(path)))
	switch ext {
	case ".png", ".jpg", ".jpeg", ".gif", ".webp":
		return true
	default:
		return strings.HasPrefix(mime.TypeByExtension(ext), "image/")
	}
}

func mimeTypeForPath(path string) string {
	if mt := mime.TypeByExtension(strings.ToLower(filepath.Ext(path))); mt != "" {
		return mt
	}
	if isImagePath(path) {
		return "image/png"
	}
	return "application/octet-stream"
}

func cleanRoleArtifactMatch(raw string) string {
	raw = strings.TrimSpace(raw)
	raw = strings.Trim(raw, "`'\"")
	raw = strings.TrimRight(raw, ".,;:")
	return trimUnmatchedClosingDelimiters(raw)
}

func trimUnmatchedClosingDelimiters(raw string) string {
	for raw != "" {
		last := raw[len(raw)-1]
		open := byte(0)
		switch last {
		case ')':
			open = '('
		case ']':
			open = '['
		case '}':
			open = '{'
		default:
			return raw
		}
		if strings.Count(raw, string(last)) <= strings.Count(raw, string(open)) {
			return raw
		}
		raw = strings.TrimSpace(raw[:len(raw)-1])
	}
	return raw
}
