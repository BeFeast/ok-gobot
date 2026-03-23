package role

import (
	"embed"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

//go:embed bundled/*.md
var bundledFS embed.FS

// BundledNames returns the names of all bundled role manifests, sorted.
func BundledNames() ([]string, error) {
	entries, err := fs.ReadDir(bundledFS, "bundled")
	if err != nil {
		return nil, fmt.Errorf("reading bundled roles: %w", err)
	}

	var names []string
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".md") {
			continue
		}
		names = append(names, strings.TrimSuffix(entry.Name(), ".md"))
	}
	sort.Strings(names)
	return names, nil
}

// LoadBundled parses all bundled role manifests and returns them sorted by name.
func LoadBundled() ([]*Manifest, error) {
	entries, err := fs.ReadDir(bundledFS, "bundled")
	if err != nil {
		return nil, fmt.Errorf("reading bundled roles: %w", err)
	}

	var manifests []*Manifest
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".md") {
			continue
		}

		data, err := fs.ReadFile(bundledFS, "bundled/"+entry.Name())
		if err != nil {
			return nil, fmt.Errorf("reading bundled %s: %w", entry.Name(), err)
		}

		name := strings.TrimSuffix(entry.Name(), ".md")
		m, err := Parse(name, data)
		if err != nil {
			return nil, fmt.Errorf("parsing bundled %s: %w", entry.Name(), err)
		}

		manifests = append(manifests, m)
	}

	sort.Slice(manifests, func(i, j int) bool {
		return manifests[i].Name < manifests[j].Name
	})

	return manifests, nil
}

// Scaffold copies all bundled role manifests into dir.
// Existing files are not overwritten. Returns the list of files written.
func Scaffold(dir string) ([]string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("creating roles directory %s: %w", dir, err)
	}

	entries, err := fs.ReadDir(bundledFS, "bundled")
	if err != nil {
		return nil, fmt.Errorf("reading bundled roles: %w", err)
	}

	var written []string
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".md") {
			continue
		}

		dest := filepath.Join(dir, entry.Name())
		if _, err := os.Stat(dest); err == nil {
			continue // already exists, skip
		}

		data, err := fs.ReadFile(bundledFS, "bundled/"+entry.Name())
		if err != nil {
			return written, fmt.Errorf("reading bundled %s: %w", entry.Name(), err)
		}

		if err := os.WriteFile(dest, data, 0o644); err != nil {
			return written, fmt.Errorf("writing %s: %w", dest, err)
		}

		written = append(written, dest)
	}

	sort.Strings(written)
	return written, nil
}
