package bootstrap

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// AuditSeverity indicates the severity of an audit finding.
type AuditSeverity string

const (
	SeverityError   AuditSeverity = "error"
	SeverityWarning AuditSeverity = "warning"
)

// AuditFinding represents a single safety issue found during audit.
type AuditFinding struct {
	Severity AuditSeverity
	Path     string // relative path within the skill directory
	Message  string
}

// String formats the finding for display.
func (f AuditFinding) String() string {
	if f.Path != "" {
		return fmt.Sprintf("[%s] %s: %s", f.Severity, f.Path, f.Message)
	}
	return fmt.Sprintf("[%s] %s", f.Severity, f.Message)
}

// scriptExtensions are file extensions associated with executable scripts.
var scriptExtensions = map[string]bool{
	".sh": true, ".bash": true, ".zsh": true, ".fish": true,
	".py": true, ".rb": true, ".pl": true, ".php": true,
	".js": true, ".ts": true, ".lua": true,
	".bat": true, ".cmd": true, ".ps1": true,
	".exe": true, ".com": true, ".dll": true, ".so": true,
}

// pipeToShellPattern matches common pipe-to-shell patterns in markdown.
var pipeToShellPattern = regexp.MustCompile(`(?i)(curl|wget|fetch)\s+[^\n|]*\|\s*(sh|bash|zsh|dash|fish|python|ruby|perl|node)`)

// escapingLinkPattern matches markdown links that escape the skill root via ../.
var escapingLinkPattern = regexp.MustCompile(`\]\(\s*\.\.\/`)

// AuditSkill performs a static safety audit on a skill directory.
// It checks for symlinks, scripts, pipe-to-shell patterns, and escaping links.
func AuditSkill(skillPath string) ([]AuditFinding, error) {
	skillPath, err := filepath.Abs(skillPath)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve path: %w", err)
	}

	info, err := os.Lstat(skillPath)
	if err != nil {
		return nil, fmt.Errorf("skill path does not exist: %w", err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("skill path is not a directory: %s", skillPath)
	}

	var findings []AuditFinding

	err = filepath.Walk(skillPath, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}

		relPath, _ := filepath.Rel(skillPath, path)
		if relPath == "." {
			return nil
		}

		// Skip .git directory entirely.
		if info.IsDir() && info.Name() == ".git" {
			return filepath.SkipDir
		}

		// Check for symlinks.
		linfo, err := os.Lstat(path)
		if err != nil {
			return nil
		}
		if linfo.Mode()&os.ModeSymlink != 0 {
			target, _ := os.Readlink(path)
			findings = append(findings, AuditFinding{
				Severity: SeverityError,
				Path:     relPath,
				Message:  fmt.Sprintf("symlink detected (target: %s); remove the symlink or replace with the actual file", target),
			})
			return nil
		}

		if info.IsDir() {
			return nil
		}

		ext := strings.ToLower(filepath.Ext(info.Name()))

		// Check for script/executable file extensions.
		if scriptExtensions[ext] {
			findings = append(findings, AuditFinding{
				Severity: SeverityError,
				Path:     relPath,
				Message:  fmt.Sprintf("script or executable file (%s); skills must be markdown-only", ext),
			})
		}

		// Check for executable permission bits.
		if info.Mode()&0o111 != 0 {
			findings = append(findings, AuditFinding{
				Severity: SeverityWarning,
				Path:     relPath,
				Message:  "file has executable permission; consider removing with chmod -x",
			})
		}

		// For text files, check content.
		if isTextFile(ext) {
			content, err := os.ReadFile(path)
			if err != nil {
				return nil
			}
			text := string(content)

			// Check for pipe-to-shell patterns.
			if matches := pipeToShellPattern.FindAllString(text, -1); len(matches) > 0 {
				findings = append(findings, AuditFinding{
					Severity: SeverityError,
					Path:     relPath,
					Message:  fmt.Sprintf("pipe-to-shell pattern detected (%s); this is a common attack vector", matches[0]),
				})
			}

			// Check for markdown links escaping the skill root.
			if escapingLinkPattern.MatchString(text) {
				findings = append(findings, AuditFinding{
					Severity: SeverityError,
					Path:     relPath,
					Message:  "markdown link escapes skill directory (../); links must stay within the skill root",
				})
			}
		}

		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("failed to walk skill directory: %w", err)
	}

	return findings, nil
}

// AuditHasErrors returns true if any finding has error severity.
func AuditHasErrors(findings []AuditFinding) bool {
	for _, f := range findings {
		if f.Severity == SeverityError {
			return true
		}
	}
	return false
}

// SkillAuditResult captures strict audit findings plus workspace compatibility
// classification used for already-mounted skills.
type SkillAuditResult struct {
	Findings            []AuditFinding
	Compatibility       SkillCompatibility
	CompatibilityReason string
	ScriptAssets        []string
}

// AuditSkillForWorkspace audits a skill and classifies how runtime/listing
// should treat it. It never changes the strict AuditSkill behavior used by
// installs: script assets are only downgraded for skills already rooted under
// basePath/skills and only when trusted workspace compatibility is enabled.
func AuditSkillForWorkspace(basePath, skillPath string, opts LoaderOptions) (SkillAuditResult, error) {
	findings, err := AuditSkill(skillPath)
	if err != nil {
		return SkillAuditResult{}, err
	}

	scriptAssets, err := ScriptAssetPaths(skillPath)
	if err != nil {
		return SkillAuditResult{}, err
	}

	result := SkillAuditResult{
		Findings:            findings,
		Compatibility:       SkillCompatibilityNative,
		CompatibilityReason: "markdown-only skill",
		ScriptAssets:        scriptAssets,
	}

	if len(scriptAssets) == 0 {
		if AuditHasErrors(findings) {
			result.Compatibility = SkillCompatibilityBlocked
			result.CompatibilityReason = "strict audit errors"
		}
		return result, nil
	}

	if !opts.TrustWorkspaceScripts {
		result.Compatibility = SkillCompatibilityBlocked
		result.CompatibilityReason = "script assets require skills.trust_workspace_scripts or OKGOBOT_SKILLS_TRUST_WORKSPACE_SCRIPTS"
		return result, nil
	}

	insideWorkspace, workspaceRoot, err := SkillPathWithinWorkspace(basePath, skillPath)
	if err != nil {
		result.Compatibility = SkillCompatibilityBlocked
		result.CompatibilityReason = fmt.Sprintf("workspace root check failed: %v", err)
		return result, nil
	}
	if !insideWorkspace {
		result.Compatibility = SkillCompatibilityBlocked
		result.CompatibilityReason = fmt.Sprintf("script assets are outside configured workspace skills root %s", workspaceRoot)
		return result, nil
	}

	result.Findings = downgradeWorkspaceScriptFindings(findings, scriptAssets)
	if AuditHasErrors(result.Findings) {
		result.Compatibility = SkillCompatibilityBlocked
		result.CompatibilityReason = "trusted workspace mode does not allow non-script audit errors"
		return result, nil
	}

	result.Compatibility = SkillCompatibilityTrustedWorkspace
	result.CompatibilityReason = fmt.Sprintf("script assets trusted under workspace root %s; ok-gobot does not execute them automatically", workspaceRoot)
	return result, nil
}

// ScriptAssetPaths returns script/executable asset paths relative to skillPath.
func ScriptAssetPaths(skillPath string) ([]string, error) {
	skillPath, err := filepath.Abs(skillPath)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve path: %w", err)
	}

	var assets []string
	err = filepath.Walk(skillPath, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if info.IsDir() {
			if info.Name() == ".git" {
				return filepath.SkipDir
			}
			return nil
		}

		linfo, err := os.Lstat(path)
		if err != nil {
			return nil
		}
		if linfo.Mode()&os.ModeSymlink != 0 {
			return nil
		}

		ext := strings.ToLower(filepath.Ext(info.Name()))
		if !scriptExtensions[ext] {
			return nil
		}

		relPath, err := filepath.Rel(skillPath, path)
		if err != nil {
			return err
		}
		assets = append(assets, relPath)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("failed to walk skill directory: %w", err)
	}
	sort.Strings(assets)
	return assets, nil
}

// SkillPathWithinWorkspace verifies that skillPath resolves under basePath/skills.
func SkillPathWithinWorkspace(basePath, skillPath string) (bool, string, error) {
	basePath = ExpandPath(basePath)
	workspaceRoot := filepath.Join(basePath, "skills")

	absRoot, err := filepath.Abs(workspaceRoot)
	if err != nil {
		return false, workspaceRoot, fmt.Errorf("failed to resolve workspace skills root: %w", err)
	}
	absSkill, err := filepath.Abs(skillPath)
	if err != nil {
		return false, absRoot, fmt.Errorf("failed to resolve skill path: %w", err)
	}

	root, err := filepath.EvalSymlinks(absRoot)
	if err != nil {
		return false, absRoot, fmt.Errorf("failed to resolve workspace skills root symlinks: %w", err)
	}
	skill, err := filepath.EvalSymlinks(absSkill)
	if err != nil {
		return false, root, fmt.Errorf("failed to resolve skill path symlinks: %w", err)
	}

	rel, err := filepath.Rel(root, skill)
	if err != nil {
		return false, root, fmt.Errorf("failed to compare skill path with workspace root: %w", err)
	}
	if rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return false, root, nil
	}
	return true, root, nil
}

func downgradeWorkspaceScriptFindings(findings []AuditFinding, scriptAssets []string) []AuditFinding {
	scriptSet := make(map[string]struct{}, len(scriptAssets))
	for _, asset := range scriptAssets {
		scriptSet[asset] = struct{}{}
	}

	out := make([]AuditFinding, len(findings))
	copy(out, findings)
	for i := range out {
		if out[i].Severity != SeverityError {
			continue
		}
		if _, ok := scriptSet[out[i].Path]; !ok {
			continue
		}
		if !strings.Contains(out[i].Message, "script or executable file") {
			continue
		}
		out[i].Severity = SeverityWarning
		out[i].Message = "script asset allowed by trusted workspace compatibility; ok-gobot does not execute skill scripts automatically"
	}
	return out
}

// ListSkills returns all installed skills in the workspace.
func ListSkills(basePath string) ([]SkillEntry, error) {
	return ListSkillsWithOptions(basePath, LoaderOptions{})
}

// ListSkillsWithOptions returns installed skills with compatibility metadata.
func ListSkillsWithOptions(basePath string, opts LoaderOptions) ([]SkillEntry, error) {
	basePath = ExpandPath(basePath)
	skillsDir := filepath.Join(basePath, "skills")

	if _, err := os.Stat(skillsDir); os.IsNotExist(err) {
		return nil, nil
	}

	entries, err := os.ReadDir(skillsDir)
	if err != nil {
		return nil, fmt.Errorf("failed to read skills directory: %w", err)
	}

	var skills []SkillEntry
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}

		skillFile := filepath.Join(skillsDir, entry.Name(), "SKILL.md")
		if _, err := os.Stat(skillFile); err != nil {
			continue // skip dirs without SKILL.md
		}

		content, err := os.ReadFile(skillFile)
		if err != nil {
			continue
		}

		description := parseSkillDescription(string(content))
		audit, err := AuditSkillForWorkspace(basePath, filepath.Join(skillsDir, entry.Name()), opts)
		if err != nil {
			audit = SkillAuditResult{
				Compatibility:       SkillCompatibilityBlocked,
				CompatibilityReason: fmt.Sprintf("audit failed: %v", err),
			}
		}
		skills = append(skills, SkillEntry{
			Name:                entry.Name(),
			Description:         description,
			Path:                skillFile,
			Compatibility:       audit.Compatibility,
			CompatibilityReason: audit.CompatibilityReason,
			ScriptAssets:        audit.ScriptAssets,
		})
	}

	return skills, nil
}

// InstallSkill installs a skill from a local path or git URL into the workspace.
// It audits the skill first and refuses to install if errors are found.
func InstallSkill(basePath, source string) (string, []AuditFinding, error) {
	basePath = ExpandPath(basePath)
	skillsDir := filepath.Join(basePath, "skills")

	if err := os.MkdirAll(skillsDir, 0o755); err != nil {
		return "", nil, fmt.Errorf("failed to create skills directory: %w", err)
	}

	var sourcePath string
	var skillName string
	var isGit bool

	if isGitURL(source) {
		isGit = true
		// Clone to a temp directory first for audit.
		tmpDir, err := os.MkdirTemp("", "ok-gobot-skill-*")
		if err != nil {
			return "", nil, fmt.Errorf("failed to create temp directory: %w", err)
		}
		defer os.RemoveAll(tmpDir)

		clonePath := filepath.Join(tmpDir, "skill")
		cmd := exec.Command("git", "clone", "--depth", "1", source, clonePath)
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			return "", nil, fmt.Errorf("git clone failed: %w", err)
		}
		sourcePath = clonePath
		skillName = extractSkillName(source)
	} else {
		// Local path.
		absSource, err := filepath.Abs(source)
		if err != nil {
			return "", nil, fmt.Errorf("failed to resolve source path: %w", err)
		}

		info, err := os.Stat(absSource)
		if err != nil {
			return "", nil, fmt.Errorf("source path does not exist: %w", err)
		}
		if !info.IsDir() {
			return "", nil, fmt.Errorf("source path is not a directory: %s", absSource)
		}

		sourcePath = absSource
		skillName = filepath.Base(absSource)
		isGit = false
	}

	// Verify SKILL.md exists in source.
	skillFile := filepath.Join(sourcePath, "SKILL.md")
	if _, err := os.Stat(skillFile); err != nil {
		return "", nil, fmt.Errorf("source does not contain SKILL.md; a valid skill must have a SKILL.md file")
	}

	// Audit the skill.
	findings, err := AuditSkill(sourcePath)
	if err != nil {
		return "", nil, fmt.Errorf("audit failed: %w", err)
	}

	if AuditHasErrors(findings) {
		return skillName, findings, fmt.Errorf("skill %q failed safety audit; fix the issues and try again", skillName)
	}

	// Install: copy to skills directory.
	destPath := filepath.Join(skillsDir, skillName)
	if _, err := os.Stat(destPath); err == nil {
		return "", findings, fmt.Errorf("skill %q is already installed; remove it first with: ok-gobot skills remove %s", skillName, skillName)
	}

	if err := copyDir(sourcePath, destPath, isGit); err != nil {
		return "", findings, fmt.Errorf("failed to install skill: %w", err)
	}

	return skillName, findings, nil
}

// RemoveSkill removes an installed skill from the workspace.
func RemoveSkill(basePath, name string) error {
	basePath = ExpandPath(basePath)
	skillPath := filepath.Join(basePath, "skills", name)

	info, err := os.Stat(skillPath)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("skill %q is not installed", name)
		}
		return fmt.Errorf("failed to check skill: %w", err)
	}
	if !info.IsDir() {
		return fmt.Errorf("skill %q is not a directory", name)
	}

	// Verify it's actually a skill (has SKILL.md).
	skillFile := filepath.Join(skillPath, "SKILL.md")
	if _, err := os.Stat(skillFile); err != nil {
		return fmt.Errorf("%q does not contain SKILL.md; refusing to remove non-skill directory", name)
	}

	if err := os.RemoveAll(skillPath); err != nil {
		return fmt.Errorf("failed to remove skill: %w", err)
	}

	return nil
}

// isGitURL returns true if source looks like a git URL.
func isGitURL(source string) bool {
	return strings.HasPrefix(source, "http://") ||
		strings.HasPrefix(source, "https://") ||
		strings.HasPrefix(source, "git@") ||
		strings.HasPrefix(source, "git://") ||
		strings.HasSuffix(source, ".git")
}

// extractSkillName derives a skill name from a git URL.
func extractSkillName(gitURL string) string {
	// Remove trailing .git
	name := strings.TrimSuffix(gitURL, ".git")
	// Remove trailing /
	name = strings.TrimRight(name, "/")
	// Get last path component
	parts := strings.Split(name, "/")
	if len(parts) > 0 {
		name = parts[len(parts)-1]
	}
	// Also handle git@host:user/repo format
	if idx := strings.LastIndex(name, ":"); idx >= 0 {
		name = name[idx+1:]
	}
	if name == "" {
		name = "unnamed-skill"
	}
	return name
}

// copyDir recursively copies a directory, skipping .git if isGit is true.
func copyDir(src, dst string, skipGit bool) error {
	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		relPath, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}

		destPath := filepath.Join(dst, relPath)

		// Skip .git directory.
		if skipGit && info.IsDir() && info.Name() == ".git" {
			return filepath.SkipDir
		}

		// Skip symlinks entirely during copy.
		linfo, err := os.Lstat(path)
		if err != nil {
			return err
		}
		if linfo.Mode()&os.ModeSymlink != 0 {
			return nil
		}

		if info.IsDir() {
			return os.MkdirAll(destPath, 0o755)
		}

		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		// Strip executable bits from installed files.
		return os.WriteFile(destPath, content, info.Mode()&^0o111)
	})
}

// parseSkillDescription extracts description from SKILL.md content.
func parseSkillDescription(content string) string {
	lines := strings.Split(content, "\n")
	inFrontmatter := false
	description := ""

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "---" {
			inFrontmatter = !inFrontmatter
			continue
		}
		if inFrontmatter {
			if strings.HasPrefix(trimmed, "description:") {
				description = strings.TrimSpace(strings.TrimPrefix(trimmed, "description:"))
			}
			continue
		}
		if trimmed != "" && !strings.HasPrefix(trimmed, "#") && description == "" {
			description = trimmed
			break
		}
	}

	if description == "" {
		description = "No description available"
	}
	return description
}

// isTextFile returns true for file extensions we should scan for content patterns.
func isTextFile(ext string) bool {
	textExts := map[string]bool{
		".md": true, ".txt": true, ".yaml": true, ".yml": true,
		".json": true, ".toml": true, ".xml": true, ".html": true,
		".css": true, ".csv": true, ".rst": true, ".adoc": true,
		"": true, // extensionless files
	}
	return textExts[ext]
}
