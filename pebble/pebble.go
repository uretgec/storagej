// Package pebble implements the storagej.Store contract using CockroachDB's
// Pebble engine. It is LSM-Tree based, WAL-protected, and optimized for low
// resource consumption. Events and metrics are surfaced through optional
// callbacks; the package has no logger dependency.
package pebble

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"sync"
	"time"

	"github.com/cockroachdb/pebble"
	"github.com/cockroachdb/pebble/bloom"
	"github.com/cockroachdb/pebble/vfs"
	"github.com/uretgec/storagej"
)

// Store is the Pebble implementation of storagej.Store. It is safe for
// concurrent use. When MetricsInterval > 0, a background metrics goroutine
// runs and is stopped on Close.
type Store struct {
	db        *pebble.DB
	cache     *pebble.Cache
	opts      storagej.Options
	meterStop chan struct{}
	closeOnce sync.Once
}

// Compile-time interface guarantee.
var _ storagej.Store = (*Store)(nil)

// Open opens a Pebble database at the given path. Defaults are applied via
// Options.Normalize. When OnEvent or MetricsInterval+OnMetrics are set, the
// corresponding background behaviors are enabled.
func Open(path string, opts storagej.Options) (*Store, error) {
	return open(path, opts, nil)
}

// OpenMem opens a purely in-memory Pebble store: nothing touches disk, not
// even a WAL directory. It shares Open's exact Options handling, callback
// wiring, and interface — the only difference is the backing filesystem.
//
// Two things worth knowing:
//   - Options.WALDir is ignored. A separate WAL directory is a
//     real-filesystem concept that doesn't apply here.
//   - Checkpoint(dir) still works, but it writes into this same in-memory
//     filesystem, not real disk — the checkpoint doesn't survive process
//     exit any more than the store itself does. Checkpointing to real disk
//     from a mem store isn't supported; use Open for anything that needs
//     that.
//
// Good for tests (much faster than a real Open with a temp directory —
// no real file I/O at all) and ephemeral caches where durability across a
// restart was never the point.
func OpenMem(opts storagej.Options) (*Store, error) {
	return open("", opts, vfs.NewMem())
}

// open is Open and OpenMem's shared implementation. fs is nil for Open
// (Pebble defaults a nil Options.FS to the real OS filesystem itself) or
// an in-memory vfs.FS for OpenMem.
func open(path string, opts storagej.Options, fs vfs.FS) (*Store, error) {
	opts = opts.Normalize()

	cache := pebble.NewCache(int64(opts.CacheSizeMB) * 1024 * 1024)

	compaction := opts.CompactionThreads
	if compaction <= 0 {
		compaction = runtime.NumCPU() / 2
		if compaction < 1 {
			compaction = 1
		}
	}

	// Level options: 2x progression with bloom filters.
	levels := make([]pebble.LevelOptions, 7)
	for i := 0; i < 7; i++ {
		levels[i] = pebble.LevelOptions{
			TargetFileSize: 2 << (20 + i), // 2MB → 128MB
			FilterPolicy:   bloom.FilterPolicy(10),
		}
	}

	// A separate WAL directory is a real-filesystem concept: skip creating
	// one entirely for an in-memory store (fs != nil), where there's no
	// real directory to create and Options.WALDir wouldn't mean anything.
	var walDir string
	if fs == nil {
		walDir = opts.WALDir
		if walDir == "" && !opts.ReadOnly {
			walDir = filepath.Join(path, "wal")
		}
		if walDir != "" {
			if err := os.MkdirAll(walDir, 0o755); err != nil {
				cache.Unref()
				return nil, fmt.Errorf("storagej: create wal dir: %w", err)
			}
		}
	}

	s := &Store{
		cache:     cache,
		opts:      opts,
		meterStop: make(chan struct{}),
	}

	pebbleOpts := &pebble.Options{
		Cache:                       cache,
		MaxOpenFiles:                opts.MaxOpenFiles,
		MemTableSize:                uint64(opts.MemTableSizeMB) * 1024 * 1024,
		MemTableStopWritesThreshold: 4,
		MaxConcurrentCompactions:    func() int { return compaction },
		BytesPerSync:                1 << 20, // 1MB
		L0CompactionThreshold:       2,
		L0StopWritesThreshold:       1000,
		Levels:                      levels,
		WALDir:                      walDir,
		ReadOnly:                    opts.ReadOnly,
		EventListener:               s.eventListener(),
		FS:                          fs, // nil means Pebble's own default (the real OS filesystem)
	}
	// Disable seek compaction for sequential-read workloads.
	pebbleOpts.Experimental.ReadSamplingMultiplier = -1

	db, err := pebble.Open(path, pebbleOpts)
	if err != nil {
		cache.Unref()
		return nil, fmt.Errorf("storagej: open pebble: %w", err)
	}
	s.db = db

	// Start the periodic metrics goroutine only when requested.
	if opts.MetricsInterval > 0 && opts.OnMetrics != nil {
		go s.startMetricsGathering(opts.MetricsInterval)
	}

	return s, nil
}

// eventListener builds a pebble.EventListener that forwards Pebble events to
// the OnEvent callback. It returns nil when OnEvent is unset, letting Pebble
// use its no-op default. Callbacks are invoked from background goroutines.
func (s *Store) eventListener() *pebble.EventListener {
	emit := s.opts.OnEvent
	if emit == nil {
		return nil
	}
	return &pebble.EventListener{
		BackgroundError: func(err error) {
			emit(storagej.Event{
				Type:    storagej.EventBackgroundError,
				Message: "background error",
				Fields:  []string{"err", err.Error()},
			})
		},
		WriteStallBegin: func(info pebble.WriteStallBeginInfo) {
			emit(storagej.Event{
				Type:    storagej.EventWriteStallBegin,
				Message: "write stall begin",
				Fields:  []string{"reason", info.Reason},
			})
		},
		WriteStallEnd: func() {
			emit(storagej.Event{Type: storagej.EventWriteStallEnd, Message: "write stall end"})
		},
		CompactionBegin: func(info pebble.CompactionInfo) {
			emit(storagej.Event{
				Type:    storagej.EventCompactionBegin,
				Message: "compaction begin",
				Fields:  []string{"inputs", strconv.Itoa(len(info.Input))},
			})
		},
		CompactionEnd: func(info pebble.CompactionInfo) {
			// BytesWritten is not exposed directly; sum the output table sizes.
			var bytesWritten uint64
			for _, t := range info.Output.Tables {
				bytesWritten += t.Size
			}
			emit(storagej.Event{
				Type:    storagej.EventCompactionEnd,
				Message: "compaction end",
				Fields: []string{
					"duration", info.Duration.String(),
					"bytes_written", strconv.FormatUint(bytesWritten, 10),
					"output_level", strconv.Itoa(info.Output.Level),
				},
			})
		},
		WALCreated: func(info pebble.WALCreateInfo) {
			emit(storagej.Event{
				Type:    storagej.EventWALCreated,
				Message: "wal created",
				Fields:  []string{"path", info.Path},
			})
		},
	}
}

// startMetricsGathering collects metrics at the given interval and delivers
// them to the OnMetrics callback. It runs until Close closes meterStop.
func (s *Store) startMetricsGathering(interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-s.meterStop:
			return
		case <-ticker.C:
			s.opts.OnMetrics(s.snapshot())
		}
	}
}

// snapshot converts the current Pebble metrics into a MetricsSnapshot.
func (s *Store) snapshot() storagej.MetricsSnapshot {
	m := s.db.Metrics()

	var hitRate float64
	if total := m.BlockCache.Hits + m.BlockCache.Misses; total > 0 {
		hitRate = float64(m.BlockCache.Hits) / float64(total) * 100
	}

	var totalFiles, totalSize int64
	for _, lm := range m.Levels {
		totalFiles += lm.NumFiles
		totalSize += lm.Size
	}

	return storagej.MetricsSnapshot{
		DiskMB:                int64(m.DiskSpaceUsage() / 1024 / 1024),
		CacheHitRatePercent:   hitRate,
		TotalFiles:            totalFiles,
		TotalSizeMB:           totalSize / 1024 / 1024,
		CompactionCount:       m.Compact.Count,
		CompactionsInProgress: m.Compact.NumInProgress,
		WALSizeMB:             int64(m.WAL.Size) / 1024 / 1024,
	}
}

// Get reads a key and returns a SAFE copy. It returns storagej.ErrNotFound when
// the key is absent. Pebble's returned slice is bound to a closer, so copying
// is mandatory.
func (s *Store) Get(ctx context.Context, key []byte) ([]byte, error) {
	val, closer, err := s.db.Get(key)
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

// GetInto reads a key into dst, reusing its underlying array when there is
// enough capacity and allocating a new one only when there isn't. This avoids
// the one allocation Get always pays for the safe copy — useful for callers
// that already manage a buffer (e.g. a sync.Pool of read buffers).
func (s *Store) GetInto(ctx context.Context, key, dst []byte) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	val, closer, err := s.db.Get(key)
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

// Exists reports whether key is present. Unlike Get, it never copies the
// value out of Pebble's internal buffer — it inspects, then immediately
// closes it — so it costs 0 allocations, matching the Set/Delete hot path.
func (s *Store) Exists(ctx context.Context, key []byte) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}

	_, closer, err := s.db.Get(key)
	if err != nil {
		if errors.Is(err, pebble.ErrNotFound) {
			return false, nil
		}
		return false, err
	}
	closer.Close()
	return true, nil
}

// Set writes a key-value pair using NoSync. The WAL provides durability.
func (s *Store) Set(ctx context.Context, key, value []byte) error {
	return s.db.Set(key, value, pebble.NoSync)
}

// Delete removes a key using NoSync. It is idempotent.
func (s *Store) Delete(ctx context.Context, key []byte) error {
	return s.db.Delete(key, pebble.NoSync)
}

// Scan iterates over the half-open range [start, end) in sorted order using an
// O(1) seek. If the callback returns storagej.ErrStopIteration, the scan stops
// cleanly.
func (s *Store) Scan(ctx context.Context, start, end []byte, fn func(key, value []byte) error) error {
	iter, err := s.db.NewIter(&pebble.IterOptions{
		LowerBound: start,
		UpperBound: end,
	})
	if err != nil {
		return fmt.Errorf("storagej: new iter: %w", err)
	}
	defer iter.Close()

	// Checking ctx on every key would add a mutex read to the hot path of
	// small/paginated scans for no benefit. Checking periodically bounds how
	// long a canceled scan keeps running without taxing the common case
	// (page sizes are typically far below this interval).
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

// PrefixScan iterates over all keys sharing the given prefix. The upper bound
// is prefix + 0xFF, the safe approach recommended by Pebble.
func (s *Store) PrefixScan(ctx context.Context, prefix []byte, fn func(key, value []byte) error) error {
	end := make([]byte, len(prefix), len(prefix)+1)
	copy(end, prefix)
	end = append(end, 0xFF)
	return s.Scan(ctx, prefix, end, fn)
}

// NewBatch starts a new batch for atomic writes. The returned Batch is NOT
// thread-safe.
func (s *Store) NewBatch() storagej.Batch {
	return &Batch{batch: s.db.NewBatch()}
}

// Close releases the database, the metrics goroutine, and the cache. It is
// idempotent (guarded by sync.Once).
func (s *Store) Close() error {
	s.closeOnce.Do(func() {
		close(s.meterStop)
		s.db.Close()
		s.cache.Unref()
	})
	return nil
}

// --- Concrete (Pebble-specific) methods — kept out of the interface ---

// dir without blocking reads. It flushes the active memtable first so that
// all data — including NoSync writes still sitting in the memtable/WAL — is
// captured in SST files, making the checkpoint fully self-contained. It is
// the foundation of the backup system.
func (s *Store) Checkpoint(dir string) error {
	// Flush the memtable so unflushed data lands in SST files. Without this,
	// the checkpoint relies on a WAL copy that may replay zero keys, silently
	// losing recently written data.
	if err := s.db.Flush(); err != nil {
		return fmt.Errorf("storagej: flush before checkpoint: %w", err)
	}
	return s.db.Checkpoint(dir)
}

// Metrics returns Pebble's raw metrics. For periodic logging, prefer
// snapshot() + OnMetrics; this method is intended for ad-hoc reads.
func (s *Store) Metrics() *pebble.Metrics {
	return s.db.Metrics()
}

// MetricsSnapshot returns a point-in-time storagej.MetricsSnapshot, computed
// on demand. Unlike the periodic OnMetrics callback (which requires
// Options.MetricsInterval > 0 to be configured up front), this can be
// called any time — e.g. from an HTTP /health endpoint — without setting up
// periodic collection.
func (s *Store) MetricsSnapshot() storagej.MetricsSnapshot {
	return s.snapshot()
}

// Batch is the Pebble implementation of storagej.Batch. It is NOT thread-safe;
// each goroutine must create its own.
type Batch struct {
	batch *pebble.Batch
}

// Compile-time interface guarantee.
var _ storagej.Batch = (*Batch)(nil)

// Set adds a write operation to the batch.
func (b *Batch) Set(key, value []byte) error {
	return b.batch.Set(key, value, pebble.NoSync)
}

// Delete adds a delete operation to the batch.
func (b *Batch) Delete(key []byte) error {
	return b.batch.Delete(key, pebble.NoSync)
}

// Commit atomically applies all accumulated operations.
func (b *Batch) Commit() error {
	return b.batch.Commit(pebble.NoSync)
}

// Count returns the number of operations currently in the batch.
func (b *Batch) Count() int {
	return int(b.batch.Count())
}

// Close releases the batch resources.
func (b *Batch) Close() error {
	return b.batch.Close()
}
