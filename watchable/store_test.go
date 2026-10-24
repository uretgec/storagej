package watchable

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/uretgec/storagej/v2"
	"github.com/uretgec/storagej/v2/pebble"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	db, err := pebble.OpenMem(storagej.DefaultOptions())
	if err != nil {
		t.Fatalf("openmem: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return New(db)
}

// recvEvent waits briefly for an event, failing the test if none arrives —
// used where an event is expected.
func recvEvent(t *testing.T, ch <-chan Event) Event {
	t.Helper()
	select {
	case evt, ok := <-ch:
		if !ok {
			t.Fatal("channel closed while waiting for an event")
		}
		return evt
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for an event")
		return Event{}
	}
}

// expectNoEvent fails the test if an event arrives within a short window —
// used where delivery should NOT happen.
func expectNoEvent(t *testing.T, ch <-chan Event) {
	t.Helper()
	select {
	case evt, ok := <-ch:
		if ok {
			t.Fatalf("received unexpected event: %+v", evt)
		}
		// ok == false: channel closed, that's a different assertion's job
	case <-time.After(50 * time.Millisecond):
		// good — nothing arrived
	}
}

// -----------------------------------------------------------------------------
// Watch (exact key)
// -----------------------------------------------------------------------------

func TestWatchReceivesSetEvent(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	ch, cancel := s.Watch([]byte("k"))
	defer cancel()

	if err := s.Set(ctx, []byte("k"), []byte("v")); err != nil {
		t.Fatalf("set: %v", err)
	}

	evt := recvEvent(t, ch)
	if evt.Type != EventSet {
		t.Fatalf("Type = %v, want EventSet", evt.Type)
	}
	if string(evt.Key) != "k" || string(evt.Value) != "v" {
		t.Fatalf("evt = %+v, want Key=k Value=v", evt)
	}
}

func TestWatchReceivesDeleteEvent(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	if err := s.Set(ctx, []byte("k"), []byte("v")); err != nil {
		t.Fatalf("set: %v", err)
	}

	ch, cancel := s.Watch([]byte("k"))
	defer cancel()

	if err := s.Delete(ctx, []byte("k")); err != nil {
		t.Fatalf("delete: %v", err)
	}

	evt := recvEvent(t, ch)
	if evt.Type != EventDelete {
		t.Fatalf("Type = %v, want EventDelete", evt.Type)
	}
	if evt.Value != nil {
		t.Fatalf("Value = %q, want nil for a delete", evt.Value)
	}
}

func TestWatchIgnoresOtherKeys(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	ch, cancel := s.Watch([]byte("k"))
	defer cancel()

	if err := s.Set(ctx, []byte("different-key"), []byte("v")); err != nil {
		t.Fatalf("set: %v", err)
	}

	expectNoEvent(t, ch)
}

// TestWatchEventsAreSafeCopies confirms mutating the caller's key/value
// slices after Set returns doesn't corrupt what a watcher already
// received or is about to receive.
func TestWatchEventsAreSafeCopies(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	ch, cancel := s.Watch([]byte("k"))
	defer cancel()

	key := []byte("k")
	val := []byte("original")
	if err := s.Set(ctx, key, val); err != nil {
		t.Fatalf("set: %v", err)
	}
	val[0] = 'X' // mutate after Set returns

	evt := recvEvent(t, ch)
	if string(evt.Value) != "original" {
		t.Fatalf("evt.Value = %q, want %q (should not alias the caller's slice)", evt.Value, "original")
	}
}

// -----------------------------------------------------------------------------
// WatchPrefix
// -----------------------------------------------------------------------------

func TestWatchPrefixReceivesMatchingKeys(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	ch, cancel := s.WatchPrefix([]byte("post:"))
	defer cancel()

	if err := s.Set(ctx, []byte("post:1"), []byte("v1")); err != nil {
		t.Fatalf("set: %v", err)
	}
	if err := s.Set(ctx, []byte("post:2"), []byte("v2")); err != nil {
		t.Fatalf("set: %v", err)
	}

	e1 := recvEvent(t, ch)
	e2 := recvEvent(t, ch)
	if string(e1.Key) != "post:1" || string(e2.Key) != "post:2" {
		t.Fatalf("got keys %q, %q, want post:1, post:2", e1.Key, e2.Key)
	}
}

func TestWatchPrefixIgnoresNonMatchingKeys(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	ch, cancel := s.WatchPrefix([]byte("post:"))
	defer cancel()

	if err := s.Set(ctx, []byte("page:1"), []byte("v")); err != nil {
		t.Fatalf("set: %v", err)
	}

	expectNoEvent(t, ch)
}

// -----------------------------------------------------------------------------
// Cancel
// -----------------------------------------------------------------------------

func TestCancelClosesChannel(t *testing.T) {
	s := newTestStore(t)
	ch, cancel := s.Watch([]byte("k"))
	cancel()

	_, ok := <-ch
	if ok {
		t.Fatal("channel should be closed after cancel")
	}
}

func TestCancelStopsDelivery(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	ch, cancel := s.Watch([]byte("k"))
	cancel()

	// Set after cancel must not panic (send-on-closed-channel) and must
	// not deliver anything — the watcher is gone.
	if err := s.Set(ctx, []byte("k"), []byte("v")); err != nil {
		t.Fatalf("set: %v", err)
	}
	if _, ok := <-ch; ok {
		t.Fatal("received an event after cancel")
	}
}

func TestCancelIsIdempotent(t *testing.T) {
	s := newTestStore(t)
	_, cancel := s.Watch([]byte("k"))
	cancel()
	cancel() // must not panic (double close)
}

// -----------------------------------------------------------------------------
// Backpressure — the design's central guarantee
// -----------------------------------------------------------------------------

// TestSetDoesNotBlockWhenWatcherBufferIsFull is the test that matters most
// here: it fills a watcher's buffer without draining it, then confirms Set
// still returns promptly. If notify's non-blocking send were broken (a
// blocking send instead), this test would hang until killed by the test
// timeout rather than complete quickly.
func TestSetDoesNotBlockWhenWatcherBufferIsFull(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	ch, cancel := s.Watch([]byte("k"))
	defer cancel()

	// Overfill the buffer (DefaultChannelBuffer + a few more) without ever
	// reading from ch.
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < DefaultChannelBuffer+5; i++ {
			if err := s.Set(ctx, []byte("k"), []byte("v")); err != nil {
				t.Errorf("set %d: %v", i, err)
				return
			}
		}
	}()

	select {
	case <-done:
		// good — every Set returned even with a full, undrained buffer
	case <-time.After(2 * time.Second):
		t.Fatal("Set blocked on a full watcher buffer — backpressure design is broken")
	}

	// Buffer should be full (DefaultChannelBuffer events), not more.
	count := 0
	for {
		select {
		case _, ok := <-ch:
			if !ok {
				t.Fatal("channel closed unexpectedly")
			}
			count++
		default:
			if count != DefaultChannelBuffer {
				t.Fatalf("buffered events = %d, want exactly %d (buffer capacity)", count, DefaultChannelBuffer)
			}
			return
		}
	}
}

// -----------------------------------------------------------------------------
// Batch
// -----------------------------------------------------------------------------

func TestBatchNotifiesOnlyAfterCommit(t *testing.T) {
	s := newTestStore(t)

	ch, cancel := s.Watch([]byte("k"))
	defer cancel()

	b := s.NewBatch()
	if err := b.Set([]byte("k"), []byte("v")); err != nil {
		t.Fatalf("batch set: %v", err)
	}
	// Not committed yet — must not have notified.
	expectNoEvent(t, ch)

	if err := b.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}
	b.Close()

	evt := recvEvent(t, ch)
	if string(evt.Key) != "k" || string(evt.Value) != "v" {
		t.Fatalf("evt = %+v, want Key=k Value=v", evt)
	}
}

func TestBatchClosedWithoutCommitNotifiesNobody(t *testing.T) {
	s := newTestStore(t)

	ch, cancel := s.Watch([]byte("k"))
	defer cancel()

	b := s.NewBatch()
	if err := b.Set([]byte("k"), []byte("v")); err != nil {
		t.Fatalf("batch set: %v", err)
	}
	b.Close() // never committed

	expectNoEvent(t, ch)
}

func TestBatchCommitNotifiesAllOperationsInOrder(t *testing.T) {
	s := newTestStore(t)

	ch, cancel := s.WatchPrefix([]byte(""))
	defer cancel()

	b := s.NewBatch()
	if err := b.Set([]byte("a"), []byte("1")); err != nil {
		t.Fatalf("set a: %v", err)
	}
	if err := b.Set([]byte("b"), []byte("2")); err != nil {
		t.Fatalf("set b: %v", err)
	}
	if err := b.Delete([]byte("c")); err != nil {
		t.Fatalf("delete c: %v", err)
	}
	if err := b.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}
	b.Close()

	e1 := recvEvent(t, ch)
	e2 := recvEvent(t, ch)
	e3 := recvEvent(t, ch)

	if string(e1.Key) != "a" || e1.Type != EventSet {
		t.Fatalf("e1 = %+v, want Set a", e1)
	}
	if string(e2.Key) != "b" || e2.Type != EventSet {
		t.Fatalf("e2 = %+v, want Set b", e2)
	}
	if string(e3.Key) != "c" || e3.Type != EventDelete {
		t.Fatalf("e3 = %+v, want Delete c", e3)
	}
}

// -----------------------------------------------------------------------------
// Close
// -----------------------------------------------------------------------------

func TestStoreCloseClosesWatcherChannels(t *testing.T) {
	db, err := pebble.OpenMem(storagej.DefaultOptions())
	if err != nil {
		t.Fatalf("openmem: %v", err)
	}
	s := New(db)

	ch, _ := s.Watch([]byte("k")) // deliberately not cancelling

	if err := s.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	if _, ok := <-ch; ok {
		t.Fatal("channel should be closed after Store.Close")
	}
}

// -----------------------------------------------------------------------------
// Multiple watchers
// -----------------------------------------------------------------------------

func TestMultipleWatchersOnSameKeyAllNotified(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	ch1, cancel1 := s.Watch([]byte("k"))
	defer cancel1()
	ch2, cancel2 := s.Watch([]byte("k"))
	defer cancel2()

	if err := s.Set(ctx, []byte("k"), []byte("v")); err != nil {
		t.Fatalf("set: %v", err)
	}

	e1 := recvEvent(t, ch1)
	e2 := recvEvent(t, ch2)
	if string(e1.Value) != "v" || string(e2.Value) != "v" {
		t.Fatalf("both watchers should see the event: e1=%+v e2=%+v", e1, e2)
	}
}

// -----------------------------------------------------------------------------
// EventType
// -----------------------------------------------------------------------------

func TestEventTypeString(t *testing.T) {
	if EventSet.String() != "SET" {
		t.Fatalf("EventSet.String() = %q, want SET", EventSet.String())
	}
	if EventDelete.String() != "DELETE" {
		t.Fatalf("EventDelete.String() = %q, want DELETE", EventDelete.String())
	}
	if EventType(99).String() != "UNKNOWN" {
		t.Fatalf("EventType(99).String() = %q, want UNKNOWN", EventType(99).String())
	}
}

// -----------------------------------------------------------------------------
// Concurrency
// -----------------------------------------------------------------------------

// TestConcurrentWatchSetAndCancelIsRaceFree exercises watch/notify/cancel
// all happening concurrently — the scenario the RWMutex split (RLock in
// notify, Lock in watch/cancel) exists for. Run with -race.
func TestConcurrentWatchSetAndCancelIsRaceFree(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	var wg sync.WaitGroup

	// Writers.
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for j := 0; j < 20; j++ {
				_ = s.Set(ctx, []byte("k"), []byte("v"))
			}
		}(i)
	}

	// Watchers that immediately cancel, churning the watcher map.
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 20; j++ {
				_, cancel := s.Watch([]byte("k"))
				cancel()
			}
		}()
	}

	// A long-lived watcher, draining as it goes.
	ch, cancel := s.Watch([]byte("k"))
	defer cancel()
	drainDone := make(chan struct{})
	go func() {
		defer close(drainDone)
		for range ch {
		}
	}()

	wg.Wait()
	cancel()
	<-drainDone
}
