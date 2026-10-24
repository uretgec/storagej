package blog

import (
	"time"

	"github.com/uretgec/storagej/v2"
)

// PageType is the entity-type segment Page records are stored under. See
// PostType's doc comment for why this is exported.
const PageType = "page"

// Page is a single static blog page (e.g. "About", "Contact") — same shape
// as Post, deliberately: this is the second entity built on
// Repository[T, PT], added at the point where that repetition was real.
type Page struct {
	ID        string    `json:"id"`
	Slug      string    `json:"slug"`
	Title     string    `json:"title"`
	Body      string    `json:"body"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Compile-time check that *Page satisfies Entity.
var _ Entity = (*Page)(nil)

func (p *Page) GetID() string            { return p.ID }
func (p *Page) SetID(id string)          { p.ID = id }
func (p *Page) GetSlug() string          { return p.Slug }
func (p *Page) SetUpdatedAt(t time.Time) { p.UpdatedAt = t }

// CreatedAt derives the page's creation time from its id — see the
// package-level CreatedAt for the shared logic.
func (p *Page) CreatedAt() (time.Time, error) {
	return CreatedAt(p)
}

// PageRepository stores Pages for one tenant.
type PageRepository = Repository[Page, *Page]

// NewPageRepository returns a PageRepository scoped to tenant.
func NewPageRepository(store storagej.Store, tenant string) *PageRepository {
	return NewRepository[Page, *Page](store, tenant, PageType)
}
