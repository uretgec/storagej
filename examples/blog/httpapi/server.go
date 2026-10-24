// Package httpapi is a minimal, framework-free HTTP API over blog entity
// repositories. It uses only net/http — no router library — relying on the
// stdlib http.ServeMux's method+wildcard routing (Go 1.22+).
//
// Server itself knows nothing about Post, Page, or any other entity — it
// only owns cross-cutting concerns: the shared mux, GET /health, and error
// reporting. Each entity type is wired in with Mount (see entity.go), the
// HTTP-layer counterpart to blog.Repository[T, PT]: a third entity costs
// one Mount call here, the same way it costs one blog.Repository
// instantiation there.
//
// The API is single-tenant: the tenant is fixed when each
// blog.Repository[T, PT] passed to Mount was constructed, not exposed in
// the URL. The layers underneath already support arbitrary tenants; this
// layer just doesn't route on tenant yet.
package httpapi

import (
	"encoding/json"
	"net/http"

	"github.com/uretgec/storagej/v2"
)

// defaultListLimit is used when a GET /{resource} request omits ?limit.
const defaultListLimit = 20

// HealthChecker is implemented by a storage backend that can report a
// point-in-time metrics snapshot. httpapi depends only on this interface,
// not on any concrete backend (e.g. pebble.Store) — the HTTP layer stays
// backend-agnostic, the same way storagej.Store itself does. pebble.Store
// already satisfies this via its MetricsSnapshot method; no adapter needed.
type HealthChecker interface {
	MetricsSnapshot() storagej.MetricsSnapshot
}

// Server composes one or more entity resources (see Mount) plus
// cross-cutting concerns into a single http.Handler.
type Server struct {
	mux *http.ServeMux

	// OnError, if set, receives any error that was mapped to a generic 500
	// response instead of being shown to the client, from any mounted
	// resource. Called synchronously from the handler goroutine. Optional
	// and logger-agnostic, same pattern as storagej.Options.OnEvent.
	OnError func(error)

	// Health, if set, backs GET /health's metrics field. Optional: without
	// it, /health still returns 200 with just {"status":"ok"} — the
	// endpoint reports liveness regardless of whether metrics are wired.
	Health HealthChecker
}

// NewServer returns a Server with GET /health already registered and no
// entity resources mounted yet — call Mount for each one (see entity.go).
func NewServer() *Server {
	s := &Server{mux: http.NewServeMux()}
	s.mux.HandleFunc("GET /health", s.handleHealth)
	return s
}

// ServeHTTP implements http.Handler.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mux.ServeHTTP(w, r)
}

type healthResponse struct {
	Status  string                    `json:"status"`
	Metrics *storagej.MetricsSnapshot `json:"metrics,omitempty"`
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	resp := healthResponse{Status: "ok"}
	if s.Health != nil {
		m := s.Health.MetricsSnapshot()
		resp.Metrics = &m
	}
	writeJSON(w, http.StatusOK, resp)
}

type errorResponse struct {
	Error string `json:"error"`
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, errorResponse{Error: msg})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
