package role

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// LoadDir reads all .md files from dir and returns a slice of parsed manifests.
// Files that are not valid manifests return an error; use LoadDirLenient to skip
// invalid files instead.
func LoadDir(dir string) ([]*Manifest, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("reading role directory %s: %w", dir, err)
	}

	var manifests []*Manifest
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".md") {
			continue
		}

		path := filepath.Join(dir, entry.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("reading %s: %w", path, err)
		}

		name := strings.TrimSuffix(entry.Name(), ".md")
		m, err := Parse(name, data)
		if err != nil {
			return nil, fmt.Errorf("parsing %s: %w", path, err)
		}

		m.SourcePath = path
		manifests = append(manifests, m)
	}

	sort.Slice(manifests, func(i, j int) bool {
		return manifests[i].Name < manifests[j].Name
	})

	return manifests, nil
}

// LoadDirLenient reads all .md files from dir and returns the successfully
// parsed manifests alongside any per-file errors. Processing continues past
// individual failures so that one bad file does not block the rest.
func LoadDirLenient(dir string) ([]*Manifest, []error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, []error{fmt.Errorf("reading role directory %s: %w", dir, err)}
	}

	var manifests []*Manifest
	var errs []error

	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".md") {
			continue
		}

		path := filepath.Join(dir, entry.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			errs = append(errs, fmt.Errorf("reading %s: %w", path, err))
			continue
		}

		name := strings.TrimSuffix(entry.Name(), ".md")
		m, err := Parse(name, data)
		if err != nil {
			errs = append(errs, fmt.Errorf("parsing %s: %w", path, err))
			continue
		}

		m.SourcePath = path
		manifests = append(manifests, m)
	}

	sort.Slice(manifests, func(i, j int) bool {
		return manifests[i].Name < manifests[j].Name
	})

	return manifests, errs
}

// LoadFile reads and parses a single role manifest from path.
// The role name is derived from the filename stem.
func LoadFile(path string) (*Manifest, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}

	name := strings.TrimSuffix(filepath.Base(path), ".md")
	m, err := Parse(name, data)
	if err != nil {
		return nil, err
	}

	m.SourcePath = path
	return m, nil
}
