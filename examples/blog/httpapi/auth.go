package httpapi

import (
	"crypto/subtle"
	"net/http"
	"strings"
	"sync"
)

// APIKeyAuth authenticates multi-tenant requests against a per-tenant API
// key: the Authorization: Bearer <key> header must match the key
// registered for the tenant named in the URL. This is the
// "framework-free, minimal dependency" choice for auth here — no JWT
// library, no external identity provider, just a comparison against a key
// the operator issued out of band.
//
// Wiring one into MountMultiTenant (via MountOptions) also resolves the
// two caveats MountMultiTenant's own doc comment raises about running
// without access control: only tenants with an issued key can be reached
// at all, and the per-tenant repository cache can no longer be grown by
// hitting arbitrary made-up tenant names.
type APIKeyAuth struct {
	mu   sync.RWMutex
	keys map[string]string // tenant -> key
}

// NewAPIKeyAuth returns an APIKeyAuth seeded with keys (tenant -> key). A
// nil or empty map is valid — it starts out authenticating nothing until
// Set is called.
func NewAPIKeyAuth(keys map[string]string) *APIKeyAuth {
	a := &APIKeyAuth{keys: make(map[string]string, len(keys))}
	for tenant, key := range keys {
		a.keys[tenant] = key
	}
	return a
}

// Set issues (or replaces) the key for tenant. Safe to call concurrently
// with requests being served — e.g. to provision a new tenant without a
// restart.
func (a *APIKeyAuth) Set(tenant, key string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.keys[tenant] = key
}

// Revoke removes tenant's key; subsequent requests for it are rejected
// until Set is called again.
func (a *APIKeyAuth) Revoke(tenant string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	delete(a.keys, tenant)
}

// Authenticate reports whether r carries the correct key for tenant.
func (a *APIKeyAuth) Authenticate(tenant string, r *http.Request) bool {
	a.mu.RLock()
	want, ok := a.keys[tenant]
	a.mu.RUnlock()
	if !ok || want == "" {
		return false
	}

	got := bearerToken(r)
	if got == "" {
		return false
	}
	// Constant-time comparison: a naive == would let a timing attack
	// narrow the key down one byte at a time.
	return subtle.ConstantTimeCompare([]byte(got), []byte(want)) == 1
}

// bearerToken extracts token from an "Authorization: Bearer <token>"
// header, or "" if the header is missing or doesn't use that scheme.
func bearerToken(r *http.Request) string {
	const prefix = "Bearer "
	h := r.Header.Get("Authorization")
	if !strings.HasPrefix(h, prefix) {
		return ""
	}
	return strings.TrimPrefix(h, prefix)
}
