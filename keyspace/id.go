// id.go implements a sortable, time-ordered identifier: a 48-bit millisecond
// timestamp followed by 80 bits of cryptographically random entropy,
// Crockford base32 encoded into a 26-character string. Keys built with these
// ids sort lexicographically in creation order, so a PrefixScan over a type
// naturally walks entities oldest-first with no separate secondary index.
//
// The text encoding is compatible with the well-known ULID spec
// (github.com/ulid/spec), but the codec is implemented from scratch here to
// keep storagej's dependency graph at just Pebble.
//
// IDs generated within the same millisecond are NOT guaranteed to sort
// relative to each other (their entropy bytes are independent random draws).
// That's an accepted tradeoff: a monotonic-within-millisecond counter would
// need to hold mutable state across calls for a guarantee that a blog's
// write volume will never actually need.
package keyspace

import (
	"crypto/rand"
	"errors"
	"time"
)

// idEncoding is Crockford's base32 alphabet: no I, L, O, U, to avoid visual
// ambiguity between characters.
const idEncoding = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

// ErrInvalidID is returned when decoding a string that isn't a valid
// keyspace id (wrong length, or a character outside the encoding alphabet).
var ErrInvalidID = errors.New("keyspace: invalid id")

// NewID returns a new sortable, 26-character identifier.
//
// The only failure mode is the OS entropy source being unavailable, which
// crypto/rand itself treats as unrecoverable. NewID panics in that case
// rather than pushing an unchecked error return onto every caller for a
// condition that does not happen on a running system.
func NewID() string {
	return newID(time.Now())
}

// newID is the deterministic core of NewID, split out so tests can supply a
// fixed timestamp instead of depending on the wall clock.
func newID(t time.Time) string {
	var raw [16]byte

	ms := uint64(t.UnixMilli())
	raw[0] = byte(ms >> 40)
	raw[1] = byte(ms >> 32)
	raw[2] = byte(ms >> 24)
	raw[3] = byte(ms >> 16)
	raw[4] = byte(ms >> 8)
	raw[5] = byte(ms)

	if _, err := rand.Read(raw[6:]); err != nil {
		panic("keyspace: crypto/rand unavailable: " + err.Error())
	}

	return encode(raw)
}

// Time extracts the creation timestamp embedded in an id produced by NewID.
// This lets a repository layer sort/filter by creation time using only the
// id — no separate created_at field or index required.
func Time(id string) (time.Time, error) {
	raw, err := decode(id)
	if err != nil {
		return time.Time{}, err
	}
	ms := uint64(raw[0])<<40 | uint64(raw[1])<<32 | uint64(raw[2])<<24 |
		uint64(raw[3])<<16 | uint64(raw[4])<<8 | uint64(raw[5])
	return time.UnixMilli(int64(ms)), nil
}

// encode base32-encodes 16 raw bytes into the 26-character text form.
func encode(id [16]byte) string {
	dst := make([]byte, 26)

	dst[0] = idEncoding[(id[0]&224)>>5]
	dst[1] = idEncoding[id[0]&31]
	dst[2] = idEncoding[(id[1]&248)>>3]
	dst[3] = idEncoding[((id[1]&7)<<2)|((id[2]&192)>>6)]
	dst[4] = idEncoding[(id[2]&62)>>1]
	dst[5] = idEncoding[((id[2]&1)<<4)|((id[3]&240)>>4)]
	dst[6] = idEncoding[((id[3]&15)<<1)|((id[4]&128)>>7)]
	dst[7] = idEncoding[(id[4]&124)>>2]
	dst[8] = idEncoding[((id[4]&3)<<3)|((id[5]&224)>>5)]
	dst[9] = idEncoding[id[5]&31]

	dst[10] = idEncoding[(id[6]&248)>>3]
	dst[11] = idEncoding[((id[6]&7)<<2)|((id[7]&192)>>6)]
	dst[12] = idEncoding[(id[7]&62)>>1]
	dst[13] = idEncoding[((id[7]&1)<<4)|((id[8]&240)>>4)]
	dst[14] = idEncoding[((id[8]&15)<<1)|((id[9]&128)>>7)]
	dst[15] = idEncoding[(id[9]&124)>>2]
	dst[16] = idEncoding[((id[9]&3)<<3)|((id[10]&224)>>5)]
	dst[17] = idEncoding[id[10]&31]
	dst[18] = idEncoding[(id[11]&248)>>3]
	dst[19] = idEncoding[((id[11]&7)<<2)|((id[12]&192)>>6)]
	dst[20] = idEncoding[(id[12]&62)>>1]
	dst[21] = idEncoding[((id[12]&1)<<4)|((id[13]&240)>>4)]
	dst[22] = idEncoding[((id[13]&15)<<1)|((id[14]&128)>>7)]
	dst[23] = idEncoding[(id[14]&124)>>2]
	dst[24] = idEncoding[((id[14]&3)<<3)|((id[15]&224)>>5)]
	dst[25] = idEncoding[id[15]&31]

	return string(dst)
}

// decodeTable maps each ASCII byte to its 5-bit value in idEncoding, or 0xFF
// if the byte is not a valid character.
var decodeTable = buildDecodeTable()

func buildDecodeTable() [256]byte {
	var t [256]byte
	for i := range t {
		t[i] = 0xFF
	}
	for i := 0; i < len(idEncoding); i++ {
		c := idEncoding[i]
		t[c] = byte(i)
		if c >= 'A' && c <= 'Z' {
			t[c+('a'-'A')] = byte(i) // accept lowercase on decode
		}
	}
	return t
}

// decode parses the 26-character text form back into 16 raw bytes. Each
// output line is the algebraic inverse of the matching line in encode.
func decode(s string) ([16]byte, error) {
	var raw [16]byte
	if len(s) != 26 {
		return raw, ErrInvalidID
	}

	var b [26]byte
	for i := 0; i < 26; i++ {
		v := decodeTable[s[i]]
		if v == 0xFF {
			return raw, ErrInvalidID
		}
		b[i] = v
	}

	raw[0] = (b[0] << 5) | b[1]
	raw[1] = (b[2] << 3) | (b[3] >> 2)
	raw[2] = (b[3] << 6) | (b[4] << 1) | (b[5] >> 4)
	raw[3] = (b[5] << 4) | (b[6] >> 1)
	raw[4] = (b[6] << 7) | (b[7] << 2) | (b[8] >> 3)
	raw[5] = (b[8] << 5) | b[9]

	raw[6] = (b[10] << 3) | (b[11] >> 2)
	raw[7] = (b[11] << 6) | (b[12] << 1) | (b[13] >> 4)
	raw[8] = (b[13] << 4) | (b[14] >> 1)
	raw[9] = (b[14] << 7) | (b[15] << 2) | (b[16] >> 3)
	raw[10] = (b[16] << 5) | b[17]
	raw[11] = (b[18] << 3) | (b[19] >> 2)
	raw[12] = (b[19] << 6) | (b[20] << 1) | (b[21] >> 4)
	raw[13] = (b[21] << 4) | (b[22] >> 1)
	raw[14] = (b[22] << 7) | (b[23] << 2) | (b[24] >> 3)
	raw[15] = (b[24] << 5) | b[25]

	return raw, nil
}
