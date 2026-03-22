package role

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const (
	// ManifestFile is the canonical manifest filename inside a role directory.
	ManifestFile = "ROLE.md"
)

// Loader discovers and loads role manifests from a base directory.
// It looks for roles/<name>/ROLE.md files.
type Loader struct {
	basePath string
}

// NewLoader creates a role loader rooted at basePath.
// The loader will look for a "roles" subdirectory containing role manifests.
func NewLoader(basePath string) *Loader {
	return &Loader{basePath: basePath}
}

// LoadAll discovers and loads all role manifests from the roles directory.
// Returns the loaded manifests sorted by name. Roles that fail to parse are
// skipped with their errors collected.
func (l *Loader) LoadAll() ([]*Manifest, []error) {
	rolesDir := filepath.Join(l.basePath, "roles")
	entries, err := os.ReadDir(rolesDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, []error{fmt.Errorf("failed to read roles directory: %w", err)}
	}

	var manifests []*Manifest
	var errs []error

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}

		roleName := entry.Name()
		manifestPath := filepath.Join(rolesDir, roleName, ManifestFile)

		m, err := l.loadOne(manifestPath)
		if err != nil {
			errs = append(errs, fmt.Errorf("role %q: %w", roleName, err))
			continue
		}

		manifests = append(manifests, m)
	}

	sort.Slice(manifests, func(i, j int) bool {
		return manifests[i].Name < manifests[j].Name
	})

	return manifests, errs
}

// Load loads a single role manifest by name.
func (l *Loader) Load(name string) (*Manifest, error) {
	// Reject path traversal in role names.
	if strings.Contains(name, "/") || strings.Contains(name, "\\") || name == ".." || name == "." {
		return nil, fmt.Errorf("invalid role name: %q", name)
	}

	manifestPath := filepath.Join(l.basePath, "roles", name, ManifestFile)
	return l.loadOne(manifestPath)
}

func (l *Loader) loadOne(path string) (*Manifest, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading manifest: %w", err)
	}

	m, err := ParseManifest(string(content))
	if err != nil {
		return nil, fmt.Errorf("parsing %s: %w", filepath.Base(path), err)
	}

	m.SourcePath = path
	return m, nil
}
