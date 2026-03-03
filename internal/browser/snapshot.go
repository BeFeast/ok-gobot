package browser

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/chromedp/cdproto/accessibility"
	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/dom"
	"github.com/chromedp/cdproto/input"
	"github.com/chromedp/chromedp"
)

const (
	defaultSnapshotTTL = 30 * time.Second
	maxSnapshots       = 20
)

// ElementRef describes an interactive element in a snapshot.
type ElementRef struct {
	Ref           string            `json:"ref"`
	Role          string            `json:"role"`
	Name          string            `json:"name"`
	BackendNodeID cdp.BackendNodeID `json:"-"`
}

// Snapshot holds a page accessibility snapshot with ref mappings.
type Snapshot struct {
	ID        string       `json:"snapshot_id"`
	URL       string       `json:"url"`
	Title     string       `json:"title"`
	Elements  []ElementRef `json:"elements"`
	CreatedAt time.Time    `json:"-"`

	refMap map[string]cdp.BackendNodeID
}

// SnapshotStore is a TTL-based in-memory cache for page snapshots.
type SnapshotStore struct {
	mu        sync.RWMutex
	snapshots map[string]*Snapshot
	ttl       time.Duration
}

// NewSnapshotStore creates a new store with the given TTL.
func NewSnapshotStore(ttl time.Duration) *SnapshotStore {
	if ttl <= 0 {
		ttl = defaultSnapshotTTL
	}
	return &SnapshotStore{
		snapshots: make(map[string]*Snapshot),
		ttl:       ttl,
	}
}

// Put stores a snapshot. If the store exceeds maxSnapshots, the oldest entry is evicted.
func (s *SnapshotStore) Put(snap *Snapshot) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Evict expired entries first.
	now := time.Now()
	for id, sn := range s.snapshots {
		if now.Sub(sn.CreatedAt) > s.ttl {
			delete(s.snapshots, id)
		}
	}

	// If still at capacity, evict oldest.
	if len(s.snapshots) >= maxSnapshots {
		var oldestID string
		var oldestTime time.Time
		for id, sn := range s.snapshots {
			if oldestID == "" || sn.CreatedAt.Before(oldestTime) {
				oldestID = id
				oldestTime = sn.CreatedAt
			}
		}
		if oldestID != "" {
			delete(s.snapshots, oldestID)
		}
	}

	s.snapshots[snap.ID] = snap
}

// Get returns a snapshot by ID. Returns nil if not found or expired.
func (s *SnapshotStore) Get(id string) *Snapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()

	snap, ok := s.snapshots[id]
	if !ok {
		return nil
	}
	if time.Since(snap.CreatedAt) > s.ttl {
		return nil
	}
	return snap
}

// Resolve returns the BackendNodeID for a given snapshot_id + ref.
func (s *SnapshotStore) Resolve(snapshotID, ref string) (cdp.BackendNodeID, error) {
	snap := s.Get(snapshotID)
	if snap == nil {
		return 0, fmt.Errorf("snapshot %q not found or expired — take a new snapshot first", snapshotID)
	}
	nodeID, ok := snap.refMap[ref]
	if !ok {
		return 0, fmt.Errorf("ref %q not found in snapshot %q", ref, snapshotID)
	}
	return nodeID, nil
}

// Len returns the number of non-expired snapshots.
func (s *SnapshotStore) Len() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	count := 0
	now := time.Now()
	for _, sn := range s.snapshots {
		if now.Sub(sn.CreatedAt) <= s.ttl {
			count++
		}
	}
	return count
}

// generateID returns a short random hex string.
func generateID() string {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// interactiveRoles lists AX roles that are typically actionable.
var interactiveRoles = map[string]bool{
	"button":        true,
	"link":          true,
	"textbox":       true,
	"checkbox":      true,
	"radio":         true,
	"combobox":      true,
	"menuitem":      true,
	"tab":           true,
	"switch":        true,
	"searchbox":     true,
	"spinbutton":    true,
	"slider":        true,
	"option":        true,
	"menuitemradio": true,
	"treeitem":      true,
}

// TakeSnapshot captures the accessibility tree of the current page,
// assigns refs to interactive elements, and stores the snapshot.
func TakeSnapshot(ctx context.Context, store *SnapshotStore) (*Snapshot, error) {
	// Get page URL + title.
	var url, title string
	if err := chromedp.Run(ctx,
		chromedp.Location(&url),
		chromedp.Title(&title),
	); err != nil {
		return nil, fmt.Errorf("failed to get page info: %w", err)
	}

	// Fetch the full accessibility tree.
	var nodes []*accessibility.Node
	if err := chromedp.Run(ctx, chromedp.ActionFunc(func(ctx context.Context) error {
		var err error
		nodes, err = accessibility.GetFullAXTree().Do(ctx)
		return err
	})); err != nil {
		return nil, fmt.Errorf("failed to get accessibility tree: %w", err)
	}

	// Build refs for interactive elements.
	elements := make([]ElementRef, 0, 64)
	refMap := make(map[string]cdp.BackendNodeID, 64)
	counter := 0

	for _, node := range nodes {
		if node.Ignored {
			continue
		}
		if node.BackendDOMNodeID == 0 {
			continue
		}

		role := axValueString(node.Role)
		if role == "" {
			continue
		}
		name := axValueString(node.Name)

		if !interactiveRoles[role] && name == "" {
			continue
		}
		if !interactiveRoles[role] {
			continue
		}

		counter++
		ref := fmt.Sprintf("e%d", counter)
		elements = append(elements, ElementRef{
			Ref:           ref,
			Role:          role,
			Name:          name,
			BackendNodeID: node.BackendDOMNodeID,
		})
		refMap[ref] = node.BackendDOMNodeID
	}

	snap := &Snapshot{
		ID:        generateID(),
		URL:       url,
		Title:     title,
		Elements:  elements,
		CreatedAt: time.Now(),
		refMap:    refMap,
	}
	store.Put(snap)
	return snap, nil
}

// ClickByRef clicks an element identified by snapshot_id + ref.
func ClickByRef(ctx context.Context, store *SnapshotStore, snapshotID, ref string) error {
	backendID, err := store.Resolve(snapshotID, ref)
	if err != nil {
		return err
	}
	return clickByBackendNodeID(ctx, backendID)
}

// TypeByRef types text into an element identified by snapshot_id + ref.
func TypeByRef(ctx context.Context, store *SnapshotStore, snapshotID, ref, text string) error {
	backendID, err := store.Resolve(snapshotID, ref)
	if err != nil {
		return err
	}
	return typeByBackendNodeID(ctx, backendID, text)
}

// FocusByRef focuses an element identified by snapshot_id + ref.
func FocusByRef(ctx context.Context, store *SnapshotStore, snapshotID, ref string) error {
	backendID, err := store.Resolve(snapshotID, ref)
	if err != nil {
		return err
	}
	return focusByBackendNodeID(ctx, backendID)
}

// clickByBackendNodeID clicks the center of the element.
func clickByBackendNodeID(ctx context.Context, backendID cdp.BackendNodeID) error {
	return chromedp.Run(ctx, chromedp.ActionFunc(func(ctx context.Context) error {
		// Get element position.
		quads, err := dom.GetContentQuads().WithBackendNodeID(backendID).Do(ctx)
		if err != nil {
			return fmt.Errorf("failed to get element position: %w", err)
		}
		if len(quads) == 0 {
			return fmt.Errorf("element has no visible quads")
		}

		// Compute center of first quad (8 floats: 4 x,y pairs).
		q := quads[0]
		if len(q) < 8 {
			return fmt.Errorf("invalid quad data")
		}
		x := (q[0] + q[2] + q[4] + q[6]) / 4
		y := (q[1] + q[3] + q[5] + q[7]) / 4

		// Dispatch mouse press + release.
		if err := input.DispatchMouseEvent(input.MousePressed, x, y).
			WithButton(input.Left).WithClickCount(1).Do(ctx); err != nil {
			return err
		}
		return input.DispatchMouseEvent(input.MouseReleased, x, y).
			WithButton(input.Left).WithClickCount(1).Do(ctx)
	}))
}

// focusByBackendNodeID focuses the element.
func focusByBackendNodeID(ctx context.Context, backendID cdp.BackendNodeID) error {
	return chromedp.Run(ctx, chromedp.ActionFunc(func(ctx context.Context) error {
		return dom.Focus().WithBackendNodeID(backendID).Do(ctx)
	}))
}

// typeByBackendNodeID focuses the element and dispatches key events.
func typeByBackendNodeID(ctx context.Context, backendID cdp.BackendNodeID, text string) error {
	return chromedp.Run(ctx, chromedp.ActionFunc(func(ctx context.Context) error {
		// Focus first.
		if err := dom.Focus().WithBackendNodeID(backendID).Do(ctx); err != nil {
			return fmt.Errorf("failed to focus element: %w", err)
		}

		// Dispatch key events for each character.
		for _, ch := range text {
			s := string(ch)
			if err := input.DispatchKeyEvent(input.KeyChar).WithText(s).Do(ctx); err != nil {
				return fmt.Errorf("failed to type character %q: %w", s, err)
			}
		}
		return nil
	}))
}

// FormatSnapshot returns a human-readable text representation of a snapshot.
func FormatSnapshot(snap *Snapshot) string {
	var b strings.Builder
	fmt.Fprintf(&b, "snapshot_id: %s\n", snap.ID)
	fmt.Fprintf(&b, "url: %s\n", snap.URL)
	fmt.Fprintf(&b, "title: %s\n", snap.Title)
	fmt.Fprintf(&b, "elements: %d\n\n", len(snap.Elements))
	for _, e := range snap.Elements {
		if e.Name != "" {
			fmt.Fprintf(&b, "  [%s] %s %q\n", e.Ref, e.Role, e.Name)
		} else {
			fmt.Fprintf(&b, "  [%s] %s\n", e.Ref, e.Role)
		}
	}
	return b.String()
}

// axValueString extracts the string value from an AX Value.
func axValueString(v *accessibility.Value) string {
	if v == nil {
		return ""
	}
	raw := string(v.Value)
	// Strip surrounding quotes from JSON string values.
	raw = strings.TrimSpace(raw)
	if len(raw) >= 2 && raw[0] == '"' && raw[len(raw)-1] == '"' {
		raw = raw[1 : len(raw)-1]
	}
	return raw
}
