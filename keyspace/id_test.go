package keyspace

import (
	"errors"
	"testing"
	"time"
)

func TestNewIDLength(t *testing.T) {
	id := NewID()
	if len(id) != 26 {
		t.Fatalf("len(id) = %d, want 26", len(id))
	}
}

func TestNewIDIsUnique(t *testing.T) {
	const n = 10000
	seen := make(map[string]struct{}, n)
	for i := 0; i < n; i++ {
		id := NewID()
		if _, dup := seen[id]; dup {
			t.Fatalf("duplicate id generated: %s", id)
		}
		seen[id] = struct{}{}
	}
}

func TestNewIDSortsChronologically(t *testing.T) {
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	earlier := newID(base)
	later := newID(base.Add(2 * time.Millisecond))

	if earlier >= later {
		t.Fatalf("earlier id %q should sort before later id %q", earlier, later)
	}
}

func TestTimeRoundTrip(t *testing.T) {
	// Truncate to millisecond: that's the precision NewID actually encodes.
	want := time.Date(2026, 3, 14, 15, 9, 26, 0, time.UTC).Truncate(time.Millisecond)
	id := newID(want)

	got, err := Time(id)
	if err != nil {
		t.Fatalf("time: %v", err)
	}
	if !got.Equal(want) {
		t.Fatalf("time = %v, want %v", got, want)
	}
}

func TestTimeRejectsInvalidID(t *testing.T) {
	valid := NewID()

	invalidChar := []byte(valid)
	invalidChar[0] = 'U' // 'U' is excluded from the Crockford alphabet

	cases := map[string]string{
		"empty":                 "",
		"too short":             valid[:20],
		"too long":              valid + "0",
		"invalid alphabet char": string(invalidChar),
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := Time(c); !errors.Is(err, ErrInvalidID) {
				t.Fatalf("err = %v, want ErrInvalidID", err)
			}
		})
	}
}

func TestEncodeDecodeRoundTrip(t *testing.T) {
	var zero [16]byte

	var max [16]byte
	for i := range max {
		max[i] = 0xFF
	}

	var mixed [16]byte
	for i := range mixed {
		mixed[i] = byte(i*17 + 3)
	}

	cases := [][16]byte{zero, max, mixed}
	for _, raw := range cases {
		encoded := encode(raw)
		if len(encoded) != 26 {
			t.Fatalf("encode(%x) len = %d, want 26", raw, len(encoded))
		}
		decoded, err := decode(encoded)
		if err != nil {
			t.Fatalf("decode(%q): %v", encoded, err)
		}
		if decoded != raw {
			t.Fatalf("round trip mismatch: got %x, want %x", decoded, raw)
		}
	}
}

func TestDecodeAcceptsLowercase(t *testing.T) {
	id := NewID()
	lower := toLower(id)

	upperRaw, err := decode(id)
	if err != nil {
		t.Fatalf("decode upper: %v", err)
	}
	lowerRaw, err := decode(lower)
	if err != nil {
		t.Fatalf("decode lower: %v", err)
	}
	if upperRaw != lowerRaw {
		t.Fatal("lowercase decoding did not match uppercase decoding")
	}
}

func toLower(s string) string {
	b := []byte(s)
	for i, c := range b {
		if c >= 'A' && c <= 'Z' {
			b[i] = c + ('a' - 'A')
		}
	}
	return string(b)
}
