// Package watchable wraps a storagej.Store, letting callers subscribe to
// Set/Delete notifications for an exact key or a key prefix. It's an
// optional, opt-in layer — nothing else in this toolkit imports it, and it
// adds no dependency: sync and context from the standard library are all
// it needs.
package watchable

import (
	"bytes"
	"context"
	"sync"

	"github.com/uretgec/storagej/v2"
)

// DefaultChannelBuffer is the buffer size Watch and WatchPrefix give each
// subscription's channel.
const DefaultChannelBuffer = 16

// EventType categorizes a watch notification.
type EventType int

const (
	// EventSet indicates a Set (including a committed batch Set).
	EventSet EventType = iota
	// EventDelete indicates a Delete (including a committed batch Delete).
	EventDelete
)

// eventNames is an O(1) lookup table for EventType names, same pattern as
// storagej.EventType.String.
var eventNames = [2]string{"SET", "DELETE"}

// String returns the human-readable name of the event type.
func (t EventType) String() string {
	if t >= 0 && int(t) < len(eventNames) {
		return eventNames[t]
	}
	return "UNKNOWN"
}

// Event is one notification delivered to a watch channel. Key and Value
// are safe copies the receiver may retain indefinitely — never the same
// backing array the triggering Set/Delete call was given. Value is nil for
// EventDelete.
type Event struct {
	Type  EventType
	Key   []byte
	Value []byte
}

// Store wraps a storagej.Store, notifying watchers of successful Set and
// Delete calls (including batch commits) on keys they're watching. It
// implements storagej.Store itself, so it's a drop-in replacement anywhere
// a Store is expected.
//
// Delivery is best-effort, by design: each watch channel is buffered
// (DefaultChannelBuffer), and a Set/Delete that finds a watcher's buffer
// full drops that watcher's event rather than blocking. A watch channel is
// a live hint to re-fetch, not a durable event log — if you need every
// change recorded, that's a different feature (an append-only log,
// storagej.Store.Scan over versioned keys, or similar), not this one. This
// tradeoff exists specifically so Set/Delete on Store never block on a
// slow or absent consumer — a wrapper that could make an ordinary write
// hang because nobody drained a channel would be a worse bug than
// occasionally dropping a notification.
type Store struct {
	inner storagej.Store

	mu       sync.RWMutex
	watchers map[int]*watcher
	nextID   int
}

// Compile-time check that *Store satisfies storagej.Store.
var _ storagej.Store = (*Store)(nil)

// New wraps inner, adding Watch/WatchPrefix.
func New(inner storagej.Store) *Store {
	return &Store{inner: inner, watchers: make(map[int]*watcher)}
}

type watcher struct {
	// Exactly one of exact/prefix is non-nil, set at construction and
	// never mutated afterward — matches reads it without a lock.
	exact  []byte
	prefix []byte
	ch     chan Event

	// once guards this watcher's teardown (map removal + channel close).
	// It lives here, not as a variable local to watch()'s closure,
	// specifically so both the cancel func Watch/WatchPrefix return AND
	// Store.Close() can call stop() and safely race: whichever gets there
	// first tears it down, the other is a guaranteed no-op. Without a
	// shared guard, Close() closing the channel directly and a caller's
	// own cancel() firing afterward would double-close it — a real panic,
	// not a hypothetical one.
	once sync.Once
}

func (w *watcher) matches(key []byte) bool {
	if w.prefix != nil {
		return bytes.HasPrefix(key, w.prefix)
	}
	return bytes.Equal(key, w.exact)
}

// stop removes w from s's watcher map (if id is still present) and closes
// its channel — exactly once, no matter how many times or from where it's
// called. See watcher.once's doc comment for why that guarantee matters.
func (w *watcher) stop(s *Store, id int) {
	w.once.Do(func() {
		s.mu.Lock()
		delete(s.watchers, id)
		s.mu.Unlock()
		close(w.ch)
	})
}

// Watch returns a channel that receives an Event for every Set or Delete
// of exactly key through this Store, and a cancel function. Call cancel
// when done (e.g. via defer) — it stops delivery and closes the channel.
// Forgetting to call it doesn't block or leak a goroutine (Set/Delete
// never block trying to deliver, per Store's doc comment), but it does
// leak the watcher entry itself: it stays in Store's internal map,
// silently dropping every future event once its buffer fills, until the
// whole Store is closed.
func (s *Store) Watch(key []byte) (<-chan Event, func()) {
	return s.watch(&watcher{
		exact: append([]byte{}, key...),
		ch:    make(chan Event, DefaultChannelBuffer),
	})
}

// WatchPrefix is Watch's prefix-matching equivalent: the channel receives
// an Event for every Set/Delete whose key starts with prefix. Same
// delivery and cancellation contract as Watch.
func (s *Store) WatchPrefix(prefix []byte) (<-chan Event, func()) {
	return s.watch(&watcher{
		prefix: append([]byte{}, prefix...),
		ch:     make(chan Event, DefaultChannelBuffer),
	})
}

func (s *Store) watch(w *watcher) (<-chan Event, func()) {
	s.mu.Lock()
	id := s.nextID
	s.nextID++
	s.watchers[id] = w
	s.mu.Unlock()

	return w.ch, func() { w.stop(s, id) }
}

// notify delivers evt to every watcher whose key/prefix matches, dropping
// it for any watcher whose buffer is full instead of blocking. Holding
// only a read lock here (never a write lock) is what lets notify run
// concurrently with other Set/Delete calls; it's serialized against
// watch/cancel, which do take the write lock, so a channel is never sent
// on after being removed from the map and closed.
func (s *Store) notify(evt Event) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, w := range s.watchers {
		if !w.matches(evt.Key) {
			continue
		}
		select {
		case w.ch <- evt:
		default:
			// Buffer full — drop. See Store's doc comment: this is the
			// documented tradeoff, not a bug.
		}
	}
}

func (s *Store) Get(ctx context.Context, key []byte) ([]byte, error) {
	return s.inner.Get(ctx, key)
}

func (s *Store) GetInto(ctx context.Context, key, dst []byte) ([]byte, error) {
	return s.inner.GetInto(ctx, key, dst)
}

func (s *Store) Exists(ctx context.Context, key []byte) (bool, error) {
	return s.inner.Exists(ctx, key)
}

// Set writes through to the wrapped Store, then — only if that succeeded —
// notifies matching watchers.
func (s *Store) Set(ctx context.Context, key, value []byte) error {
	if err := s.inner.Set(ctx, key, value); err != nil {
		return err
	}
	s.notify(Event{
		Type:  EventSet,
		Key:   append([]byte{}, key...),
		Value: append([]byte{}, value...),
	})
	return nil
}

// Delete writes through to the wrapped Store, then — only if that
// succeeded — notifies matching watchers.
func (s *Store) Delete(ctx context.Context, key []byte) error {
	if err := s.inner.Delete(ctx, key); err != nil {
		return err
	}
	s.notify(Event{Type: EventDelete, Key: append([]byte{}, key...)})
	return nil
}

func (s *Store) Scan(ctx context.Context, start, end []byte, fn func(key, value []byte) error) error {
	return s.inner.Scan(ctx, start, end, fn)
}

func (s *Store) PrefixScan(ctx context.Context, prefix []byte, fn func(key, value []byte) error) error {
	return s.inner.PrefixScan(ctx, prefix, fn)
}

// NewBatch starts a batch. Individual Set/Delete calls on it are buffered,
// same as any storagej.Batch — watchers are only notified for operations
// in a batch that actually Commits, and only once Commit has succeeded, so
// a batch that's Closed without committing (or that fails to commit)
// notifies nobody. This matches Commit's atomic all-or-nothing semantics:
// watchers never see a partial batch.
func (s *Store) NewBatch() storagej.Batch {
	return &batch{inner: s.inner.NewBatch(), store: s}
}

// Close stops every outstanding watcher (removing it and closing its
// channel, via the same guarded path Watch/WatchPrefix's own cancel func
// uses — see watcher.stop) and releases the wrapped Store. A consumer
// ranging over a watch channel doesn't block forever just because the
// Store went away, and a caller's own cancel() firing before or after
// Close(), in either order or concurrently, is always safe.
func (s *Store) Close() error {
	s.mu.Lock()
	type entry struct {
		w  *watcher
		id int
	}
	entries := make([]entry, 0, len(s.watchers))
	for id, w := range s.watchers {
		entries = append(entries, entry{w, id})
	}
	s.mu.Unlock()

	for _, e := range entries {
		e.w.stop(s, e.id)
	}
	return s.inner.Close()
}

// batch buffers pending events and only hands them to Store.notify once
// Commit has actually succeeded.
type batch struct {
	inner   storagej.Batch
	store   *Store
	pending []Event
}

var _ storagej.Batch = (*batch)(nil)

func (b *batch) Set(key, value []byte) error {
	if err := b.inner.Set(key, value); err != nil {
		return err
	}
	b.pending = append(b.pending, Event{
		Type:  EventSet,
		Key:   append([]byte{}, key...),
		Value: append([]byte{}, value...),
	})
	return nil
}

func (b *batch) Delete(key []byte) error {
	if err := b.inner.Delete(key); err != nil {
		return err
	}
	b.pending = append(b.pending, Event{Type: EventDelete, Key: append([]byte{}, key...)})
	return nil
}

func (b *batch) Commit() error {
	if err := b.inner.Commit(); err != nil {
		return err
	}
	for _, evt := range b.pending {
		b.store.notify(evt)
	}
	return nil
}

func (b *batch) Count() int {
	return b.inner.Count()
}

func (b *batch) Close() error {
	return b.inner.Close()
}
