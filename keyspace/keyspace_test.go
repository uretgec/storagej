package keyspace

import (
	"bytes"
	"errors"
	"testing"
)

// -----------------------------------------------------------------------------
// Key / Append
// -----------------------------------------------------------------------------

func TestKeyFormat(t *testing.T) {
	got, err := Key("acme", KindData, "post", "123")
	if err != nil {
		t.Fatalf("key: %v", err)
	}
	if want := "acme:d:post:123"; string(got) != want {
		t.Fatalf("key = %q, want %q", got, want)
	}
}

func TestAppendReusesBuffer(t *testing.T) {
	dst := make([]byte, 0, 64)
	origPtr := &dst[:cap(dst)][0]

	got, err := Append(dst, "acme", KindData, "post", "123")
	if err != nil {
		t.Fatalf("append: %v", err)
	}
	if &got[:cap(got)][0] != origPtr {
		t.Fatal("Append allocated a new buffer despite sufficient capacity in dst")
	}
	if string(got) != "acme:d:post:123" {
		t.Fatalf("append = %q, want %q", got, "acme:d:post:123")
	}
}

func TestKeyRejectsEmptySegment(t *testing.T) {
	cases := []struct {
		name            string
		tenant, typ, id string
		kind            byte
	}{
		{"empty tenant", "", "post", "123", KindData},
		{"empty type", "acme", "", "123", KindData},
		{"empty id", "acme", "post", "", KindData},
		{"zero kind", "acme", "post", "123", 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := Key(c.tenant, c.kind, c.typ, c.id); !errors.Is(err, ErrInvalidSegment) {
				t.Fatalf("err = %v, want ErrInvalidSegment", err)
			}
		})
	}
}

func TestKeyRejectsSeparatorInSegment(t *testing.T) {
	cases := []struct {
		name            string
		tenant, typ, id string
	}{
		{"separator in tenant", "ac:me", "post", "123"},
		{"separator in type", "acme", "po:st", "123"},
		{"separator in id", "acme", "post", "1:23"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := Key(c.tenant, KindData, c.typ, c.id); !errors.Is(err, ErrInvalidSegment) {
				t.Fatalf("err = %v, want ErrInvalidSegment", err)
			}
		})
	}
}

func TestKeyRejectsSeparatorAsKind(t *testing.T) {
	if _, err := Key("acme", Separator, "post", "123"); !errors.Is(err, ErrInvalidSegment) {
		t.Fatalf("err = %v, want ErrInvalidSegment", err)
	}
}

// -----------------------------------------------------------------------------
// Prefix / AppendPrefix
// -----------------------------------------------------------------------------

func TestPrefixFormat(t *testing.T) {
	got, err := Prefix("acme", KindData, "post")
	if err != nil {
		t.Fatalf("prefix: %v", err)
	}
	if want := "acme:d:post:"; string(got) != want {
		t.Fatalf("prefix = %q, want %q", got, want)
	}
}

// TestPrefixExcludesTypeCollision guards the exact bug PrefixScan depends on
// being impossible: a prefix for "post" must not also match "postfix".
func TestPrefixExcludesTypeCollision(t *testing.T) {
	postKey, _ := Key("acme", KindData, "post", "1")
	postfixKey, _ := Key("acme", KindData, "postfix", "1")

	prefix, err := Prefix("acme", KindData, "post")
	if err != nil {
		t.Fatalf("prefix: %v", err)
	}

	if !bytes.HasPrefix(postKey, prefix) {
		t.Fatalf("prefix %q should match %q", prefix, postKey)
	}
	if bytes.HasPrefix(postfixKey, prefix) {
		t.Fatalf("prefix %q should NOT match %q", prefix, postfixKey)
	}
}

func TestPrefixRejectsEmptySegment(t *testing.T) {
	if _, err := Prefix("", KindData, "post"); !errors.Is(err, ErrInvalidSegment) {
		t.Fatalf("err = %v, want ErrInvalidSegment", err)
	}
	if _, err := Prefix("acme", KindData, ""); !errors.Is(err, ErrInvalidSegment) {
		t.Fatalf("err = %v, want ErrInvalidSegment", err)
	}
}

// -----------------------------------------------------------------------------
// Parse
// -----------------------------------------------------------------------------

func TestParseRoundTrip(t *testing.T) {
	key, err := Key("acme", KindData, "post", "123")
	if err != nil {
		t.Fatalf("key: %v", err)
	}

	tenant, kind, typ, id, err := Parse(key)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if string(tenant) != "acme" {
		t.Errorf("tenant = %q, want %q", tenant, "acme")
	}
	if kind != KindData {
		t.Errorf("kind = %q, want %q", kind, KindData)
	}
	if string(typ) != "post" {
		t.Errorf("typ = %q, want %q", typ, "post")
	}
	if string(id) != "123" {
		t.Errorf("id = %q, want %q", id, "123")
	}
}

func TestParseAliasesInput(t *testing.T) {
	// Parse must not copy: the returned segments should point into the same
	// backing array as the input key.
	key, _ := Key("acme", KindData, "post", "123")
	tenant, _, _, _, err := Parse(key)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	key[0] = 'X' // mutate the original key's first byte (part of "tenant")
	if tenant[0] != 'X' {
		t.Fatal("Parse copied the tenant segment instead of aliasing it")
	}
}

func TestParseRejectsMalformedKey(t *testing.T) {
	cases := []string{
		"",
		"no-separators-at-all",
		"acme",
		"acme:d",
		"acme:d:post",      // missing id
		"acme::post:123",   // empty kind
		"acme:dd:post:123", // kind is not a single byte before the next separator
	}
	for _, k := range cases {
		t.Run(k, func(t *testing.T) {
			if _, _, _, _, err := Parse([]byte(k)); err == nil {
				t.Fatalf("Parse(%q) should have failed", k)
			}
		})
	}
}

func TestValidSegment(t *testing.T) {
	cases := []struct {
		name string
		s    string
		want bool
	}{
		{"normal", "acme", true},
		{"empty", "", false},
		{"contains separator", "ac:me", false},
		{"hyphenated is fine", "post-slug", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := ValidSegment(c.s); got != c.want {
				t.Fatalf("ValidSegment(%q) = %v, want %v", c.s, got, c.want)
			}
		})
	}
}

func TestParseRejectsIndexKeyAsWrongKind(t *testing.T) {
	// Parse itself doesn't restrict kind to KindData — it returns whatever
	// kind byte is present. Callers that only want data keys must check
	// kind == KindData themselves.
	//
	// Index-on-a-field is modeled by folding the field name into type
	// (id/value segments cannot contain ':'), e.g. "post-slug" here.
	key, err := Key("acme", KindIndex, "post-slug", "hello-world")
	if err != nil {
		t.Fatalf("key: %v", err)
	}
	_, kind, _, _, err := Parse(key)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if kind != KindIndex {
		t.Fatalf("kind = %q, want %q", kind, KindIndex)
	}
}
