package bootstrap

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// DefaultMaxVersions is the default number of skill versions to retain.
const DefaultMaxVersions = 10

// VersionEntry describes a single version backup of a skill file.
type VersionEntry struct {
	Filename  string    // e.g. "SKILL.md.v20240101120000123456"
	Timestamp time.Time // parsed from filename
	Path      string    // absolute path to the version file
}

// SaveSkillVersion backs up the current SKILL.md to the skill's .versions/
// directory before a modification. skillFilePath is the absolute path to the
// SKILL.md file. maxVersions controls pruning (0 → DefaultMaxVersions).
func SaveSkillVersion(skillFilePath string, maxVersions int) error {
	if maxVersions <= 0 {
		maxVersions = DefaultMaxVersions
	}

	content, err := os.ReadFile(skillFilePath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // nothing to back up yet
		}
		return fmt.Errorf("failed to read skill file: %w", err)
	}

	skillDir := filepath.Dir(skillFilePath)
	versionsDir := filepath.Join(skillDir, ".versions")
	if err := os.MkdirAll(versionsDir, 0o755); err != nil {
		return fmt.Errorf("failed to create versions directory: %w", err)
	}

	now := time.Now()
	// Include microseconds so rapid successive saves produce distinct filenames.
	timestamp := fmt.Sprintf("%s%06d", now.Format("20060102150405"), now.Nanosecond()/1000)
	versionFile := filepath.Join(versionsDir, "SKILL.md.v"+timestamp)
	if err := os.WriteFile(versionFile, content, 0o644); err != nil {
		return fmt.Errorf("failed to write version file: %w", err)
	}

	return pruneOldVersions(versionsDir, maxVersions)
}

// ListSkillVersions returns the version history for a skill directory,
// sorted newest-first. skillDir is the directory that contains SKILL.md.
func ListSkillVersions(skillDir string) ([]VersionEntry, error) {
	versionsDir := filepath.Join(skillDir, ".versions")
	entries, err := os.ReadDir(versionsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to read versions directory: %w", err)
	}

	const prefix = "SKILL.md.v"
	var versions []VersionEntry
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if len(name) <= len(prefix) || name[:len(prefix)] != prefix {
			continue
		}
		timestampStr := name[len(prefix):]
		// Support both legacy 14-char (YYYYMMDDHHmmss) and current 20-char
		// (YYYYMMDDHHmmss + 6-digit microseconds) formats.
		var t time.Time
		var parseErr error
		if len(timestampStr) >= 14 {
			t, parseErr = time.ParseInLocation("20060102150405", timestampStr[:14], time.Local)
		} else {
			parseErr = fmt.Errorf("timestamp too short")
		}
		if parseErr != nil {
			continue // skip malformed names
		}
		versions = append(versions, VersionEntry{
			Filename:  name,
			Timestamp: t,
			Path:      filepath.Join(versionsDir, name),
		})
	}

	sort.Slice(versions, func(i, j int) bool {
		return versions[i].Timestamp.After(versions[j].Timestamp)
	})

	return versions, nil
}

// RollbackSkillVersion restores a previous version to SKILL.md.
// skillDir is the skill directory; versionFilename is the base filename of the
// backup (e.g. "SKILL.md.v20240101120000123456").
func RollbackSkillVersion(skillDir, versionFilename string) error {
	versionPath := filepath.Join(skillDir, ".versions", versionFilename)
	content, err := os.ReadFile(versionPath)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("version %q not found", versionFilename)
		}
		return fmt.Errorf("failed to read version file: %w", err)
	}

	skillFilePath := filepath.Join(skillDir, "SKILL.md")
	if err := os.WriteFile(skillFilePath, content, 0o644); err != nil {
		return fmt.Errorf("failed to restore skill file: %w", err)
	}

	return nil
}

// pruneOldVersions removes oldest version files when the count exceeds maxVersions.
func pruneOldVersions(versionsDir string, maxVersions int) error {
	entries, err := os.ReadDir(versionsDir)
	if err != nil {
		return nil
	}

	const prefix = "SKILL.md.v"
	var names []string
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if len(name) > len(prefix) && name[:len(prefix)] == prefix {
			names = append(names, name)
		}
	}

	sort.Strings(names) // timestamp format is lexicographically sortable
	for len(names) > maxVersions {
		_ = os.Remove(filepath.Join(versionsDir, names[0]))
		names = names[1:]
	}

	return nil
}
