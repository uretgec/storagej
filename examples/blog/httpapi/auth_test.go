package httpapi

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func reqWithBearer(token string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	return r
}

func TestAPIKeyAuthAuthenticate(t *testing.T) {
	a := NewAPIKeyAuth(map[string]string{"acme": "secret-1"})

	if !a.Authenticate("acme", reqWithBearer("secret-1")) {
		t.Fatal("correct tenant+key should authenticate")
	}
	if a.Authenticate("acme", reqWithBearer("wrong")) {
		t.Fatal("wrong key should not authenticate")
	}
	if a.Authenticate("acme", reqWithBearer("")) {
		t.Fatal("missing key should not authenticate")
	}
	if a.Authenticate("unknown-tenant", reqWithBearer("secret-1")) {
		t.Fatal("unprovisioned tenant should not authenticate, even with a valid key for someone else")
	}
}

func TestAPIKeyAuthNilAndEmptyMap(t *testing.T) {
	a := NewAPIKeyAuth(nil)
	if a.Authenticate("acme", reqWithBearer("anything")) {
		t.Fatal("an APIKeyAuth built from nil should authenticate nothing")
	}
}

func TestAPIKeyAuthSet(t *testing.T) {
	a := NewAPIKeyAuth(nil)
	a.Set("acme", "new-secret")

	if !a.Authenticate("acme", reqWithBearer("new-secret")) {
		t.Fatal("key added via Set should authenticate")
	}
}

func TestAPIKeyAuthSetOverwrites(t *testing.T) {
	a := NewAPIKeyAuth(map[string]string{"acme": "old-secret"})
	a.Set("acme", "new-secret")

	if a.Authenticate("acme", reqWithBearer("old-secret")) {
		t.Fatal("old key should no longer authenticate after Set overwrites it")
	}
	if !a.Authenticate("acme", reqWithBearer("new-secret")) {
		t.Fatal("new key should authenticate")
	}
}

func TestAPIKeyAuthRevoke(t *testing.T) {
	a := NewAPIKeyAuth(map[string]string{"acme": "secret-1"})
	a.Revoke("acme")

	if a.Authenticate("acme", reqWithBearer("secret-1")) {
		t.Fatal("revoked tenant should not authenticate even with its former key")
	}
}

func TestAPIKeyAuthRevokeUnknownTenantIsNoop(t *testing.T) {
	a := NewAPIKeyAuth(map[string]string{"acme": "secret-1"})
	a.Revoke("never-existed") // must not panic

	if !a.Authenticate("acme", reqWithBearer("secret-1")) {
		t.Fatal("revoking an unrelated tenant should not affect acme")
	}
}

func TestBearerTokenExtraction(t *testing.T) {
	cases := []struct {
		name   string
		header string
		want   string
	}{
		{"well formed", "Bearer abc123", "abc123"},
		{"missing", "", ""},
		{"wrong scheme", "Basic abc123", ""},
		{"empty token after Bearer", "Bearer ", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "/", nil)
			if c.header != "" {
				r.Header.Set("Authorization", c.header)
			}
			if got := bearerToken(r); got != c.want {
				t.Fatalf("bearerToken(%q) = %q, want %q", c.header, got, c.want)
			}
		})
	}
}
