package watchable

import (
	"context"
	"fmt"
	"testing"

	"github.com/uretgec/storagej/v2"
	"github.com/uretgec/storagej/v2/pebble"
)

func openBenchStore(b *testing.B) *Store {
	b.Helper()
	db, err := pebble.OpenMem(storagej.DefaultOptions())
	if err != nil {
		b.Fatalf("openmem: %v", err)
	}
	b.Cleanup(func() { db.Close() })
	return New(db)
}

// BenchmarkSetNoWatchers is the baseline: what Set costs on a
// watchable.Store with nothing registered — notify() still runs (an empty
// map range), so this quantifies the wrapper's own floor, separate from
// any dispatch cost.
func BenchmarkSetNoWatchers(b *testing.B) {
	s := openBenchStore(b)
	ctx := context.Background()
	val := []byte("benchmark-value")

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := s.Set(ctx, []byte("k"), val); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkSetOneWatcherDraining measures Set with exactly one matching
// watcher, continuously drained so notify's channel send always finds
// room (an undrained benchmark would understate steady-state cost once
// the buffer fills and sends become no-ops).
func BenchmarkSetOneWatcherDraining(b *testing.B) {
	s := openBenchStore(b)
	ctx := context.Background()
	val := []byte("benchmark-value")

	ch, cancel := s.Watch([]byte("k"))
	defer cancel()
	go func() {
		for range ch {
		}
	}()

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := s.Set(ctx, []byte("k"), val); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkSetManyNonMatchingWatchers registers 100 watchers, none of
// which match the key being Set, to quantify the linear-scan cost
// notify() pays checking every subscription's prefix — the honest
// tradeoff of the simple design (no trie, no index by prefix) against a
// large watcher count.
func BenchmarkSetManyNonMatchingWatchers(b *testing.B) {
	s := openBenchStore(b)
	ctx := context.Background()
	val := []byte("benchmark-value")

	for i := 0; i < 100; i++ {
		_, cancel := s.Watch([]byte(fmt.Sprintf("other-key-%d", i)))
		defer cancel()
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := s.Set(ctx, []byte("k"), val); err != nil {
			b.Fatal(err)
		}
	}
}
