package keyspace

import "testing"

// BenchmarkKey measures a fresh, one-off key allocation — the common case
// for a single Set/Get call.
func BenchmarkKey(b *testing.B) {
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := Key("acme", KindData, "post", "01ARZ3NDEKTSV4RRFFQ69G5FAV"); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkAppendReused measures building a key into a reused buffer, the
// pattern a batch-write loop should use to avoid per-key allocation.
func BenchmarkAppendReused(b *testing.B) {
	dst := make([]byte, 0, 64)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		var err error
		dst, err = Append(dst[:0], "acme", KindData, "post", "01ARZ3NDEKTSV4RRFFQ69G5FAV")
		if err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkParse measures splitting a key back into its segments. Parse
// aliases the input, so this should report 0 allocations.
func BenchmarkParse(b *testing.B) {
	key, err := Key("acme", KindData, "post", "01ARZ3NDEKTSV4RRFFQ69G5FAV")
	if err != nil {
		b.Fatal(err)
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, _, _, _, err := Parse(key); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkNewID measures id generation (timestamp + crypto/rand + base32
// encode). One allocation is expected: the returned string itself.
func BenchmarkNewID(b *testing.B) {
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = NewID()
	}
}
