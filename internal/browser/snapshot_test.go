package browser

import (
	"testing"
	"time"

	"github.com/chromedp/cdproto/cdp"
)

func TestSnapshotStore_PutAndGet(t *testing.T) {
	store := NewSnapshotStore(5 * time.Second)

	snap := &Snapshot{
		ID:        "abc123",
		URL:       "https://example.com",
		Title:     "Example",
		CreatedAt: time.Now(),
		refMap: map[string]cdp.BackendNodeID{
			"e1": 100,
			"e2": 200,
		},
		Elements: []ElementRef{
			{Ref: "e1", Role: "button", Name: "Submit", BackendNodeID: 100},
			{Ref: "e2", Role: "link", Name: "Home", BackendNodeID: 200},
		},
	}

	store.Put(snap)

	got := store.Get("abc123")
	if got == nil {
		t.Fatal("expected snapshot, got nil")
	}
	if got.URL != "https://example.com" {
		t.Errorf("URL = %q, want %q", got.URL, "https://example.com")
	}
	if len(got.Elements) != 2 {
		t.Errorf("len(Elements) = %d, want 2", len(got.Elements))
	}
}

func TestSnapshotStore_GetMissing(t *testing.T) {
	store := NewSnapshotStore(5 * time.Second)
	if got := store.Get("nonexistent"); got != nil {
		t.Errorf("expected nil for missing snapshot, got %+v", got)
	}
}

func TestSnapshotStore_TTLExpiry(t *testing.T) {
	store := NewSnapshotStore(50 * time.Millisecond)

	snap := &Snapshot{
		ID:        "exp1",
		CreatedAt: time.Now(),
		refMap:    map[string]cdp.BackendNodeID{"e1": 1},
	}
	store.Put(snap)

	// Should be available immediately.
	if store.Get("exp1") == nil {
		t.Fatal("expected snapshot before TTL expiry")
	}

	// Wait for expiry.
	time.Sleep(100 * time.Millisecond)

	if store.Get("exp1") != nil {
		t.Error("expected nil after TTL expiry")
	}
}

func TestSnapshotStore_Resolve(t *testing.T) {
	store := NewSnapshotStore(5 * time.Second)

	snap := &Snapshot{
		ID:        "res1",
		CreatedAt: time.Now(),
		refMap: map[string]cdp.BackendNodeID{
			"e1": 42,
			"e2": 99,
		},
	}
	store.Put(snap)

	// Valid ref.
	nodeID, err := store.Resolve("res1", "e1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if nodeID != 42 {
		t.Errorf("nodeID = %d, want 42", nodeID)
	}

	// Invalid ref.
	_, err = store.Resolve("res1", "e99")
	if err == nil {
		t.Error("expected error for invalid ref")
	}

	// Invalid snapshot.
	_, err = store.Resolve("nonexistent", "e1")
	if err == nil {
		t.Error("expected error for invalid snapshot_id")
	}
}

func TestSnapshotStore_ResolveExpired(t *testing.T) {
	store := NewSnapshotStore(50 * time.Millisecond)

	snap := &Snapshot{
		ID:        "exp2",
		CreatedAt: time.Now(),
		refMap:    map[string]cdp.BackendNodeID{"e1": 1},
	}
	store.Put(snap)

	time.Sleep(100 * time.Millisecond)

	_, err := store.Resolve("exp2", "e1")
	if err == nil {
		t.Error("expected error for expired snapshot")
	}
	if err != nil && !containsSubstring(err.Error(), "expired") && !containsSubstring(err.Error(), "not found") {
		t.Errorf("error should mention expired/not found, got: %v", err)
	}
}

func TestSnapshotStore_EvictsOldest(t *testing.T) {
	store := NewSnapshotStore(10 * time.Second)

	// Fill beyond capacity.
	for i := 0; i < maxSnapshots+5; i++ {
		snap := &Snapshot{
			ID:        generateID(),
			CreatedAt: time.Now(),
			refMap:    map[string]cdp.BackendNodeID{},
		}
		store.Put(snap)
	}

	if store.Len() > maxSnapshots {
		t.Errorf("store.Len() = %d, want <= %d", store.Len(), maxSnapshots)
	}
}

func TestSnapshotStore_Len(t *testing.T) {
	store := NewSnapshotStore(5 * time.Second)

	if store.Len() != 0 {
		t.Errorf("empty store Len() = %d, want 0", store.Len())
	}

	store.Put(&Snapshot{ID: "a", CreatedAt: time.Now(), refMap: map[string]cdp.BackendNodeID{}})
	store.Put(&Snapshot{ID: "b", CreatedAt: time.Now(), refMap: map[string]cdp.BackendNodeID{}})

	if store.Len() != 2 {
		t.Errorf("Len() = %d, want 2", store.Len())
	}
}

func TestFormatSnapshot(t *testing.T) {
	snap := &Snapshot{
		ID:    "fmt1",
		URL:   "https://example.com",
		Title: "Test Page",
		Elements: []ElementRef{
			{Ref: "e1", Role: "button", Name: "OK"},
			{Ref: "e2", Role: "link", Name: ""},
		},
	}

	out := FormatSnapshot(snap)
	if !containsSubstring(out, "snapshot_id: fmt1") {
		t.Errorf("output missing snapshot_id, got:\n%s", out)
	}
	if !containsSubstring(out, "[e1] button") {
		t.Errorf("output missing e1, got:\n%s", out)
	}
	if !containsSubstring(out, `"OK"`) {
		t.Errorf("output missing name, got:\n%s", out)
	}
	if !containsSubstring(out, "[e2] link") {
		t.Errorf("output missing e2, got:\n%s", out)
	}
}

func TestGenerateID(t *testing.T) {
	id1 := generateID()
	id2 := generateID()
	if id1 == id2 {
		t.Error("generateID returned duplicate IDs")
	}
	if len(id1) != 8 {
		t.Errorf("expected 8-char hex ID, got %q (len %d)", id1, len(id1))
	}
}

func TestAxValueString(t *testing.T) {
	if got := axValueString(nil); got != "" {
		t.Errorf("axValueString(nil) = %q, want empty", got)
	}
}

func containsSubstring(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(s) > 0 && contains(s, sub))
}

func contains(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
