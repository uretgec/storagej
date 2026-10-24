package pebble

import (
	"context"
	"errors"
	"fmt"

	"github.com/cockroachdb/pebble/v2"
	"github.com/uretgec/storagej"
)

// SnapshotReader is a consistent, point-in-time view of a Store, returned
// by Store.Snapshot. It satisfies storagej.Reader — Get/GetInto/Exists/
// Scan/PrefixScan — but not the write methods: there's no coherent "Set"
// on a view fixed to a moment that has already passed.
//
// Call Close exactly once when done. Until then, the snapshot pins Pebble
// resources that keep the data it can still see from being compacted away,
// even if that data has since been overwritten or deleted in the live
// store — an open snapshot left unclosed is a real, if slow, resource leak.
type SnapshotReader struct {
	snap *pebble.Snapshot
}

// Compile-time check that *SnapshotReader satisfies storagej.Reader.
var _ storagej.Reader = (*SnapshotReader)(nil)

// Snapshot returns a SnapshotReader fixed to this exact moment: every read
// through it reflects the store's state as of this call, regardless of any
// write that happens afterward — including from concurrent goroutines in
// this process, which are the only concurrent writers possible at all,
// since Pebble locks its data directory to one process (the same reasoning
// repository.Core's doc comments apply to index writes).
//
// This is for reading several keys "as of one moment" — e.g. a page that
// shows a post plus its author plus a comment count, all needing to agree
// with each other even if a write lands on one of them mid-request — not
// for anything held open a long time; see SnapshotReader's doc comment on
// why an unclosed one has a real cost.
func (s *Store) Snapshot() *SnapshotReader {
	return &SnapshotReader{snap: s.db.NewSnapshot()}
}

// Get reads a key as of the snapshot's moment, returning a safe copy —
// same contract as Store.Get.
func (r *SnapshotReader) Get(ctx context.Context, key []byte) ([]byte, error) {
	val, closer, err := r.snap.Get(key)
	if err != nil {
		if errors.Is(err, pebble.ErrNotFound) {
			return nil, storagej.ErrNotFound
		}
		return nil, err
	}
	defer closer.Close()

	out := make([]byte, len(val))
	copy(out, val)
	return out, nil
}

// GetInto is Store.GetInto's snapshot-scoped equivalent: reuses dst's
// underlying array when its capacity is sufficient.
func (r *SnapshotReader) GetInto(ctx context.Context, key, dst []byte) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	val, closer, err := r.snap.Get(key)
	if err != nil {
		if errors.Is(err, pebble.ErrNotFound) {
			return nil, storagej.ErrNotFound
		}
		return nil, err
	}
	defer closer.Close()

	if cap(dst) < len(val) {
		dst = make([]byte, len(val))
	}
	dst = dst[:len(val)]
	copy(dst, val)
	return dst, nil
}

// Exists is Store.Exists's snapshot-scoped equivalent: 0 allocations, no
// value copy.
func (r *SnapshotReader) Exists(ctx context.Context, key []byte) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}

	_, closer, err := r.snap.Get(key)
	if err != nil {
		if errors.Is(err, pebble.ErrNotFound) {
			return false, nil
		}
		return false, err
	}
	closer.Close()
	return true, nil
}

// Scan is Store.Scan's snapshot-scoped equivalent: iterates [start, end) in
// sorted order as of the snapshot's moment. Same ErrStopIteration and
// periodic context-cancellation behavior as Store.Scan.
func (r *SnapshotReader) Scan(ctx context.Context, start, end []byte, fn func(key, value []byte) error) error {
	iter, err := r.snap.NewIter(&pebble.IterOptions{
		LowerBound: start,
		UpperBound: end,
	})
	if err != nil {
		return fmt.Errorf("storagej: new iter: %w", err)
	}
	defer iter.Close()

	const ctxCheckInterval = 256
	visited := 0

	for iter.First(); iter.Valid(); iter.Next() {
		visited++
		if visited%ctxCheckInterval == 0 {
			if err := ctx.Err(); err != nil {
				return err
			}
		}
		if err := fn(iter.Key(), iter.Value()); err != nil {
			if errors.Is(err, storagej.ErrStopIteration) {
				return nil
			}
			return err
		}
	}
	return iter.Error()
}

// PrefixScan is Store.PrefixScan's snapshot-scoped equivalent.
func (r *SnapshotReader) PrefixScan(ctx context.Context, prefix []byte, fn func(key, value []byte) error) error {
	end := make([]byte, len(prefix), len(prefix)+1)
	copy(end, prefix)
	end = append(end, 0xFF)
	return r.Scan(ctx, prefix, end, fn)
}

// Close releases the snapshot. Call it exactly once — see the type's doc
// comment for why leaving it open has a real cost.
func (r *SnapshotReader) Close() error {
	return r.snap.Close()
}
