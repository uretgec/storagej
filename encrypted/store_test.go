package encrypted

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/uretgec/storagej/v2"
	"github.com/uretgec/storagej/v2/pebble"
)

func testKey(t *testing.T) [32]byte {
	t.Helper()
	var key [32]byte
	for i := range key {
		key[i] = byte(i) // deterministic, not secret — fine for tests
	}
	return key
}

// newTestStores returns both the encrypted wrapper and the raw store it
// wraps, so tests can inspect what actually landed on "disk" (OpenMem, in
// this case) versus what the wrapper hands back.
func newTestStores(t *testing.T) (enc *Store, raw storagej.Store) {
	t.Helper()
	db, err := pebble.OpenMem(storagej.DefaultOptions())
	if err != nil {
		t.Fatalf("openmem: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	s, err := New(db, testKey(t))
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	return s, db
}

// -----------------------------------------------------------------------------
// Core round trip
// -----------------------------------------------------------------------------

func TestRoundTrip(t *testing.T) {
	s, _ := newTestStores(t)
	ctx := context.Background()

	if err := s.Set(ctx, []byte("k"), []byte("hello world")); err != nil {
		t.Fatalf("set: %v", err)
	}
	got, err := s.Get(ctx, []byte("k"))
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if string(got) != "hello world" {
		t.Fatalf("get = %q, want %q", got, "hello world")
	}
}

// TestValuesAreActuallyEncryptedAtRest is the test that matters most here:
// it proves this package isn't a no-op. It reads the raw stored bytes
// directly through the wrapped store (bypassing the wrapper entirely) and
// confirms the plaintext is nowhere in them.
func TestValuesAreActuallyEncryptedAtRest(t *testing.T) {
	s, raw := newTestStores(t)
	ctx := context.Background()

	plaintext := []byte("this is sensitive data, do not leak me")
	if err := s.Set(ctx, []byte("k"), plaintext); err != nil {
		t.Fatalf("set: %v", err)
	}

	stored, err := raw.Get(ctx, []byte("k"))
	if err != nil {
		t.Fatalf("raw get: %v", err)
	}
	if bytes.Contains(stored, plaintext) {
		t.Fatalf("plaintext found in raw stored bytes — encryption did not happen: %q", stored)
	}
	if bytes.Equal(stored, plaintext) {
		t.Fatal("stored value equals plaintext exactly")
	}
}

// TestKeysAreNotEncrypted confirms the one property everything else in
// this package depends on: the key bytes reaching the wrapped store are
// identical to what the caller passed in, unchanged.
func TestKeysAreNotEncrypted(t *testing.T) {
	s, raw := newTestStores(t)
	ctx := context.Background()

	key := []byte("plain-key:123")
	if err := s.Set(ctx, key, []byte("v")); err != nil {
		t.Fatalf("set: %v", err)
	}

	// The exact same key bytes must resolve directly against the raw,
	// wrapped store — if Store transformed the key at all, this would 404.
	if _, err := raw.Get(ctx, key); err != nil {
		t.Fatalf("raw get with the exact original key failed: %v — keys must not be transformed", err)
	}
}

func TestGetNotFound(t *testing.T) {
	s, _ := newTestStores(t)
	_, err := s.Get(context.Background(), []byte("missing"))
	if !errors.Is(err, storagej.ErrNotFound) {
		t.Fatalf("err = %v, want storagej.ErrNotFound", err)
	}
}

// -----------------------------------------------------------------------------
// GetInto
// -----------------------------------------------------------------------------

func TestGetIntoReusesBufferWhenPossible(t *testing.T) {
	s, _ := newTestStores(t)
	ctx := context.Background()
	if err := s.Set(ctx, []byte("k"), []byte("hello")); err != nil {
		t.Fatalf("set: %v", err)
	}

	dst := make([]byte, 0, 32)
	origPtr := &dst[:cap(dst)][0]

	got, err := s.GetInto(ctx, []byte("k"), dst)
	if err != nil {
		t.Fatalf("getinto: %v", err)
	}
	if string(got) != "hello" {
		t.Fatalf("getinto = %q, want %q", got, "hello")
	}
	if &got[:cap(got)][0] != origPtr {
		t.Fatal("GetInto did not reuse dst's backing array despite sufficient capacity")
	}
}

func TestGetIntoGrowsBuffer(t *testing.T) {
	s, _ := newTestStores(t)
	ctx := context.Background()
	val := bytes.Repeat([]byte("x"), 256)
	if err := s.Set(ctx, []byte("k"), val); err != nil {
		t.Fatalf("set: %v", err)
	}

	got, err := s.GetInto(ctx, []byte("k"), make([]byte, 0, 4))
	if err != nil {
		t.Fatalf("getinto: %v", err)
	}
	if !bytes.Equal(got, val) {
		t.Fatal("getinto with grown buffer returned the wrong value")
	}
}

// -----------------------------------------------------------------------------
// Exists / Delete
// -----------------------------------------------------------------------------

func TestExists(t *testing.T) {
	s, _ := newTestStores(t)
	ctx := context.Background()

	ok, err := s.Exists(ctx, []byte("k"))
	if err != nil {
		t.Fatalf("exists on missing key: %v", err)
	}
	if ok {
		t.Fatal("exists = true, want false")
	}

	if err := s.Set(ctx, []byte("k"), []byte("v")); err != nil {
		t.Fatalf("set: %v", err)
	}
	ok, err = s.Exists(ctx, []byte("k"))
	if err != nil {
		t.Fatalf("exists: %v", err)
	}
	if !ok {
		t.Fatal("exists = false, want true")
	}
}

func TestDelete(t *testing.T) {
	s, _ := newTestStores(t)
	ctx := context.Background()
	if err := s.Set(ctx, []byte("k"), []byte("v")); err != nil {
		t.Fatalf("set: %v", err)
	}
	if err := s.Delete(ctx, []byte("k")); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := s.Get(ctx, []byte("k")); !errors.Is(err, storagej.ErrNotFound) {
		t.Fatalf("get after delete err = %v, want ErrNotFound", err)
	}
}

// -----------------------------------------------------------------------------
// Scan / PrefixScan
// -----------------------------------------------------------------------------

func TestScanDecryptsEveryValue(t *testing.T) {
	s, _ := newTestStores(t)
	ctx := context.Background()
	want := map[string]string{"a": "alpha", "b": "beta", "c": "gamma"}
	for k, v := range want {
		if err := s.Set(ctx, []byte(k), []byte(v)); err != nil {
			t.Fatalf("set %s: %v", k, err)
		}
	}

	got := map[string]string{}
	err := s.Scan(ctx, []byte("a"), []byte("z"), func(key, value []byte) error {
		got[string(key)] = string(value)
		return nil
	})
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if len(got) != len(want) {
		t.Fatalf("got %d entries, want %d", len(got), len(want))
	}
	for k, v := range want {
		if got[k] != v {
			t.Fatalf("scan[%s] = %q, want %q", k, got[k], v)
		}
	}
}

func TestPrefixScanDecryptsEveryValue(t *testing.T) {
	s, _ := newTestStores(t)
	ctx := context.Background()
	if err := s.Set(ctx, []byte("post:1"), []byte("first")); err != nil {
		t.Fatalf("set: %v", err)
	}
	if err := s.Set(ctx, []byte("post:2"), []byte("second")); err != nil {
		t.Fatalf("set: %v", err)
	}
	if err := s.Set(ctx, []byte("page:1"), []byte("other")); err != nil {
		t.Fatalf("set: %v", err)
	}

	var got []string
	err := s.PrefixScan(ctx, []byte("post:"), func(key, value []byte) error {
		got = append(got, string(value))
		return nil
	})
	if err != nil {
		t.Fatalf("prefixscan: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d values, want 2 (page:1 should be excluded)", len(got))
	}
}

func TestScanStopIteration(t *testing.T) {
	s, _ := newTestStores(t)
	ctx := context.Background()
	for _, k := range []string{"a", "b", "c", "d"} {
		if err := s.Set(ctx, []byte(k), []byte(k)); err != nil {
			t.Fatalf("set %s: %v", k, err)
		}
	}

	count := 0
	err := s.Scan(ctx, []byte("a"), []byte("z"), func(key, value []byte) error {
		count++
		if count == 2 {
			return storagej.ErrStopIteration
		}
		return nil
	})
	if err != nil {
		t.Fatalf("scan should stop cleanly, got err %v", err)
	}
	if count != 2 {
		t.Fatalf("count = %d, want 2 (early stop)", count)
	}
}

// -----------------------------------------------------------------------------
// Batch
// -----------------------------------------------------------------------------

func TestBatchEncryptsValues(t *testing.T) {
	s, raw := newTestStores(t)
	ctx := context.Background()

	b := s.NewBatch()
	if err := b.Set([]byte("k1"), []byte("v1")); err != nil {
		t.Fatalf("batch set k1: %v", err)
	}
	if err := b.Set([]byte("k2"), []byte("v2")); err != nil {
		t.Fatalf("batch set k2: %v", err)
	}
	if err := b.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}
	b.Close()

	got, err := s.Get(ctx, []byte("k1"))
	if err != nil {
		t.Fatalf("get k1: %v", err)
	}
	if string(got) != "v1" {
		t.Fatalf("get k1 = %q, want %q", got, "v1")
	}

	// And confirm the batch path encrypted too, not just Set.
	stored, err := raw.Get(ctx, []byte("k1"))
	if err != nil {
		t.Fatalf("raw get k1: %v", err)
	}
	if bytes.Contains(stored, []byte("v1")) {
		t.Fatal("batch-written value found in plaintext in the raw store")
	}
}

func TestBatchCount(t *testing.T) {
	s, _ := newTestStores(t)
	b := s.NewBatch()
	defer b.Close()

	b.Set([]byte("a"), []byte("1"))
	b.Set([]byte("b"), []byte("2"))
	b.Delete([]byte("c"))

	if b.Count() != 3 {
		t.Fatalf("count = %d, want 3", b.Count())
	}
}

// -----------------------------------------------------------------------------
// Failure modes
// -----------------------------------------------------------------------------

func TestWrongKeyFailsToDecrypt(t *testing.T) {
	db, err := pebble.OpenMem(storagej.DefaultOptions())
	if err != nil {
		t.Fatalf("openmem: %v", err)
	}
	defer db.Close()

	var keyA, keyB [32]byte
	keyB[0] = 1 // different from keyA's all-zero key

	a, err := New(db, keyA)
	if err != nil {
		t.Fatalf("new a: %v", err)
	}
	b, err := New(db, keyB)
	if err != nil {
		t.Fatalf("new b: %v", err)
	}

	ctx := context.Background()
	if err := a.Set(ctx, []byte("k"), []byte("v")); err != nil {
		t.Fatalf("set: %v", err)
	}

	if _, err := b.Get(ctx, []byte("k")); !errors.Is(err, ErrDecryptionFailed) {
		t.Fatalf("err = %v, want ErrDecryptionFailed", err)
	}
}

func TestDecryptionFailsCleanlyOnCorruptedData(t *testing.T) {
	s, raw := newTestStores(t)
	ctx := context.Background()

	if err := s.Set(ctx, []byte("k"), []byte("v")); err != nil {
		t.Fatalf("set: %v", err)
	}
	// Corrupt the stored ciphertext directly, bypassing the wrapper.
	stored, err := raw.Get(ctx, []byte("k"))
	if err != nil {
		t.Fatalf("raw get: %v", err)
	}
	corrupted := append([]byte{}, stored...)
	corrupted[len(corrupted)-1] ^= 0xFF
	if err := raw.Set(ctx, []byte("k"), corrupted); err != nil {
		t.Fatalf("raw set corrupted: %v", err)
	}

	if _, err := s.Get(ctx, []byte("k")); !errors.Is(err, ErrDecryptionFailed) {
		t.Fatalf("err = %v, want ErrDecryptionFailed", err)
	}
}

// TestDecryptionFailsCleanlyOnUnencryptedValue guards the case where a
// value was written directly through the wrapped store, bypassing this
// package's Set entirely — a real scenario if a store gets this wrapper
// added after it already has plaintext data in it.
func TestDecryptionFailsCleanlyOnUnencryptedValue(t *testing.T) {
	s, raw := newTestStores(t)
	ctx := context.Background()

	if err := raw.Set(ctx, []byte("k"), []byte("never encrypted")); err != nil {
		t.Fatalf("raw set: %v", err)
	}

	if _, err := s.Get(ctx, []byte("k")); !errors.Is(err, ErrDecryptionFailed) {
		t.Fatalf("err = %v, want ErrDecryptionFailed", err)
	}
}

func TestDecryptionFailsCleanlyOnTooShortValue(t *testing.T) {
	s, raw := newTestStores(t)
	ctx := context.Background()

	// Shorter than nonceSize (12) — must not panic on the slice bounds.
	if err := raw.Set(ctx, []byte("k"), []byte("short")); err != nil {
		t.Fatalf("raw set: %v", err)
	}

	if _, err := s.Get(ctx, []byte("k")); !errors.Is(err, ErrDecryptionFailed) {
		t.Fatalf("err = %v, want ErrDecryptionFailed", err)
	}
}

// -----------------------------------------------------------------------------
// Nonce uniqueness
// -----------------------------------------------------------------------------

// TestSameValueEncryptsDifferentlyEachTime confirms every Set gets a fresh
// nonce: writing the identical plaintext twice must not produce identical
// ciphertext, which would leak that the two values are the same without
// ever decrypting either.
func TestSameValueEncryptsDifferentlyEachTime(t *testing.T) {
	s, raw := newTestStores(t)
	ctx := context.Background()

	if err := s.Set(ctx, []byte("k1"), []byte("identical value")); err != nil {
		t.Fatalf("set k1: %v", err)
	}
	if err := s.Set(ctx, []byte("k2"), []byte("identical value")); err != nil {
		t.Fatalf("set k2: %v", err)
	}

	v1, err := raw.Get(ctx, []byte("k1"))
	if err != nil {
		t.Fatalf("raw get k1: %v", err)
	}
	v2, err := raw.Get(ctx, []byte("k2"))
	if err != nil {
		t.Fatalf("raw get k2: %v", err)
	}
	if bytes.Equal(v1, v2) {
		t.Fatal("identical plaintexts produced identical ciphertext — nonce is not varying per Set")
	}
}
