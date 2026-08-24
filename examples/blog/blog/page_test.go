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

func newTestPageRepo(t *testing.T, tenant string) *PageRepository {
	t.Helper()
	db, err := pebble.Open(t.TempDir(), storagej.DefaultOptions())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return NewPageRepository(db, tenant)
}

// -----------------------------------------------------------------------------
// Create / Get / GetBySlug
// -----------------------------------------------------------------------------

func TestPageCreateAndGet(t *testing.T) {
	repo := newTestPageRepo(t, "acme")
	ctx := context.Background()

	p := &Page{Slug: "about", Title: "About", Body: "Who we are"}
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
	if got.Title != "About" || got.Body != "Who we are" || got.Slug != "about" {
		t.Fatalf("got = %+v, want matching Create input", got)
	}
}

func TestPageCreateRequiresSlug(t *testing.T) {
	repo := newTestPageRepo(t, "acme")
	err := repo.Create(context.Background(), &Page{Title: "No slug"})
	if !errors.Is(err, ErrSlugRequired) {
		t.Fatalf("err = %v, want ErrSlugRequired", err)
	}
}

func TestPageCreateRejectsDuplicateSlug(t *testing.T) {
	repo := newTestPageRepo(t, "acme")
	ctx := context.Background()

	if err := repo.Create(ctx, &Page{Slug: "about", Title: "A"}); err != nil {
		t.Fatalf("first create: %v", err)
	}
	err := repo.Create(ctx, &Page{Slug: "about", Title: "B"})
	if !errors.Is(err, ErrSlugTaken) {
		t.Fatalf("err = %v, want ErrSlugTaken", err)
	}
}

func TestPageGetBySlug(t *testing.T) {
	repo := newTestPageRepo(t, "acme")
	ctx := context.Background()

	p := &Page{Slug: "contact", Title: "Contact"}
	if err := repo.Create(ctx, p); err != nil {
		t.Fatalf("create: %v", err)
	}

	got, err := repo.GetBySlug(ctx, "contact")
	if err != nil {
		t.Fatalf("getbyslug: %v", err)
	}
	if got.ID != p.ID {
		t.Fatalf("got.ID = %s, want %s", got.ID, p.ID)
	}
}

func TestPageGetNotFound(t *testing.T) {
	repo := newTestPageRepo(t, "acme")
	_, err := repo.Get(context.Background(), "missing-id")
	if !errors.Is(err, storagej.ErrNotFound) {
		t.Fatalf("err = %v, want storagej.ErrNotFound", err)
	}
}

func TestPageCreatedAtDerivedFromID(t *testing.T) {
	repo := newTestPageRepo(t, "acme")
	ctx := context.Background()

	before := time.Now().Add(-time.Second)
	p := &Page{Slug: "about", Title: "T"}
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

func TestPageUpdateChangesFields(t *testing.T) {
	repo := newTestPageRepo(t, "acme")
	ctx := context.Background()

	p := &Page{Slug: "about", Title: "Original", Body: "v1"}
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

func TestPageUpdateReindexesSlug(t *testing.T) {
	repo := newTestPageRepo(t, "acme")
	ctx := context.Background()

	p := &Page{Slug: "old-slug", Title: "T"}
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

func TestPageUpdateRejectsDuplicateSlug(t *testing.T) {
	repo := newTestPageRepo(t, "acme")
	ctx := context.Background()

	a := &Page{Slug: "slug-a", Title: "A"}
	b := &Page{Slug: "slug-b", Title: "B"}
	if err := repo.Create(ctx, a); err != nil {
		t.Fatalf("create a: %v", err)
	}
	if err := repo.Create(ctx, b); err != nil {
		t.Fatalf("create b: %v", err)
	}

	b.Slug = "slug-a"
	if err := repo.Update(ctx, b); !errors.Is(err, ErrSlugTaken) {
		t.Fatalf("err = %v, want ErrSlugTaken", err)
	}
}

// -----------------------------------------------------------------------------
// Delete
// -----------------------------------------------------------------------------

func TestPageDeleteRemovesDataAndSlugIndex(t *testing.T) {
	repo := newTestPageRepo(t, "acme")
	ctx := context.Background()

	p := &Page{Slug: "about", Title: "T"}
	if err := repo.Create(ctx, p); err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := repo.Delete(ctx, p.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}

	if _, err := repo.Get(ctx, p.ID); !errors.Is(err, storagej.ErrNotFound) {
		t.Fatalf("get after delete err = %v, want ErrNotFound", err)
	}
	if _, err := repo.GetBySlug(ctx, "about"); !errors.Is(err, storagej.ErrNotFound) {
		t.Fatalf("getbyslug after delete err = %v, want ErrNotFound", err)
	}
}

func TestPageDeleteIsIdempotent(t *testing.T) {
	repo := newTestPageRepo(t, "acme")
	if err := repo.Delete(context.Background(), "never-existed"); err != nil {
		t.Fatalf("delete missing page should not error, got %v", err)
	}
}

// -----------------------------------------------------------------------------
// List
// -----------------------------------------------------------------------------

func TestPageListOrdersChronologically(t *testing.T) {
	repo := newTestPageRepo(t, "acme")
	ctx := context.Background()

	var ids []string
	for i := 0; i < 5; i++ {
		p := &Page{Slug: fmt.Sprintf("page-%d", i), Title: fmt.Sprintf("Page %d", i)}
		if err := repo.Create(ctx, p); err != nil {
			t.Fatalf("create %d: %v", i, err)
		}
		ids = append(ids, p.ID)
		// See the identical comment in post_test.go's
		// TestListOrdersChronologically: same-millisecond ids have a
		// random, not creation-order, suffix.
		time.Sleep(2 * time.Millisecond)
	}

	pages, _, hasMore, err := repo.List(ctx, "", 10)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if hasMore {
		t.Fatal("hasMore = true, want false")
	}
	if len(pages) != 5 {
		t.Fatalf("len(pages) = %d, want 5", len(pages))
	}
	for i, p := range pages {
		if p.ID != ids[i] {
			t.Fatalf("pages[%d].ID = %s, want %s (wrong order)", i, p.ID, ids[i])
		}
	}
}

// -----------------------------------------------------------------------------
// Post/Page independence
// -----------------------------------------------------------------------------

// TestPostAndPageDoNotCollide guards the reason Post and Page can share one
// tenant safely: repository.Core folds the entity type into the key
// (.../d/post/... vs .../d/page/...), so same-id or same-slug values in one
// don't affect the other.
func TestPostAndPageDoNotCollide(t *testing.T) {
	db, err := pebble.Open(t.TempDir(), storagej.DefaultOptions())
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	posts := NewPostRepository(db, "acme")
	pages := NewPageRepository(db, "acme")
	ctx := context.Background()

	post := &Post{ID: "shared-id", Slug: "shared-slug", Title: "A Post"}
	if err := posts.Create(ctx, post); err != nil {
		t.Fatalf("create post: %v", err)
	}
	page := &Page{ID: "shared-id", Slug: "shared-slug", Title: "A Page"}
	if err := pages.Create(ctx, page); err != nil {
		t.Fatalf("create page with same id+slug as post: %v", err)
	}

	gotPost, err := posts.Get(ctx, "shared-id")
	if err != nil {
		t.Fatalf("get post: %v", err)
	}
	gotPage, err := pages.Get(ctx, "shared-id")
	if err != nil {
		t.Fatalf("get page: %v", err)
	}
	if gotPost.Title != "A Post" || gotPage.Title != "A Page" {
		t.Fatalf("post/page data collided: post=%+v page=%+v", gotPost, gotPage)
	}
}
