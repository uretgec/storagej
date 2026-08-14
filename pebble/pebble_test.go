package pebble

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/uretgec/storagej"
)

// openTestStore opens an isolated Pebble store in a temporary directory and
// registers cleanup. Each test gets its own database.
func openTestStore(t *testing.T, opts storagej.Options) *Store {
	t.Helper()
	dir := t.TempDir()
	s, err := Open(dir, opts)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// -----------------------------------------------------------------------------
// CRUD
// -----------------------------------------------------------------------------

func TestSetGet(t *testing.T) {
	s := openTestStore(t, storagej.DefaultOptions())
	ctx := context.Background()

	key := []byte("uretgec:d:post:1")
	val := []byte("hello world")

	if err := s.Set(ctx, key, val); err != nil {
		t.Fatalf("set: %v", err)
	}
	got, err := s.Get(ctx, key)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if !bytes.Equal(got, val) {
		t.Fatalf("get = %q, want %q", got, val)
	}
}

func TestGetReturnsSafeCopy(t *testing.T) {
	s := openTestStore(t, storagej.DefaultOptions())
	ctx := context.Background()

	key := []byte("k")
	if err := s.Set(ctx, key, []byte("original")); err != nil {
		t.Fatalf("set: %v", err)
	}

	// Mutating the returned slice must not affect the stored value.
	got, _ := s.Get(ctx, key)
	got[0] = 'X'

	again, _ := s.Get(ctx, key)
	if string(again) != "original" {
		t.Fatalf("stored value mutated through returned slice: got %q", again)
	}
}

func TestGetNotFound(t *testing.T) {
	s := openTestStore(t, storagej.DefaultOptions())
	_, err := s.Get(context.Background(), []byte("missing"))
	if !errors.Is(err, storagej.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

// -----------------------------------------------------------------------------
// Exists & GetInto
// -----------------------------------------------------------------------------

func TestExists(t *testing.T) {
	s := openTestStore(t, storagej.DefaultOptions())
	ctx := context.Background()

	ok, err := s.Exists(ctx, []byte("missing"))
	if err != nil {
		t.Fatalf("exists on missing key: %v", err)
	}
	if ok {
		t.Fatal("exists = true, want false for missing key")
	}

	if err := s.Set(ctx, []byte("k"), []byte("v")); err != nil {
		t.Fatalf("set: %v", err)
	}
	ok, err = s.Exists(ctx, []byte("k"))
	if err != nil {
		t.Fatalf("exists: %v", err)
	}
	if !ok {
		t.Fatal("exists = false, want true for present key")
	}
}

func TestExistsAfterDelete(t *testing.T) {
	s := openTestStore(t, storagej.DefaultOptions())
	ctx := context.Background()

	if err := s.Set(ctx, []byte("k"), []byte("v")); err != nil {
		t.Fatalf("set: %v", err)
	}
	if err := s.Delete(ctx, []byte("k")); err != nil {
		t.Fatalf("delete: %v", err)
	}

	ok, err := s.Exists(ctx, []byte("k"))
	if err != nil {
		t.Fatalf("exists: %v", err)
	}
	if ok {
		t.Fatal("exists = true after delete, want false")
	}
}

func TestGetIntoReusesBuffer(t *testing.T) {
	s := openTestStore(t, storagej.DefaultOptions())
	ctx := context.Background()

	if err := s.Set(ctx, []byte("k"), []byte("hello")); err != nil {
		t.Fatalf("set: %v", err)
	}

	dst := make([]byte, 0, 32)
	origPtr := &dst[:cap(dst)][0]

	got, err := s.GetInto(ctx, []byte("k"), dst)
	if err != nil {
		t.Fatalf("getinto: %v", err)
	}
	if string(got) != "hello" {
		t.Fatalf("getinto = %q, want %q", got, "hello")
	}
	if &got[:cap(got)][0] != origPtr {
		t.Fatal("GetInto allocated a new buffer despite sufficient capacity in dst")
	}
}

func TestGetIntoGrowsBuffer(t *testing.T) {
	s := openTestStore(t, storagej.DefaultOptions())
	ctx := context.Background()

	val := bytes.Repeat([]byte("x"), 256)
	if err := s.Set(ctx, []byte("k"), val); err != nil {
		t.Fatalf("set: %v", err)
	}

	dst := make([]byte, 0, 4) // deliberately too small
	got, err := s.GetInto(ctx, []byte("k"), dst)
	if err != nil {
		t.Fatalf("getinto: %v", err)
	}
	if !bytes.Equal(got, val) {
		t.Fatal("getinto with grown buffer returned wrong value")
	}
}

func TestGetIntoNotFound(t *testing.T) {
	s := openTestStore(t, storagej.DefaultOptions())
	_, err := s.GetInto(context.Background(), []byte("missing"), nil)
	if !errors.Is(err, storagej.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func TestDelete(t *testing.T) {
	s := openTestStore(t, storagej.DefaultOptions())
	ctx := context.Background()

	key := []byte("k")
	if err := s.Set(ctx, key, []byte("v")); err != nil {
		t.Fatalf("set: %v", err)
	}
	if err := s.Delete(ctx, key); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := s.Get(ctx, key); !errors.Is(err, storagej.ErrNotFound) {
		t.Fatalf("after delete, err = %v, want ErrNotFound", err)
	}
}

func TestDeleteMissingKeyIsIdempotent(t *testing.T) {
	s := openTestStore(t, storagej.DefaultOptions())
	if err := s.Delete(context.Background(), []byte("never-existed")); err != nil {
		t.Fatalf("delete missing key should not error, got %v", err)
	}
}

// -----------------------------------------------------------------------------
// Batch
// -----------------------------------------------------------------------------

func TestBatchCommit(t *testing.T) {
	s := openTestStore(t, storagej.DefaultOptions())
	ctx := context.Background()

	b := s.NewBatch()
	for i := 0; i < 10; i++ {
		key := []byte(fmt.Sprintf("key:%d", i))
		val := []byte(fmt.Sprintf("val:%d", i))
		if err := b.Set(key, val); err != nil {
			t.Fatalf("batch set: %v", err)
		}
	}
	if err := b.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}
	b.Close()

	for i := 0; i < 10; i++ {
		got, err := s.Get(ctx, []byte(fmt.Sprintf("key:%d", i)))
		if err != nil {
			t.Fatalf("get key:%d: %v", i, err)
		}
		if want := fmt.Sprintf("val:%d", i); string(got) != want {
			t.Fatalf("key:%d = %q, want %q", i, got, want)
		}
	}
}

func TestBatchCount(t *testing.T) {
	s := openTestStore(t, storagej.DefaultOptions())
	b := s.NewBatch()
	defer b.Close()

	b.Set([]byte("a"), []byte("1"))
	b.Set([]byte("b"), []byte("2"))
	b.Delete([]byte("c"))

	if b.Count() != 3 {
		t.Fatalf("count = %d, want 3", b.Count())
	}
}

func TestBatchDelete(t *testing.T) {
	s := openTestStore(t, storagej.DefaultOptions())
	ctx := context.Background()

	if err := s.Set(ctx, []byte("x"), []byte("1")); err != nil {
		t.Fatalf("set: %v", err)
	}

	b := s.NewBatch()
	b.Delete([]byte("x"))
	b.Set([]byte("y"), []byte("2"))
	if err := b.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}
	b.Close()

	if _, err := s.Get(ctx, []byte("x")); !errors.Is(err, storagej.ErrNotFound) {
		t.Fatalf("x should be deleted, got err %v", err)
	}
	if _, err := s.Get(ctx, []byte("y")); err != nil {
		t.Fatalf("y should exist: %v", err)
	}
}

// -----------------------------------------------------------------------------
// Scan & PrefixScan
// -----------------------------------------------------------------------------

func TestScanRange(t *testing.T) {
	s := openTestStore(t, storagej.DefaultOptions())
	ctx := context.Background()

	for _, k := range []string{"a", "b", "c", "d", "e"} {
		if err := s.Set(ctx, []byte(k), []byte(k+"-val")); err != nil {
			t.Fatalf("set %s: %v", k, err)
		}
	}

	// Half-open range [b, d) must yield b and c only.
	var got []string
	err := s.Scan(ctx, []byte("b"), []byte("d"), func(key, value []byte) error {
		got = append(got, string(key))
		return nil
	})
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if want := []string{"b", "c"}; !slices.Equal(got, want) {
		t.Fatalf("scan = %v, want %v", got, want)
	}
}

func TestScanStopIteration(t *testing.T) {
	s := openTestStore(t, storagej.DefaultOptions())
	ctx := context.Background()

	for i := 0; i < 10; i++ {
		key := []byte(fmt.Sprintf("k%02d", i))
		if err := s.Set(ctx, key, []byte("v")); err != nil {
			t.Fatalf("set: %v", err)
		}
	}

	count := 0
	err := s.Scan(ctx, []byte("k"), []byte("k\xFF"), func(key, value []byte) error {
		count++
		if count == 3 {
			return storagej.ErrStopIteration
		}
		return nil
	})
	if err != nil {
		t.Fatalf("scan should stop cleanly, got err %v", err)
	}
	if count != 3 {
		t.Fatalf("count = %d, want 3 (early stop)", count)
	}
}

func TestScanRespectsContextCancellation(t *testing.T) {
	s := openTestStore(t, storagej.DefaultOptions())
	ctx := context.Background()

	for i := 0; i < 1000; i++ {
		key := []byte(fmt.Sprintf("k%04d", i))
		if err := s.Set(ctx, key, []byte("v")); err != nil {
			t.Fatalf("set: %v", err)
		}
	}

	cancelCtx, cancel := context.WithCancel(ctx)
	cancel() // canceled before the scan even starts

	visited := 0
	err := s.Scan(cancelCtx, []byte("k"), []byte("k\xff"), func(key, value []byte) error {
		visited++
		return nil
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	// The check runs every 256 keys, so cancellation must be observed within
	// the first batch rather than after walking all 1000 keys.
	if visited > 256 {
		t.Fatalf("scan visited %d keys after cancellation, want <= 256", visited)
	}
}

// TestPrefixScan verifies prefix matching AND tenant/type isolation, which is
// the foundation of the multi-tenant key layout ({tenant}:d:{type}:{id}).
func TestPrefixScan(t *testing.T) {
	s := openTestStore(t, storagej.DefaultOptions())
	ctx := context.Background()

	keys := []string{
		"uretgec:d:post:1",
		"uretgec:d:post:2",
		"uretgec:d:page:1",    // same tenant, different type — must be excluded
		"uretgec:d:postfix:1", // shares "uretgec:d:post" but not "uretgec:d:post:" — excluded
		"other:d:post:1",      // different tenant — must be excluded
	}
	for _, k := range keys {
		if err := s.Set(ctx, []byte(k), []byte("v")); err != nil {
			t.Fatalf("set %s: %v", k, err)
		}
	}

	var got []string
	err := s.PrefixScan(ctx, []byte("uretgec:d:post:"), func(key, value []byte) error {
		got = append(got, string(key))
		return nil
	})
	if err != nil {
		t.Fatalf("prefix scan: %v", err)
	}
	if want := []string{"uretgec:d:post:1", "uretgec:d:post:2"}; !slices.Equal(got, want) {
		t.Fatalf("prefix scan = %v, want %v", got, want)
	}
}

// -----------------------------------------------------------------------------
// Lifecycle
// -----------------------------------------------------------------------------

func TestOpenCustomWALDir(t *testing.T) {
	dataDir := t.TempDir()
	walDir := filepath.Join(t.TempDir(), "custom-wal")

	opts := storagej.DefaultOptions()
	opts.WALDir = walDir

	s, err := Open(dataDir, opts)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer s.Close()

	entries, err := os.ReadDir(walDir)
	if err != nil {
		t.Fatalf("read custom wal dir: %v", err)
	}
	if len(entries) == 0 {
		t.Fatal("custom WAL dir is empty; opts.WALDir may not have been honored")
	}

	// The default {path}/wal must NOT also have been created.
	if _, err := os.Stat(filepath.Join(dataDir, "wal")); !os.IsNotExist(err) {
		t.Fatalf("default wal dir should not exist when WALDir is set, stat err = %v", err)
	}
}

func TestOpenReadOnlyDoesNotCreateDefaultWALDir(t *testing.T) {
	// Build a normal store, checkpoint it, then reopen the checkpoint
	// read-only and confirm no {path}/wal directory gets created for it.
	s := openTestStore(t, storagej.DefaultOptions())
	ctx := context.Background()
	if err := s.Set(ctx, []byte("k"), []byte("v")); err != nil {
		t.Fatalf("set: %v", err)
	}

	cpDir := filepath.Join(t.TempDir(), "checkpoint")
	if err := s.Checkpoint(cpDir); err != nil {
		t.Fatalf("checkpoint: %v", err)
	}

	restored, err := Open(cpDir, storagej.Options{ReadOnly: true})
	if err != nil {
		t.Fatalf("open checkpoint read-only: %v", err)
	}
	defer restored.Close()

	if _, err := os.Stat(filepath.Join(cpDir, "wal")); !os.IsNotExist(err) {
		t.Fatalf("expected no default wal dir for a read-only store, stat err = %v", err)
	}
}

func TestCloseIdempotent(t *testing.T) {
	s, err := Open(t.TempDir(), storagej.DefaultOptions())
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("first close: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("second close should not error: %v", err)
	}
}

// -----------------------------------------------------------------------------
// Events & Metrics
// -----------------------------------------------------------------------------

func TestEventListenerNilWithoutCallback(t *testing.T) {
	s := &Store{opts: storagej.DefaultOptions()}
	if s.eventListener() != nil {
		t.Fatal("eventListener should be nil when OnEvent is unset")
	}
}

func TestEventListenerSetWithCallback(t *testing.T) {
	opts := storagej.DefaultOptions()
	opts.OnEvent = func(storagej.Event) {}
	s := &Store{opts: opts}
	if s.eventListener() == nil {
		t.Fatal("eventListener should be non-nil when OnEvent is set")
	}
}

func TestOnEventReceivesWALCreated(t *testing.T) {
	events := make(chan storagej.Event, 16)
	opts := storagej.DefaultOptions()
	opts.OnEvent = func(e storagej.Event) { events <- e }

	s, err := Open(t.TempDir(), opts)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer s.Close()

	// A WAL is created synchronously during Open.
	select {
	case e := <-events:
		if e.Type != storagej.EventWALCreated {
			t.Fatalf("first event = %s, want WAL_CREATED", e.Type)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for WALCreated event")
	}
}

func TestMetricsSnapshotFields(t *testing.T) {
	s := openTestStore(t, storagej.DefaultOptions())
	ctx := context.Background()

	for i := 0; i < 100; i++ {
		key := []byte(fmt.Sprintf("k%d", i))
		if err := s.Set(ctx, key, bytes.Repeat([]byte("x"), 256)); err != nil {
			t.Fatalf("set: %v", err)
		}
	}

	snap := s.snapshot()
	if snap.CacheHitRatePercent < 0 || snap.CacheHitRatePercent > 100 {
		t.Fatalf("cache hit rate out of range: %f", snap.CacheHitRatePercent)
	}
	if snap.TotalFiles < 0 || snap.TotalSizeMB < 0 || snap.WALSizeMB < 0 {
		t.Fatalf("negative metric value: %+v", snap)
	}
}

func TestOnMetricsDelivered(t *testing.T) {
	received := make(chan storagej.MetricsSnapshot, 1)
	opts := storagej.DefaultOptions()
	opts.MetricsInterval = 20 * time.Millisecond
	opts.OnMetrics = func(m storagej.MetricsSnapshot) {
		select {
		case received <- m:
		default:
		}
	}

	s, err := Open(t.TempDir(), opts)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer s.Close()

	select {
	case <-received:
		// OK
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for metrics snapshot")
	}
}

// -----------------------------------------------------------------------------
// Checkpoint (backup foundation)
// -----------------------------------------------------------------------------

func TestCheckpoint(t *testing.T) {
	s := openTestStore(t, storagej.DefaultOptions())
	ctx := context.Background()

	if err := s.Set(ctx, []byte("k"), []byte("v")); err != nil {
		t.Fatalf("set: %v", err)
	}

	cpDir := filepath.Join(t.TempDir(), "checkpoint")
	if err := s.Checkpoint(cpDir); err != nil {
		t.Fatalf("checkpoint: %v", err)
	}

	entries, err := os.ReadDir(cpDir)
	if err != nil {
		t.Fatalf("read checkpoint dir: %v", err)
	}
	if len(entries) == 0 {
		t.Fatal("checkpoint dir is empty")
	}
}

// TestCheckpointRestoresData opens the checkpoint as a separate read-only
// store and verifies the data survived — the core guarantee of the backup
// system.
func TestCheckpointRestoresData(t *testing.T) {
	s := openTestStore(t, storagej.DefaultOptions())
	ctx := context.Background()

	if err := s.Set(ctx, []byte("k"), []byte("v")); err != nil {
		t.Fatalf("set: %v", err)
	}

	cpDir := filepath.Join(t.TempDir(), "checkpoint")
	if err := s.Checkpoint(cpDir); err != nil {
		t.Fatalf("checkpoint: %v", err)
	}

	// Open the checkpoint read-only. WALDir is intentionally left empty so
	// Pebble follows the checkpoint's own manifest layout.
	restored, err := Open(cpDir, storagej.Options{ReadOnly: true})
	if err != nil {
		t.Fatalf("open checkpoint: %v", err)
	}
	defer restored.Close()

	got, err := restored.Get(ctx, []byte("k"))
	if err != nil {
		t.Fatalf("get from checkpoint: %v", err)
	}
	if string(got) != "v" {
		t.Fatalf("restored = %q, want %q", got, "v")
	}
}
