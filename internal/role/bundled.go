package role

import (
	"embed"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

//go:embed prebuilt/*.md
var bundledFS embed.FS

// BundledNames returns the names of all bundled (prebuilt) roles.
func BundledNames() []string {
	entries, err := bundledFS.ReadDir("prebuilt")
	if err != nil {
		return nil
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".md") {
			names = append(names, strings.TrimSuffix(e.Name(), ".md"))
		}
	}
	return names
}

// LoadBundled returns all bundled (prebuilt) role manifests.
// These are the roles shipped with the binary as examples.
// They are not active by default — operators copy and customise them.
func LoadBundled() ([]*Manifest, error) {
	entries, err := bundledFS.ReadDir("prebuilt")
	if err != nil {
		return nil, fmt.Errorf("reading bundled roles: %w", err)
	}

	var manifests []*Manifest
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		data, err := bundledFS.ReadFile("prebuilt/" + e.Name())
		if err != nil {
			return nil, fmt.Errorf("reading bundled role %s: %w", e.Name(), err)
		}
		name := strings.TrimSuffix(e.Name(), ".md")
		m, err := Parse(name, data)
		if err != nil {
			return nil, fmt.Errorf("parsing bundled role %s: %w", e.Name(), err)
		}
		manifests = append(manifests, m)
	}
	return manifests, nil
}

// WriteBundledTo copies all bundled role files to dir so operators can
// inspect and customise them. Existing files are skipped unless overwrite
// is true. Returns the list of files written.
func WriteBundledTo(dir string, overwrite bool) ([]string, error) {
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("creating roles directory %s: %w", dir, err)
	}

	entries, err := fs.ReadDir(bundledFS, "prebuilt")
	if err != nil {
		return nil, fmt.Errorf("reading bundled roles: %w", err)
	}

	var written []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}

		dest := filepath.Join(dir, e.Name())
		if !overwrite {
			if _, err := os.Stat(dest); err == nil {
				continue // skip existing
			}
		}

		data, err := bundledFS.ReadFile("prebuilt/" + e.Name())
		if err != nil {
			return written, fmt.Errorf("reading bundled role %s: %w", e.Name(), err)
		}

		if err := os.WriteFile(dest, data, 0644); err != nil {
			return written, fmt.Errorf("writing %s: %w", dest, err)
		}
		written = append(written, dest)
	}
	return written, nil
}
