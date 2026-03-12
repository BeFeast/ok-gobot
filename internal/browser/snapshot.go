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
	var nodes []*accessibility.Node
	err := chromedp.Run(ctx, chromedp.ActionFunc(func(innerCtx context.Context) error {
		// Use raw CDP Execute to avoid strict enum parsing on newer
		// Chromium versions (e.g. Chrome 145 adds "uninteresting"
		// PropertyName which cdproto doesn't recognise).
		var rawResult struct {
			Nodes []json.RawMessage `json:"nodes"`
		}
		if err := cdp.Execute(innerCtx, "Accessibility.getFullAXTree", nil, &rawResult); err != nil {
			return err
		}
		for _, raw := range rawResult.Nodes {
			var node accessibility.Node
			if err := json.Unmarshal(raw, &node); err != nil {
				// Skip nodes with unknown enum values.
				continue
			}
			nodes = append(nodes, &node)
		}
		return nil
	}))
	return nodes, err
}

func pushNodesByBackendIDs(ctx context.Context, backendIDs []cdp.BackendNodeID) ([]cdp.NodeID, error) {
	var nodeIDs []cdp.NodeID
	err := chromedp.Run(ctx, chromedp.ActionFunc(func(innerCtx context.Context) error {
		var err error
		nodeIDs, err = dom.PushNodesByBackendIDsToFrontend(backendIDs).Do(innerCtx)
		return err
	}))
	return nodeIDs, err
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

	// Roles worth including in the snapshot (interactive or meaningful).
	interactiveRoles := map[string]bool{
		"button": true, "link": true, "textbox": true, "checkbox": true,
		"radio": true, "combobox": true, "menuitem": true, "tab": true,
		"switch": true, "slider": true, "spinbutton": true, "searchbox": true,
		"option": true, "menubar": true, "menu": true, "listbox": true,
		"dialog": true, "alertdialog": true, "alert": true, "heading": true,
		"img": true, "navigation": true, "main": true, "form": true,
		"table": true, "row": true, "cell": true, "columnheader": true,
		"rowheader": true, "banner": true, "contentinfo": true,
		"complementary": true, "region": true, "article": true,
		"tree": true, "treeitem": true, "gridcell": true,
	}

	const maxNodes = 200

	isRelevant := func(node *accessibility.Node) bool {
		role := normalizeAXValue(node.Role)
		if interactiveRoles[role] {
			return true
		}
		name := normalizeAXValue(node.Name)
		if role == "text" && name != "" {
			return true
		}
		return false
	}

	var visit func(accessibility.NodeID) (AXNode, bool)
	visit = func(id accessibility.NodeID) (AXNode, bool) {
		node := nodesByID[id]
		visited[id] = true

		role := normalizeAXValue(node.Role)
		name := normalizeAXValue(node.Name)

		var children []AXNode
		for _, childID := range node.ChildIDs {
			if _, ok := nodesByID[childID]; !ok || visited[childID] {
				continue
			}
			if child, ok := visit(childID); ok {
				children = append(children, child)
			}
		}

		relevant := isRelevant(node) || len(children) > 0
		if !relevant {
			return AXNode{}, false
		}

		// Non-relevant containers: keep them as groups to preserve tree
		// structure (needed for ref resolution).
		if !isRelevant(node) && len(children) > 0 {
			refCounter++
			ref := fmt.Sprintf("r%d", refCounter)
			result := AXNode{
				Ref:      ref,
				Role:     role,
				Name:     name,
				Children: children,
			}
			if nodeID, ok := backendToNodeID[node.BackendDOMNodeID]; ok && nodeID != 0 {
				refToNodeID[ref] = nodeID
			}
			return result, true
		}

		refCounter++
		ref := fmt.Sprintf("r%d", refCounter)
		result := AXNode{
			Ref:      ref,
			Role:     role,
			Name:     name,
			Children: children,
		}
		if nodeID, ok := backendToNodeID[node.BackendDOMNodeID]; ok && nodeID != 0 {
			refToNodeID[ref] = nodeID
		}
		return result, true
	}

	out := make([]AXNode, 0, len(roots))
	for _, rootID := range roots {
		if visited[rootID] {
			continue
		}
		if node, ok := visit(rootID); ok {
			out = append(out, node)
		}
	}

	for _, node := range axTree {
		if node == nil || visited[node.NodeID] {
			continue
		}
		if n, ok := visit(node.NodeID); ok {
			out = append(out, n)
		}
	}

	// Truncate to maxNodes to keep context manageable.
	out = truncateAXNodes(out, maxNodes)

	return out, refToNodeID
}

// truncateAXNodes does a breadth-first traversal and keeps at most max nodes.
func truncateAXNodes(roots []AXNode, max int) []AXNode {
	count := countAXNodes(roots)
	if count <= max {
		return roots
	}
	// BFS: keep first `max` nodes, trim children of later nodes.
	var result []AXNode
	total := 0
	for _, root := range roots {
		if total >= max {
			break
		}
		trimmed := trimAXNode(root, &total, max)
		result = append(result, trimmed)
	}
	return result
}

func countAXNodes(nodes []AXNode) int {
	count := len(nodes)
	for _, n := range nodes {
		count += countAXNodes(n.Children)
	}
	return count
}

func trimAXNode(node AXNode, total *int, max int) AXNode {
	*total++
	if *total >= max {
		node.Children = nil
		return node
	}
	var children []AXNode
	for _, child := range node.Children {
		if *total >= max {
			break
		}
		children = append(children, trimAXNode(child, total, max))
	}
	node.Children = children
	return node
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
