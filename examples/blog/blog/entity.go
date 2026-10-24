package blog

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/uretgec/storagej/v2"
	"github.com/uretgec/storagej/v2/keyspace"
	"github.com/uretgec/storagej/v2/repository"
)

// slugField is the field name PutWithIndex/ReplaceIndex use for the
// slug secondary index, shared by every Entity type.
const slugField = "slug"

var (
	// ErrSlugRequired is returned when creating or updating an entity
	// without a slug: the slug is the secondary-index key, so an empty one
	// can't be indexed.
	ErrSlugRequired = errors.New("blog: slug is required")
	// ErrIDRequired is returned when Get/Update/Delete is called with an
	// empty id.
	ErrIDRequired = errors.New("blog: id is required")
	// ErrSlugTaken is returned by Create/Update when the slug already
	// belongs to a different record of the same type in the same tenant.
	ErrSlugTaken = errors.New("blog: slug is already taken")
)

// Entity is implemented by *T for any type T that Repository[T, PT] can
// store: just enough surface (id, slug, update timestamp) for Repository to
// do its job without knowing anything else about T. Post and Page implement
// it; a third entity type would too, and would need no new Repository code
// — see post.go and page.go for how little is left type-specific.
type Entity interface {
	GetID() string
	SetID(string)
	GetSlug() string
	SetUpdatedAt(time.Time)
}

// CreatedAt derives an entity's creation time from its id. Every id comes
// from keyspace.NewID, which embeds a millisecond timestamp, so no entity
// needs its own stored created_at field or index — see keyspace.Time. Works
// for any Entity, e.g. CreatedAt(post) or CreatedAt(page).
func CreatedAt(e Entity) (time.Time, error) {
	return keyspace.Time(e.GetID())
}

// Repository stores entities of type T for one tenant, on one
// repository.Core. PT ties T to its pointer type's Entity implementation —
// callers never write PT explicitly; see NewPostRepository/NewPageRepository
// for the two-line pattern a new entity type follows.
type Repository[T any, PT interface {
	*T
	Entity
}] struct {
	core *repository.Core
}

// NewRepository returns a Repository[T, PT] scoped to tenant, storing
// records under key type typ. Construct one per tenant/request as
// convenient — it holds no state beyond the scope.
func NewRepository[T any, PT interface {
	*T
	Entity
}](store storagej.Store, tenant, typ string) *Repository[T, PT] {
	return &Repository[T, PT]{core: repository.NewCore(store, tenant, typ)}
}

// Get returns the record with the given id, or storagej.ErrNotFound.
func (r *Repository[T, PT]) Get(ctx context.Context, id string) (*T, error) {
	if id == "" {
		return nil, ErrIDRequired
	}
	val, err := r.core.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	var v T
	if err := json.Unmarshal(val, &v); err != nil {
		return nil, err
	}
	return &v, nil
}

// GetBySlug returns the record with the given slug, or storagej.ErrNotFound.
func (r *Repository[T, PT]) GetBySlug(ctx context.Context, slug string) (*T, error) {
	if slug == "" {
		return nil, ErrSlugRequired
	}
	id, err := r.core.GetIndex(ctx, slugField, slug)
	if err != nil {
		return nil, err
	}
	return r.Get(ctx, id)
}

// Create writes a new record. If it has no id, a new sortable id is
// generated. Its UpdatedAt is always set to the current time; any value the
// caller set is overwritten.
//
// Create assumes the id (whether generated or explicitly supplied) has
// never been used before. Calling Create twice for the same id with a
// different slug will overwrite the data but leave the first call's slug
// index entry dangling — use Update for a record that may already exist.
func (r *Repository[T, PT]) Create(ctx context.Context, v *T) error {
	e := PT(v)
	if e.GetSlug() == "" {
		return ErrSlugRequired
	}
	if e.GetID() == "" {
		e.SetID(keyspace.NewID())
	}
	e.SetUpdatedAt(time.Now().UTC())

	val, err := json.Marshal(v)
	if err != nil {
		return err
	}
	if err := r.core.PutWithIndex(ctx, e.GetID(), slugField, e.GetSlug(), val); err != nil {
		if errors.Is(err, repository.ErrIndexConflict) {
			return ErrSlugTaken
		}
		return err
	}
	return nil
}

// Update writes changes to an existing record; its id must already exist.
// If its slug differs from the record's current slug, the slug index is
// atomically repointed (old slug freed, new slug reserved) in the same
// write as the data update. UpdatedAt is always set to the current time.
func (r *Repository[T, PT]) Update(ctx context.Context, v *T) error {
	e := PT(v)
	if e.GetID() == "" {
		return ErrIDRequired
	}
	if e.GetSlug() == "" {
		return ErrSlugRequired
	}

	existing, err := r.Get(ctx, e.GetID())
	if err != nil {
		return err
	}
	existingSlug := PT(existing).GetSlug()
	e.SetUpdatedAt(time.Now().UTC())

	val, err := json.Marshal(v)
	if err != nil {
		return err
	}
	if err := r.core.ReplaceIndex(ctx, e.GetID(), slugField, existingSlug, e.GetSlug(), val); err != nil {
		if errors.Is(err, repository.ErrIndexConflict) {
			return ErrSlugTaken
		}
		return err
	}
	return nil
}

// Delete removes a record and its slug index entry. It is idempotent:
// deleting a missing id is not an error, matching
// storagej.Store.Delete's contract.
func (r *Repository[T, PT]) Delete(ctx context.Context, id string) error {
	if id == "" {
		return ErrIDRequired
	}
	existing, err := r.Get(ctx, id)
	if err != nil {
		if errors.Is(err, storagej.ErrNotFound) {
			return nil
		}
		return err
	}
	slug := PT(existing).GetSlug()
	return r.core.DeleteWithIndex(ctx, id, slugField, slug)
}

// List returns up to limit records, oldest first, starting after cursor
// (the id of the last record from a previous page, or "" for the first
// page). hasMore reports whether another page exists; nextCursor is what to
// pass as cursor for that next page.
func (r *Repository[T, PT]) List(ctx context.Context, cursor string, limit int) (items []*T, nextCursor string, hasMore bool, err error) {
	if limit <= 0 {
		return nil, "", false, nil
	}

	fetch := limit + 1
	count := 0
	var lastID string

	scanErr := r.core.ListFrom(ctx, cursor, func(id string, value []byte) error {
		if count >= fetch {
			return storagej.ErrStopIteration
		}
		var v T
		if err := json.Unmarshal(value, &v); err != nil {
			return err
		}
		items = append(items, &v)
		count++
		if count == limit {
			lastID = PT(&v).GetID()
		}
		return nil
	})
	if scanErr != nil {
		return nil, "", false, scanErr
	}

	if count == fetch {
		hasMore = true
		nextCursor = lastID
		items = items[:limit]
	}
	return items, nextCursor, hasMore, nil
}
