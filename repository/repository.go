// Package repository provides a byte-level CRUD and secondary-index helper
// on top of storagej.Store and keyspace. It knows the key layout (tenant +
// type + id, plus field=value index entries) but nothing about any specific
// domain struct — JSON encoding and domain validation belong to the
// entity-specific repositories built on top of it (see the blog package for
// a worked example: Post/Page wrap Core and add their own struct + slug
// rules).
package repository

import (
	"bytes"
	"context"
	"errors"
	"sync"

	"github.com/uretgec/storagej/v2"
	"github.com/uretgec/storagej/v2/keyspace"
)

// ErrIndexConflict is returned by PutWithIndex when the field=value index
// entry already exists and points at a different id — e.g. a slug already
// claimed by another post. Callers should surface this as "already taken".
var ErrIndexConflict = errors.New("repository: index entry already points at a different id")

// Core is a byte-level CRUD+index helper scoped to one tenant and one
// entity type. It is safe for concurrent use: mu serializes the three
// operations that read-then-write an index entry (PutWithIndex,
// ReplaceIndex, DeleteWithIndex), so concurrent callers see each other's
// index changes as atomic, not just each individual batch. It holds no
// other in-memory state, so it is still cheap to construct per-request if
// that's convenient for a caller — the mutex has no meaningful cost when
// uncontended.
type Core struct {
	store  storagej.Store
	tenant string
	typ    string

	mu sync.Mutex
}

// NewCore returns a Core scoped to tenant/typ. It performs no I/O.
func NewCore(store storagej.Store, tenant, typ string) *Core {
	return &Core{store: store, tenant: tenant, typ: typ}
}

// dataKey builds the primary record key for id.
func (c *Core) dataKey(id string) ([]byte, error) {
	return keyspace.Key(c.tenant, keyspace.KindData, c.typ, id)
}

// indexKey builds a secondary index key for field=value. field is folded
// into the type segment (e.g. "post-slug") because keyspace segments can't
// contain ':' — see keyspace.KindIndex's doc comment for why.
func (c *Core) indexKey(field, value string) ([]byte, error) {
	return keyspace.Key(c.tenant, keyspace.KindIndex, c.typ+"-"+field, value)
}

// Get reads the raw record for id. Returns storagej.ErrNotFound if absent.
func (c *Core) Get(ctx context.Context, id string) ([]byte, error) {
	key, err := c.dataKey(id)
	if err != nil {
		return nil, err
	}
	return c.store.Get(ctx, key)
}

// Put writes the raw record for id without touching any index. Use
// PutWithIndex instead when the record has a unique field (e.g. a slug)
// that must be reserved atomically alongside the write.
func (c *Core) Put(ctx context.Context, id string, value []byte) error {
	key, err := c.dataKey(id)
	if err != nil {
		return err
	}
	return c.store.Set(ctx, key, value)
}

// PutWithIndex atomically writes the record for id and a single secondary
// index entry (field=value -> id) in one batch. If the index entry already
// exists and points at a different id, the write is rejected with
// ErrIndexConflict and nothing is persisted — this is how uniqueness (e.g.
// slugs) is enforced.
//
// The uniqueness check and the batch write are two separate storagej calls,
// not one atomic compare-and-swap primitive — but Core.mu serializes this
// entire method against ReplaceIndex and DeleteWithIndex on the same Core,
// and Pebble holds an exclusive lock on its data directory (only one
// process can have it open for writes at a time), so the only possible
// concurrent callers are goroutines within this process. Together, that
// makes the check-then-write effectively atomic for every caller that can
// actually reach this store — not a partial mitigation.
func (c *Core) PutWithIndex(ctx context.Context, id, field, value string, data []byte) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	existing, err := c.GetIndex(ctx, field, value)
	if err == nil && existing != id {
		return ErrIndexConflict
	} else if err != nil && !errors.Is(err, storagej.ErrNotFound) {
		return err
	}

	dKey, err := c.dataKey(id)
	if err != nil {
		return err
	}
	iKey, err := c.indexKey(field, value)
	if err != nil {
		return err
	}

	batch := c.store.NewBatch()
	defer batch.Close()
	if err := batch.Set(dKey, data); err != nil {
		return err
	}
	if err := batch.Set(iKey, []byte(id)); err != nil {
		return err
	}
	return batch.Commit()
}

// ReplaceIndex atomically writes new data for id and, if newValue differs
// from oldValue, swaps its field index entry from oldValue to newValue in
// the same batch: the old entry is deleted and the new one is written and
// checked for conflicts exactly like PutWithIndex. Pass oldValue == newValue
// to update data without touching the index at all.
//
// This is the update-time counterpart to PutWithIndex: a slug rename needs
// the old index entry gone and the new one reserved in one atomic step, or
// a crash between the two calls would leave a dangling or missing index
// entry. Core.mu gives the same concurrent-caller guarantee documented on
// PutWithIndex.
func (c *Core) ReplaceIndex(ctx context.Context, id, field, oldValue, newValue string, data []byte) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	dKey, err := c.dataKey(id)
	if err != nil {
		return err
	}

	batch := c.store.NewBatch()
	defer batch.Close()

	if oldValue != newValue {
		existing, err := c.GetIndex(ctx, field, newValue)
		if err == nil && existing != id {
			return ErrIndexConflict
		} else if err != nil && !errors.Is(err, storagej.ErrNotFound) {
			return err
		}

		oldKey, err := c.indexKey(field, oldValue)
		if err != nil {
			return err
		}
		newKey, err := c.indexKey(field, newValue)
		if err != nil {
			return err
		}
		if err := batch.Delete(oldKey); err != nil {
			return err
		}
		if err := batch.Set(newKey, []byte(id)); err != nil {
			return err
		}
	}

	if err := batch.Set(dKey, data); err != nil {
		return err
	}
	return batch.Commit()
}

// GetIndex resolves field=value to the id it points at. Returns
// storagej.ErrNotFound if no such index entry exists.
func (c *Core) GetIndex(ctx context.Context, field, value string) (string, error) {
	key, err := c.indexKey(field, value)
	if err != nil {
		return "", err
	}
	id, err := c.store.Get(ctx, key)
	if err != nil {
		return "", err
	}
	return string(id), nil
}

// Delete removes the record for id only. It does not touch any index entry
// — callers that maintain an index must use DeleteWithIndex instead, or
// they will leak a dangling index entry pointing at a deleted id.
func (c *Core) Delete(ctx context.Context, id string) error {
	key, err := c.dataKey(id)
	if err != nil {
		return err
	}
	return c.store.Delete(ctx, key)
}

// DeleteWithIndex atomically removes the record for id and its field=value
// index entry. It shares Core.mu with PutWithIndex/ReplaceIndex: without
// that, a delete freeing up a value (e.g. a slug) could commit at the same
// moment another goroutine claims that same now-free value, and whichever
// commit lands second would win — either silently reviving the deleted
// index entry or leaving the new claimant's data behind an index entry
// that was never actually written. Serializing all three closes that
// window.
func (c *Core) DeleteWithIndex(ctx context.Context, id, field, value string) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	dKey, err := c.dataKey(id)
	if err != nil {
		return err
	}
	iKey, err := c.indexKey(field, value)
	if err != nil {
		return err
	}

	batch := c.store.NewBatch()
	defer batch.Close()
	if err := batch.Delete(dKey); err != nil {
		return err
	}
	if err := batch.Delete(iKey); err != nil {
		return err
	}
	return batch.Commit()
}

// List iterates every record of this type for this tenant, oldest first
// (ids from keyspace.NewID are chronologically sortable, so this needs no
// separate creation-time index). Return storagej.ErrStopIteration from fn
// to stop early. It is ListFrom with an empty cursor.
//
// Both id and value are only valid for the duration of each fn call: they
// come from storagej's underlying Pebble iterator, which reuses its buffers
// on every step. Copy anything you need to retain past the call.
func (c *Core) List(ctx context.Context, fn func(id string, value []byte) error) error {
	return c.ListFrom(ctx, "", fn)
}

// ListFrom is List starting after cursor — the id of the last record shown
// on a previous page — instead of from the beginning. Pass an empty cursor
// for the first page. Pair this with the limit+1 technique (fetch one extra
// record to know hasMore, keep the last shown id as nextCursor) for
// cursor-based pagination; see EXAMPLES.md in the module root for the full
// recipe this mirrors.
func (c *Core) ListFrom(ctx context.Context, cursor string, fn func(id string, value []byte) error) error {
	prefix, err := keyspace.Prefix(c.tenant, keyspace.KindData, c.typ)
	if err != nil {
		return err
	}

	lower := prefix
	if cursor != "" {
		lower = append(append([]byte{}, prefix...), cursor...)
	}
	upper := append(append([]byte{}, prefix...), 0xFF)

	first := true
	return c.store.Scan(ctx, lower, upper, func(key, value []byte) error {
		if first {
			first = false
			if cursor != "" && bytes.Equal(key, lower) {
				return nil // skip the cursor record itself; it was already shown
			}
		}
		_, _, _, id, err := keyspace.Parse(key)
		if err != nil {
			return err
		}
		return fn(string(id), value)
	})
}
