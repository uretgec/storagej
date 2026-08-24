// Package storagej defines a high-performance, domain-agnostic key-value store
// contract. It transports raw bytes only and has no knowledge of tenants,
// models, indexes, or relations — those concerns belong to higher layers
// (e.g., an ORM).
package storagej

import (
	"context"
	"errors"
	"time"
)

// -----------------------------------------------------------------------------
// Errors
// -----------------------------------------------------------------------------

// ErrNotFound is returned when the requested key does not exist.
var ErrNotFound = errors.New("storagej: key not found")

// ErrStopIteration is returned from a Scan callback to terminate the iteration
// early and cleanly. It is not treated as an error; it signals a successful
// early exit (e.g., "page limit reached" in cursor-based pagination).
var ErrStopIteration = errors.New("storagej: stop iteration")

// -----------------------------------------------------------------------------
// Store & Batch Contracts
// -----------------------------------------------------------------------------

// Store is the base contract for all key-value backend implementations.
// All methods are safe for concurrent use.
type Store interface {
	// Get returns the value associated with the given key, or ErrNotFound.
	// The returned slice is a safe copy the caller may retain indefinitely.
	Get(ctx context.Context, key []byte) ([]byte, error)

	// GetInto reads a key into the caller-supplied buffer, reusing dst's
	// underlying array when its capacity is sufficient (dst may be nil or
	// empty). It returns ErrNotFound when the key is absent. Prefer this over
	// Get when the caller manages its own buffer pool and wants to avoid
	// Get's one allocation per call.
	GetInto(ctx context.Context, key, dst []byte) ([]byte, error)

	// Exists reports whether a key is present, without paying for the value
	// copy that Get performs. Use this for existence-only checks (e.g. slug
	// collision checks) where the value itself is not needed.
	Exists(ctx context.Context, key []byte) (bool, error)

	// Set writes a key-value pair using NoSync (WAL provides durability).
	Set(ctx context.Context, key, value []byte) error

	// Delete removes a key. It is idempotent: deleting a missing key is not
	// an error.
	Delete(ctx context.Context, key []byte) error

	// Scan iterates over all keys in the half-open range [start, end) in
	// sorted order. It is the foundation of cursor-based pagination (O(1)
	// seek). If the callback returns ErrStopIteration, the scan stops cleanly.
	Scan(ctx context.Context, start, end []byte, fn func(key, value []byte) error) error

	// PrefixScan iterates over all keys sharing the given prefix. It is a
	// convenience wrapper around Scan where end = prefix + 0xFF.
	PrefixScan(ctx context.Context, prefix []byte, fn func(key, value []byte) error) error

	// NewBatch starts a new batch for atomic multi-key writes. A Batch is NOT
	// thread-safe; each goroutine must create its own.
	NewBatch() Batch

	// Close releases the database and all associated resources. It is
	// idempotent.
	Close() error
}

// Batch accumulates write operations and applies them atomically on Commit,
// guaranteeing that no partial state is ever persisted.
type Batch interface {
	// Set adds a write operation to the batch.
	Set(key, value []byte) error
	// Delete adds a delete operation to the batch.
	Delete(key []byte) error
	// Commit atomically applies all accumulated operations.
	Commit() error
	// Count returns the number of operations currently in the batch.
	Count() int
	// Close releases the batch resources.
	Close() error
}

// Reader is the read-only subset of Store: Get, GetInto, Exists, Scan,
// PrefixScan — everything except Set/Delete/NewBatch/Close. Store already
// has all five with identical signatures, so it satisfies Reader
// automatically (Go's structural typing) without any change to Store's own
// definition above; this is purely additive.
//
// Reader exists for views that support reading but not writing — most
// notably a point-in-time snapshot, where "Set" wouldn't mean anything
// coherent. See pebble.Store.Snapshot for the concrete example.
type Reader interface {
	Get(ctx context.Context, key []byte) ([]byte, error)
	GetInto(ctx context.Context, key, dst []byte) ([]byte, error)
	Exists(ctx context.Context, key []byte) (bool, error)
	Scan(ctx context.Context, start, end []byte, fn func(key, value []byte) error) error
	PrefixScan(ctx context.Context, prefix []byte, fn func(key, value []byte) error) error
}

// Compile-time check that Store's read methods are a superset of Reader's
// — i.e. that the doc comment above is actually true.
var _ Reader = (Store)(nil)

// -----------------------------------------------------------------------------
// Options
// -----------------------------------------------------------------------------

// Options configures the construction of a Store. Zero values are filled with
// sensible defaults by Normalize.
type Options struct {
	// CacheSizeMB is the memory (in MB) allocated to the read/write cache.
	// Default: 64
	CacheSizeMB int

	// MemTableSizeMB is the write buffer size (in MB). Default: 64
	MemTableSizeMB int

	// MaxOpenFiles is the maximum number of open file descriptors. Default: 64
	MaxOpenFiles int

	// CompactionThreads is the number of concurrent compaction goroutines.
	// 0 means CPU/2 (minimum 1). Default: 0
	CompactionThreads int

	// WALDir is a dedicated directory for the write-ahead log. If empty,
	// {path}/wal is used. Default: ""
	WALDir string

	// ReadOnly opens the database in read-only mode when true. Default: false
	ReadOnly bool

	// OnEvent is an optional callback for database events (background errors,
	// write stalls, compactions, WAL creation). If nil, events are ignored.
	// It is invoked from background goroutines and is NOT part of the hot path.
	OnEvent func(Event)

	// MetricsInterval is the interval for periodic metrics collection. When 0,
	// no background metrics goroutine is started. Recommended: 45s.
	// Default: 0 (off — a generic package must not spawn surprise goroutines).
	MetricsInterval time.Duration

	// OnMetrics is the periodic metrics callback. It is active only when
	// MetricsInterval > 0 AND this field is non-nil.
	OnMetrics func(MetricsSnapshot)
}

// DefaultOptions returns a configuration optimized for low-resource systems
// (1-2 vCPU, 2-4GB RAM). Event and metrics hooks are disabled by default;
// the caller opts in explicitly.
func DefaultOptions() Options {
	return Options{
		CacheSizeMB:       64,
		MemTableSizeMB:    64,
		MaxOpenFiles:      64,
		CompactionThreads: 0,
		WALDir:            "",
		ReadOnly:          false,
		MetricsInterval:   0,
	}
}

// Normalize fills zero values with defaults and enforces consistency. It is
// called automatically by Open and may also be called by the caller to
// validate a custom Options value.
func (o Options) Normalize() Options {
	d := DefaultOptions()
	if o.CacheSizeMB <= 0 {
		o.CacheSizeMB = d.CacheSizeMB
	}
	if o.MemTableSizeMB <= 0 {
		o.MemTableSizeMB = d.MemTableSizeMB
	}
	if o.MaxOpenFiles <= 0 {
		o.MaxOpenFiles = d.MaxOpenFiles
	}
	if o.CompactionThreads <= 0 {
		o.CompactionThreads = d.CompactionThreads
	}
	return o
}

// -----------------------------------------------------------------------------
// Events
// -----------------------------------------------------------------------------

// EventType categorizes a database event delivered via the OnEvent callback.
type EventType uint8

const (
	// EventBackgroundError indicates a critical background error.
	EventBackgroundError EventType = iota
	// EventWriteStallBegin indicates that writes have temporarily stalled.
	EventWriteStallBegin
	// EventWriteStallEnd indicates that a write stall has ended.
	EventWriteStallEnd
	// EventCompactionBegin indicates that a compaction has started.
	EventCompactionBegin
	// EventCompactionEnd indicates that a compaction has completed.
	EventCompactionEnd
	// EventWALCreated indicates that a new WAL file was created.
	EventWALCreated
)

// eventNames is an O(1) lookup table for EventType names.
var eventNames = [6]string{
	"BACKGROUND_ERROR",
	"WRITE_STALL_BEGIN",
	"WRITE_STALL_END",
	"COMPACTION_BEGIN",
	"COMPACTION_END",
	"WAL_CREATED",
}

// String returns the human-readable name of the event type.
func (t EventType) String() string {
	if t < EventType(len(eventNames)) {
		return eventNames[t]
	}
	return "UNKNOWN"
}

// Event represents a structured database event delivered to the OnEvent
// callback. It is NOT part of the hot path; it is emitted from background
// goroutines, so forwarding it to a logger is safe.
type Event struct {
	// Type is the event category.
	Type EventType
	// Message is a short human-readable description.
	Message string
	// Fields holds alternating key-value pairs (compatible with loggerj fields).
	Fields []string
}

// -----------------------------------------------------------------------------
// Metrics
// -----------------------------------------------------------------------------

// MetricsSnapshot holds a point-in-time summary of database metrics. It is
// collected periodically when MetricsInterval > 0 and delivered to the
// OnMetrics callback. All values are pre-computed; formatting is left to the
// caller.
type MetricsSnapshot struct {
	// DiskMB is the total disk usage in megabytes.
	DiskMB int64 `json:"disk_mb"`
	// CacheHitRatePercent is the block cache hit ratio (0-100).
	CacheHitRatePercent float64 `json:"cache_hit_rate_percent"`
	// TotalFiles is the total number of SST files across all levels.
	TotalFiles int64 `json:"total_files"`
	// TotalSizeMB is the total data size across all levels in megabytes.
	TotalSizeMB int64 `json:"total_size_mb"`
	// CompactionCount is the total number of completed compactions.
	CompactionCount int64 `json:"compaction_count"`
	// CompactionsInProgress is the number of compactions currently running.
	CompactionsInProgress int64 `json:"compactions_in_progress"`
	// WALSizeMB is the write-ahead log size in megabytes.
	WALSizeMB int64 `json:"wal_size_mb"`
}
