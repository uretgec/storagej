package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/uretgec/storagej/v2"
	"github.com/uretgec/storagej/v2/examples/blog/blog"
	"github.com/uretgec/storagej/v2/pebble"
)

func newMultiTenantTestServer(t *testing.T) *Server {
	t.Helper()
	db, err := pebble.Open(t.TempDir(), storagej.DefaultOptions())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	s := NewServer()
	MountMultiTenant[blog.Post, *blog.Post](s, "posts", db, blog.PostType, MountOptions{})
	return s
}

func createTenantPost(t *testing.T, s *Server, tenant, body string) *blog.Post {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/t/"+tenant+"/posts", strings.NewReader(body))
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create post for tenant %q failed: status=%d body=%s", tenant, rec.Code, rec.Body.String())
	}
	var p blog.Post
	if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil {
		t.Fatalf("decode create response: %v", err)
	}
	return &p
}

// TestMultiTenantRoutingIsolatesTenants is the HTTP-layer version of
// blog's TestTenantIsolation: two tenants sharing the same slug must not
// collide, and each tenant's list must only ever see its own data.
func TestMultiTenantRoutingIsolatesTenants(t *testing.T) {
	s := newMultiTenantTestServer(t)

	acme := createTenantPost(t, s, "acme", `{"slug":"hello","title":"Acme Post"}`)
	other := createTenantPost(t, s, "other", `{"slug":"hello","title":"Other Post"}`)

	if acme.ID == other.ID {
		t.Fatalf("acme and other got the same id: %s", acme.ID)
	}

	req := httptest.NewRequest(http.MethodGet, "/t/acme/posts", nil)
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)

	var resp listResponse[blog.Post]
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Items) != 1 || resp.Items[0].Title != "Acme Post" {
		t.Fatalf("acme's list = %+v, want just the Acme post", resp.Items)
	}
}

func TestMultiTenantGetByIDScopedToTenant(t *testing.T) {
	s := newMultiTenantTestServer(t)
	created := createTenantPost(t, s, "acme", `{"slug":"hello","title":"T"}`)

	req := httptest.NewRequest(http.MethodGet, "/t/acme/posts/"+created.ID, nil)
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("same-tenant get status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	// The same id under a different tenant must not resolve — ids aren't
	// shared across tenant namespaces.
	req2 := httptest.NewRequest(http.MethodGet, "/t/other/posts/"+created.ID, nil)
	rec2 := httptest.NewRecorder()
	s.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusNotFound {
		t.Fatalf("cross-tenant get status = %d, want %d", rec2.Code, http.StatusNotFound)
	}
}

// TestMultiTenantUnseenTenantStartsEmpty confirms a tenant nobody has
// written to yet is not an error — it's just an empty, freshly-usable
// namespace, matching the "no pre-registration needed" design.
func TestMultiTenantUnseenTenantStartsEmpty(t *testing.T) {
	s := newMultiTenantTestServer(t)

	req := httptest.NewRequest(http.MethodGet, "/t/brand-new-tenant/posts", nil)
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	var resp listResponse[blog.Post]
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Items) != 0 {
		t.Fatalf("items = %v, want empty for an unseen tenant", resp.Items)
	}
}

// TestMultiTenantInvalidTenantReturns400 uses a URL-encoded ':' in the
// tenant segment (decoded by net/http before r.PathValue sees it) to
// exercise keyspace.ValidSegment's rejection through the HTTP layer,
// rather than letting it surface as an opaque 500 from deep in the key
// builder.
func TestMultiTenantInvalidTenantReturns400(t *testing.T) {
	s := newMultiTenantTestServer(t)

	req := httptest.NewRequest(http.MethodGet, "/t/a%3Ab/posts", nil)
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
}

// TestMultiTenantConcurrentTenantCreationIsRaceFree fires many goroutines
// at brand-new, distinct tenants simultaneously — the scenario that would
// trip the race detector if MountMultiTenant's per-tenant repo cache weren't
// protected by its mutex. Run with -race.
func TestMultiTenantConcurrentTenantCreationIsRaceFree(t *testing.T) {
	s := newMultiTenantTestServer(t)

	const n = 20
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			tenant := fmt.Sprintf("tenant-%d", i)
			createTenantPost(t, s, tenant, `{"slug":"hello","title":"T"}`)
		}(i)
	}
	wg.Wait()

	// Spot-check a couple of tenants actually landed correctly.
	for _, tenant := range []string{"tenant-0", "tenant-19"} {
		req := httptest.NewRequest(http.MethodGet, "/t/"+tenant+"/posts", nil)
		rec := httptest.NewRecorder()
		s.ServeHTTP(rec, req)
		var resp listResponse[blog.Post]
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatalf("decode %s: %v", tenant, err)
		}
		if len(resp.Items) != 1 {
			t.Fatalf("%s has %d items, want 1", tenant, len(resp.Items))
		}
	}
}

// -----------------------------------------------------------------------------
// MountOptions.Auth
// -----------------------------------------------------------------------------

func newAuthedTestServer(t *testing.T, keys map[string]string) *Server {
	t.Helper()
	db, err := pebble.Open(t.TempDir(), storagej.DefaultOptions())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	s := NewServer()
	MountMultiTenant[blog.Post, *blog.Post](s, "posts", db, blog.PostType, MountOptions{
		Auth: NewAPIKeyAuth(keys),
	})
	return s
}

func TestMultiTenantAuthRejectsMissingKey(t *testing.T) {
	s := newAuthedTestServer(t, map[string]string{"acme": "secret-1"})

	req := httptest.NewRequest(http.MethodGet, "/t/acme/posts", nil)
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusUnauthorized, rec.Body.String())
	}
	if got := rec.Header().Get("WWW-Authenticate"); got != "Bearer" {
		t.Fatalf("WWW-Authenticate = %q, want %q", got, "Bearer")
	}
}

func TestMultiTenantAuthRejectsWrongKey(t *testing.T) {
	s := newAuthedTestServer(t, map[string]string{"acme": "secret-1"})

	req := httptest.NewRequest(http.MethodGet, "/t/acme/posts", nil)
	req.Header.Set("Authorization", "Bearer wrong-key")
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusUnauthorized)
	}
}

// TestMultiTenantAuthRejectsCrossTenantKey guards the actual point of
// per-tenant keys: a key that's valid for "acme" must not also unlock
// "other" — otherwise every tenant would effectively share one key.
func TestMultiTenantAuthRejectsCrossTenantKey(t *testing.T) {
	s := newAuthedTestServer(t, map[string]string{
		"acme":  "acme-secret",
		"other": "other-secret",
	})

	req := httptest.NewRequest(http.MethodGet, "/t/other/posts", nil)
	req.Header.Set("Authorization", "Bearer acme-secret")
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d (acme's key must not work for other)", rec.Code, http.StatusUnauthorized)
	}
}

func TestMultiTenantAuthAcceptsCorrectKey(t *testing.T) {
	s := newAuthedTestServer(t, map[string]string{"acme": "secret-1"})

	req := httptest.NewRequest(http.MethodPost, "/t/acme/posts", strings.NewReader(`{"slug":"hi","title":"Hi"}`))
	req.Header.Set("Authorization", "Bearer secret-1")
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusCreated, rec.Body.String())
	}
}

// TestMultiTenantAuthRejectsUnprovisionedTenant confirms the cache-growth
// fix MountMultiTenant's doc comment claims: a tenant with no issued key at
// all must be rejected, not silently given a fresh empty namespace.
func TestMultiTenantAuthRejectsUnprovisionedTenant(t *testing.T) {
	s := newAuthedTestServer(t, map[string]string{"acme": "secret-1"})

	req := httptest.NewRequest(http.MethodGet, "/t/never-provisioned/posts", nil)
	req.Header.Set("Authorization", "Bearer secret-1") // even acme's real key
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusUnauthorized)
	}
}

func TestAPIKeyAuthSetAndRevoke(t *testing.T) {
	db, err := pebble.Open(t.TempDir(), storagej.DefaultOptions())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	// Keep a direct reference to the *APIKeyAuth passed into MountOptions
	// so Set/Revoke below actually mutate the instance the server is using
	// — a helper that builds its own disconnected APIKeyAuth wouldn't.
	auth := NewAPIKeyAuth(nil)
	s := NewServer()
	MountMultiTenant[blog.Post, *blog.Post](s, "posts", db, blog.PostType, MountOptions{Auth: auth})

	req := httptest.NewRequest(http.MethodGet, "/t/acme/posts", nil)
	req.Header.Set("Authorization", "Bearer late-secret")

	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("before Set: status = %d, want %d", rec.Code, http.StatusUnauthorized)
	}

	auth.Set("acme", "late-secret")

	rec = httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("after Set: status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	auth.Revoke("acme")

	rec = httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("after Revoke: status = %d, want %d", rec.Code, http.StatusUnauthorized)
	}
}

// -----------------------------------------------------------------------------
// MountOptions.Limiter
// -----------------------------------------------------------------------------

func TestMultiTenantRateLimitBlocksAfterBurst(t *testing.T) {
	db, err := pebble.Open(t.TempDir(), storagej.DefaultOptions())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	s := NewServer()
	MountMultiTenant[blog.Post, *blog.Post](s, "posts", db, blog.PostType, MountOptions{
		Limiter: NewRateLimiter(0, 3), // 0 refill/sec: exactly 3 requests, ever, in this test's timeframe
	})

	var codes []int
	for i := 0; i < 5; i++ {
		req := httptest.NewRequest(http.MethodGet, "/t/acme/posts", nil)
		rec := httptest.NewRecorder()
		s.ServeHTTP(rec, req)
		codes = append(codes, rec.Code)
	}

	for i, code := range codes[:3] {
		if code != http.StatusOK {
			t.Fatalf("request %d = %d, want %d (within burst)", i, code, http.StatusOK)
		}
	}
	for i, code := range codes[3:] {
		if code != http.StatusTooManyRequests {
			t.Fatalf("request %d = %d, want %d (over burst)", i+3, code, http.StatusTooManyRequests)
		}
	}
}

// TestMultiTenantRateLimitIsPerTenant confirms one tenant exhausting its
// bucket doesn't affect a different tenant's — the whole point of scoping
// the limiter by tenant instead of globally.
func TestMultiTenantRateLimitIsPerTenant(t *testing.T) {
	db, err := pebble.Open(t.TempDir(), storagej.DefaultOptions())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	s := NewServer()
	MountMultiTenant[blog.Post, *blog.Post](s, "posts", db, blog.PostType, MountOptions{
		Limiter: NewRateLimiter(0, 1),
	})

	get := func(tenant string) int {
		req := httptest.NewRequest(http.MethodGet, "/t/"+tenant+"/posts", nil)
		rec := httptest.NewRecorder()
		s.ServeHTTP(rec, req)
		return rec.Code
	}

	if code := get("acme"); code != http.StatusOK {
		t.Fatalf("acme first request = %d, want %d", code, http.StatusOK)
	}
	if code := get("acme"); code != http.StatusTooManyRequests {
		t.Fatalf("acme second request = %d, want %d", code, http.StatusTooManyRequests)
	}
	if code := get("other"); code != http.StatusOK {
		t.Fatalf("other's first request = %d, want %d (acme's limit must not apply)", code, http.StatusOK)
	}
}

func TestRateLimiterAllowsBurstThenRefills(t *testing.T) {
	rl := NewRateLimiter(1000, 2) // fast refill so the test doesn't need a real sleep

	if !rl.Allow("t") {
		t.Fatal("1st call should be allowed (burst)")
	}
	if !rl.Allow("t") {
		t.Fatal("2nd call should be allowed (burst)")
	}
	if rl.Allow("t") {
		t.Fatal("3rd call should be blocked (burst exhausted)")
	}

	time.Sleep(5 * time.Millisecond) // 1000/sec => plenty refilled by now
	if !rl.Allow("t") {
		t.Fatal("call after refill window should be allowed")
	}
}
