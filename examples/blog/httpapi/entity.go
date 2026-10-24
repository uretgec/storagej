package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/uretgec/storagej/v2"
	"github.com/uretgec/storagej/v2/examples/blog/blog"
)

// repoForError optionally carries the HTTP status a repoFor failure should
// map to. A plain error (anything that isn't one of these) defaults to
// 400 — the original, simpler contract, still correct for Mount's repoFor
// (which never errors at all) and for a bare validation failure.
type repoForError struct {
	status int
	msg    string
}

func (e *repoForError) Error() string { return e.msg }

func repoForStatus(err error) int {
	var se *repoForError
	if errors.As(err, &se) {
		return se.status
	}
	return http.StatusBadRequest
}

// Mount registers CRUD routes for one entity type on s, rooted at
// /{resource} (e.g. "posts", "pages"), all backed by the single tenant
// baked into repo at construction time:
//
//	POST   /{resource}             create
//	GET    /{resource}             list (?cursor=&limit=)
//	GET    /{resource}/slug/{slug} get by slug
//	GET    /{resource}/{id}        get by id
//	PUT    /{resource}/{id}        update (path id is authoritative, not the body)
//	DELETE /{resource}/{id}        delete (idempotent)
//
// repo is typically blog.NewPostRepository/NewPageRepository's result, or
// any blog.Repository[T, PT] for a future entity. Mount needs nothing about
// T beyond blog.Entity, mirroring how little blog.Repository itself needs —
// a third entity costs one Mount call here.
//
// For a server that resolves the tenant from the request instead of fixing
// one at startup — optionally with per-tenant auth and rate limiting — see
// MountMultiTenant.
func Mount[T any, PT interface {
	*T
	blog.Entity
}](s *Server, resource string, repo *blog.Repository[T, PT]) {
	h := &entityHandler[T, PT]{
		server: s,
		repoFor: func(w http.ResponseWriter, r *http.Request) (*blog.Repository[T, PT], error) {
			return repo, nil
		},
	}
	registerEntityRoutes(s, "/"+resource, h)
}

// entityHandler holds the CRUD handlers registered by one Mount or
// MountMultiTenant call. repoFor resolves which blog.Repository[T, PT] (or
// error) a given request gets: Mount's always returns the same one and
// never errors; MountMultiTenant's resolves tenant, checks auth/rate limit,
// and builds or reuses a tenant-scoped one, writing response headers
// directly (e.g. WWW-Authenticate) when it needs to before returning an
// error. Every handler below calls it first and maps a non-nil error via
// repoForStatus.
//
// entityHandler keeps a reference to server rather than copying OnError,
// so a later assignment to server.OnError is still picked up (Mount and
// MountMultiTenant are typically called right after NewServer, before
// OnError is set).
type entityHandler[T any, PT interface {
	*T
	blog.Entity
}] struct {
	server  *Server
	repoFor func(w http.ResponseWriter, r *http.Request) (*blog.Repository[T, PT], error)
}

// registerEntityRoutes wires h's six handlers under prefix (e.g. "/posts"
// or "/t/{tenant}/posts").
func registerEntityRoutes[T any, PT interface {
	*T
	blog.Entity
}](s *Server, prefix string, h *entityHandler[T, PT]) {
	s.mux.HandleFunc("POST "+prefix, h.handleCreate)
	s.mux.HandleFunc("GET "+prefix, h.handleList)
	s.mux.HandleFunc("GET "+prefix+"/slug/{slug}", h.handleGetBySlug)
	s.mux.HandleFunc("GET "+prefix+"/{id}", h.handleGet)
	s.mux.HandleFunc("PUT "+prefix+"/{id}", h.handleUpdate)
	s.mux.HandleFunc("DELETE "+prefix+"/{id}", h.handleDelete)
}

func (h *entityHandler[T, PT]) handleCreate(w http.ResponseWriter, r *http.Request) {
	repo, err := h.repoFor(w, r)
	if err != nil {
		writeError(w, repoForStatus(err), err.Error())
		return
	}
	var v T
	if err := json.NewDecoder(r.Body).Decode(&v); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if err := repo.Create(r.Context(), &v); err != nil {
		h.writeEntityError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, &v)
}

func (h *entityHandler[T, PT]) handleGet(w http.ResponseWriter, r *http.Request) {
	repo, err := h.repoFor(w, r)
	if err != nil {
		writeError(w, repoForStatus(err), err.Error())
		return
	}
	v, err := repo.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		h.writeEntityError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func (h *entityHandler[T, PT]) handleGetBySlug(w http.ResponseWriter, r *http.Request) {
	repo, err := h.repoFor(w, r)
	if err != nil {
		writeError(w, repoForStatus(err), err.Error())
		return
	}
	v, err := repo.GetBySlug(r.Context(), r.PathValue("slug"))
	if err != nil {
		h.writeEntityError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func (h *entityHandler[T, PT]) handleUpdate(w http.ResponseWriter, r *http.Request) {
	repo, err := h.repoFor(w, r)
	if err != nil {
		writeError(w, repoForStatus(err), err.Error())
		return
	}
	var v T
	if err := json.NewDecoder(r.Body).Decode(&v); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	PT(&v).SetID(r.PathValue("id")) // the URL path is authoritative, not the body
	if err := repo.Update(r.Context(), &v); err != nil {
		h.writeEntityError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, &v)
}

func (h *entityHandler[T, PT]) handleDelete(w http.ResponseWriter, r *http.Request) {
	repo, err := h.repoFor(w, r)
	if err != nil {
		writeError(w, repoForStatus(err), err.Error())
		return
	}
	if err := repo.Delete(r.Context(), r.PathValue("id")); err != nil {
		h.writeEntityError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// listResponse is the GET /{resource} response body shape, shared by every
// mounted entity type: {"items": [...], "next_cursor": "...", "has_more": bool}.
type listResponse[T any] struct {
	Items      []*T   `json:"items"`
	NextCursor string `json:"next_cursor,omitempty"`
	HasMore    bool   `json:"has_more"`
}

func (h *entityHandler[T, PT]) handleList(w http.ResponseWriter, r *http.Request) {
	repo, err := h.repoFor(w, r)
	if err != nil {
		writeError(w, repoForStatus(err), err.Error())
		return
	}

	limit := defaultListLimit
	if v := r.URL.Query().Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 {
			writeError(w, http.StatusBadRequest, "limit must be a positive integer")
			return
		}
		limit = n
	}

	items, nextCursor, hasMore, err := repo.List(r.Context(), r.URL.Query().Get("cursor"), limit)
	if err != nil {
		h.writeEntityError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, listResponse[T]{
		Items:      items,
		NextCursor: nextCursor,
		HasMore:    hasMore,
	})
}

// writeEntityError maps a blog/storagej error to the right HTTP status. Any
// error not recognized here is treated as internal: the client gets a
// generic 500 and the real error goes to server.OnError instead of the
// response body, so internals never leak to a client.
func (h *entityHandler[T, PT]) writeEntityError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, storagej.ErrNotFound):
		writeError(w, http.StatusNotFound, "not found")
	case errors.Is(err, blog.ErrSlugTaken):
		writeError(w, http.StatusConflict, err.Error())
	case errors.Is(err, blog.ErrSlugRequired), errors.Is(err, blog.ErrIDRequired):
		writeError(w, http.StatusBadRequest, err.Error())
	default:
		if h.server.OnError != nil {
			h.server.OnError(err)
		}
		writeError(w, http.StatusInternalServerError, "internal error")
	}
}
