package checkpoint

import (
	"testing"
	"time"

	"github.com/RedHuang-0622/Seele/workplan/core/types"
)

func TestNewMemoryStore(t *testing.T) {
	s := NewMemoryStore()
	if s == nil {
		t.Fatal("NewMemoryStore() returned nil")
	}
	if s.snaps == nil {
		t.Error("expected non-nil snaps map")
	}
}

func TestMemoryStoreSaveAndLoadRoundTrip(t *testing.T) {
	s := NewMemoryStore()
	wc := types.NewWorkflowContext()
	wc.PrevOutput = `"hello-world"`
	wc.Vars["key"] = "value"

	snap := &types.Snapshot{
		NodeID:    "node1",
		Context:   wc,
		Timestamp: time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC),
		Status:    types.StatusRunning,
	}

	err := s.Save("node1", snap)
	if err != nil {
		t.Fatalf("Save failed: %v", err)
	}

	loaded, err := s.Load("node1")
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if loaded == nil {
		t.Fatal("expected non-nil snapshot")
	}
	if loaded.NodeID != "node1" {
		t.Errorf("expected NodeID 'node1', got %q", loaded.NodeID)
	}
	if loaded.Status != types.StatusRunning {
		t.Errorf("expected StatusRunning, got %v", loaded.Status)
	}
	if loaded.Context.PrevOutput != `"hello-world"` {
		t.Errorf("expected PrevOutput '\"hello-world\"', got %q", loaded.Context.PrevOutput)
	}
	if loaded.Context.Vars["key"] != "value" {
		t.Errorf("expected Vars['key'] = 'value', got %q", loaded.Context.Vars["key"])
	}
	if loaded.Context.Result == nil {
		t.Error("expected non-nil Result in loaded context")
	}
}

func TestMemoryStoreSaveMultiple(t *testing.T) {
	s := NewMemoryStore()

	snap1 := &types.Snapshot{NodeID: "a", Context: types.NewWorkflowContext(), Timestamp: time.Now(), Status: types.StatusRunning}
	snap2 := &types.Snapshot{NodeID: "b", Context: types.NewWorkflowContext(), Timestamp: time.Now(), Status: types.StatusRunning}

	_ = s.Save("a", snap1)
	_ = s.Save("b", snap2)

	loadedA, _ := s.Load("a")
	loadedB, _ := s.Load("b")
	if loadedA.NodeID != "a" || loadedB.NodeID != "b" {
		t.Error("Save/Load round-trip for multiple entries failed")
	}
}

func TestMemoryStoreOverwrite(t *testing.T) {
	s := NewMemoryStore()

	snap1 := &types.Snapshot{NodeID: "x", Context: types.NewWorkflowContext(), Timestamp: time.Now(), Status: types.StatusRunning}
	snap2 := &types.Snapshot{NodeID: "x", Context: types.NewWorkflowContext(), Timestamp: time.Now(), Status: types.StatusCompleted}

	_ = s.Save("x", snap1)
	_ = s.Save("x", snap2) // overwrite

	loaded, _ := s.Load("x")
	if loaded.Status != types.StatusCompleted {
		t.Errorf("expected overwritten StatusCompleted, got %v", loaded.Status)
	}
}

func TestMemoryStoreLoadMissingReturnsError(t *testing.T) {
	s := NewMemoryStore()
	_, err := s.Load("nonexistent")
	if err == nil {
		t.Fatal("expected error for missing snapshot, got nil")
	}
}

func TestNewManager(t *testing.T) {
	store := NewMemoryStore()
	m := NewManager(store)
	if m == nil {
		t.Fatal("NewManager() returned nil")
	}
	if m.store == nil {
		t.Error("expected non-nil store")
	}
	if m.store != store {
		t.Error("store not set correctly")
	}
}

func TestManagerSaveAndLoad(t *testing.T) {
	store := NewMemoryStore()
	m := NewManager(store)

	wc := types.NewWorkflowContext()
	wc.PrevOutput = `"test-output"`

	snap, err := m.Save("node1", wc)
	if err != nil {
		t.Fatalf("Manager Save failed: %v", err)
	}
	if snap == nil {
		t.Fatal("expected non-nil snapshot")
	}
	if snap.NodeID != "node1" {
		t.Errorf("expected NodeID 'node1', got %q", snap.NodeID)
	}
	if snap.Status != types.StatusRunning {
		t.Errorf("expected StatusRunning, got %v", snap.Status)
	}
	if snap.Context == nil {
		t.Fatal("expected non-nil Context in snapshot")
	}

	// Load the workflow context back
	loaded, err := m.Load("node1")
	if err != nil {
		t.Fatalf("Manager Load failed: %v", err)
	}
	if loaded.PrevOutput != `"test-output"` {
		t.Errorf("expected PrevOutput '\"test-output\"', got %q", loaded.PrevOutput)
	}
}

func TestManagerSaveWithUnknownNode(t *testing.T) {
	// Manager.Save does not validate that the node exists.
	// It creates a snapshot regardless of the node ID.
	store := NewMemoryStore()
	m := NewManager(store)

	wc := types.NewWorkflowContext()
	snap, err := m.Save("unknown-node", wc)
	if err != nil {
		t.Fatalf("Save with unknown node should not fail: %v", err)
	}
	if snap == nil {
		t.Fatal("expected non-nil snapshot")
	}
	if snap.NodeID != "unknown-node" {
		t.Errorf("expected NodeID 'unknown-node', got %q", snap.NodeID)
	}
	if snap.Context != wc {
		t.Error("expected Context to be the same WorkflowContext reference")
	}
}

func TestManagerLoadNonExistent(t *testing.T) {
	store := NewMemoryStore()
	m := NewManager(store)

	_, err := m.Load("nonexistent")
	if err == nil {
		t.Fatal("expected error when loading non-existent snapshot, got nil")
	}
}
