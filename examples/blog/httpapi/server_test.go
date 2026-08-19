package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/uretgec/storagej"
	"github.com/uretgec/storagej/examples/blog/blog"
	"github.com/uretgec/storagej/pebble"
)

// newTestServer opens a fresh store and mounts both /posts and /pages on a
// new Server. Most tests exercise only one resource, but mounting both
// unconditionally is itself part of the test: it proves two Mount calls on
// one Server coexist without route collisions.
func newTestServer(t *testing.T) *Server {
	t.Helper()
	db, err := pebble.Open(t.TempDir(), storagej.DefaultOptions())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	s := NewServer()
	Mount(s, "posts", blog.NewPostRepository(db, "acme"))
	Mount(s, "pages", blog.NewPageRepository(db, "acme"))
	return s
}

func createPost(t *testing.T, s *Server, body string) *blog.Post {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/posts", strings.NewReader(body))
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create post failed: status=%d body=%s", rec.Code, rec.Body.String())
	}
	var p blog.Post
	if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil {
		t.Fatalf("decode create post response: %v", err)
	}
	return &p
}

func createPage(t *testing.T, s *Server, body string) *blog.Page {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/pages", strings.NewReader(body))
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create page failed: status=%d body=%s", rec.Code, rec.Body.String())
	}
	var p blog.Page
	if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil {
		t.Fatalf("decode create page response: %v", err)
	}
	return &p
}

// -----------------------------------------------------------------------------
// POST /{resource}
// -----------------------------------------------------------------------------

func TestHandleCreate(t *testing.T) {
	s := newTestServer(t)
	got := createPost(t, s, `{"slug":"hello-world","title":"Hello","body":"World"}`)

	if got.ID == "" {
		t.Fatal("response post has no id")
	}
	if got.Slug != "hello-world" || got.Title != "Hello" || got.Body != "World" {
		t.Fatalf("got = %+v, want matching request fields", got)
	}
}

func TestHandleCreateInvalidJSON(t *testing.T) {
	s := newTestServer(t)
	req := httptest.NewRequest(http.MethodPost, "/posts", strings.NewReader("not json"))
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
}

func TestHandleCreateMissingSlug(t *testing.T) {
	s := newTestServer(t)
	req := httptest.NewRequest(http.MethodPost, "/posts", strings.NewReader(`{"title":"No slug"}`))
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
}

func TestHandleCreateDuplicateSlug(t *testing.T) {
	s := newTestServer(t)
	createPost(t, s, `{"slug":"hello","title":"A"}`)

	req := httptest.NewRequest(http.MethodPost, "/posts", strings.NewReader(`{"slug":"hello","title":"B"}`))
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)

	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusConflict, rec.Body.String())
	}
}

// -----------------------------------------------------------------------------
// GET /{resource}/{id}, GET /{resource}/slug/{slug}
// -----------------------------------------------------------------------------

func TestHandleGet(t *testing.T) {
	s := newTestServer(t)
	created := createPost(t, s, `{"slug":"hello","title":"Hello"}`)

	req := httptest.NewRequest(http.MethodGet, "/posts/"+created.ID, nil)
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	var got blog.Post
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.ID != created.ID {
		t.Fatalf("got.ID = %s, want %s", got.ID, created.ID)
	}
}

func TestHandleGetNotFound(t *testing.T) {
	s := newTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/posts/missing-id", nil)
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNotFound)
	}
}

// TestHandleGetBySlugRoutesCorrectly guards route specificity: a request for
// /posts/slug/hello-world must hit handleGetBySlug, not be swallowed by the
// /posts/{id} wildcard treating "slug" as an id.
func TestHandleGetBySlugRoutesCorrectly(t *testing.T) {
	s := newTestServer(t)
	created := createPost(t, s, `{"slug":"hello-world","title":"Hello"}`)

	req := httptest.NewRequest(http.MethodGet, "/posts/slug/hello-world", nil)
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	var got blog.Post
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.ID != created.ID {
		t.Fatalf("got.ID = %s, want %s", got.ID, created.ID)
	}
}

func TestHandleGetBySlugNotFound(t *testing.T) {
	s := newTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/posts/slug/missing", nil)
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNotFound)
	}
}

// -----------------------------------------------------------------------------
// PUT /{resource}/{id}
// -----------------------------------------------------------------------------

func TestHandleUpdate(t *testing.T) {
	s := newTestServer(t)
	created := createPost(t, s, `{"slug":"hello","title":"Original"}`)

	body := `{"slug":"hello","title":"Updated","body":"new body"}`
	req := httptest.NewRequest(http.MethodPut, "/posts/"+created.ID, strings.NewReader(body))
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	var got blog.Post
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Title != "Updated" || got.Body != "new body" || got.ID != created.ID {
		t.Fatalf("got = %+v, want Title=Updated Body=\"new body\" ID=%s", got, created.ID)
	}
}

// TestHandleUpdateIgnoresBodyID guards that the URL path id is authoritative
// over any id present in the request body.
func TestHandleUpdateIgnoresBodyID(t *testing.T) {
	s := newTestServer(t)
	created := createPost(t, s, `{"slug":"hello","title":"Original"}`)

	body := `{"id":"someone-elses-id","slug":"hello","title":"Updated"}`
	req := httptest.NewRequest(http.MethodPut, "/posts/"+created.ID, strings.NewReader(body))
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}

	getReq := httptest.NewRequest(http.MethodGet, "/posts/"+created.ID, nil)
	getRec := httptest.NewRecorder()
	s.ServeHTTP(getRec, getReq)
	var got blog.Post
	if err := json.Unmarshal(getRec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Title != "Updated" {
		t.Fatalf("update via path id did not apply: got %+v", got)
	}

	bogusReq := httptest.NewRequest(http.MethodGet, "/posts/someone-elses-id", nil)
	bogusRec := httptest.NewRecorder()
	s.ServeHTTP(bogusRec, bogusReq)
	if bogusRec.Code != http.StatusNotFound {
		t.Fatalf("bogus id status = %d, want %d (body id should have been ignored)", bogusRec.Code, http.StatusNotFound)
	}
}

func TestHandleUpdateDuplicateSlug(t *testing.T) {
	s := newTestServer(t)
	createPost(t, s, `{"slug":"slug-a","title":"A"}`)
	b := createPost(t, s, `{"slug":"slug-b","title":"B"}`)

	body := `{"slug":"slug-a","title":"B"}`
	req := httptest.NewRequest(http.MethodPut, "/posts/"+b.ID, strings.NewReader(body))
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)

	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusConflict, rec.Body.String())
	}
}

// -----------------------------------------------------------------------------
// DELETE /{resource}/{id}
// -----------------------------------------------------------------------------

func TestHandleDelete(t *testing.T) {
	s := newTestServer(t)
	created := createPost(t, s, `{"slug":"hello","title":"T"}`)

	req := httptest.NewRequest(http.MethodDelete, "/posts/"+created.ID, nil)
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNoContent)
	}

	getReq := httptest.NewRequest(http.MethodGet, "/posts/"+created.ID, nil)
	getRec := httptest.NewRecorder()
	s.ServeHTTP(getRec, getReq)
	if getRec.Code != http.StatusNotFound {
		t.Fatalf("get after delete status = %d, want %d", getRec.Code, http.StatusNotFound)
	}
}

func TestHandleDeleteIsIdempotent(t *testing.T) {
	s := newTestServer(t)
	req := httptest.NewRequest(http.MethodDelete, "/posts/never-existed", nil)
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNoContent)
	}
}

// -----------------------------------------------------------------------------
// GET /{resource}
// -----------------------------------------------------------------------------

func TestHandleList(t *testing.T) {
	s := newTestServer(t)
	for i := 0; i < 3; i++ {
		createPost(t, s, fmt.Sprintf(`{"slug":"post-%d","title":"Post %d"}`, i, i))
	}

	req := httptest.NewRequest(http.MethodGet, "/posts", nil)
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	var resp listResponse[blog.Post]
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Items) != 3 {
		t.Fatalf("len(items) = %d, want 3", len(resp.Items))
	}
	if resp.HasMore {
		t.Fatal("HasMore = true, want false")
	}
}

func TestHandleListPagination(t *testing.T) {
	s := newTestServer(t)
	for i := 0; i < 3; i++ {
		createPost(t, s, fmt.Sprintf(`{"slug":"post-%d","title":"Post %d"}`, i, i))
	}

	req := httptest.NewRequest(http.MethodGet, "/posts?limit=2", nil)
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)

	var page1 listResponse[blog.Post]
	if err := json.Unmarshal(rec.Body.Bytes(), &page1); err != nil {
		t.Fatalf("decode page1: %v", err)
	}
	if len(page1.Items) != 2 || !page1.HasMore || page1.NextCursor == "" {
		t.Fatalf("page1 = %+v, want 2 items, HasMore=true, non-empty cursor", page1)
	}

	req2 := httptest.NewRequest(http.MethodGet, "/posts?limit=2&cursor="+page1.NextCursor, nil)
	rec2 := httptest.NewRecorder()
	s.ServeHTTP(rec2, req2)

	var page2 listResponse[blog.Post]
	if err := json.Unmarshal(rec2.Body.Bytes(), &page2); err != nil {
		t.Fatalf("decode page2: %v", err)
	}
	if len(page2.Items) != 1 || page2.HasMore {
		t.Fatalf("page2 = %+v, want 1 item, HasMore=false", page2)
	}
}

func TestHandleListInvalidLimit(t *testing.T) {
	s := newTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/posts?limit=abc", nil)
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
}

func TestHandleListZeroLimit(t *testing.T) {
	s := newTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/posts?limit=0", nil)
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
}

// -----------------------------------------------------------------------------
// Two mounted resources coexisting
// -----------------------------------------------------------------------------

// TestMountedResourcesDoNotCollide is the HTTP-layer counterpart to
// blog's TestPostAndPageDoNotCollide: /posts and /pages are mounted on the
// same Server, and a shared id/slug between a post and a page must not
// leak data from one resource into the other.
func TestMountedResourcesDoNotCollide(t *testing.T) {
	s := newTestServer(t)

	post := createPost(t, s, `{"slug":"shared","title":"A Post"}`)
	page := createPage(t, s, `{"slug":"shared","title":"A Page"}`)

	req := httptest.NewRequest(http.MethodGet, "/posts/"+post.ID, nil)
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	var gotPost blog.Post
	if err := json.Unmarshal(rec.Body.Bytes(), &gotPost); err != nil {
		t.Fatalf("decode post: %v", err)
	}
	if gotPost.Title != "A Post" {
		t.Fatalf("gotPost.Title = %q, want %q", gotPost.Title, "A Post")
	}

	req2 := httptest.NewRequest(http.MethodGet, "/pages/"+page.ID, nil)
	rec2 := httptest.NewRecorder()
	s.ServeHTTP(rec2, req2)
	var gotPage blog.Page
	if err := json.Unmarshal(rec2.Body.Bytes(), &gotPage); err != nil {
		t.Fatalf("decode page: %v", err)
	}
	if gotPage.Title != "A Page" {
		t.Fatalf("gotPage.Title = %q, want %q", gotPage.Title, "A Page")
	}

	// A page's slug must not resolve through the posts resource, and
	// vice versa — the two Mount calls must not share an index namespace.
	missReq := httptest.NewRequest(http.MethodGet, "/posts/slug/shared", nil)
	missRec := httptest.NewRecorder()
	s.ServeHTTP(missRec, missReq)
	var postBySlug blog.Post
	if err := json.Unmarshal(missRec.Body.Bytes(), &postBySlug); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if postBySlug.ID != post.ID {
		t.Fatalf("/posts/slug/shared resolved to %q, want the post's id %q", postBySlug.ID, post.ID)
	}
}

// -----------------------------------------------------------------------------
// Health
// -----------------------------------------------------------------------------

func TestHandleHealthWithoutChecker(t *testing.T) {
	s := newTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if strings.Contains(rec.Body.String(), "metrics") {
		t.Fatalf("body should omit metrics when Health is unset, got %s", rec.Body.String())
	}
}

func TestHandleHealthWithChecker(t *testing.T) {
	db, err := pebble.Open(t.TempDir(), storagej.DefaultOptions())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	s := NewServer()
	Mount(s, "posts", blog.NewPostRepository(db, "acme"))
	s.Health = db // *pebble.Store satisfies HealthChecker structurally

	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	var resp healthResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Status != "ok" {
		t.Fatalf("status field = %q, want %q", resp.Status, "ok")
	}
	if resp.Metrics == nil {
		t.Fatal("Metrics should be populated when Health is set")
	}
}

// -----------------------------------------------------------------------------
// Error responses are always JSON, never bare Go error text
// -----------------------------------------------------------------------------

func TestErrorResponsesAreJSON(t *testing.T) {
	s := newTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/posts/missing-id", nil)
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)

	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Fatalf("Content-Type = %q, want %q", ct, "application/json")
	}
	var resp errorResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode error response: %v", err)
	}
	if resp.Error == "" {
		t.Fatal("error response has empty Error field")
	}
}
