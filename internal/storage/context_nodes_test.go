package storage

import (
	"testing"
)

func TestContextNodesTableCreated(t *testing.T) {
	s := newV2TestStore(t)
	if !tableExists(t, s.DB(), "context_nodes") {
		t.Fatal("context_nodes table not created by migration")
	}
}

func TestSaveAndGetContextNodes(t *testing.T) {
	s := newV2TestStore(t)

	node := ContextNode{
		SessionKey: "agent:bot:main",
		Density:    1,
		Summary:    "first summary",
		SpanStart:  10,
		SpanEnd:    20,
		TokenCount: 50,
	}

	id, err := s.SaveContextNode(node)
	if err != nil {
		t.Fatalf("SaveContextNode: %v", err)
	}
	if id <= 0 {
		t.Fatalf("expected positive ID, got %d", id)
	}

	nodes, err := s.GetContextNodes("agent:bot:main", 1)
	if err != nil {
		t.Fatalf("GetContextNodes: %v", err)
	}
	if len(nodes) != 1 {
		t.Fatalf("expected 1 node, got %d", len(nodes))
	}
	if nodes[0].ID != id {
		t.Errorf("ID = %d, want %d", nodes[0].ID, id)
	}
	if nodes[0].Summary != "first summary" {
		t.Errorf("Summary = %q, want %q", nodes[0].Summary, "first summary")
	}
	if nodes[0].SpanStart != 10 || nodes[0].SpanEnd != 20 {
		t.Errorf("Span = [%d, %d], want [10, 20]", nodes[0].SpanStart, nodes[0].SpanEnd)
	}
	if nodes[0].TokenCount != 50 {
		t.Errorf("TokenCount = %d, want 50", nodes[0].TokenCount)
	}
}

func TestGetContextNodes_FiltersByDensity(t *testing.T) {
	s := newV2TestStore(t)

	for _, d := range []int{0, 1, 1, 2} {
		_, err := s.SaveContextNode(ContextNode{
			SessionKey: "sess",
			Density:    d,
			SpanStart:  1,
			SpanEnd:    10,
		})
		if err != nil {
			t.Fatalf("SaveContextNode density=%d: %v", d, err)
		}
	}

	d1, err := s.GetContextNodes("sess", 1)
	if err != nil {
		t.Fatalf("GetContextNodes: %v", err)
	}
	if len(d1) != 2 {
		t.Fatalf("expected 2 D1 nodes, got %d", len(d1))
	}

	d2, err := s.GetContextNodes("sess", 2)
	if err != nil {
		t.Fatalf("GetContextNodes: %v", err)
	}
	if len(d2) != 1 {
		t.Fatalf("expected 1 D2 node, got %d", len(d2))
	}
}

func TestGetAllContextNodes_OrderedByDensityDesc(t *testing.T) {
	s := newV2TestStore(t)

	for _, d := range []int{1, 2, 1} {
		_, err := s.SaveContextNode(ContextNode{
			SessionKey: "sess",
			Density:    d,
			SpanStart:  1,
			SpanEnd:    10,
		})
		if err != nil {
			t.Fatal(err)
		}
	}

	all, err := s.GetAllContextNodes("sess")
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 3 {
		t.Fatalf("expected 3 nodes, got %d", len(all))
	}
	// First node should be density 2 (highest)
	if all[0].Density != 2 {
		t.Errorf("first node density = %d, want 2", all[0].Density)
	}
}

func TestSetContextNodeParent(t *testing.T) {
	s := newV2TestStore(t)

	childID, _ := s.SaveContextNode(ContextNode{
		SessionKey: "sess",
		Density:    1,
		SpanStart:  1,
		SpanEnd:    10,
	})
	parentID, _ := s.SaveContextNode(ContextNode{
		SessionKey: "sess",
		Density:    2,
		SpanStart:  1,
		SpanEnd:    20,
	})

	if err := s.SetContextNodeParent(childID, parentID); err != nil {
		t.Fatalf("SetContextNodeParent: %v", err)
	}

	children, err := s.GetContextNodeChildren(parentID)
	if err != nil {
		t.Fatalf("GetContextNodeChildren: %v", err)
	}
	if len(children) != 1 {
		t.Fatalf("expected 1 child, got %d", len(children))
	}
	if children[0].ID != childID {
		t.Errorf("child ID = %d, want %d", children[0].ID, childID)
	}
}

func TestDeleteContextNodes(t *testing.T) {
	s := newV2TestStore(t)

	for i := 0; i < 3; i++ {
		_, err := s.SaveContextNode(ContextNode{
			SessionKey: "sess",
			Density:    1,
			SpanStart:  int64(i * 10),
			SpanEnd:    int64(i*10 + 9),
		})
		if err != nil {
			t.Fatal(err)
		}
	}

	if err := s.DeleteContextNodes("sess"); err != nil {
		t.Fatalf("DeleteContextNodes: %v", err)
	}

	nodes, err := s.GetAllContextNodes("sess")
	if err != nil {
		t.Fatal(err)
	}
	if len(nodes) != 0 {
		t.Fatalf("expected 0 nodes after delete, got %d", len(nodes))
	}
}

func TestGetContextNodes_EmptySession(t *testing.T) {
	s := newV2TestStore(t)

	nodes, err := s.GetContextNodes("nonexistent", 1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(nodes) != 0 {
		t.Fatalf("expected 0 nodes, got %d", len(nodes))
	}
}
