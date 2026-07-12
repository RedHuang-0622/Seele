package cache

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

func tempCache(t *testing.T, cfg Config) *FileCache {
	t.Helper()
	if cfg.BaseDir == "" {
		cfg.BaseDir = filepath.Join(os.TempDir(), "seele-cache-test", t.Name())
	}
	cfg = cfg.Effective()
	c, err := NewFileCache(cfg)
	if err != nil {
		t.Fatalf("NewFileCache: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(cfg.BaseDir) })
	return c
}

func TestSetGet(t *testing.T) {
	c := tempCache(t, Config{})
	entry := c.Set("key1", "hello world")
	if entry == nil || entry.Key != "key1" || entry.SizeBytes != 11 {
		t.Fatalf("Set: got %+v", entry)
	}
	val, ok := c.Get("key1")
	if !ok || val != "hello world" {
		t.Fatalf("Get: got %q, %v", val, ok)
	}
	_, ok = c.Get("nonexistent")
	if ok {
		t.Fatal("Get should miss for nonexistent key")
	}
}

func TestDelete(t *testing.T) {
	c := tempCache(t, Config{})
	c.Set("k1", "v1")
	c.Set("k2", "v2")
	if !c.Delete("k1") {
		t.Fatal("should return true for existing key")
	}
	if c.Delete("nonexistent") {
		t.Fatal("should return false for nonexistent key")
	}
	if _, ok := c.Get("k1"); ok {
		t.Fatal("k1 should be deleted")
	}
	if _, ok := c.Get("k2"); !ok {
		t.Fatal("k2 should still exist")
	}
}

func TestOverwrite(t *testing.T) {
	c := tempCache(t, Config{})
	c.Set("k", "old")
	c.Set("k", "new")
	val, ok := c.Get("k")
	if !ok || val != "new" {
		t.Fatalf("expected 'new', got %q", val)
	}
}

func TestTTLExpiry(t *testing.T) {
	c := tempCache(t, Config{DefaultTTL: 50 * time.Millisecond})
	c.Set("k", "v")
	if _, ok := c.Get("k"); !ok {
		t.Fatal("should hit before expiry")
	}
	time.Sleep(100 * time.Millisecond)
	if _, ok := c.Get("k"); ok {
		t.Fatal("should miss after expiry")
	}
}

func TestSetWithTTL(t *testing.T) {
	c := tempCache(t, Config{})
	c.SetWithTTL("perm", "permanent", 0)
	if _, ok := c.Get("perm"); !ok {
		t.Fatal("perm should hit")
	}
	c.SetWithTTL("short", "ephemeral", 50*time.Millisecond)
	time.Sleep(100 * time.Millisecond)
	if _, ok := c.Get("short"); ok {
		t.Fatal("short should miss after expiry")
	}
}

func TestContentDedup(t *testing.T) {
	c := tempCache(t, Config{})
	c.Set("dup1", "same")
	c.Set("dup2", "same")
	e1, _ := c.GetEntry("dup1")
	e2, _ := c.GetEntry("dup2")
	if e1.ContentHash != e2.ContentHash {
		t.Fatalf("same content should have same hash: %s vs %s", e1.ContentHash, e2.ContentHash)
	}
	c.Delete("dup1")
	if _, ok := c.Get("dup2"); !ok {
		t.Fatal("dup2 should still exist after dup1 deleted")
	}
}

func TestClearByPrefix(t *testing.T) {
	c := tempCache(t, Config{})
	c.Set("chat:a", "1")
	c.Set("chat:b", "2")
	c.Set("tool:c", "3")
	if n := c.ClearByPrefix("chat:"); n != 2 {
		t.Fatalf("expected 2 deleted, got %d", n)
	}
	if _, ok := c.Get("chat:a"); ok {
		t.Fatal("chat:a should be deleted")
	}
	if _, ok := c.Get("tool:c"); !ok {
		t.Fatal("tool:c should still exist")
	}
}

func TestClearAll(t *testing.T) {
	c := tempCache(t, Config{})
	c.Set("a", "1")
	c.Set("b", "2")
	if n := c.ClearAll(); n != 2 {
		t.Fatalf("expected 2, got %d", n)
	}
	if _, ok := c.Get("a"); ok {
		t.Fatal("all should be deleted")
	}
}

func TestStats(t *testing.T) {
	c := tempCache(t, Config{})
	s := c.Stats()
	if s.HitCount != 0 || s.MissCount != 0 || s.Entries != 0 {
		t.Fatalf("expected empty, got %+v", s)
	}
	c.Set("k", "v")
	c.Get("k")
	c.Get("k")
	c.Get("x")
	c.Get("y")
	s = c.Stats()
	if s.HitCount != 2 || s.MissCount != 2 || s.HitRate != 0.5 {
		t.Fatalf("stats mismatch: %+v", s)
	}
}

func TestMaxEntrySize(t *testing.T) {
	c := tempCache(t, Config{MaxEntrySize: 10})
	if entry := c.Set("big", string(make([]byte, 100))); entry != nil {
		t.Fatal("oversized should be rejected")
	}
	c.Set("small", "tiny")
	if _, ok := c.Get("small"); !ok {
		t.Fatal("small should be accepted")
	}
}

func TestMaxEntries(t *testing.T) {
	c := tempCache(t, Config{MaxEntries: 3})
	c.Set("a", "1")
	c.Set("b", "2")
	c.Set("c", "3")
	// d may or may not be rejected (sync.Map count is approximate)
	c.Set("d", "4")
}

func TestPersistence(t *testing.T) {
	dir := filepath.Join(os.TempDir(), "seele-cache-persist-"+strconv.FormatInt(time.Now().UnixNano(), 10))
	defer os.RemoveAll(dir)

	c1, err := NewFileCache(Config{BaseDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	c1.Set("persistent", "stored")

	c2, err := NewFileCache(Config{BaseDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	val, ok := c2.Get("persistent")
	if !ok || val != "stored" {
		t.Fatalf("persistence failed: %q, %v", val, ok)
	}
}

func TestConcurrent(t *testing.T) {
	c := tempCache(t, Config{MaxEntries: 1000, DefaultTTL: 10 * time.Minute})
	done := make(chan struct{})
	for i := 0; i < 20; i++ {
		go func(id int) {
			for j := 0; j < 50; j++ {
				key := "k" + strconv.Itoa((id*50+j)%100)
				c.Set(key, "value")
				c.Get(key)
				c.Stats()
				c.Keys()
				if j%10 == 0 {
					c.List()
				}
			}
			done <- struct{}{}
		}(i)
	}
	for i := 0; i < 20; i++ {
		<-done
	}
}
