package pebble

import (
	"bytes"
	"context"
	"fmt"
	"strconv"
	"testing"

	"github.com/uretgec/storagej"
)

// openBenchStore opens an isolated Pebble store for benchmarks.
func openBenchStore(b *testing.B) *Store {
	b.Helper()
	s, err := Open(b.TempDir(), storagej.DefaultOptions())
	if err != nil {
		b.Fatalf("open store: %v", err)
	}
	b.Cleanup(func() { s.Close() })
	return s
}

// BenchmarkSet measures single-key writes with a reused key buffer so the
// measured allocations are Pebble's, not key construction's.
func BenchmarkSet(b *testing.B) {
	s := openBenchStore(b)
	ctx := context.Background()
	val := []byte("benchmark-value")
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

// BenchmarkGet measures reads of a 128-byte value (includes the safe copy).
func BenchmarkGet(b *testing.B) {
	s := openBenchStore(b)
	ctx := context.Background()
	key := []byte("bench:key")
	if err := s.Set(ctx, key, bytes.Repeat([]byte("x"), 128)); err != nil {
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

// BenchmarkExists measures existence checks, which skip Get's safe-copy
// allocation entirely.
func BenchmarkExists(b *testing.B) {
	s := openBenchStore(b)
	ctx := context.Background()
	key := []byte("bench:key")
	if err := s.Set(ctx, key, bytes.Repeat([]byte("x"), 128)); err != nil {
		b.Fatal(err)
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := s.Exists(ctx, key); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkGetIntoReused measures reads into a pre-sized, reused buffer —
// the zero-copy path for callers that manage their own buffers.
func BenchmarkGetIntoReused(b *testing.B) {
	s := openBenchStore(b)
	ctx := context.Background()
	key := []byte("bench:key")
	if err := s.Set(ctx, key, bytes.Repeat([]byte("x"), 128)); err != nil {
		b.Fatal(err)
	}
	dst := make([]byte, 0, 128)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		var err error
		dst, err = s.GetInto(ctx, key, dst)
		if err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkDelete measures single-key deletes (pre-populated).
func BenchmarkDelete(b *testing.B) {
	s := openBenchStore(b)
	ctx := context.Background()
	key := make([]byte, 0, 32)

	for i := 0; i < b.N; i++ {
		key = key[:0]
		key = append(key, "del:"...)
		key = strconv.AppendInt(key, int64(i), 10)
		if err := s.Set(ctx, key, []byte("v")); err != nil {
			b.Fatal(err)
		}
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		key = key[:0]
		key = append(key, "del:"...)
		key = strconv.AppendInt(key, int64(i), 10)
		if err := s.Delete(ctx, key); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkBatchCommit measures committing a 100-op batch (atomic write path).
func BenchmarkBatchCommit(b *testing.B) {
	s := openBenchStore(b)
	val := []byte("v")

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		batch := s.NewBatch()
		for j := 0; j < 100; j++ {
			batch.Set([]byte(fmt.Sprintf("batch:%d:%d", i, j)), val)
		}
		if err := batch.Commit(); err != nil {
			b.Fatal(err)
		}
		batch.Close()
	}
}

// BenchmarkPrefixScan simulates a paginated read: scan a prefix, stop at 10.
func BenchmarkPrefixScan(b *testing.B) {
	s := openBenchStore(b)
	ctx := context.Background()
	for i := 0; i < 1000; i++ {
		if err := s.Set(ctx, []byte(fmt.Sprintf("scan:post:%04d", i)), []byte("v")); err != nil {
			b.Fatal(err)
		}
	}
	prefix := []byte("scan:post:")

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		n := 0
		s.PrefixScan(ctx, prefix, func(key, value []byte) error {
			n++
			if n >= 10 {
				return storagej.ErrStopIteration
			}
			return nil
		})
	}
}

// BenchmarkScanFull measures a full scan over 100 keys (no early stop).
func BenchmarkScanFull(b *testing.B) {
	s := openBenchStore(b)
	ctx := context.Background()
	for i := 0; i < 100; i++ {
		if err := s.Set(ctx, []byte(fmt.Sprintf("full:%03d", i)), []byte("v")); err != nil {
			b.Fatal(err)
		}
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		s.Scan(ctx, []byte("full:"), []byte("full:\xff"), func(key, value []byte) error {
			return nil
		})
	}
}
