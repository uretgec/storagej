package blog

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/uretgec/storagej"
	"github.com/uretgec/storagej/pebble"
)

func newTestRepo(t *testing.T, tenant string) *PostRepository {
	t.Helper()
	db, err := pebble.Open(t.TempDir(), storagej.DefaultOptions())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return NewPostRepository(db, tenant)
}

// -----------------------------------------------------------------------------
// Create / Get / GetBySlug
// -----------------------------------------------------------------------------

func TestCreateAndGet(t *testing.T) {
	repo := newTestRepo(t, "acme")
	ctx := context.Background()

	p := &Post{Slug: "hello-world", Title: "Hello", Body: "World"}
	if err := repo.Create(ctx, p); err != nil {
		t.Fatalf("create: %v", err)
	}
	if p.ID == "" {
		t.Fatal("Create did not assign an id")
	}

	got, err := repo.Get(ctx, p.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Title != "Hello" || got.Body != "World" || got.Slug != "hello-world" {
		t.Fatalf("got = %+v, want matching Create input", got)
	}
}

func TestCreatePreservesExplicitID(t *testing.T) {
	repo := newTestRepo(t, "acme")
	p := &Post{ID: "custom-id", Slug: "hello", Title: "T"}
	if err := repo.Create(context.Background(), p); err != nil {
		t.Fatalf("create: %v", err)
	}
	if p.ID != "custom-id" {
		t.Fatalf("Create overwrote an explicit id: got %q", p.ID)
	}
}

func TestCreateRequiresSlug(t *testing.T) {
	repo := newTestRepo(t, "acme")
	err := repo.Create(context.Background(), &Post{Title: "No slug"})
	if !errors.Is(err, ErrSlugRequired) {
		t.Fatalf("err = %v, want ErrSlugRequired", err)
	}
}

func TestCreateRejectsDuplicateSlug(t *testing.T) {
	repo := newTestRepo(t, "acme")
	ctx := context.Background()

	if err := repo.Create(ctx, &Post{Slug: "hello", Title: "A"}); err != nil {
		t.Fatalf("first create: %v", err)
	}
	err := repo.Create(ctx, &Post{Slug: "hello", Title: "B"})
	if !errors.Is(err, ErrSlugTaken) {
		t.Fatalf("err = %v, want ErrSlugTaken", err)
	}
}

func TestGetBySlug(t *testing.T) {
	repo := newTestRepo(t, "acme")
	ctx := context.Background()

	p := &Post{Slug: "hello-world", Title: "Hello"}
	if err := repo.Create(ctx, p); err != nil {
		t.Fatalf("create: %v", err)
	}

	got, err := repo.GetBySlug(ctx, "hello-world")
	if err != nil {
		t.Fatalf("getbyslug: %v", err)
	}
	if got.ID != p.ID {
		t.Fatalf("got.ID = %s, want %s", got.ID, p.ID)
	}
}

func TestGetNotFound(t *testing.T) {
	repo := newTestRepo(t, "acme")
	_, err := repo.Get(context.Background(), "missing-id")
	if !errors.Is(err, storagej.ErrNotFound) {
		t.Fatalf("err = %v, want storagej.ErrNotFound", err)
	}
}

func TestGetBySlugNotFound(t *testing.T) {
	repo := newTestRepo(t, "acme")
	_, err := repo.GetBySlug(context.Background(), "missing-slug")
	if !errors.Is(err, storagej.ErrNotFound) {
		t.Fatalf("err = %v, want storagej.ErrNotFound", err)
	}
}

func TestPostCreatedAtDerivedFromID(t *testing.T) {
	repo := newTestRepo(t, "acme")
	ctx := context.Background()

	before := time.Now().Add(-time.Second)
	p := &Post{Slug: "hello", Title: "T"}
	if err := repo.Create(ctx, p); err != nil {
		t.Fatalf("create: %v", err)
	}
	after := time.Now().Add(time.Second)

	created, err := p.CreatedAt()
	if err != nil {
		t.Fatalf("createdat: %v", err)
	}
	if created.Before(before) || created.After(after) {
		t.Fatalf("createdAt = %v, want between %v and %v", created, before, after)
	}
}

// -----------------------------------------------------------------------------
// Update
// -----------------------------------------------------------------------------

func TestUpdateChangesFields(t *testing.T) {
	repo := newTestRepo(t, "acme")
	ctx := context.Background()

	p := &Post{Slug: "hello", Title: "Original", Body: "v1"}
	if err := repo.Create(ctx, p); err != nil {
		t.Fatalf("create: %v", err)
	}

	p.Title = "Updated"
	p.Body = "v2"
	if err := repo.Update(ctx, p); err != nil {
		t.Fatalf("update: %v", err)
	}

	got, err := repo.Get(ctx, p.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Title != "Updated" || got.Body != "v2" {
		t.Fatalf("got = %+v, want Title=Updated Body=v2", got)
	}
}

func TestUpdateRequiresID(t *testing.T) {
	repo := newTestRepo(t, "acme")
	err := repo.Update(context.Background(), &Post{Slug: "hello", Title: "T"})
	if !errors.Is(err, ErrIDRequired) {
		t.Fatalf("err = %v, want ErrIDRequired", err)
	}
}

func TestUpdateReindexesSlug(t *testing.T) {
	repo := newTestRepo(t, "acme")
	ctx := context.Background()

	p := &Post{Slug: "old-slug", Title: "T"}
	if err := repo.Create(ctx, p); err != nil {
		t.Fatalf("create: %v", err)
	}

	p.Slug = "new-slug"
	if err := repo.Update(ctx, p); err != nil {
		t.Fatalf("update: %v", err)
	}

	if _, err := repo.GetBySlug(ctx, "old-slug"); !errors.Is(err, storagej.ErrNotFound) {
		t.Fatalf("old slug lookup err = %v, want ErrNotFound", err)
	}
	got, err := repo.GetBySlug(ctx, "new-slug")
	if err != nil {
		t.Fatalf("get by new slug: %v", err)
	}
	if got.ID != p.ID {
		t.Fatalf("got.ID = %s, want %s", got.ID, p.ID)
	}
}

func TestUpdateRejectsDuplicateSlug(t *testing.T) {
	repo := newTestRepo(t, "acme")
	ctx := context.Background()

	a := &Post{Slug: "slug-a", Title: "A"}
	b := &Post{Slug: "slug-b", Title: "B"}
	if err := repo.Create(ctx, a); err != nil {
		t.Fatalf("create a: %v", err)
	}
	if err := repo.Create(ctx, b); err != nil {
		t.Fatalf("create b: %v", err)
	}

	b.Slug = "slug-a"
	err := repo.Update(ctx, b)
	if !errors.Is(err, ErrSlugTaken) {
		t.Fatalf("err = %v, want ErrSlugTaken", err)
	}

	// b must be unaffected by the rejected update.
	got, err := repo.Get(ctx, b.ID)
	if err != nil {
		t.Fatalf("get b: %v", err)
	}
	if got.Slug != "slug-b" {
		t.Fatalf("b.Slug = %q, want unchanged %q", got.Slug, "slug-b")
	}
}

func TestUpdateSameSlugSucceeds(t *testing.T) {
	repo := newTestRepo(t, "acme")
	ctx := context.Background()

	p := &Post{Slug: "hello", Title: "Original"}
	if err := repo.Create(ctx, p); err != nil {
		t.Fatalf("create: %v", err)
	}

	p.Title = "Updated"
	if err := repo.Update(ctx, p); err != nil {
		t.Fatalf("update with unchanged slug: %v", err)
	}
	got, err := repo.GetBySlug(ctx, "hello")
	if err != nil {
		t.Fatalf("getbyslug: %v", err)
	}
	if got.Title != "Updated" {
		t.Fatalf("got.Title = %q, want %q", got.Title, "Updated")
	}
}

// -----------------------------------------------------------------------------
// Delete
// -----------------------------------------------------------------------------

func TestDeleteRemovesDataAndSlugIndex(t *testing.T) {
	repo := newTestRepo(t, "acme")
	ctx := context.Background()

	p := &Post{Slug: "hello", Title: "T"}
	if err := repo.Create(ctx, p); err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := repo.Delete(ctx, p.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}

	if _, err := repo.Get(ctx, p.ID); !errors.Is(err, storagej.ErrNotFound) {
		t.Fatalf("get after delete err = %v, want ErrNotFound", err)
	}
	if _, err := repo.GetBySlug(ctx, "hello"); !errors.Is(err, storagej.ErrNotFound) {
		t.Fatalf("getbyslug after delete err = %v, want ErrNotFound", err)
	}
}

func TestDeleteIsIdempotent(t *testing.T) {
	repo := newTestRepo(t, "acme")
	if err := repo.Delete(context.Background(), "never-existed"); err != nil {
		t.Fatalf("delete missing post should not error, got %v", err)
	}
}

// -----------------------------------------------------------------------------
// List
// -----------------------------------------------------------------------------

func TestListOrdersChronologically(t *testing.T) {
	repo := newTestRepo(t, "acme")
	ctx := context.Background()

	var ids []string
	for i := 0; i < 5; i++ {
		p := &Post{Slug: fmt.Sprintf("post-%d", i), Title: fmt.Sprintf("Post %d", i)}
		if err := repo.Create(ctx, p); err != nil {
			t.Fatalf("create %d: %v", i, err)
		}
		ids = append(ids, p.ID)
		// keyspace.NewID only guarantees ordering to millisecond
		// resolution: two ids minted within the same millisecond have a
		// random (not creation-order) suffix. Force distinct milliseconds
		// so this test's order assertion is deterministic rather than
		// flaky under fast test execution.
		time.Sleep(2 * time.Millisecond)
	}

	posts, _, hasMore, err := repo.List(ctx, "", 10)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if hasMore {
		t.Fatal("hasMore = true, want false")
	}
	if len(posts) != 5 {
		t.Fatalf("len(posts) = %d, want 5", len(posts))
	}
	for i, p := range posts {
		if p.ID != ids[i] {
			t.Fatalf("posts[%d].ID = %s, want %s (wrong order)", i, p.ID, ids[i])
		}
	}
}

func TestListPagination(t *testing.T) {
	repo := newTestRepo(t, "acme")
	ctx := context.Background()

	for i := 0; i < 5; i++ {
		p := &Post{Slug: fmt.Sprintf("post-%d", i), Title: fmt.Sprintf("Post %d", i)}
		if err := repo.Create(ctx, p); err != nil {
			t.Fatalf("create %d: %v", i, err)
		}
	}

	page1, cursor1, hasMore1, err := repo.List(ctx, "", 2)
	if err != nil {
		t.Fatalf("list page1: %v", err)
	}
	if !hasMore1 || len(page1) != 2 {
		t.Fatalf("page1 = %d posts, hasMore = %v, want 2 posts and hasMore = true", len(page1), hasMore1)
	}

	page2, cursor2, hasMore2, err := repo.List(ctx, cursor1, 2)
	if err != nil {
		t.Fatalf("list page2: %v", err)
	}
	if !hasMore2 || len(page2) != 2 {
		t.Fatalf("page2 = %d posts, hasMore = %v, want 2 posts and hasMore = true", len(page2), hasMore2)
	}

	page3, _, hasMore3, err := repo.List(ctx, cursor2, 2)
	if err != nil {
		t.Fatalf("list page3: %v", err)
	}
	if hasMore3 || len(page3) != 1 {
		t.Fatalf("page3 = %d posts, hasMore = %v, want 1 post and hasMore = false", len(page3), hasMore3)
	}

	all := make([]*Post, 0, 5)
	all = append(all, page1...)
	all = append(all, page2...)
	all = append(all, page3...)

	seen := make(map[string]bool, 5)
	for _, p := range all {
		if seen[p.ID] {
			t.Fatalf("id %s appeared twice across pages", p.ID)
		}
		seen[p.ID] = true
	}
	if len(seen) != 5 {
		t.Fatalf("total unique posts across pages = %d, want 5", len(seen))
	}
}

func TestListZeroLimitReturnsEmpty(t *testing.T) {
	repo := newTestRepo(t, "acme")
	posts, cursor, hasMore, err := repo.List(context.Background(), "", 0)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if posts != nil || cursor != "" || hasMore {
		t.Fatalf("list with limit=0 = (%v, %q, %v), want (nil, \"\", false)", posts, cursor, hasMore)
	}
}

// -----------------------------------------------------------------------------
// Tenant isolation
// -----------------------------------------------------------------------------

func TestTenantIsolation(t *testing.T) {
	db, err := pebble.Open(t.TempDir(), storagej.DefaultOptions())
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	acme := NewPostRepository(db, "acme")
	other := NewPostRepository(db, "other")
	ctx := context.Background()

	if err := acme.Create(ctx, &Post{Slug: "hello", Title: "Acme"}); err != nil {
		t.Fatalf("create acme: %v", err)
	}
	// Same slug, different tenant: must NOT collide with acme's index entry.
	if err := other.Create(ctx, &Post{Slug: "hello", Title: "Other"}); err != nil {
		t.Fatalf("create other: %v", err)
	}

	posts, _, _, err := acme.List(ctx, "", 10)
	if err != nil {
		t.Fatalf("list acme: %v", err)
	}
	if len(posts) != 1 || posts[0].Title != "Acme" {
		t.Fatalf("acme posts = %+v, want just the Acme post", posts)
	}
}
