// Package encrypted wraps a storagej.Store, transparently encrypting
// values with AES-256-GCM before they reach the underlying store and
// decrypting them on the way back out. It's an optional, opt-in layer —
// nothing else in this toolkit imports it, and importing it pulls in only
// the Go standard library's crypto/* packages, no new dependency.
package encrypted

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"errors"
	"fmt"
	"io"

	"github.com/uretgec/storagej/v2"
)

// nonceSize is AES-GCM's standard nonce length.
const nonceSize = 12

// ErrDecryptionFailed is returned when a stored value can't be decrypted:
// wrong key, corrupted data, or a value nothing in this package ever wrote
// (e.g. it was Set directly through the wrapped Store, bypassing
// encryption). AES-GCM's authentication tag catches all three the same
// way — there's no way to tell them apart from the ciphertext alone, and
// no attempt is made to.
var ErrDecryptionFailed = errors.New("encrypted: decryption failed (wrong key, corrupted data, or an unencrypted value)")

// Store wraps a storagej.Store, encrypting/decrypting values transparently.
// It implements storagej.Store itself, so it's a drop-in replacement
// anywhere a Store is expected — keyspace, repository, and everything
// built on those can use one without any code of theirs knowing it's there.
//
// Keys are NEVER encrypted — only values. This is a hard design
// requirement, not an oversight: storagej's ordering guarantees
// (keyspace's sortable ids, Scan/PrefixScan's sorted iteration) all depend
// on keys sorting as plain bytes. Encrypting them would silently break
// every one of those guarantees. If key content itself needs to be
// hidden, that's a different and harder problem — out of scope here.
//
// Key management is entirely the caller's responsibility: Store takes a
// raw 32-byte AES-256 key and does nothing else with it. No rotation, no
// derivation, no storage of the key anywhere. Generate it with
// crypto/rand and keep it somewhere this package never sees — an
// environment variable, a secrets manager, a KMS.
//
// What this does NOT hide: value length (ciphertext is plaintext length
// plus a fixed overhead, so anyone who can read the raw stored bytes can
// estimate a value's size) and, obviously, keys and their access pattern
// (which keys exist, how often each is read/written). It hides value
// *content* from anyone who can read the underlying store's raw bytes but
// doesn't have the key — that's the whole and only guarantee.
type Store struct {
	inner storagej.Store
	aead  cipher.AEAD
}

// Compile-time check that *Store satisfies storagej.Store.
var _ storagej.Store = (*Store)(nil)

// New wraps inner, encrypting/decrypting values with key (AES-256, so
// exactly 32 bytes — enforced by the parameter's type, not a runtime
// length check: there's no way to call New with a wrong-size key that
// compiles).
func New(inner storagej.Store, key [32]byte) (*Store, error) {
	block, err := aes.NewCipher(key[:])
	if err != nil {
		// Unreachable in practice — aes.NewCipher only rejects key lengths
		// other than 16/24/32, and key's type guarantees 32 — but a crypto
		// constructor's error is not one to silently discard on the
		// off chance the standard library's behavior ever changes under us.
		return nil, fmt.Errorf("encrypted: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("encrypted: %w", err) // also unreachable today, same reasoning
	}
	return &Store{inner: inner, aead: aead}, nil
}

// encrypt returns nonce||seal(plaintext) as one []byte: a fresh random
// nonce (crypto/rand — reusing a nonce under the same key breaks AES-GCM's
// guarantees entirely, so every encrypt call gets its own) followed by
// AEAD's sealed output, which already includes the authentication tag.
func (s *Store) encrypt(plaintext []byte) ([]byte, error) {
	nonce := make([]byte, nonceSize, nonceSize+len(plaintext)+s.aead.Overhead())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, fmt.Errorf("encrypted: generate nonce: %w", err)
	}
	return s.aead.Seal(nonce, nonce, plaintext, nil), nil
}

// decrypt is encrypt's inverse.
func (s *Store) decrypt(stored []byte) ([]byte, error) {
	if len(stored) < nonceSize {
		return nil, ErrDecryptionFailed
	}
	nonce, ciphertext := stored[:nonceSize], stored[nonceSize:]
	plaintext, err := s.aead.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return nil, ErrDecryptionFailed
	}
	return plaintext, nil
}

// Get decrypts the value stored under key, or returns storagej.ErrNotFound
// (unchanged from the wrapped Store) or ErrDecryptionFailed.
func (s *Store) Get(ctx context.Context, key []byte) ([]byte, error) {
	stored, err := s.inner.Get(ctx, key)
	if err != nil {
		return nil, err
	}
	return s.decrypt(stored)
}

// GetInto decrypts into dst when its capacity allows, same contract as
// storagej.Store.GetInto. One honest caveat: decryption can't write
// directly into dst the way a plain Get can — the stored (encrypted) value
// has to be read and authenticated first — so this doesn't carry the
// wrapped Store's own GetInto forward at zero extra allocation. It still
// avoids the caller managing their own decrypt buffer, which is the part
// of GetInto's contract that still holds here.
func (s *Store) GetInto(ctx context.Context, key, dst []byte) ([]byte, error) {
	stored, err := s.inner.Get(ctx, key)
	if err != nil {
		return nil, err
	}
	plaintext, err := s.decrypt(stored)
	if err != nil {
		return nil, err
	}
	if cap(dst) < len(plaintext) {
		dst = make([]byte, len(plaintext))
	}
	dst = dst[:len(plaintext)]
	copy(dst, plaintext)
	return dst, nil
}

// Exists delegates directly to the wrapped Store. Keys are never
// encrypted, so this needs no decryption at all — it costs exactly what
// the wrapped Store's own Exists costs (0 allocations on pebble.Store).
func (s *Store) Exists(ctx context.Context, key []byte) (bool, error) {
	return s.inner.Exists(ctx, key)
}

// Set encrypts value and writes it under the unmodified key.
func (s *Store) Set(ctx context.Context, key, value []byte) error {
	encrypted, err := s.encrypt(value)
	if err != nil {
		return err
	}
	return s.inner.Set(ctx, key, encrypted)
}

// Delete removes key. Keys aren't encrypted, so there's nothing to
// transform — this is a direct passthrough.
func (s *Store) Delete(ctx context.Context, key []byte) error {
	return s.inner.Delete(ctx, key)
}

// Scan decrypts each value before calling fn — everything else (sorted
// order, ErrStopIteration to stop early) is exactly storagej.Store.Scan's
// contract, unchanged, since keys and iteration order are untouched by
// this wrapper.
func (s *Store) Scan(ctx context.Context, start, end []byte, fn func(key, value []byte) error) error {
	return s.inner.Scan(ctx, start, end, func(key, value []byte) error {
		plaintext, err := s.decrypt(value)
		if err != nil {
			return err
		}
		return fn(key, plaintext)
	})
}

// PrefixScan is Scan's prefix-bounded equivalent, same as on
// storagej.Store.
func (s *Store) PrefixScan(ctx context.Context, prefix []byte, fn func(key, value []byte) error) error {
	return s.inner.PrefixScan(ctx, prefix, func(key, value []byte) error {
		plaintext, err := s.decrypt(value)
		if err != nil {
			return err
		}
		return fn(key, plaintext)
	})
}

// NewBatch starts a batch that encrypts values on Set, same as Store.Set,
// before delegating to the wrapped Store's own batch.
func (s *Store) NewBatch() storagej.Batch {
	return &batch{inner: s.inner.NewBatch(), store: s}
}

// Close releases the wrapped Store. Store holds no resources of its own
// beyond the cipher (which needs no cleanup).
func (s *Store) Close() error {
	return s.inner.Close()
}

// batch encrypts values before delegating to the wrapped Store's own
// Batch. Not safe for concurrent use — same contract as storagej.Batch.
type batch struct {
	inner storagej.Batch
	store *Store
}

var _ storagej.Batch = (*batch)(nil)

func (b *batch) Set(key, value []byte) error {
	encrypted, err := b.store.encrypt(value)
	if err != nil {
		return err
	}
	return b.inner.Set(key, encrypted)
}

func (b *batch) Delete(key []byte) error {
	return b.inner.Delete(key)
}

func (b *batch) Commit() error {
	return b.inner.Commit()
}

func (b *batch) Count() int {
	return b.inner.Count()
}

func (b *batch) Close() error {
	return b.inner.Close()
}
