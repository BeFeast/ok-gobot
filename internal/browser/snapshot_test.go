package browser

import (
	"context"
	"fmt"
	"reflect"
	"regexp"
	"strconv"
	"testing"

	"github.com/chromedp/cdproto/accessibility"
	"github.com/chromedp/cdproto/cdp"
	"github.com/go-json-experiment/json/jsontext"
)

func TestSnapshotBuildsRefMapAndCachesByTab(t *testing.T) {
	manager := NewManager("")
	manager.resolveTabID = func(context.Context) string { return "tab-1" }
	manager.getFullAXTree = func(context.Context) ([]*accessibility.Node, error) {
		return []*accessibility.Node{
			{
				NodeID:           accessibility.NodeID("root"),
				Role:             stringAXValue("RootWebArea"),
				Name:             stringAXValue("Example"),
				ChildIDs:         []accessibility.NodeID{accessibility.NodeID("button")},
				BackendDOMNodeID: cdp.BackendNodeID(1),
			},
			{
				NodeID:           accessibility.NodeID("button"),
				ParentID:         accessibility.NodeID("root"),
				Role:             stringAXValue("button"),
				Name:             stringAXValue("Continue"),
				BackendDOMNodeID: cdp.BackendNodeID(2),
			},
		}, nil
	}
	manager.resolveNodeIDs = func(_ context.Context, backendIDs []cdp.BackendNodeID) ([]cdp.NodeID, error) {
		want := []cdp.BackendNodeID{cdp.BackendNodeID(1), cdp.BackendNodeID(2)}
		if !reflect.DeepEqual(backendIDs, want) {
			t.Fatalf("resolveNodeIDs backendIDs = %v, want %v", backendIDs, want)
		}
		return []cdp.NodeID{cdp.NodeID(1001), cdp.NodeID(2002)}, nil
	}

	snapshotID, nodes, err := manager.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("Snapshot failed: %v", err)
	}

	uuidPattern := regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
	if !uuidPattern.MatchString(snapshotID) {
		t.Fatalf("snapshotID %q is not a valid UUID v4", snapshotID)
	}

	if len(nodes) != 1 {
		t.Fatalf("Snapshot nodes length = %d, want 1", len(nodes))
	}
	if len(nodes[0].Children) != 1 {
		t.Fatalf("Snapshot root children = %d, want 1", len(nodes[0].Children))
	}

	buttonRef := nodes[0].Children[0].Ref
	if buttonRef == "" {
		t.Fatal("button ref is empty")
	}

	nodeID, err := manager.resolveNodeID(context.Background(), snapshotID, buttonRef)
	if err != nil {
		t.Fatalf("resolveNodeID failed: %v", err)
	}
	if nodeID != cdp.NodeID(2002) {
		t.Fatalf("resolved nodeID = %d, want %d", nodeID, cdp.NodeID(2002))
	}
}

func TestClickByRefUsesResolvedNodeID(t *testing.T) {
	manager := NewManager("")
	manager.resolveTabID = func(context.Context) string { return "tab-1" }
	manager.storeSnapshotForTab("tab-1", "snap-1", map[string]cdp.NodeID{
		"r7": cdp.NodeID(77),
	})

	var clicked cdp.NodeID
	manager.clickByNodeID = func(_ context.Context, nodeID cdp.NodeID) error {
		clicked = nodeID
		return nil
	}

	if err := manager.ClickByRef(context.Background(), "snap-1", "r7"); err != nil {
		t.Fatalf("ClickByRef failed: %v", err)
	}
	if clicked != cdp.NodeID(77) {
		t.Fatalf("clicked nodeID = %d, want %d", clicked, cdp.NodeID(77))
	}
}

func TestSnapshotReplacesCacheForSameTab(t *testing.T) {
	manager := NewManager("")
	manager.resolveTabID = func(context.Context) string { return "tab-1" }

	calls := 0
	manager.getFullAXTree = func(context.Context) ([]*accessibility.Node, error) {
		calls++
		nodeID := accessibility.NodeID(fmt.Sprintf("node-%d", calls))
		backendID := cdp.BackendNodeID(calls)
		return []*accessibility.Node{
			{
				NodeID:           nodeID,
				Role:             stringAXValue("button"),
				Name:             stringAXValue("Next"),
				BackendDOMNodeID: backendID,
			},
		}, nil
	}
	manager.resolveNodeIDs = func(_ context.Context, backendIDs []cdp.BackendNodeID) ([]cdp.NodeID, error) {
		if len(backendIDs) != 1 {
			t.Fatalf("backendIDs length = %d, want 1", len(backendIDs))
		}
		return []cdp.NodeID{cdp.NodeID(9000 + int64(backendIDs[0]))}, nil
	}

	snapshotID1, nodes1, err := manager.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("first Snapshot failed: %v", err)
	}
	if len(nodes1) != 1 {
		t.Fatalf("first snapshot nodes length = %d, want 1", len(nodes1))
	}

	snapshotID2, nodes2, err := manager.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("second Snapshot failed: %v", err)
	}
	if snapshotID1 == snapshotID2 {
		t.Fatal("snapshot IDs should differ across snapshots")
	}
	if len(nodes2) != 1 {
		t.Fatalf("second snapshot nodes length = %d, want 1", len(nodes2))
	}

	if _, err := manager.resolveNodeID(context.Background(), snapshotID1, nodes1[0].Ref); err == nil {
		t.Fatal("expected first snapshot_id to be stale after second snapshot")
	}

	nodeID, err := manager.resolveNodeID(context.Background(), snapshotID2, nodes2[0].Ref)
	if err != nil {
		t.Fatalf("resolveNodeID for second snapshot failed: %v", err)
	}
	if nodeID != cdp.NodeID(9002) {
		t.Fatalf("resolved nodeID = %d, want %d", nodeID, cdp.NodeID(9002))
	}
}

func TestInvalidateSnapshotForTab(t *testing.T) {
	manager := NewManager("")
	manager.resolveTabID = func(context.Context) string { return "tab-1" }
	manager.storeSnapshotForTab("tab-1", "snap-1", map[string]cdp.NodeID{"r1": cdp.NodeID(1)})

	manager.invalidateSnapshotForTab("tab-1")

	if _, err := manager.resolveNodeID(context.Background(), "snap-1", "r1"); err == nil {
		t.Fatal("expected cache miss after invalidation")
	}
}

func stringAXValue(v string) *accessibility.Value {
	return &accessibility.Value{Value: jsontext.Value(strconv.Quote(v))}
}
