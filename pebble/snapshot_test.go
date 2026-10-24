package pebble

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/uretgec/storagej/v2"
)

// TestSnapshotStaysConsistentDuringLaterWrites is the actual point of a
// snapshot: read several keys "as of one moment," unaffected by writes
// that land on some of them afterward — the scenario a page rendering a
// post + its author + a comment count would hit under real traffic.
func TestSnapshotStaysConsistentDuringLaterWrites(t *testing.T) {
	s := openTestStore(t, storagej.DefaultOptions())
	ctx := context.Background()

	keys := []string{"a", "b", "c", "d", "e"}
	for _, k := range keys {
		if err := s.Set(ctx, []byte(k), []byte("original")); err != nil {
			t.Fatalf("seed %s: %v", k, err)
		}
	}

	snap := s.Snapshot()
	defer snap.Close()

	for _, k := range keys {
		if err := s.Set(ctx, []byte(k), []byte("mutated")); err != nil {
			t.Fatalf("mutate %s: %v", k, err)
		}
	}

	for _, k := range keys {
		got, err := snap.Get(ctx, []byte(k))
		if err != nil {
			t.Fatalf("snapshot get %s: %v", k, err)
		}
		if string(got) != "original" {
			t.Fatalf("snapshot get %s = %q, want %q (must not see the later write)", k, got, "original")
		}
	}

	for _, k := range keys {
		got, err := s.Get(ctx, []byte(k))
		if err != nil {
			t.Fatalf("live get %s: %v", k, err)
		}
		if string(got) != "mutated" {
			t.Fatalf("live get %s = %q, want %q", k, got, "mutated")
		}
	}
}

func TestSnapshotDoesNotSeeLaterWrites(t *testing.T) {
	s := openTestStore(t, storagej.DefaultOptions())
	ctx := context.Background()

	snap := s.Snapshot()
	defer snap.Close()

	if err := s.Set(ctx, []byte("new-key"), []byte("v")); err != nil {
		t.Fatalf("set: %v", err)
	}

	if _, err := snap.Get(ctx, []byte("new-key")); !errors.Is(err, storagej.ErrNotFound) {
		t.Fatalf("snapshot should not see a key written after it was taken, err = %v", err)
	}
}

func TestSnapshotSeesDataDeletedAfterward(t *testing.T) {
	s := openTestStore(t, storagej.DefaultOptions())
	ctx := context.Background()
	if err := s.Set(ctx, []byte("k"), []byte("v")); err != nil {
		t.Fatalf("set: %v", err)
	}

	snap := s.Snapshot()
	defer snap.Close()

	if err := s.Delete(ctx, []byte("k")); err != nil {
		t.Fatalf("delete: %v", err)
	}

	if _, err := s.Get(ctx, []byte("k")); !errors.Is(err, storagej.ErrNotFound) {
		t.Fatalf("live get after delete err = %v, want ErrNotFound", err)
	}

	got, err := snap.Get(ctx, []byte("k"))
	if err != nil {
		t.Fatalf("snapshot get: %v", err)
	}
	if string(got) != "v" {
		t.Fatalf("snapshot get = %q, want %q", got, "v")
	}
}

func TestSnapshotExistsIgnoresLaterDelete(t *testing.T) {
	s := openTestStore(t, storagej.DefaultOptions())
	ctx := context.Background()
	if err := s.Set(ctx, []byte("k"), []byte("v")); err != nil {
		t.Fatalf("set: %v", err)
	}

	snap := s.Snapshot()
	defer snap.Close()

	if err := s.Delete(ctx, []byte("k")); err != nil {
		t.Fatalf("delete: %v", err)
	}

	ok, err := snap.Exists(ctx, []byte("k"))
	if err != nil {
		t.Fatalf("exists: %v", err)
	}
	if !ok {
		t.Fatal("snapshot Exists should still report true for a key deleted afterward")
	}
}

func TestSnapshotGetIntoReusesBuffer(t *testing.T) {
	s := openTestStore(t, storagej.DefaultOptions())
	ctx := context.Background()
	if err := s.Set(ctx, []byte("k"), []byte("hello")); err != nil {
		t.Fatalf("set: %v", err)
	}

	snap := s.Snapshot()
	defer snap.Close()

	dst := make([]byte, 0, 32)
	origPtr := &dst[:cap(dst)][0]

	got, err := snap.GetInto(ctx, []byte("k"), dst)
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

func TestSnapshotScanIsConsistent(t *testing.T) {
	s := openTestStore(t, storagej.DefaultOptions())
	ctx := context.Background()
	for _, k := range []string{"a", "b", "c"} {
		if err := s.Set(ctx, []byte(k), []byte(k)); err != nil {
			t.Fatalf("set %s: %v", k, err)
		}
	}

	snap := s.Snapshot()
	defer snap.Close()

	if err := s.Delete(ctx, []byte("b")); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if err := s.Set(ctx, []byte("d"), []byte("d")); err != nil {
		t.Fatalf("set d: %v", err)
	}

	var got []string
	err := snap.PrefixScan(ctx, []byte(""), func(key, value []byte) error {
		got = append(got, string(key))
		return nil
	})
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if !slices.Equal(got, []string{"a", "b", "c"}) {
		t.Fatalf("snapshot scan = %v, want [a b c] (the state as of the snapshot, unaffected by the later delete/set)", got)
	}
}

func TestSnapshotScanStopIteration(t *testing.T) {
	s := openTestStore(t, storagej.DefaultOptions())
	ctx := context.Background()
	for _, k := range []string{"a", "b", "c", "d"} {
		if err := s.Set(ctx, []byte(k), []byte(k)); err != nil {
			t.Fatalf("set %s: %v", k, err)
		}
	}

	snap := s.Snapshot()
	defer snap.Close()

	count := 0
	err := snap.Scan(ctx, []byte("a"), []byte("z"), func(key, value []byte) error {
		count++
		if count == 2 {
			return storagej.ErrStopIteration
		}
		return nil
	})
	if err != nil {
		t.Fatalf("scan should stop cleanly, got err %v", err)
	}
	if count != 2 {
		t.Fatalf("count = %d, want 2 (early stop)", count)
	}
}

func TestSnapshotClose(t *testing.T) {
	s := openTestStore(t, storagej.DefaultOptions())
	snap := s.Snapshot()
	if err := snap.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
}

// TestSnapshotIndependentFromOtherSnapshots guards against a shared-state
// bug: two snapshots taken at different moments must each keep their own
// view, not silently reflect whichever was taken most recently.
func TestSnapshotIndependentFromOtherSnapshots(t *testing.T) {
	s := openTestStore(t, storagej.DefaultOptions())
	ctx := context.Background()

	if err := s.Set(ctx, []byte("k"), []byte("v1")); err != nil {
		t.Fatalf("set v1: %v", err)
	}
	snap1 := s.Snapshot()
	defer snap1.Close()

	if err := s.Set(ctx, []byte("k"), []byte("v2")); err != nil {
		t.Fatalf("set v2: %v", err)
	}
	snap2 := s.Snapshot()
	defer snap2.Close()

	if err := s.Set(ctx, []byte("k"), []byte("v3")); err != nil {
		t.Fatalf("set v3: %v", err)
	}

	got1, err := snap1.Get(ctx, []byte("k"))
	if err != nil {
		t.Fatalf("snap1 get: %v", err)
	}
	if string(got1) != "v1" {
		t.Fatalf("snap1 = %q, want %q", got1, "v1")
	}

	got2, err := snap2.Get(ctx, []byte("k"))
	if err != nil {
		t.Fatalf("snap2 get: %v", err)
	}
	if string(got2) != "v2" {
		t.Fatalf("snap2 = %q, want %q", got2, "v2")
	}

	live, err := s.Get(ctx, []byte("k"))
	if err != nil {
		t.Fatalf("live get: %v", err)
	}
	if string(live) != "v3" {
		t.Fatalf("live = %q, want %q", live, "v3")
	}
}
