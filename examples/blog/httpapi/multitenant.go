package httpapi

import (
	"net/http"
	"sync"

	"github.com/uretgec/storagej"
	"github.com/uretgec/storagej/examples/blog/blog"
	"github.com/uretgec/storagej/keyspace"
)

// MountOptions configures optional cross-cutting behavior for
// MountMultiTenant. The zero value (MountOptions{}) means no auth and no
// rate limiting — the original, fully permissive behavior, unchanged.
type MountOptions struct {
	// Auth, if set, requires a valid per-tenant API key on every request
	// (see APIKeyAuth). Failing auth returns 401 before a repository is
	// ever looked up or created for the claimed tenant — an unauthenticated
	// request never touches the per-tenant cache at all.
	Auth *APIKeyAuth

	// Limiter, if set, enforces a per-tenant request rate (see
	// RateLimiter). Checked after Auth succeeds (or immediately, if Auth
	// is nil), so a request can't burn a DIFFERENT tenant's quota just by
	// naming it in the URL once Auth actually confirms who's asking.
	// Failing the limit returns 429.
	Limiter *RateLimiter
}

// MountMultiTenant registers CRUD routes for one entity type on s, rooted
// at /t/{tenant}/{resource} — the multi-tenant counterpart to Mount, which
// fixes its tenant at construction time. Here tenant is resolved from the
// URL on every request, and a blog.Repository[T, PT] is built lazily per
// tenant on first use and cached for reuse — no tenant needs to be known or
// pre-registered ahead of time (unless opts.Auth is set — see below).
//
// The two route styles can coexist on the same Server (they live under
// different path prefixes) but mounting both for the same resource name is
// unusual; pick one per resource in practice.
//
// Every route, handler, and error-mapping rule is identical to Mount's —
// MountMultiTenant only adds tenant resolution (and, if configured, auth
// and rate limiting) in front of the same entityHandler[T, PT] Mount uses,
// so there is no duplicated CRUD logic to drift out of sync between the two.
//
// Pass MountOptions{} for the original, fully open behavior. Two things
// worth knowing about running that way:
//
//  1. No access control. Any tenant name that passes keyspace.ValidSegment
//     (non-empty, no ':') routes successfully and gets its own isolated
//     data on first request. Set opts.Auth to require a per-tenant API key
//     instead — this is exactly what closes that gap.
//  2. Unbounded cache growth. The per-tenant repository cache never
//     evicts. opts.Auth also closes this: an unauthenticated request never
//     reaches the point where a cache entry would be created.
func MountMultiTenant[T any, PT interface {
	*T
	blog.Entity
}](s *Server, resource string, store storagej.Store, typ string, opts MountOptions) {
	var mu sync.Mutex
	repos := make(map[string]*blog.Repository[T, PT])

	repoFor := func(w http.ResponseWriter, r *http.Request) (*blog.Repository[T, PT], error) {
		tenant := r.PathValue("tenant")
		if !keyspace.ValidSegment(tenant) {
			return nil, &repoForError{status: http.StatusBadRequest, msg: "invalid tenant"}
		}

		if opts.Auth != nil && !opts.Auth.Authenticate(tenant, r) {
			w.Header().Set("WWW-Authenticate", "Bearer")
			return nil, &repoForError{status: http.StatusUnauthorized, msg: "invalid or missing API key"}
		}

		if opts.Limiter != nil && !opts.Limiter.Allow(tenant) {
			return nil, &repoForError{status: http.StatusTooManyRequests, msg: "rate limit exceeded"}
		}

		mu.Lock()
		defer mu.Unlock()
		if repo, ok := repos[tenant]; ok {
			return repo, nil
		}
		repo := blog.NewRepository[T, PT](store, tenant, typ)
		repos[tenant] = repo
		return repo, nil
	}

	h := &entityHandler[T, PT]{server: s, repoFor: repoFor}
	registerEntityRoutes(s, "/t/{tenant}/"+resource, h)
}
