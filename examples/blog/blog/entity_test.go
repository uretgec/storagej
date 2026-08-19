package blog

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/uretgec/storagej"
	"github.com/uretgec/storagej/pebble"
)

// testWidget is a throwaway third entity type, used only to prove
// Repository[T, PT] generalizes beyond Post/Page — exactly the claim made
// in post.go's package doc comment. It is test-only, not part of the
// public API.
type testWidget struct {
	ID        string    `json:"id"`
	Slug      string    `json:"slug"`
	Note      string    `json:"note"`
	UpdatedAt time.Time `json:"updated_at"`
}

var _ Entity = (*testWidget)(nil)

func (w *testWidget) GetID() string            { return w.ID }
func (w *testWidget) SetID(id string)          { w.ID = id }
func (w *testWidget) GetSlug() string          { return w.Slug }
func (w *testWidget) SetUpdatedAt(t time.Time) { w.UpdatedAt = t }

// TestRepositoryGeneralizesToAThirdEntityType exercises Repository[T, PT]
// with a type that has never seen a single line of Post/Page-specific code:
// the struct + four one-line methods above are the entire cost of adding
// this entity.
func TestRepositoryGeneralizesToAThirdEntityType(t *testing.T) {
	db, err := pebble.Open(t.TempDir(), storagej.DefaultOptions())
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	widgets := NewRepository[testWidget, *testWidget](db, "acme", "widget")
	ctx := context.Background()

	w := &testWidget{Slug: "gadget", Note: "a widget"}
	if err := widgets.Create(ctx, w); err != nil {
		t.Fatalf("create: %v", err)
	}
	if w.ID == "" {
		t.Fatal("create did not assign an id")
	}

	got, err := widgets.GetBySlug(ctx, "gadget")
	if err != nil {
		t.Fatalf("getbyslug: %v", err)
	}
	if got.Note != "a widget" {
		t.Fatalf("got.Note = %q, want %q", got.Note, "a widget")
	}

	created, err := CreatedAt(got)
	if err != nil {
		t.Fatalf("createdat: %v", err)
	}
	if created.IsZero() {
		t.Fatal("CreatedAt returned zero time")
	}

	w.Note = "an updated widget"
	if err := widgets.Update(ctx, w); err != nil {
		t.Fatalf("update: %v", err)
	}
	got, err = widgets.Get(ctx, w.ID)
	if err != nil {
		t.Fatalf("get after update: %v", err)
	}
	if got.Note != "an updated widget" {
		t.Fatalf("got.Note = %q, want %q", got.Note, "an updated widget")
	}

	if err := widgets.Delete(ctx, w.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := widgets.Get(ctx, w.ID); !errors.Is(err, storagej.ErrNotFound) {
		t.Fatalf("get after delete err = %v, want ErrNotFound", err)
	}
}

// TestWidgetsDoNotCollideWithPostsOrPages is the three-entity version of
// TestPostAndPageDoNotCollide: shared id and slug values across three
// different types under the same tenant must stay fully isolated, purely
// because repository.Core folds typ into the key.
func TestWidgetsDoNotCollideWithPostsOrPages(t *testing.T) {
	db, err := pebble.Open(t.TempDir(), storagej.DefaultOptions())
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	posts := NewPostRepository(db, "acme")
	pages := NewPageRepository(db, "acme")
	widgets := NewRepository[testWidget, *testWidget](db, "acme", "widget")
	ctx := context.Background()

	if err := posts.Create(ctx, &Post{ID: "shared", Slug: "shared", Title: "P"}); err != nil {
		t.Fatalf("create post: %v", err)
	}
	if err := pages.Create(ctx, &Page{ID: "shared", Slug: "shared", Title: "G"}); err != nil {
		t.Fatalf("create page: %v", err)
	}
	if err := widgets.Create(ctx, &testWidget{ID: "shared", Slug: "shared", Note: "W"}); err != nil {
		t.Fatalf("create widget: %v", err)
	}

	post, err := posts.Get(ctx, "shared")
	if err != nil {
		t.Fatalf("get post: %v", err)
	}
	page, err := pages.Get(ctx, "shared")
	if err != nil {
		t.Fatalf("get page: %v", err)
	}
	widget, err := widgets.Get(ctx, "shared")
	if err != nil {
		t.Fatalf("get widget: %v", err)
	}

	if post.Title != "P" || page.Title != "G" || widget.Note != "W" {
		t.Fatalf("data collided across types: post=%+v page=%+v widget=%+v", post, page, widget)
	}
}
