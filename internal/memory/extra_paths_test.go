package memory

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	_ "github.com/mattn/go-sqlite3"
)

func TestNormalizeExtraPathsExpandsTildeAndDefaultsPattern(t *testing.T) {
	home := t.TempDir()
	prevHome := HomeDirFunc
	HomeDirFunc = func() (string, error) { return home, nil }
	defer func() { HomeDirFunc = prevHome }()

	rw := false
	raw := []RawExtraPath{
		{Name: "Obsidian", Path: "~/notes", Scope: "personal"},
		{Name: "homelab", Path: filepath.Join(home, "homelab"), Patterns: []string{"docs/*.md", "**/*.md"}, ReadOnly: &rw},
	}

	got, err := NormalizeExtraPaths(raw)
	if err != nil {
		t.Fatalf("NormalizeExtraPaths failed: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(got))
	}

	if got[0].Name != "obsidian" {
		t.Fatalf("expected lowercased name, got %q", got[0].Name)
	}
	wantPath := filepath.Join(home, "notes")
	if got[0].Path != wantPath {
		t.Fatalf("home expansion failed: got %q want %q", got[0].Path, wantPath)
	}
	if len(got[0].Patterns) != 1 || got[0].Patterns[0] != DefaultExtraPathPattern {
		t.Fatalf("default pattern not applied: %v", got[0].Patterns)
	}
	if !got[0].ReadOnly {
		t.Fatalf("read-only must default to true")
	}
	if got[0].Scope != "personal" {
		t.Fatalf("scope not preserved: %q", got[0].Scope)
	}

	if got[1].ReadOnly {
		t.Fatalf("read-only=false override not applied")
	}
	if len(got[1].Patterns) != 2 {
		t.Fatalf("explicit patterns not preserved: %v", got[1].Patterns)
	}
}

func TestNormalizeExtraPathsRejectsBadInputs(t *testing.T) {
	cases := []struct {
		name string
		raw  []RawExtraPath
		want string
	}{
		{
			name: "empty name",
			raw:  []RawExtraPath{{Name: "", Path: "/tmp"}},
			want: "name is required",
		},
		{
			name: "invalid name",
			raw:  []RawExtraPath{{Name: "has space", Path: "/tmp"}},
			want: "invalid name",
		},
		{
			name: "duplicate name",
			raw: []RawExtraPath{
				{Name: "obsidian", Path: "/tmp/a"},
				{Name: "obsidian", Path: "/tmp/b"},
			},
			want: "duplicate name",
		},
		{
			name: "empty path",
			raw:  []RawExtraPath{{Name: "vault", Path: ""}},
			want: "path is required",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := NormalizeExtraPaths(tc.raw)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("expected error containing %q, got %v", tc.want, err)
			}
		})
	}
}

func TestSourceLabelForExtraAndParse(t *testing.T) {
	label := SourceLabelForExtra("obsidian", "notes/today.md")
	if label != "extra:obsidian/notes/today.md" {
		t.Fatalf("unexpected label %q", label)
	}
	collection, rel, ok := ParseExtraSourceLabel(label)
	if !ok || collection != "obsidian" || rel != "notes/today.md" {
		t.Fatalf("parse failed: collection=%q rel=%q ok=%v", collection, rel, ok)
	}
	if _, _, ok := ParseExtraSourceLabel("MEMORY.md"); ok {
		t.Fatalf("plain source must not parse as extra")
	}
	if _, _, ok := ParseExtraSourceLabel("extra:onlyone"); ok {
		t.Fatalf("source without collection/path must not parse")
	}
}

func TestExtraPathSourcesMatchesGlobsAndSkipsHidden(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "top.md"), "# top")
	mustWrite(t, filepath.Join(root, "notes", "a.md"), "# a")
	mustWrite(t, filepath.Join(root, "notes", "deep", "b.md"), "# b")
	mustWrite(t, filepath.Join(root, "notes", "ignore.txt"), "ignore")
	mustWrite(t, filepath.Join(root, ".secret", "hidden.md"), "# hidden")
	mustWrite(t, filepath.Join(root, "notes", ".private", "skip.md"), "# skip")

	extra := ExtraPath{Name: "vault", Path: root, Patterns: []string{"**/*.md"}}
	sources, err := ExtraPathSources(extra)
	if err != nil {
		t.Fatalf("ExtraPathSources failed: %v", err)
	}

	got := make([]string, len(sources))
	for i, s := range sources {
		got[i] = s.RelativePath
	}
	sort.Strings(got)
	want := []string{
		"extra:vault/notes/a.md",
		"extra:vault/notes/deep/b.md",
		"extra:vault/top.md",
	}
	if len(got) != len(want) {
		t.Fatalf("expected %v, got %v", want, got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("source[%d] = %q, want %q (full: %v)", i, got[i], want[i], got)
		}
	}
}

func TestExtraPathSourcesMissingRootIsNonFatal(t *testing.T) {
	extra := ExtraPath{Name: "missing", Path: filepath.Join(t.TempDir(), "does-not-exist"), Patterns: []string{"**/*.md"}}
	sources, err := ExtraPathSources(extra)
	if err != nil {
		t.Fatalf("missing root must not error: got %v", err)
	}
	if len(sources) != 0 {
		t.Fatalf("missing root must yield zero sources, got %d", len(sources))
	}
}

func TestResolveExtraPathFileRejectsTraversal(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "ok.md"), "# ok")
	extra := ExtraPath{Name: "vault", Path: root, Patterns: []string{"**/*.md"}}

	if _, err := ResolveExtraPathFile(extra, "ok.md"); err != nil {
		t.Fatalf("legit path failed: %v", err)
	}
	if _, err := ResolveExtraPathFile(extra, "../escape.md"); err == nil {
		t.Fatalf("expected traversal rejection")
	}
	if _, err := ResolveExtraPathFile(extra, "/etc/passwd"); err == nil {
		t.Fatalf("expected absolute path rejection")
	}
	if _, err := ResolveExtraPathFile(extra, ".hidden/file.md"); err == nil {
		t.Fatalf("expected hidden segment rejection")
	}
}

func TestResolveExtraPathFileBlocksSymlinkEscape(t *testing.T) {
	outside := t.TempDir()
	mustWrite(t, filepath.Join(outside, "secret.md"), "# secret")

	root := t.TempDir()
	if err := os.Symlink(filepath.Join(outside, "secret.md"), filepath.Join(root, "evil.md")); err != nil {
		t.Skipf("symlink unsupported on this filesystem: %v", err)
	}

	extra := ExtraPath{Name: "vault", Path: root, Patterns: []string{"**/*.md"}}
	if _, err := ResolveExtraPathFile(extra, "evil.md"); err == nil {
		t.Fatalf("expected symlink-escape rejection")
	}
}

func TestExtraPathByLabel(t *testing.T) {
	extras := []ExtraPath{
		{Name: "obsidian", Path: "/tmp/o"},
		{Name: "homelab", Path: "/tmp/h"},
	}
	extra, rel, ok := ExtraPathByLabel(extras, "extra:obsidian/notes/x.md")
	if !ok || extra.Name != "obsidian" || rel != "notes/x.md" {
		t.Fatalf("lookup failed: extra=%v rel=%q ok=%v", extra, rel, ok)
	}
	if _, _, ok := ExtraPathByLabel(extras, "extra:unknown/x.md"); ok {
		t.Fatalf("unknown collection must not match")
	}
	if _, _, ok := ExtraPathByLabel(extras, "MEMORY.md"); ok {
		t.Fatalf("workspace source must not match extras")
	}
}

func TestIndexExtraPathsAndDiagnostics(t *testing.T) {
	db := openIndexerTestDB(t)
	defer db.Close() //nolint:errcheck

	store, err := NewMemoryStore(db)
	if err != nil {
		t.Fatalf("NewMemoryStore failed: %v", err)
	}

	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "a.md"), "# A\n\nFirst.")
	mustWrite(t, filepath.Join(root, "sub", "b.md"), "# B\n\nSecond.")
	mustWrite(t, filepath.Join(root, "ignored.txt"), "skip")

	missing := filepath.Join(t.TempDir(), "missing")
	extras := []ExtraPath{
		{Name: "vault", Path: root, Patterns: []string{"**/*.md"}, ReadOnly: true, Scope: "personal"},
		{Name: "missing", Path: missing, Patterns: []string{"**/*.md"}, ReadOnly: true},
	}

	indexer := NewIndexer("", store, &stubBatchEmbedder{}, WithIndexerChunking(64, 0))
	stats, errs := IndexExtraPaths(context.Background(), extras, indexer)
	if len(errs) != 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	if stats.FilesIndexed != 2 {
		t.Fatalf("FilesIndexed = %d, want 2", stats.FilesIndexed)
	}

	got := countAllChunks(t, db)
	if got != 2 {
		t.Fatalf("chunk count = %d, want 2", got)
	}
	if c := countSourceChunks(t, db, "extra:vault/a.md"); c != 1 {
		t.Fatalf("vault/a chunk = %d, want 1", c)
	}
	if c := countSourceChunks(t, db, "extra:vault/sub/b.md"); c != 1 {
		t.Fatalf("vault/sub/b chunk = %d, want 1", c)
	}

	diag := store.ExtraPathDiagnostics(context.Background(), extras)
	if len(diag) != 2 {
		t.Fatalf("expected 2 diag entries, got %d", len(diag))
	}
	if !diag[0].Available || diag[0].SourceCount != 2 || diag[0].ChunkCount != 2 || diag[0].Scope != "personal" || !diag[0].ReadOnly {
		t.Fatalf("vault diag wrong: %+v", diag[0])
	}
	if diag[1].Available || diag[1].Error == "" || diag[1].SourceCount != 0 || diag[1].ChunkCount != 0 {
		t.Fatalf("missing diag wrong: %+v", diag[1])
	}

	if err := store.ClearExtraSources(context.Background(), "vault"); err != nil {
		t.Fatalf("ClearExtraSources failed: %v", err)
	}
	if got := countAllChunks(t, db); got != 0 {
		t.Fatalf("chunks after clear = %d, want 0", got)
	}
}

func TestExtraPathRelativeMatchesAndFiltersHidden(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "x.md"), "")
	mustWrite(t, filepath.Join(root, ".secret", "y.md"), "")

	extra := ExtraPath{Name: "vault", Path: root, Patterns: []string{"**/*.md"}}

	rel, ok := ExtraPathRelative(extra, filepath.Join(root, "x.md"))
	if !ok || rel != "x.md" {
		t.Fatalf("expected x.md, got rel=%q ok=%v", rel, ok)
	}
	if _, ok := ExtraPathRelative(extra, filepath.Join(root, ".secret", "y.md")); ok {
		t.Fatalf("hidden segment must be rejected")
	}
	outside := filepath.Join(t.TempDir(), "outside.md")
	mustWrite(t, outside, "")
	if _, ok := ExtraPathRelative(extra, outside); ok {
		t.Fatalf("path outside extra root must not match")
	}
}

func TestMatchExtraGlob(t *testing.T) {
	cases := []struct {
		pattern string
		name    string
		want    bool
	}{
		{"**/*.md", "a.md", true},
		{"**/*.md", "x/y/z.md", true},
		{"**/*.md", "a.txt", false},
		{"docs/**/*.md", "docs/a.md", true},
		{"docs/**/*.md", "docs/sub/a.md", true},
		{"docs/**/*.md", "other/a.md", false},
		{"*.md", "a.md", true},
		{"*.md", "sub/a.md", false},
	}
	for _, tc := range cases {
		got, err := matchExtraGlob(tc.pattern, tc.name)
		if err != nil {
			t.Fatalf("pattern %q: %v", tc.pattern, err)
		}
		if got != tc.want {
			t.Fatalf("matchExtraGlob(%q,%q) = %v, want %v", tc.pattern, tc.name, got, tc.want)
		}
	}
}
