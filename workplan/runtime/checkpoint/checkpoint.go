// Package checkpoint provides snapshot save/load for workflow resume.
package checkpoint

import (
	"fmt"
	"sync"

	"github.com/RedHuang-0622/Seele/workplan/core/types"
)

// Store is the interface for snapshot persistence.
type Store interface {
	Save(id string, s *types.Snapshot) error
	Load(id string) (*types.Snapshot, error)
}

// MemoryStore is an in-memory implementation of Store.
type MemoryStore struct {
	mu     sync.RWMutex
	snaps  map[string]*types.Snapshot
}

// NewMemoryStore creates an in-memory checkpoint store.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{snaps: make(map[string]*types.Snapshot)}
}

func (s *MemoryStore) Save(id string, snap *types.Snapshot) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.snaps[id] = snap
	return nil
}

func (s *MemoryStore) Load(id string) (*types.Snapshot, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	snap, ok := s.snaps[id]
	if !ok {
		return nil, fmt.Errorf("snapshot %q not found", id)
	}
	return snap, nil
}

// Manager handles checkpoint creation and restoration.
type Manager struct {
	store Store
}

// NewManager creates a checkpoint manager with the given store.
func NewManager(store Store) *Manager {
	return &Manager{store: store}
}

// Save creates a snapshot of the current workflow context at a node.
func (m *Manager) Save(nodeID string, wc *types.WorkflowContext) (*types.Snapshot, error) {
	snap := &types.Snapshot{
		NodeID:  nodeID,
		Context: wc,
		Status:  types.StatusRunning,
	}
	if err := m.store.Save(nodeID, snap); err != nil {
		return nil, fmt.Errorf("checkpoint save: %w", err)
	}
	if wc.Result.Checkpoints != nil {
		wc.Result.Checkpoints[nodeID] = wc.PrevOutput
	}
	return snap, nil
}

// Load restores a workflow context from a snapshot.
func (m *Manager) Load(id string) (*types.WorkflowContext, error) {
	snap, err := m.store.Load(id)
	if err != nil {
		return nil, err
	}
	return snap.Context, nil
}
