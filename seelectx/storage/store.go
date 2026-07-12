package storage

import (
	crand "crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	types "github.com/RedHuang-0622/Seele/types"
)

// SessionMeta holds metadata for a persistent session.
type SessionMeta struct {
	SessionID  string    `json:"session_id"`
	Summary    string    `json:"summary,omitempty"`
	TokenCount int       `json:"token_count"`
	ShardCount int       `json:"shard_count"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

// Store implements JSON file-based persistent session storage.
//
// Directory layout:
//
//	.seele/sessions/
//	  ├── idx.json                          ← global index
//	  ├── {hash_prefix}/
//	  │     ├── meta.json                   ← session metadata
//	  │     ├── history.000.json            ← message shards
//	  │     ├── history.001.json
//	  │     └── storage.json                ← user KV store (reserved)
//
// Shard strategy: each shard holds at most 100 messages or 8K tokens,
// whichever is reached first.
type Store struct {
	baseDir string
	mu      sync.RWMutex
	index   map[string]*SessionMeta // sessionID → meta
}

// NewStore creates a persistent session storage.
// If baseDir is empty, it defaults to ".seele/sessions/".
func NewStore(baseDir string) (*Store, error) {
	if baseDir == "" {
		baseDir = ".seele/sessions/"
	}
	baseDir = filepath.Clean(baseDir)

	s := &Store{
		baseDir: baseDir,
		index:   make(map[string]*SessionMeta),
	}

	if err := os.MkdirAll(baseDir, 0755); err != nil {
		return nil, fmt.Errorf("storage: create base dir %s: %w", baseDir, err)
	}

	if err := s.loadIndex(); err != nil {
		return nil, fmt.Errorf("storage: load index: %w", err)
	}

	return s, nil
}

// ── Index operations ─────────────────────────────────────────────────

// loadIndex reads idx.json into the in-memory index map.
func (s *Store) loadIndex() error {
	idxPath := filepath.Join(s.baseDir, "idx.json")
	b, err := os.ReadFile(idxPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("read %s: %w", idxPath, err)
	}
	if len(b) == 0 {
		return nil
	}
	return json.Unmarshal(b, &s.index)
}

// saveIndex writes the in-memory index to idx.json atomically.
func (s *Store) saveIndex() error {
	idxPath := filepath.Join(s.baseDir, "idx.json")
	b, err := json.MarshalIndent(s.index, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal index: %w", err)
	}
	return writeFileAtomic(idxPath, b, 0644)
}

// ── Session directory helpers ────────────────────────────────────────

// sessionDir returns the hash-prefixed directory for a session ID.
// The prefix is the first 8 hex characters of SHA-256(sessionID).
func (s *Store) sessionDir(sessionID string) string {
	h := sha256.Sum256([]byte(sessionID))
	prefix := fmt.Sprintf("%x", h[:4]) // first 8 hex chars (4 bytes)
	return filepath.Join(s.baseDir, prefix)
}

// ── Public API ───────────────────────────────────────────────────────

// Save persists messages for a session. When thresholds are exceeded,
// messages are automatically split into shards.
//
// messages is []types.Message, typically from engine.History().
func (s *Store) Save(sessionID string, messages []types.Message) error {
	if sessionID == "" {
		return fmt.Errorf("storage: empty session ID")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	shards := splitMessages(messages)
	shardCount := len(shards)
	totalTokens := estimateTokens(messages)

	now := time.Now()
	meta, exists := s.index[sessionID]
	if !exists {
		meta = &SessionMeta{
			SessionID: sessionID,
			CreatedAt: now,
		}
		s.index[sessionID] = meta
	}
	meta.ShardCount = shardCount
	meta.TokenCount = totalTokens
	meta.UpdatedAt = now

	sessDir := s.sessionDir(sessionID)
	if err := os.MkdirAll(sessDir, 0755); err != nil {
		return fmt.Errorf("storage: create session dir: %w", err)
	}

	// Write each shard to history.NNN.json
	for i, shard := range shards {
		shardPath := filepath.Join(sessDir, fmt.Sprintf("history.%03d.json", i))
		b, err := json.Marshal(shard)
		if err != nil {
			return fmt.Errorf("storage: marshal shard %d: %w", i, err)
		}
		if err := writeFileAtomic(shardPath, b, 0644); err != nil {
			return fmt.Errorf("storage: write shard %d: %w", i, err)
		}
	}

	// Remove stale shard files (from a previous save with more shards)
	if err := s.cleanStaleShards(sessDir, shardCount); err != nil {
		return fmt.Errorf("storage: clean stale shards: %w", err)
	}

	// Write meta.json
	metaPath := filepath.Join(sessDir, "meta.json")
	metaBytes, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return fmt.Errorf("storage: marshal meta: %w", err)
	}
	if err := writeFileAtomic(metaPath, metaBytes, 0644); err != nil {
		return fmt.Errorf("storage: write meta: %w", err)
	}

	// Persist index
	if err := s.saveIndex(); err != nil {
		return fmt.Errorf("storage: save index: %w", err)
	}

	return nil
}

// Load retrieves all messages for a session, merging shards in order.
func (s *Store) Load(sessionID string) ([]types.Message, error) {
	if sessionID == "" {
		return nil, fmt.Errorf("storage: empty session ID")
	}

	s.mu.RLock()
	_, ok := s.index[sessionID]
	s.mu.RUnlock()

	if !ok {
		return nil, fmt.Errorf("storage: session %q not found", sessionID)
	}

	sessDir := s.sessionDir(sessionID)
	shardFiles, err := s.listShardFiles(sessDir)
	if err != nil {
		return nil, fmt.Errorf("storage: list shards: %w", err)
	}

	var allMessages []types.Message
	for _, f := range shardFiles {
		b, err := os.ReadFile(filepath.Join(sessDir, f))
		if err != nil {
			return nil, fmt.Errorf("storage: read %s: %w", f, err)
		}
		var shard []types.Message
		if err := json.Unmarshal(b, &shard); err != nil {
			return nil, fmt.Errorf("storage: unmarshal %s: %w", f, err)
		}
		allMessages = append(allMessages, shard...)
	}

	// Update token count in index based on actual loaded data
	s.mu.Lock()
	if meta, exists := s.index[sessionID]; exists {
		meta.TokenCount = estimateTokens(allMessages)
	}
	s.mu.Unlock()

	return allMessages, nil
}

// List returns metadata for all stored sessions (for matcher candidates).
// Results are sorted by UpdatedAt descending.
func (s *Store) List() []SessionMeta {
	s.mu.RLock()
	defer s.mu.RUnlock()

	result := make([]SessionMeta, 0, len(s.index))
	for _, meta := range s.index {
		result = append(result, *meta)
	}

	sort.Slice(result, func(i, j int) bool {
		return result[i].UpdatedAt.After(result[j].UpdatedAt)
	})

	return result
}

// Delete removes a session and all its data.
func (s *Store) Delete(sessionID string) error {
	if sessionID == "" {
		return fmt.Errorf("storage: empty session ID")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if _, ok := s.index[sessionID]; !ok {
		return fmt.Errorf("storage: session %q not found", sessionID)
	}

	sessDir := s.sessionDir(sessionID)
	if err := os.RemoveAll(sessDir); err != nil {
		return fmt.Errorf("storage: remove session dir: %w", err)
	}

	delete(s.index, sessionID)

	if err := s.saveIndex(); err != nil {
		return fmt.Errorf("storage: save index after delete: %w", err)
	}

	return nil
}

// ── Internal helpers ─────────────────────────────────────────────────

// listShardFiles returns history.*.json filenames from the session dir, sorted.
func (s *Store) listShardFiles(sessDir string) ([]string, error) {
	entries, err := os.ReadDir(sessDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("session dir not found")
		}
		return nil, err
	}

	var files []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if matched, _ := filepath.Match("history.*.json", e.Name()); matched {
			files = append(files, e.Name())
		}
	}
	sort.Strings(files)
	return files, nil
}

// cleanStaleShards removes history.*.json files with indices >= shardCount.
func (s *Store) cleanStaleShards(sessDir string, shardCount int) error {
	entries, err := os.ReadDir(sessDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}

	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		matched, _ := filepath.Match("history.*.json", e.Name())
		if !matched {
			continue
		}
		// Extract shard index from filename "history.NNN.json"
		var idx int
		if n, _ := fmt.Sscanf(e.Name(), "history.%d.json", &idx); n != 1 {
			continue
		}
		if idx >= shardCount {
			if err := os.Remove(filepath.Join(sessDir, e.Name())); err != nil {
				return err
			}
		}
	}
	return nil
}

// writeFileAtomic writes data to a temp file then renames to path,
// ensuring the write is crash-safe.
func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	suffix := make([]byte, 8)
	if _, err := crand.Read(suffix); err != nil {
		// Fall back to direct write if crypto/rand fails (extremely unlikely)
		return os.WriteFile(path, data, perm)
	}
	tmpPath := fmt.Sprintf("%s.%x.tmp", path, suffix)
	if err := os.WriteFile(tmpPath, data, perm); err != nil {
		return err
	}
	return os.Rename(tmpPath, path)
}
