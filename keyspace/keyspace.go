// Package keyspace turns structured identifiers (tenant, kind, type, id) into
// sortable byte keys for storagej.Store, and back again. It has no dependency
// on storagej itself — it is a standalone byte-key codec that any
// storagej.Store-shaped backend can use.
//
// Layout: {tenant}:{kind}:{type}:{id}
//
//	tenant  arbitrary namespace, e.g. a site or account identifier
//	kind    single-byte discriminator, e.g. 'd' for data, 'i' for index
//	type    the entity type, e.g. "post", "page"
//	id      the entity identifier, typically a keyspace.NewID() value
//
// Segments are joined with Separator (':'). None of the four segments may be
// empty or contain the separator byte — Append and Prefix reject them with
// ErrInvalidSegment rather than silently producing an ambiguous key.
package keyspace

import (
	"bytes"
	"errors"
	"strings"
)

// Separator joins key segments. Exported so callers building or inspecting
// keys outside this package (e.g. custom scans) can rely on the same byte.
const Separator = ':'

const (
	// KindData marks a primary data record: {tenant}:d:{type}:{id}.
	KindData byte = 'd'
	// KindIndex marks a secondary index entry: {tenant}:i:{type}:{value}.
	// Since id/value segments cannot contain ':', an index on a specific
	// field is modeled by folding the field name into type, e.g.
	// Key(tenant, KindIndex, "post-slug", "hello-world") for a slug index on
	// posts. The value stored at that key is the id of the data record it
	// points at.
	KindIndex byte = 'i'
)

// ErrInvalidSegment is returned when a key segment is empty, contains the
// separator byte, or (for kind) equals the separator or the zero byte — any
// of which would make the resulting key ambiguous to parse or scan.
var ErrInvalidSegment = errors.New("keyspace: segment must be non-empty and must not contain the separator")

// Append builds a key of the form tenant:kind:type:id and appends it to dst,
// returning the extended slice. It reuses dst's underlying array when there
// is enough capacity, so a caller reusing a buffer across calls (e.g. inside
// a batch-write loop) pays no allocation.
func Append(dst []byte, tenant string, kind byte, typ, id string) ([]byte, error) {
	if !validSegment(tenant) || !validSegment(typ) || !validSegment(id) || !validKind(kind) {
		return dst, ErrInvalidSegment
	}
	dst = append(dst, tenant...)
	dst = append(dst, Separator, kind, Separator)
	dst = append(dst, typ...)
	dst = append(dst, Separator)
	dst = append(dst, id...)
	return dst, nil
}

// Key is a convenience wrapper around Append that allocates a fresh slice
// sized exactly for the result.
func Key(tenant string, kind byte, typ, id string) ([]byte, error) {
	size := len(tenant) + len(typ) + len(id) + 4 // 3 separators + 1 kind byte
	return Append(make([]byte, 0, size), tenant, kind, typ, id)
}

// AppendPrefix builds the tenant:kind:type: prefix shared by every id of a
// given type, and appends it to dst. Pair it with storagej.Store.PrefixScan
// to iterate all entities of one type.
func AppendPrefix(dst []byte, tenant string, kind byte, typ string) ([]byte, error) {
	if !validSegment(tenant) || !validSegment(typ) || !validKind(kind) {
		return dst, ErrInvalidSegment
	}
	dst = append(dst, tenant...)
	dst = append(dst, Separator, kind, Separator)
	dst = append(dst, typ...)
	dst = append(dst, Separator)
	return dst, nil
}

// Prefix is a convenience wrapper around AppendPrefix that allocates a fresh
// slice.
func Prefix(tenant string, kind byte, typ string) ([]byte, error) {
	size := len(tenant) + len(typ) + 4 // 3 separators + 1 kind byte
	return AppendPrefix(make([]byte, 0, size), tenant, kind, typ)
}

// Parse splits a key produced by Key/Append back into its four segments. The
// returned slices alias key — they are valid only as long as key itself is.
// This matters when parsing a key handed to a storagej.Store.Scan callback:
// Pebble's iterator reuses that memory on the next step, so consume the
// segments before the callback returns, or copy them first.
func Parse(key []byte) (tenant []byte, kind byte, typ, id []byte, err error) {
	i := bytes.IndexByte(key, Separator)
	if i < 0 {
		return nil, 0, nil, nil, ErrInvalidSegment
	}
	tenant, rest := key[:i], key[i+1:]

	if len(rest) < 2 || rest[1] != Separator {
		return nil, 0, nil, nil, ErrInvalidSegment
	}
	kind, rest = rest[0], rest[2:]

	i = bytes.IndexByte(rest, Separator)
	if i < 0 {
		return nil, 0, nil, nil, ErrInvalidSegment
	}
	typ, id = rest[:i], rest[i+1:]

	if len(tenant) == 0 || len(typ) == 0 || len(id) == 0 || !validKind(kind) {
		return nil, 0, nil, nil, ErrInvalidSegment
	}
	return tenant, kind, typ, id, nil
}

func validSegment(s string) bool {
	return len(s) > 0 && strings.IndexByte(s, Separator) < 0
}

// ValidSegment reports whether s is usable as a tenant, type, or id
// segment: non-empty and free of the separator byte. Exported so callers
// that validate a segment before it reaches Key/Append/Prefix — e.g.
// httpapi rejecting a bad tenant from a URL path with a precise 400 instead
// of letting ErrInvalidSegment surface from deep in the key-building code —
// can reuse the exact same rule instead of duplicating it.
func ValidSegment(s string) bool {
	return validSegment(s)
}

func validKind(k byte) bool {
	return k != 0 && k != Separator
}
