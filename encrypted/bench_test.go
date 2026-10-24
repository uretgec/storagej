package encrypted

import (
	"context"
	"strconv"
	"testing"

	"github.com/uretgec/storagej/v2"
	"github.com/uretgec/storagej/v2/pebble"
)

func openBenchStores(b *testing.B) (enc *Store, raw storagej.Store) {
	b.Helper()
	db, err := pebble.OpenMem(storagej.DefaultOptions())
	if err != nil {
		b.Fatalf("openmem: %v", err)
	}
	b.Cleanup(func() { db.Close() })

	var key [32]byte
	s, err := New(db, key)
	if err != nil {
		b.Fatalf("new: %v", err)
	}
	return s, db
}

// BenchmarkSet and BenchmarkSetRaw isolate encryption's cost on the write
// path: compare the two to see what AES-256-GCM actually adds over a bare
// pebble.Store.Set.
func BenchmarkSet(b *testing.B) {
	s, _ := openBenchStores(b)
	ctx := context.Background()
	val := []byte("benchmark-value-benchmark-value")
	key := make([]byte, 0, 32)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		key = key[:0]
		key = append(key, "bench:"...)
		key = strconv.AppendInt(key, int64(i), 10)
		if err := s.Set(ctx, key, val); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkSetRaw(b *testing.B) {
	_, raw := openBenchStores(b)
	ctx := context.Background()
	val := []byte("benchmark-value-benchmark-value")
	key := make([]byte, 0, 32)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		key = key[:0]
		key = append(key, "bench:"...)
		key = strconv.AppendInt(key, int64(i), 10)
		if err := raw.Set(ctx, key, val); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkGet and BenchmarkGetRaw do the same for the read path.
func BenchmarkGet(b *testing.B) {
	s, _ := openBenchStores(b)
	ctx := context.Background()
	key := []byte("bench:key")
	if err := s.Set(ctx, key, []byte("benchmark-value-benchmark-value")); err != nil {
		b.Fatal(err)
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := s.Get(ctx, key); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkGetRaw(b *testing.B) {
	_, raw := openBenchStores(b)
	ctx := context.Background()
	key := []byte("bench:key")
	if err := raw.Set(ctx, key, []byte("benchmark-value-benchmark-value")); err != nil {
		b.Fatal(err)
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := raw.Get(ctx, key); err != nil {
			b.Fatal(err)
		}
	}
}
