package browser

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/chromedp/cdproto/accessibility"
	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/dom"
	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/chromedp"
)

// AXNode is a simplified accessibility node used in browser snapshots.
type AXNode struct {
	Ref      string   `json:"ref"`
	Role     string   `json:"role,omitempty"`
	Name     string   `json:"name,omitempty"`
	Children []AXNode `json:"children,omitempty"`
}

type snapshotCacheEntry struct {
	snapshotID  string
	refToNodeID map[string]cdp.NodeID
}

type tabIDResolver func(ctx context.Context) string
type fullAXTreeGetter func(ctx context.Context) ([]*accessibility.Node, error)
type nodeIDsResolver func(ctx context.Context, backendIDs []cdp.BackendNodeID) ([]cdp.NodeID, error)
type clickByNodeIDFunc func(ctx context.Context, nodeID cdp.NodeID) error
type typeByNodeIDFunc func(ctx context.Context, nodeID cdp.NodeID, value string) error

// Snapshot captures the current accessibility tree and returns a ref-addressable
// snapshot, while caching ref -> nodeId mappings in-process.
func (m *Manager) Snapshot(ctx context.Context) (string, []AXNode, error) {
	if ctx == nil {
		return "", nil, fmt.Errorf("context is required")
	}

	axTree, err := m.getFullAXTree(ctx)
	if err != nil {
		return "", nil, fmt.Errorf("failed to get accessibility tree: %w", err)
	}

	backendIDs := collectBackendNodeIDs(axTree)
	backendToNodeID := make(map[cdp.BackendNodeID]cdp.NodeID, len(backendIDs))
	if len(backendIDs) > 0 {
		nodeIDs, err := m.resolveNodeIDs(ctx, backendIDs)
		if err != nil {
			return "", nil, fmt.Errorf("failed to resolve backend DOM node IDs: %w", err)
		}
		if len(nodeIDs) != len(backendIDs) {
			return "", nil, fmt.Errorf("mismatched node ID mapping count: got %d, want %d", len(nodeIDs), len(backendIDs))
		}
		for i, backendID := range backendIDs {
			if nodeIDs[i] == 0 {
				continue
			}
			backendToNodeID[backendID] = nodeIDs[i]
		}
	}

	nodes, refMap := buildAXSnapshot(axTree, backendToNodeID)
	snapshotID, err := newSnapshotID()
	if err != nil {
		return "", nil, fmt.Errorf("failed to generate snapshot ID: %w", err)
	}

	tabID := m.resolveTabID(ctx)
	if tabID == "" {
		return "", nil, fmt.Errorf("failed to resolve tab ID for snapshot")
	}
	m.storeSnapshotForTab(tabID, snapshotID, refMap)

	return snapshotID, nodes, nil
}

// ClickByRef clicks on a node previously returned by Snapshot.
func (m *Manager) ClickByRef(ctx context.Context, snapshotID, ref string) error {
	nodeID, err := m.resolveNodeID(ctx, snapshotID, ref)
	if err != nil {
		return err
	}
	if err := m.clickByNodeID(ctx, nodeID); err != nil {
		return fmt.Errorf("failed to click ref %q: %w", ref, err)
	}
	return nil
}

// TypeByRef types text into a node previously returned by Snapshot.
func (m *Manager) TypeByRef(ctx context.Context, snapshotID, ref, value string) error {
	nodeID, err := m.resolveNodeID(ctx, snapshotID, ref)
	if err != nil {
		return err
	}
	if err := m.typeByNodeID(ctx, nodeID, value); err != nil {
		return fmt.Errorf("failed to type into ref %q: %w", ref, err)
	}
	return nil
}

func (m *Manager) resolveNodeID(ctx context.Context, snapshotID, ref string) (cdp.NodeID, error) {
	if snapshotID == "" {
		return 0, fmt.Errorf("snapshot_id is required")
	}
	if ref == "" {
		return 0, fmt.Errorf("ref is required")
	}

	tabID := m.resolveTabID(ctx)
	if tabID == "" {
		return 0, fmt.Errorf("failed to resolve tab ID")
	}

	m.snapshotMu.RLock()
	entry, ok := m.snapshotCache[tabID]
	m.snapshotMu.RUnlock()
	if !ok {
		return 0, fmt.Errorf("no snapshot cache for current tab")
	}
	if entry.snapshotID != snapshotID {
		return 0, fmt.Errorf("snapshot_id %q is stale for current tab", snapshotID)
	}

	nodeID, ok := entry.refToNodeID[ref]
	if !ok {
		return 0, fmt.Errorf("ref %q not found in snapshot %q", ref, snapshotID)
	}

	return nodeID, nil
}

func (m *Manager) attachNavigationInvalidation(ctx context.Context) {
	if ctx == nil {
		return
	}

	chromedp.ListenTarget(ctx, func(event interface{}) {
		switch event.(type) {
		case *page.EventFrameNavigated, *page.EventNavigatedWithinDocument, *dom.EventDocumentUpdated:
			m.invalidateSnapshotForContext(ctx)
		}
	})
}

func (m *Manager) invalidateSnapshotForContext(ctx context.Context) {
	tabID := m.resolveTabID(ctx)
	if tabID == "" {
		return
	}
	m.invalidateSnapshotForTab(tabID)
}

func (m *Manager) invalidateSnapshotForTab(tabID string) {
	if tabID == "" {
		return
	}
	m.snapshotMu.Lock()
	delete(m.snapshotCache, tabID)
	m.snapshotMu.Unlock()
}

func (m *Manager) storeSnapshotForTab(tabID, snapshotID string, refToNodeID map[string]cdp.NodeID) {
	copyMap := make(map[string]cdp.NodeID, len(refToNodeID))
	for ref, nodeID := range refToNodeID {
		copyMap[ref] = nodeID
	}

	m.snapshotMu.Lock()
	m.snapshotCache[tabID] = snapshotCacheEntry{
		snapshotID:  snapshotID,
		refToNodeID: copyMap,
	}
	m.snapshotMu.Unlock()
}

func (m *Manager) clearSnapshotCache() {
	m.snapshotMu.Lock()
	m.snapshotCache = make(map[string]snapshotCacheEntry)
	m.snapshotMu.Unlock()
}

func (m *Manager) defaultTabIDForContext(ctx context.Context) string {
	c := chromedp.FromContext(ctx)
	if c == nil || c.Target == nil || c.Target.TargetID == "" {
		return ""
	}
	return string(c.Target.TargetID)
}

func getFullAXTree(ctx context.Context) ([]*accessibility.Node, error) {
	return accessibility.GetFullAXTree().Do(ctx)
}

func pushNodesByBackendIDs(ctx context.Context, backendIDs []cdp.BackendNodeID) ([]cdp.NodeID, error) {
	return dom.PushNodesByBackendIDsToFrontend(backendIDs).Do(ctx)
}

func (m *Manager) defaultClickByNodeID(ctx context.Context, nodeID cdp.NodeID) error {
	return chromedp.Run(ctx,
		chromedp.WaitVisible([]cdp.NodeID{nodeID}, chromedp.ByNodeID),
		chromedp.Click([]cdp.NodeID{nodeID}, chromedp.ByNodeID),
	)
}

func (m *Manager) defaultTypeByNodeID(ctx context.Context, nodeID cdp.NodeID, value string) error {
	return chromedp.Run(ctx,
		chromedp.WaitVisible([]cdp.NodeID{nodeID}, chromedp.ByNodeID),
		chromedp.Focus([]cdp.NodeID{nodeID}, chromedp.ByNodeID),
		chromedp.SendKeys([]cdp.NodeID{nodeID}, value, chromedp.ByNodeID),
	)
}

func collectBackendNodeIDs(axTree []*accessibility.Node) []cdp.BackendNodeID {
	seen := make(map[cdp.BackendNodeID]struct{})
	backendIDs := make([]cdp.BackendNodeID, 0)

	for _, node := range axTree {
		if node == nil || node.BackendDOMNodeID == 0 {
			continue
		}
		if _, ok := seen[node.BackendDOMNodeID]; ok {
			continue
		}
		seen[node.BackendDOMNodeID] = struct{}{}
		backendIDs = append(backendIDs, node.BackendDOMNodeID)
	}

	return backendIDs
}

func buildAXSnapshot(
	axTree []*accessibility.Node,
	backendToNodeID map[cdp.BackendNodeID]cdp.NodeID,
) ([]AXNode, map[string]cdp.NodeID) {
	nodesByID := make(map[accessibility.NodeID]*accessibility.Node, len(axTree))
	for _, node := range axTree {
		if node == nil {
			continue
		}
		nodesByID[node.NodeID] = node
	}

	roots := make([]accessibility.NodeID, 0)
	for _, node := range axTree {
		if node == nil {
			continue
		}
		if node.ParentID == "" {
			roots = append(roots, node.NodeID)
			continue
		}
		if _, ok := nodesByID[node.ParentID]; !ok {
			roots = append(roots, node.NodeID)
		}
	}

	visited := make(map[accessibility.NodeID]bool, len(nodesByID))
	refToNodeID := make(map[string]cdp.NodeID)
	refCounter := 0

	var visit func(accessibility.NodeID) AXNode
	visit = func(id accessibility.NodeID) AXNode {
		node := nodesByID[id]
		refCounter++
		ref := fmt.Sprintf("r%d", refCounter)

		result := AXNode{
			Ref:  ref,
			Role: normalizeAXValue(node.Role),
			Name: normalizeAXValue(node.Name),
		}

		if nodeID, ok := backendToNodeID[node.BackendDOMNodeID]; ok && nodeID != 0 {
			refToNodeID[ref] = nodeID
		}

		visited[id] = true

		for _, childID := range node.ChildIDs {
			if _, ok := nodesByID[childID]; !ok || visited[childID] {
				continue
			}
			result.Children = append(result.Children, visit(childID))
		}

		return result
	}

	out := make([]AXNode, 0, len(roots))
	for _, rootID := range roots {
		if visited[rootID] {
			continue
		}
		out = append(out, visit(rootID))
	}

	for _, node := range axTree {
		if node == nil || visited[node.NodeID] {
			continue
		}
		out = append(out, visit(node.NodeID))
	}

	return out, refToNodeID
}

func normalizeAXValue(v *accessibility.Value) string {
	if v == nil || len(v.Value) == 0 {
		return ""
	}

	raw := []byte(v.Value)
	var asString string
	if err := json.Unmarshal(raw, &asString); err == nil {
		return asString
	}

	var generic interface{}
	if err := json.Unmarshal(raw, &generic); err == nil {
		return fmt.Sprintf("%v", generic)
	}

	return strings.TrimSpace(v.Value.String())
}

func newSnapshotID() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}

	// Set UUID v4 variant bits.
	raw[6] = (raw[6] & 0x0f) | 0x40
	raw[8] = (raw[8] & 0x3f) | 0x80

	return fmt.Sprintf("%x-%x-%x-%x-%x", raw[0:4], raw[4:6], raw[6:8], raw[8:10], raw[10:16]), nil
}
