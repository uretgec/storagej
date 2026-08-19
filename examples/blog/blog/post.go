// Package blog holds the domain entities of the blog system — Post and
// Page — built on top of repository.Core via the generic Repository[T, PT]
// in entity.go. Core carries the byte-level key/index mechanics;
// Repository[T, PT] carries the CRUD/slug-index/pagination logic shared by
// every entity; this file (and page.go) carries only what's actually
// specific to Post: its fields and its Entity method implementations.
//
// This is deliberately the second version of this package. The first had
// Post and Page as two near-identical, fully concrete files — the right
// call while Post was the only proven use of repository.Core. Once Page
// repeated that file almost mechanically, the duplication was real instead
// of speculative, which is what justified extracting Repository[T, PT].
// A third entity now costs a struct, four one-line methods, and a
// constructor — see NewPageRepository below for the actual size of that.
package blog

import (
	"time"

	"github.com/uretgec/storagej"
)

// PostType is the entity-type segment Post records are stored under
// ({tenant}:d:post:{id}). Exported so callers outside this package (e.g.
// httpapi.MountMultiTenant, which builds Repository[T,PT] itself instead of
// going through NewPostRepository) can reuse it instead of repeating the
// literal "post".
const PostType = "post"

// Post is a single blog post. Tenant is deliberately not a field here — it
// is a key-space concern, not post data, and is supplied per repository
// instance instead (see NewPostRepository).
type Post struct {
	ID        string    `json:"id"`
	Slug      string    `json:"slug"`
	Title     string    `json:"title"`
	Body      string    `json:"body"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Compile-time check that *Post satisfies Entity.
var _ Entity = (*Post)(nil)

func (p *Post) GetID() string            { return p.ID }
func (p *Post) SetID(id string)          { p.ID = id }
func (p *Post) GetSlug() string          { return p.Slug }
func (p *Post) SetUpdatedAt(t time.Time) { p.UpdatedAt = t }

// CreatedAt derives the post's creation time from its id — see the
// package-level CreatedAt for the shared logic.
func (p *Post) CreatedAt() (time.Time, error) {
	return CreatedAt(p)
}

// PostRepository stores Posts for one tenant.
type PostRepository = Repository[Post, *Post]

// NewPostRepository returns a PostRepository scoped to tenant. Construct one
// per tenant/request as convenient — it holds no state beyond the scope.
func NewPostRepository(store storagej.Store, tenant string) *PostRepository {
	return NewRepository[Post, *Post](store, tenant, PostType)
}
