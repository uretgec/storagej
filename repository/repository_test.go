package repository

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/uretgec/storagej"
	"github.com/uretgec/storagej/pebble"
)

func newTestCore(t *testing.T, tenant, typ string) *Core {
	t.Helper()
	db, err := pebble.Open(t.TempDir(), storagej.DefaultOptions())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return NewCore(db, tenant, typ)
}

// -----------------------------------------------------------------------------
// Get / Put
// -----------------------------------------------------------------------------

func TestPutGet(t *testing.T) {
	c := newTestCore(t, "acme", "post")
	ctx := context.Background()

	if err := c.Put(ctx, "1", []byte("hello")); err != nil {
		t.Fatalf("put: %v", err)
	}
	got, err := c.Get(ctx, "1")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if string(got) != "hello" {
		t.Fatalf("get = %q, want %q", got, "hello")
	}
}

func TestGetNotFound(t *testing.T) {
	c := newTestCore(t, "acme", "post")
	_, err := c.Get(context.Background(), "missing")
	if !errors.Is(err, storagej.ErrNotFound) {
		t.Fatalf("err = %v, want storagej.ErrNotFound", err)
	}
}

// -----------------------------------------------------------------------------
// PutWithIndex / GetIndex
// -----------------------------------------------------------------------------

func TestPutWithIndexAndGetIndex(t *testing.T) {
	c := newTestCore(t, "acme", "post")
	ctx := context.Background()

	if err := c.PutWithIndex(ctx, "1", "slug", "hello-world", []byte("data")); err != nil {
		t.Fatalf("putwithindex: %v", err)
	}

	id, err := c.GetIndex(ctx, "slug", "hello-world")
	if err != nil {
		t.Fatalf("getindex: %v", err)
	}
	if id != "1" {
		t.Fatalf("id = %q, want %q", id, "1")
	}

	got, err := c.Get(ctx, "1")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if string(got) != "data" {
		t.Fatalf("get = %q, want %q", got, "data")
	}
}

func TestPutWithIndexRejectsConflict(t *testing.T) {
	c := newTestCore(t, "acme", "post")
	ctx := context.Background()

	if err := c.PutWithIndex(ctx, "1", "slug", "hello-world", []byte("a")); err != nil {
		t.Fatalf("first put: %v", err)
	}
	err := c.PutWithIndex(ctx, "2", "slug", "hello-world", []byte("b"))
	if !errors.Is(err, ErrIndexConflict) {
		t.Fatalf("err = %v, want ErrIndexConflict", err)
	}

	// The rejected write must not have persisted anything for id "2".
	if _, err := c.Get(ctx, "2"); !errors.Is(err, storagej.ErrNotFound) {
		t.Fatalf("id 2 should not exist after rejected conflict, get err = %v", err)
	}
}

func TestPutWithIndexAllowsSameIDRewrite(t *testing.T) {
	c := newTestCore(t, "acme", "post")
	ctx := context.Background()

	if err := c.PutWithIndex(ctx, "1", "slug", "hello-world", []byte("a")); err != nil {
		t.Fatalf("first put: %v", err)
	}
	// Writing the same id + same index value again is not a conflict.
	if err := c.PutWithIndex(ctx, "1", "slug", "hello-world", []byte("b")); err != nil {
		t.Fatalf("rewrite same id: %v", err)
	}
	got, err := c.Get(ctx, "1")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if string(got) != "b" {
		t.Fatalf("get = %q, want %q", got, "b")
	}
}

func TestGetIndexNotFound(t *testing.T) {
	c := newTestCore(t, "acme", "post")
	_, err := c.GetIndex(context.Background(), "slug", "missing")
	if !errors.Is(err, storagej.ErrNotFound) {
		t.Fatalf("err = %v, want storagej.ErrNotFound", err)
	}
}

// -----------------------------------------------------------------------------
// ReplaceIndex
// -----------------------------------------------------------------------------

func TestReplaceIndexRenamesIndexEntry(t *testing.T) {
	c := newTestCore(t, "acme", "post")
	ctx := context.Background()

	if err := c.PutWithIndex(ctx, "1", "slug", "old-slug", []byte("v1")); err != nil {
		t.Fatalf("put: %v", err)
	}
	if err := c.ReplaceIndex(ctx, "1", "slug", "old-slug", "new-slug", []byte("v2")); err != nil {
		t.Fatalf("replace: %v", err)
	}

	if _, err := c.GetIndex(ctx, "slug", "old-slug"); !errors.Is(err, storagej.ErrNotFound) {
		t.Fatalf("old slug should be gone, err = %v", err)
	}
	id, err := c.GetIndex(ctx, "slug", "new-slug")
	if err != nil {
		t.Fatalf("getindex new-slug: %v", err)
	}
	if id != "1" {
		t.Fatalf("id = %q, want %q", id, "1")
	}
	got, err := c.Get(ctx, "1")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if string(got) != "v2" {
		t.Fatalf("get = %q, want %q", got, "v2")
	}
}

func TestReplaceIndexSameValueOnlyUpdatesData(t *testing.T) {
	c := newTestCore(t, "acme", "post")
	ctx := context.Background()

	if err := c.PutWithIndex(ctx, "1", "slug", "same-slug", []byte("v1")); err != nil {
		t.Fatalf("put: %v", err)
	}
	if err := c.ReplaceIndex(ctx, "1", "slug", "same-slug", "same-slug", []byte("v2")); err != nil {
		t.Fatalf("replace: %v", err)
	}

	id, err := c.GetIndex(ctx, "slug", "same-slug")
	if err != nil {
		t.Fatalf("getindex: %v", err)
	}
	if id != "1" {
		t.Fatalf("id = %q, want %q", id, "1")
	}
	got, err := c.Get(ctx, "1")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if string(got) != "v2" {
		t.Fatalf("get = %q, want %q", got, "v2")
	}
}

func TestReplaceIndexRejectsConflict(t *testing.T) {
	c := newTestCore(t, "acme", "post")
	ctx := context.Background()

	if err := c.PutWithIndex(ctx, "1", "slug", "slug-a", []byte("a")); err != nil {
		t.Fatalf("put 1: %v", err)
	}
	if err := c.PutWithIndex(ctx, "2", "slug", "slug-b", []byte("b")); err != nil {
		t.Fatalf("put 2: %v", err)
	}

	// id "2" tries to rename its slug to one already owned by id "1".
	err := c.ReplaceIndex(ctx, "2", "slug", "slug-b", "slug-a", []byte("b2"))
	if !errors.Is(err, ErrIndexConflict) {
		t.Fatalf("err = %v, want ErrIndexConflict", err)
	}

	// Nothing about id "2" or the "slug-b" index should have changed.
	got, err := c.Get(ctx, "2")
	if err != nil {
		t.Fatalf("get 2: %v", err)
	}
	if string(got) != "b" {
		t.Fatalf("get 2 = %q, want unchanged %q", got, "b")
	}
	id, err := c.GetIndex(ctx, "slug", "slug-b")
	if err != nil {
		t.Fatalf("getindex slug-b: %v", err)
	}
	if id != "2" {
		t.Fatalf("slug-b should still point at 2, got %q", id)
	}
}

// -----------------------------------------------------------------------------
// Delete / DeleteWithIndex
// -----------------------------------------------------------------------------

func TestDeleteWithIndexRemovesBoth(t *testing.T) {
	c := newTestCore(t, "acme", "post")
	ctx := context.Background()

	if err := c.PutWithIndex(ctx, "1", "slug", "hello-world", []byte("data")); err != nil {
		t.Fatalf("put: %v", err)
	}
	if err := c.DeleteWithIndex(ctx, "1", "slug", "hello-world"); err != nil {
		t.Fatalf("delete: %v", err)
	}

	if _, err := c.Get(ctx, "1"); !errors.Is(err, storagej.ErrNotFound) {
		t.Fatalf("get after delete err = %v, want ErrNotFound", err)
	}
	if _, err := c.GetIndex(ctx, "slug", "hello-world"); !errors.Is(err, storagej.ErrNotFound) {
		t.Fatalf("getindex after delete err = %v, want ErrNotFound", err)
	}
}

func TestDeleteIsIdempotent(t *testing.T) {
	c := newTestCore(t, "acme", "post")
	if err := c.Delete(context.Background(), "never-existed"); err != nil {
		t.Fatalf("delete missing record should not error, got %v", err)
	}
}

// -----------------------------------------------------------------------------
// List / ListFrom
// -----------------------------------------------------------------------------

func TestListOrdersChronologically(t *testing.T) {
	c := newTestCore(t, "acme", "post")
	ctx := context.Background()

	var ids []string
	for i := 0; i < 5; i++ {
		id := fmt.Sprintf("%02d-%d", i, time.Now().UnixNano())
		if err := c.Put(ctx, id, []byte(id)); err != nil {
			t.Fatalf("put %d: %v", i, err)
		}
		ids = append(ids, id)
	}

	var got []string
	err := c.List(ctx, func(id string, value []byte) error {
		got = append(got, id)
		return nil
	})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != len(ids) {
		t.Fatalf("len(got) = %d, want %d", len(got), len(ids))
	}
	for i := range ids {
		if got[i] != ids[i] {
			t.Fatalf("got[%d] = %q, want %q (insertion order should match key order here)", i, got[i], ids[i])
		}
	}
}

func TestListIsolatesByTenantAndType(t *testing.T) {
	ctx := context.Background()
	db, err := pebble.Open(t.TempDir(), storagej.DefaultOptions())
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	posts := NewCore(db, "acme", "post")
	pages := NewCore(db, "acme", "page")
	otherTenant := NewCore(db, "other", "post")

	if err := posts.Put(ctx, "1", []byte("post")); err != nil {
		t.Fatalf("put post: %v", err)
	}
	if err := pages.Put(ctx, "1", []byte("page")); err != nil {
		t.Fatalf("put page: %v", err)
	}
	if err := otherTenant.Put(ctx, "1", []byte("other")); err != nil {
		t.Fatalf("put other tenant: %v", err)
	}

	var got []string
	err = posts.List(ctx, func(id string, value []byte) error {
		got = append(got, string(value))
		return nil
	})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 1 || got[0] != "post" {
		t.Fatalf("posts.List = %v, want [\"post\"]", got)
	}
}

func TestListFromStopIteration(t *testing.T) {
	c := newTestCore(t, "acme", "post")
	ctx := context.Background()

	for i := 0; i < 10; i++ {
		id := fmt.Sprintf("%02d", i)
		if err := c.Put(ctx, id, []byte(id)); err != nil {
			t.Fatalf("put %d: %v", i, err)
		}
	}

	count := 0
	err := c.List(ctx, func(id string, value []byte) error {
		count++
		if count == 3 {
			return storagej.ErrStopIteration
		}
		return nil
	})
	if err != nil {
		t.Fatalf("list should stop cleanly, got err %v", err)
	}
	if count != 3 {
		t.Fatalf("count = %d, want 3", count)
	}
}

func TestListFromCursorPagination(t *testing.T) {
	c := newTestCore(t, "acme", "post")
	ctx := context.Background()

	var ids []string
	for i := 0; i < 5; i++ {
		id := fmt.Sprintf("%02d", i)
		if err := c.Put(ctx, id, []byte(id)); err != nil {
			t.Fatalf("put %d: %v", i, err)
		}
		ids = append(ids, id)
	}

	// Page 1: first 2.
	var page1 []string
	err := c.ListFrom(ctx, "", func(id string, value []byte) error {
		if len(page1) >= 2 {
			return storagej.ErrStopIteration
		}
		page1 = append(page1, id)
		return nil
	})
	if err != nil {
		t.Fatalf("list page1: %v", err)
	}
	if len(page1) != 2 || page1[0] != ids[0] || page1[1] != ids[1] {
		t.Fatalf("page1 = %v, want %v", page1, ids[:2])
	}

	// Page 2: starting after the last id of page 1, must not repeat it.
	cursor := page1[len(page1)-1]
	var page2 []string
	err = c.ListFrom(ctx, cursor, func(id string, value []byte) error {
		page2 = append(page2, id)
		return nil
	})
	if err != nil {
		t.Fatalf("list page2: %v", err)
	}
	if len(page2) != 3 {
		t.Fatalf("len(page2) = %d, want 3", len(page2))
	}
	for i, id := range page2 {
		if id != ids[2+i] {
			t.Fatalf("page2[%d] = %q, want %q", i, id, ids[2+i])
		}
	}
}

func TestListFromCursorAtLastRecordReturnsEmpty(t *testing.T) {
	c := newTestCore(t, "acme", "post")
	ctx := context.Background()

	if err := c.Put(ctx, "1", []byte("v")); err != nil {
		t.Fatalf("put: %v", err)
	}

	var got []string
	err := c.ListFrom(ctx, "1", func(id string, value []byte) error {
		got = append(got, id)
		return nil
	})
	if err != nil {
		t.Fatalf("listfrom: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("got = %v, want empty", got)
	}
}

// -----------------------------------------------------------------------------
// Concurrency: Core.mu must make index-affecting operations race-free, not
// just individually atomic. Run these with -race.
// -----------------------------------------------------------------------------

// TestPutWithIndexSerializesConcurrentConflicts fires many goroutines at
// PutWithIndex for the same field=value simultaneously. Exactly one may
// win; every other must see ErrIndexConflict, and the index must end up
// pointing at precisely the winner with no orphaned data for the losers.
func TestPutWithIndexSerializesConcurrentConflicts(t *testing.T) {
	c := newTestCore(t, "acme", "post")
	ctx := context.Background()

	const n = 20
	var wg sync.WaitGroup
	results := make([]error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			id := fmt.Sprintf("id-%d", i)
			results[i] = c.PutWithIndex(ctx, id, "slug", "same-slug", []byte(id))
		}(i)
	}
	wg.Wait()

	successes, conflicts := 0, 0
	for _, err := range results {
		switch {
		case err == nil:
			successes++
		case errors.Is(err, ErrIndexConflict):
			conflicts++
		default:
			t.Fatalf("unexpected error: %v", err)
		}
	}
	if successes != 1 {
		t.Fatalf("successes = %d, want exactly 1", successes)
	}
	if conflicts != n-1 {
		t.Fatalf("conflicts = %d, want %d", conflicts, n-1)
	}

	id, err := c.GetIndex(ctx, "slug", "same-slug")
	if err != nil {
		t.Fatalf("getindex: %v", err)
	}
	if _, err := c.Get(ctx, id); err != nil {
		t.Fatalf("winner's data missing: %v", err)
	}
}

// TestReplaceIndexSerializesConcurrentConflicts seeds n distinct records,
// each with its own slug, then races all of them to rename to the SAME
// target slug at once. Exactly one rename may succeed.
func TestReplaceIndexSerializesConcurrentConflicts(t *testing.T) {
	c := newTestCore(t, "acme", "post")
	ctx := context.Background()

	const n = 10
	ids := make([]string, n)
	for i := 0; i < n; i++ {
		ids[i] = fmt.Sprintf("id-%d", i)
		if err := c.PutWithIndex(ctx, ids[i], "slug", fmt.Sprintf("slug-%d", i), []byte(ids[i])); err != nil {
			t.Fatalf("seed put %d: %v", i, err)
		}
	}

	var wg sync.WaitGroup
	results := make([]error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i] = c.ReplaceIndex(ctx, ids[i], "slug", fmt.Sprintf("slug-%d", i), "target-slug", []byte(ids[i]))
		}(i)
	}
	wg.Wait()

	successes := 0
	for _, err := range results {
		if err == nil {
			successes++
		} else if !errors.Is(err, ErrIndexConflict) {
			t.Fatalf("unexpected error: %v", err)
		}
	}
	if successes != 1 {
		t.Fatalf("successes = %d, want exactly 1", successes)
	}

	id, err := c.GetIndex(ctx, "slug", "target-slug")
	if err != nil {
		t.Fatalf("getindex: %v", err)
	}
	if _, err := c.Get(ctx, id); err != nil {
		t.Fatalf("winner's data missing: %v", err)
	}
}

// TestDeleteWithIndexRaceDoesNotOrphanIndex races a delete of an existing
// record against a concurrent attempt by a different id to claim the exact
// slug the delete is freeing up. Whichever order they land in, the end
// state must be internally consistent: either the delete wins and the slug
// is free, or the claim wins and the index points at the claimant's data —
// never an index entry with no matching data, and never a slug that stays
// claimed by a deleted id.
func TestDeleteWithIndexRaceDoesNotOrphanIndex(t *testing.T) {
	c := newTestCore(t, "acme", "post")
	ctx := context.Background()

	if err := c.PutWithIndex(ctx, "owner", "slug", "contested", []byte("owner-data")); err != nil {
		t.Fatalf("seed: %v", err)
	}

	var wg sync.WaitGroup
	var deleteErr, claimErr error
	wg.Add(2)
	go func() {
		defer wg.Done()
		deleteErr = c.DeleteWithIndex(ctx, "owner", "slug", "contested")
	}()
	go func() {
		defer wg.Done()
		claimErr = c.PutWithIndex(ctx, "claimant", "slug", "contested", []byte("claimant-data"))
	}()
	wg.Wait()

	if deleteErr != nil {
		t.Fatalf("delete: %v", deleteErr)
	}

	id, err := c.GetIndex(ctx, "slug", "contested")
	if errors.Is(err, storagej.ErrNotFound) {
		// The delete's view of the world won: the slug is free, and the
		// claimant must have lost (its own write can't have landed without
		// leaving an index entry, since PutWithIndex is atomic).
		if claimErr == nil {
			t.Fatal("claim reported success but slug index is gone")
		}
		if _, err := c.Get(ctx, "claimant"); !errors.Is(err, storagej.ErrNotFound) {
			t.Fatalf("claimant has no index entry but its data exists: get err = %v", err)
		}
		return
	}
	if err != nil {
		t.Fatalf("getindex: %v", err)
	}

	// The index points at someone: that someone's data must actually exist,
	// and no OTHER id's data may also claim to be reachable by this slug.
	if _, err := c.Get(ctx, id); err != nil {
		t.Fatalf("index points at %q but its data is missing: %v", id, err)
	}
	if id != "claimant" {
		t.Fatalf("index points at %q, want %q (the only id that could still hold this slug)", id, "claimant")
	}
}
