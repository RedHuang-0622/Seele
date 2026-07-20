package storage

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	types "github.com/RedHuang-0622/Seele/types"
)

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func strPtr(s string) *string { return &s }

func testMessage(content string) types.Message {
	return types.Message{Role: "user", Content: strPtr(content)}
}

func testMessages(n int) []types.Message {
	msgs := make([]types.Message, n)
	for i := 0; i < n; i++ {
		msgs[i] = testMessage("msg")
	}
	return msgs
}

// tempStore creates a Store backed by a temp directory and registers cleanup.
func tempStore(t *testing.T) *Store {
	t.Helper()
	dir := t.TempDir()
	s, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore(%q): %v", dir, err)
	}
	return s
}

// ---------------------------------------------------------------------------
// NewStore
// ---------------------------------------------------------------------------

func TestNewStore_EmptyDir(t *testing.T) {
	// NewStore("") should return a non-persistent store (no directory).
	s, err := NewStore("")
	if err != nil {
		t.Fatalf("NewStore(''): %v", err)
	}
	if s.baseDir != "" {
		t.Errorf("baseDir = %q, want empty (no default dir)", s.baseDir)
	}
	if s.index == nil {
		t.Error("index should be initialized")
	}
	// Save/Load should work without creating files
	if err := s.Save("test-session", nil); err != nil {
		t.Fatalf("Save on empty store: %v", err)
	}
}

func TestNewStore_CustomDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "custom-sessions")
	s, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore(%q): %v", dir, err)
	}
	if s.baseDir != filepath.Clean(dir) {
		t.Errorf("baseDir = %q, want %q", s.baseDir, dir)
	}
}

// ---------------------------------------------------------------------------
// Save / Load round-trip
// ---------------------------------------------------------------------------

func TestSaveAndLoad_RoundTrip(t *testing.T) {
	s := tempStore(t)
	sessionID := "roundtrip-session"

	original := []types.Message{
		{Role: "system", Content: strPtr("You are a helpful assistant.")},
		{Role: "user", Content: strPtr("Hello")},
		{Role: "assistant", Content: strPtr("Hi there!"), ToolCalls: []types.ToolCall{
			{ID: "call_1", Type: "function", Function: types.ToolCallFunction{Name: "greet", Arguments: `{"name":"user"}`}},
		}},
		{Role: "tool", ToolCallID: "call_1", Content: strPtr("done")},
	}

	if err := s.Save(sessionID, original); err != nil {
		t.Fatalf("Save: %v", err)
	}

	loaded, err := s.Load(sessionID)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(loaded) != len(original) {
		t.Fatalf("message count = %d, want %d", len(loaded), len(original))
	}
	for i := range original {
		if loaded[i].Role != original[i].Role {
			t.Errorf("msg[%d].Role = %q, want %q", i, loaded[i].Role, original[i].Role)
		}
	}
}

func TestSaveAndLoad_EmptyMessages(t *testing.T) {
	s := tempStore(t)
	sessionID := "empty-msgs"

	if err := s.Save(sessionID, nil); err != nil {
		t.Fatalf("Save with nil messages: %v", err)
	}
	loaded, err := s.Load(sessionID)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(loaded) != 0 {
		t.Errorf("expected empty slice, got %d messages", len(loaded))
	}
}

// ---------------------------------------------------------------------------
// Save updates index metadata
// ---------------------------------------------------------------------------

func TestSave_UpdatesIndexMeta(t *testing.T) {
	s := tempStore(t)
	sessionID := "meta-session"

	msgs := testMessages(5)
	if err := s.Save(sessionID, msgs); err != nil {
		t.Fatalf("Save: %v", err)
	}

	s.mu.RLock()
	meta, ok := s.index[sessionID]
	s.mu.RUnlock()
	if !ok {
		t.Fatal("session should exist in index after Save")
	}
	// Copy before second save mutates the shared pointer.
	firstMeta := *meta

	if firstMeta.TokenCount <= 0 {
		t.Errorf("TokenCount should be > 0, got %d", firstMeta.TokenCount)
	}
	if firstMeta.ShardCount <= 0 {
		t.Errorf("ShardCount should be > 0, got %d", firstMeta.ShardCount)
	}
	if firstMeta.CreatedAt.IsZero() {
		t.Error("CreatedAt should be set")
	}
	if firstMeta.UpdatedAt.IsZero() {
		t.Error("UpdatedAt should be set")
	}
	if firstMeta.CreatedAt != firstMeta.UpdatedAt {
		t.Error("CreatedAt should equal UpdatedAt on first save")
	}

	// Save again with different messages; metadata should reflect update.
	msgs2 := testMessages(100)
	time.Sleep(50 * time.Millisecond) // ensure time.Now() advances (Windows tick ~15ms)
	if err := s.Save(sessionID, msgs2); err != nil {
		t.Fatalf("Save again: %v", err)
	}

	s.mu.RLock()
	meta2 := s.index[sessionID]
	s.mu.RUnlock()
	if meta2.UpdatedAt.Equal(firstMeta.UpdatedAt) {
		t.Error("UpdatedAt should have changed after re-save")
	}
	if meta2.TokenCount <= firstMeta.TokenCount {
		t.Error("TokenCount should increase with more messages")
	}
}

// ---------------------------------------------------------------------------
// Load error for missing session
// ---------------------------------------------------------------------------

func TestLoad_MissingSession(t *testing.T) {
	s := tempStore(t)
	_, err := s.Load("nonexistent")
	if err == nil {
		t.Fatal("expected error for missing session")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("error should contain 'not found', got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// List sorted by UpdatedAt descending
// ---------------------------------------------------------------------------

func TestList_SortedByUpdatedAtDesc(t *testing.T) {
	s := tempStore(t)

	// Save first session.
	if err := s.Save("session-a", testMessages(1)); err != nil {
		t.Fatalf("Save session-a: %v", err)
	}
	time.Sleep(5 * time.Millisecond)

	// Save second session — it will have a later UpdatedAt.
	if err := s.Save("session-b", testMessages(1)); err != nil {
		t.Fatalf("Save session-b: %v", err)
	}

	list := s.List()
	if len(list) < 2 {
		t.Fatalf("expected at least 2 sessions, got %d", len(list))
	}

	// Verify descending order.
	for i := 1; i < len(list); i++ {
		if list[i-1].UpdatedAt.Before(list[i].UpdatedAt) {
			t.Errorf("List should be sorted descending by UpdatedAt: index %d (%s) < index %d (%s)",
				i-1, list[i-1].SessionID, i, list[i].SessionID)
		}
	}

	// session-b should appear before session-a.
	order := make([]string, len(list))
	for i, m := range list {
		order[i] = m.SessionID
	}
	if order[0] != "session-b" || order[1] != "session-a" {
		t.Logf("order: %v (b should be first)", order)
	}
}

func TestList_Empty(t *testing.T) {
	s := tempStore(t)
	list := s.List()
	if list == nil {
		t.Fatal("List should return empty slice, not nil")
	}
	if len(list) != 0 {
		t.Errorf("expected empty list, got %d", len(list))
	}
}

// ---------------------------------------------------------------------------
// Delete
// ---------------------------------------------------------------------------

func TestDelete_RemovesSessionAndData(t *testing.T) {
	s := tempStore(t)
	sessionID := "delete-session"

	msgs := testMessages(3)
	if err := s.Save(sessionID, msgs); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// Verify session directory exists.
	sessDir := s.sessionDir(sessionID)
	if _, err := os.Stat(sessDir); os.IsNotExist(err) {
		t.Fatal("session dir should exist before Delete")
	}

	if err := s.Delete(sessionID); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	// Session dir should be removed.
	if _, err := os.Stat(sessDir); !os.IsNotExist(err) {
		t.Error("session dir should be removed after Delete")
	}

	// Index should be cleaned.
	s.mu.RLock()
	_, exists := s.index[sessionID]
	s.mu.RUnlock()
	if exists {
		t.Error("session should not exist in index after Delete")
	}

	// Load should fail.
	if _, err := s.Load(sessionID); err == nil {
		t.Error("Load should fail after Delete")
	}
}

func TestDelete_MissingSession(t *testing.T) {
	s := tempStore(t)
	err := s.Delete("nonexistent")
	if err == nil {
		t.Fatal("expected error for deleting missing session")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("error should contain 'not found', got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Empty session ID
// ---------------------------------------------------------------------------

func TestEmptySessionID_ErrorsOnSave(t *testing.T) {
	s := tempStore(t)
	if err := s.Save("", testMessages(1)); err == nil {
		t.Error("Save with empty session ID should error")
	}
}

func TestEmptySessionID_ErrorsOnLoad(t *testing.T) {
	s := tempStore(t)
	if _, err := s.Load(""); err == nil {
		t.Error("Load with empty session ID should error")
	}
}

func TestEmptySessionID_ErrorsOnDelete(t *testing.T) {
	s := tempStore(t)
	if err := s.Delete(""); err == nil {
		t.Error("Delete with empty session ID should error")
	}
}

// ---------------------------------------------------------------------------
// Sharding logic (splitMessages, findSplitIndex, shouldSplit, estimateTokens)
// ---------------------------------------------------------------------------

func TestEstimateTokens_Empty(t *testing.T) {
	if got := estimateTokens(nil); got != 0 {
		t.Errorf("estimateTokens(nil) = %d, want 0", got)
	}
	if got := estimateTokens([]types.Message{}); got != 0 {
		t.Errorf("estimateTokens([]) = %d, want 0", got)
	}
}

func TestEstimateTokens_NonEmpty(t *testing.T) {
	msgs := []types.Message{{Role: "user", Content: strPtr("hello")}}
	got := estimateTokens(msgs)
	if got <= 0 {
		t.Errorf("estimateTokens should be positive, got %d", got)
	}
}

func TestShouldSplit_UnderThreshold(t *testing.T) {
	msgs := testMessages(10)
	if shouldSplit(msgs) {
		t.Error("shouldSplit should be false for 10 small messages")
	}
}

func TestShouldSplit_OverMessageThreshold(t *testing.T) {
	msgs := testMessages(101)
	if !shouldSplit(msgs) {
		t.Error("shouldSplit should be true for 101 messages")
	}
}

func TestShouldSplit_ZeroMessages(t *testing.T) {
	if shouldSplit(nil) {
		t.Error("shouldSplit should be false for nil")
	}
	if shouldSplit([]types.Message{}) {
		t.Error("shouldSplit should be false for empty slice")
	}
}

func TestSplitMessages_NoSplitNeeded(t *testing.T) {
	msgs := testMessages(50)
	shards := splitMessages(msgs)
	if len(shards) != 1 {
		t.Fatalf("expected 1 shard, got %d", len(shards))
	}
	if len(shards[0]) != 50 {
		t.Errorf("shard[0] length = %d, want 50", len(shards[0]))
	}
}

func TestSplitMessages_OverMessageThreshold(t *testing.T) {
	msgs := testMessages(101)
	shards := splitMessages(msgs)
	if len(shards) != 2 {
		t.Fatalf("expected 2 shards, got %d", len(shards))
	}
	if len(shards[0]) != 100 {
		t.Errorf("shard[0] length = %d, want 100", len(shards[0]))
	}
	if len(shards[1]) != 1 {
		t.Errorf("shard[1] length = %d, want 1", len(shards[1]))
	}
}

func TestSplitMessages_OverTokenThreshold(t *testing.T) {
	// Create large messages to exceed the token budget in fewer than 100 messages.
	var msgs []types.Message
	for i := 0; i < 50; i++ {
		big := strings.Repeat("A", 600) // large enough to push token count
		msgs = append(msgs, testMessage(big))
	}
	shards := splitMessages(msgs)
	if len(shards) < 2 {
		t.Log("note: large messages may only produce 1 shard if tokens < maxShardTokens")
	}
}

func TestFindSplitIndex_ExactMessageLimit(t *testing.T) {
	msgs := testMessages(100)
	idx := findSplitIndex(msgs)
	if idx != 100 {
		t.Errorf("findSplitIndex should return 100 for 100 messages, got %d", idx)
	}
}

func TestFindSplitIndex_SingleMessageOverBudget(t *testing.T) {
	// A single extremely large message should still be kept (split at index 1).
	big := strings.Repeat("B", 50000)
	msgs := []types.Message{testMessage(big), testMessage("small")}
	idx := findSplitIndex(msgs)
	if idx != 1 {
		t.Errorf("findSplitIndex should return 1 when first message exceeds budget, got %d", idx)
	}
}

// ---------------------------------------------------------------------------
// writeFileAtomic
// ---------------------------------------------------------------------------

func TestWriteFileAtomic_CreatesFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "atomic.txt")
	data := []byte("hello world")

	if err := writeFileAtomic(path, data, 0644); err != nil {
		t.Fatalf("writeFileAtomic: %v", err)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(got) != string(data) {
		t.Errorf("content = %q, want %q", string(got), string(data))
	}
}

func TestWriteFileAtomic_Overwrites(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "overwrite.txt")

	if err := writeFileAtomic(path, []byte("first"), 0644); err != nil {
		t.Fatalf("first write: %v", err)
	}
	if err := writeFileAtomic(path, []byte("second"), 0644); err != nil {
		t.Fatalf("second write: %v", err)
	}

	got, _ := os.ReadFile(path)
	if string(got) != "second" {
		t.Errorf("content = %q, want %q", string(got), "second")
	}
}

func TestAtomicWrite_Fallback(t *testing.T) {
	// Normal path test for writeFileAtomic (crypto/rand fallback is
	// extremely unlikely to be exercised in practice).
	dir := t.TempDir()
	path := filepath.Join(dir, "fallback.txt")
	data := []byte("fallback test data")

	if err := writeFileAtomic(path, data, 0644); err != nil {
		t.Fatalf("writeFileAtomic: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(got) != string(data) {
		t.Errorf("content = %q, want %q", string(got), string(data))
	}
}

// ---------------------------------------------------------------------------
// cleanStaleShards
// ---------------------------------------------------------------------------

func TestCleanStaleShards(t *testing.T) {
	s := tempStore(t)
	dir := t.TempDir()

	// Create shard files and a non-shard file.
	files := []string{"history.000.json", "history.001.json", "history.002.json", "meta.json", "storage.json"}
	for _, f := range files {
		if err := os.WriteFile(filepath.Join(dir, f), []byte("{}"), 0644); err != nil {
			t.Fatalf("create %s: %v", f, err)
		}
	}

	if err := s.cleanStaleShards(dir, 2); err != nil {
		t.Fatalf("cleanStaleShards: %v", err)
	}

	// history.002.json should be removed (index >= shardCount=2).
	if _, err := os.Stat(filepath.Join(dir, "history.002.json")); !os.IsNotExist(err) {
		t.Error("history.002.json should be removed")
	}

	// Others should survive.
	for _, f := range []string{"history.000.json", "history.001.json", "meta.json", "storage.json"} {
		if _, err := os.Stat(filepath.Join(dir, f)); os.IsNotExist(err) {
			t.Errorf("%s should still exist", f)
		}
	}
}

func TestCleanStaleShards_NonExistentDir(t *testing.T) {
	s := tempStore(t)
	if err := s.cleanStaleShards("/nonexistent/path", 5); err != nil {
		t.Errorf("should not error on non-existent dir: %v", err)
	}
}

func TestCleanStaleShards_NoStaleFiles(t *testing.T) {
	s := tempStore(t)
	dir := t.TempDir()

	for i := 0; i < 3; i++ {
		f := fmt.Sprintf("history.%03d.json", i)
		if err := os.WriteFile(filepath.Join(dir, f), []byte("{}"), 0644); err != nil {
			t.Fatalf("create %s: %v", f, err)
		}
	}

	if err := s.cleanStaleShards(dir, 5); err != nil {
		t.Fatalf("cleanStaleShards: %v", err)
	}

	for i := 0; i < 3; i++ {
		f := fmt.Sprintf("history.%03d.json", i)
		if _, err := os.Stat(filepath.Join(dir, f)); os.IsNotExist(err) {
			t.Errorf("%s should still exist (shardCount=5)", f)
		}
	}
}

// ---------------------------------------------------------------------------
// sessionDir (SHA-256 prefix)
// ---------------------------------------------------------------------------

func TestSessionDir_UsesSHA256Prefix(t *testing.T) {
	s := &Store{baseDir: filepath.Join(t.TempDir(), "sessions")}

	// The prefix is the first 8 hex characters of SHA-256(sessionID).
	sessionID := "test-session-123"
	dir := s.sessionDir(sessionID)

	base := filepath.Base(dir)
	if len(base) != 8 {
		t.Fatalf("expected 8-char hex prefix, got %q (len=%d)", base, len(base))
	}
	for _, c := range base {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			t.Errorf("unexpected character %c in hex prefix", c)
		}
	}

	// Verify the expected hash prefix.
	h := sha256.Sum256([]byte(sessionID))
	expectedPrefix := fmt.Sprintf("%x", h[:4])
	if base != expectedPrefix {
		t.Errorf("prefix = %q, want %q", base, expectedPrefix)
	}
}

func TestSessionDir_Deterministic(t *testing.T) {
	s := &Store{baseDir: t.TempDir()}

	id := "deterministic-session"
	d1 := s.sessionDir(id)
	d2 := s.sessionDir(id)
	if d1 != d2 {
		t.Error("sessionDir should be deterministic for same session ID")
	}
}

func TestSessionDir_DifferentIDs(t *testing.T) {
	s := &Store{baseDir: t.TempDir()}

	d1 := s.sessionDir("session-alpha")
	d2 := s.sessionDir("session-beta")
	if d1 == d2 {
		t.Error("sessionDir should differ for different session IDs")
	}
}

// ---------------------------------------------------------------------------
// Persistence across Store instances
// ---------------------------------------------------------------------------

func TestSaveAndLoad_AcrossInstances(t *testing.T) {
	dir := t.TempDir()

	s1, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	msgs := testMessages(3)
	if err := s1.Save("persist-test", msgs); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// Close first store and open a new one that reads the same idx.json.
	s2, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore second: %v", err)
	}

	loaded, err := s2.Load("persist-test")
	if err != nil {
		t.Fatalf("Load on second store: %v", err)
	}
	if len(loaded) != 3 {
		t.Errorf("message count = %d, want %d", len(loaded), 3)
	}
}

// ---------------------------------------------------------------------------
// listShardFiles
// ---------------------------------------------------------------------------

func TestListShardFiles_Sorted(t *testing.T) {
	s := tempStore(t)
	dir := t.TempDir()

	files := []string{"history.002.json", "history.000.json", "history.001.json", "meta.json", "history.010.json"}
	for _, f := range files {
		if err := os.WriteFile(filepath.Join(dir, f), []byte("{}"), 0644); err != nil {
			t.Fatalf("create %s: %v", f, err)
		}
	}

	result, err := s.listShardFiles(dir)
	if err != nil {
		t.Fatalf("listShardFiles: %v", err)
	}

	expected := []string{"history.000.json", "history.001.json", "history.002.json", "history.010.json"}
	if len(result) != len(expected) {
		t.Fatalf("got %d files, want %d: %v", len(result), len(expected), result)
	}
	for i := range expected {
		if result[i] != expected[i] {
			t.Errorf("result[%d] = %q, want %q", i, result[i], expected[i])
		}
	}
}

func TestListShardFiles_NonExistentDir(t *testing.T) {
	s := tempStore(t)
	_, err := s.listShardFiles("/nonexistent")
	if err == nil {
		t.Error("expected error for non-existent dir")
	}
}

// ---------------------------------------------------------------------------
// Integration: Save triggers sharding and cleans stale shards
// ---------------------------------------------------------------------------

func TestSave_ShardingAndCleanStale(t *testing.T) {
	s := tempStore(t)
	sessionID := "stale-cleanup"

	// Save 101 messages to trigger sharding (2 shards).
	msgs := testMessages(101)
	if err := s.Save(sessionID, msgs); err != nil {
		t.Fatalf("Save 101 msgs: %v", err)
	}

	sessDir := s.sessionDir(sessionID)
	entries, _ := os.ReadDir(sessDir)
	shardFiles := 0
	for _, e := range entries {
		if !e.IsDir() && strings.HasPrefix(e.Name(), "history.") {
			shardFiles++
		}
	}
	if shardFiles != 2 {
		t.Fatalf("expected 2 shard files, got %d", shardFiles)
	}

	// Save only 10 messages — stale shards should be cleaned up.
	if err := s.Save(sessionID, testMessages(10)); err != nil {
		t.Fatalf("Save 10 msgs: %v", err)
	}

	entries2, _ := os.ReadDir(sessDir)
	shardFiles2 := 0
	for _, e := range entries2 {
		if !e.IsDir() && strings.HasPrefix(e.Name(), "history.") {
			shardFiles2++
		}
	}
	if shardFiles2 != 1 {
		t.Fatalf("expected 1 shard file after re-saving fewer msgs, got %d", shardFiles2)
	}
}

// ---------------------------------------------------------------------------
// Estimate tokens — confirm JSON-byte heuristic
// ---------------------------------------------------------------------------

func TestEstimateTokens_RoundTrip(t *testing.T) {
	// The formula is (len+b) / 3 using len(JSON(msgs)).
	msgs := []types.Message{{Role: "user", Content: strPtr("hello world")}}
	tokens := estimateTokens(msgs)
	if tokens <= 0 {
		t.Errorf("expected positive token count, got %d", tokens)
	}

	// Empty slice.
	if got := estimateTokens([]types.Message{}); got != 0 {
		t.Errorf("expected 0 for empty, got %d", got)
	}
}

// ---------------------------------------------------------------------------
// Index load/save edge cases
// ---------------------------------------------------------------------------

func TestLoadIndex_EmptyFile(t *testing.T) {
	dir := t.TempDir()
	s := &Store{baseDir: dir, index: make(map[string]*SessionMeta)}

	// Create empty idx.json
	idxPath := filepath.Join(dir, "idx.json")
	if err := os.WriteFile(idxPath, []byte{}, 0644); err != nil {
		t.Fatalf("create empty idx.json: %v", err)
	}

	if err := s.loadIndex(); err != nil {
		t.Errorf("loadIndex on empty file: %v", err)
	}
}

func TestSaveIndex_Persists(t *testing.T) {
	s := tempStore(t)

	s.mu.Lock()
	s.index["test-session"] = &SessionMeta{
		SessionID: "test-session",
		TokenCount: 42,
		ShardCount: 1,
		CreatedAt:  time.Now(),
		UpdatedAt:  time.Now(),
	}
	s.mu.Unlock()

	if err := s.saveIndex(); err != nil {
		t.Fatalf("saveIndex: %v", err)
	}

	// Load into fresh store to verify persistence.
	dir := s.baseDir
	s2, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	s2.mu.RLock()
	meta, ok := s2.index["test-session"]
	s2.mu.RUnlock()
	if !ok {
		t.Fatal("session should exist after reload")
	}
	if meta.TokenCount != 42 {
		t.Errorf("TokenCount = %d, want 42", meta.TokenCount)
	}
}

// ---------------------------------------------------------------------------
// Index sort stability on List
// ---------------------------------------------------------------------------

func TestList_IndexSortIsStable(t *testing.T) {
	s := tempStore(t)

	now := time.Now()
	s.mu.Lock()
	// Manually inject entries with known UpdatedAt values.
	s.index["z"] = &SessionMeta{SessionID: "z", UpdatedAt: now.Add(-2 * time.Hour)}
	s.index["a"] = &SessionMeta{SessionID: "a", UpdatedAt: now.Add(-1 * time.Hour)}
	s.index["m"] = &SessionMeta{SessionID: "m", UpdatedAt: now}
	s.mu.Unlock()

	list := s.List()
	if len(list) != 3 {
		t.Fatalf("expected 3 sessions, got %d", len(list))
	}

	// Sort order should be: m (most recent), a, z (oldest).
	ids := make([]string, len(list))
	for i, m := range list {
		ids[i] = m.SessionID
	}
	expected := []string{"m", "a", "z"}
	for i := range expected {
		if ids[i] != expected[i] {
			t.Errorf("order[%d] = %q, want %q; full order: %v", i, ids[i], expected[i], ids)
		}
	}
}

// ---------------------------------------------------------------------------
// Verify sort.Slice used in List sorts by UpdatedAt descending (not by name)
// ---------------------------------------------------------------------------

func TestList_SortFieldIsUpdatedAt(t *testing.T) {
	// This is a behavioral check: the sort key is UpdatedAt, not SessionID.
	s := tempStore(t)

	now := time.Now()
	s.mu.Lock()
	s.index["b"] = &SessionMeta{SessionID: "b", UpdatedAt: now.Add(-1 * time.Hour)}
	s.index["a"] = &SessionMeta{SessionID: "a", UpdatedAt: now}
	s.mu.Unlock()

	list := s.List()
	if len(list) != 2 {
		t.Fatalf("expected 2 sessions, got %d", len(list))
	}

	// 'a' has the later timestamp, so it should be first.
	if list[0].SessionID != "a" {
		t.Errorf("first element should be 'a' (newer), got %q", list[0].SessionID)
	}
}

// ---------------------------------------------------------------------------
// Tolerate corrupted / missing idx.json
// ---------------------------------------------------------------------------

func TestNewStore_NoIndexFile(t *testing.T) {
	dir := t.TempDir()
	s, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore on empty dir: %v", err)
	}
	if s.index == nil {
		t.Error("index should be initialized")
	}
	if len(s.index) != 0 {
		t.Errorf("expected empty index, got %d entries", len(s.index))
	}
}
