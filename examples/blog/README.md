# examples/blog — a worked application built on storagej

This is a **reference application**, not part of the storagej toolkit. It
exists to prove the toolkit (`storagej`, `keyspace`, `repository` — see the
[root README](../../README.md)) actually holds up end-to-end, and to give
you a concrete pattern to copy if you're building something similar.
Nothing under the repository root imports anything from here; deleting this
whole directory doesn't affect `storagej`, `pebble`, `keyspace`, or
`repository` at all.

All commands below assume you're in this directory (`cd examples/blog`).

```
examples/blog/
├── blog/            Post, Page — the domain layer (see below)
├── httpapi/          HTTP layer: Mount, MountMultiTenant, auth, rate limiting
├── cmd/storagej-server/   the actual binary
├── deploy/            Dockerfile, systemd unit
└── README.md          this file
```

## blog: the domain layer

`Post` and `Page` are small structs, each implementing `blog.Entity`
(`GetID`/`SetID`/`GetSlug`/`SetUpdatedAt` — four one-line methods) on top of
one shared, generic `repository.Core` each. All CRUD, slug-index, and
pagination logic lives once, in `blog.Repository[T, PT]`. `Post` and `Page`
started as two near-identical concrete files; once that duplication was
real (not speculative), it was extracted into `Repository[T, PT]` — a third
entity now costs exactly what `page.go` shows: a struct, four methods, a
two-line constructor.

```go
import "github.com/uretgec/storagej/v2/examples/blog/blog"

posts := blog.NewPostRepository(db, "acme") // scoped to tenant "acme"

p := &blog.Post{Slug: "hello-world", Title: "Hello", Body: "First post"}
if err := posts.Create(ctx, p); err != nil {
 // errors.Is(err, blog.ErrSlugTaken) if another post already has this slug
 log.Fatal(err)
}

got, err := posts.GetBySlug(ctx, "hello-world")

page, cursor, hasMore, err := posts.List(ctx, "", 20) // first 20, oldest first

created, err := blog.CreatedAt(got) // derived from got.ID, no stored field
```

The index-uniqueness/concurrency guarantee this relies on is
`repository.Core`'s, not anything blog-specific — see the root README's
[repository section](../../README.md#repository-crud-indexes-and-concurrency--without-a-domain)
for how that's proven safe under concurrent goroutines.

## httpapi & cmd: one binary, one touch

The HTTP layer is framework-free (`httpapi`, stdlib `net/http` only) and a
single binary wires everything together (`cmd/storagej-server`):

```bash
go run ./cmd/storagej-server -addr :8080 -data ./data -tenant default
```

That's the whole setup — no config file, no migration step. It opens the
Pebble store at `-data`, serves the API at `-addr`, and shuts down cleanly
on SIGINT/SIGTERM.

By default the API is single-tenant per instance (`-tenant`); the
`blog`/`repository` layers underneath always supported arbitrary tenants,
this was just the HTTP layer not routing on tenant yet — see Multi-tenant
routing below for the mode that does.

`Server` itself knows nothing about `Post` or `Page` — it only owns
cross-cutting concerns (`GET /health`, `OnError`). Each entity is wired in
with the generic `Mount[T, PT]`, the HTTP-layer counterpart to
`blog.Repository[T, PT]`:

```go
api := httpapi.NewServer()
httpapi.Mount(api, "posts", blog.NewPostRepository(db, tenant))
httpapi.Mount(api, "pages", blog.NewPageRepository(db, tenant))
```

A third entity costs one more `Mount` call — no new handler code, matching
what it costs `blog.Repository` itself. `Mount` registers the same six
routes under whatever resource name you give it:

| Method & Path | Description |
|---|---|
| `GET /health` | Liveness. `200` always if the process is up; includes a metrics snapshot when wired (see Observability below). |
| `POST /{resource}` | Create. Body: `{"slug","title","body"}`. `409` if the slug is taken. |
| `GET /{resource}` | List, oldest first. Query: `?limit=20&cursor=...`. Response: `{"items":[...],"next_cursor":"...","has_more":bool}`. |
| `GET /{resource}/{id}` | Get by id. `404` if absent. |
| `GET /{resource}/slug/{slug}` | Get by slug. `404` if absent. |
| `PUT /{resource}/{id}` | Update. The path id is authoritative — any `id` in the body is ignored. |
| `DELETE /{resource}/{id}` | Delete (idempotent, `204` either way). |

`cmd/storagej-server` mounts `posts` and `pages` at those names, so in
practice this is `/posts`, `/pages`, `/posts/{id}`, `/pages/{id}`, and so on.
Two `Mount` calls never collide with each other or with `/health`: each gets
its own path prefix on the same shared mux.

Errors are JSON: `{"error": "..."}`. Anything not mapped to a specific status
(400/404/409) becomes a generic `500` with no internal detail in the
response — set `httpapi.Server.OnError` to see the real error server-side
(same logger-agnostic callback pattern as `storagej.Options.OnEvent`).

### Multi-tenant routing

`MountMultiTenant[T, PT]` is the multi-tenant counterpart to `Mount`:
instead of a fixed tenant baked in at construction, it resolves `{tenant}`
from the URL on every request and builds/caches a `blog.Repository[T, PT]`
per tenant lazily, on first use — nothing needs pre-registering.

```go
api := httpapi.NewServer()
httpapi.MountMultiTenant[blog.Post, *blog.Post](api, "posts", db, blog.PostType, httpapi.MountOptions{
	Auth:    httpapi.NewAPIKeyAuth(map[string]string{"acme": "secret-1"}),
	Limiter: httpapi.NewRateLimiter(5, 20), // 5 req/s, burst 20, per tenant
})
```

Routes move under `/t/{tenant}/...`: `POST /t/{tenant}/posts`,
`GET /t/{tenant}/posts/{id}`, and so on — otherwise identical to `Mount`'s
table above. `cmd/storagej-server -multi-tenant` switches to this mode (and
ignores `-tenant`); pair it with `-api-keys "acme:secret1,other:secret2"`
and/or `-rate-limit 5 -rate-limit-burst 20`.

Pass `MountOptions{}` for the original, fully open behavior — two things to
know before running that way with untrusted clients:

- **No access control.** Any tenant name that passes `keyspace.ValidSegment`
  (non-empty, no `:`) routes successfully and gets its own isolated data on
  first request. Set `MountOptions.Auth` to close this.
- **The per-tenant repository cache never evicts.** Fine for a known,
  bounded set of real tenants; `MountOptions.Auth` closes this too, since an
  unauthenticated request never reaches the point where a cache entry gets
  created.

**Auth** (`APIKeyAuth`) is a per-tenant static key checked against
`Authorization: Bearer <key>` — no JWT library, no external identity
provider, matching the project's minimal-dependency stance. A key only
unlocks its own tenant (`acme`'s key returns `401` against `/t/other/...`).
`Set`/`Revoke` are safe to call while serving requests, for provisioning a
tenant without a restart.

**Rate limiting** (`RateLimiter`) is a per-tenant token bucket, written
in-house rather than pulling in `golang.org/x/time/rate` — the whole thing
is about thirty lines. Each tenant gets its own bucket (`burst` capacity,
refilling at `ratePerSecond`/sec), so one tenant being throttled never
affects another's quota. Over the limit returns `429`.

Auth is checked before the rate limit, and both before the per-tenant
repository is looked up or created — an unauthenticated or throttled
request never touches tenant state at all.

## Observability

storagej stays logger-agnostic (see the root README's
[Events & Metrics](../../README.md#events--metrics)) — no logging
dependency was added anywhere in this stack. `cmd/storagej-server` is what
actually wires the callbacks to something:

```go
opts := storagej.DefaultOptions()
opts.OnEvent = logEvent          // background errors, write stalls, compactions -> log.Printf
opts.MetricsInterval = 45 * time.Second
opts.OnMetrics = logMetrics      // periodic snapshot -> log.Printf

db, _ := pebble.Open(*dataDir, opts)
```

Swap `log.Printf` for a real structured logger by replacing `logEvent`/
`logMetrics` in `cmd/storagej-server/main.go` — nothing else in the stack
needs to change, since the callbacks were designed logger-agnostic from the
start.

`-metrics-interval 0` disables the periodic log line entirely (storagej
doesn't start the background goroutine at all when `MetricsInterval` is 0).
`GET /health` is unaffected either way: it computes a snapshot on demand via
`pebble.Store.MetricsSnapshot()`, independent of periodic logging.

```bash
curl localhost:8080/health
# {"status":"ok","metrics":{"disk_mb":12,"cache_hit_rate_percent":94.2,...}}
```

## Deploy

Two ready-to-use deploy paths, both wrapping the same single binary
(`cmd/storagej-server`) — nothing else to build or configure.

### Docker

```bash
docker build -t storagej-server .
docker run -d --name storagej \
  -p 8080:8080 \
  -v storagej-data:/data \
  storagej-server
```

The image is a multi-stage build: compile in `golang:1.22-bookworm`, run in
`gcr.io/distroless/static-debian12` (no shell, minimal attack surface). The
`-v` volume is not optional — without it, Pebble's data directory lives
only in the container's writable layer and is lost when the container is
removed.

One build risk worth knowing: the static build (`CGO_ENABLED=0`) should
work — Pebble's own code is pure Go, and `pebble.Open` here never turns on
zstd compression (it uses Pebble's pure-Go default, Snappy). But
`DataDog/zstd`, a transitive Pebble dependency, normally wraps zstd via
cgo, and it's still part of the build graph even unused at runtime. If the
static build fails because of it, `Dockerfile` has a commented-out cgo
fallback (`CGO_ENABLED=1` + a glibc-based final image) right below the
static build step.

Override any `cmd/storagej-server` flag via `docker run`'s command
arguments, e.g. `docker run ... storagej-server -tenant acme`.

### systemd

For a bare-metal or VM deploy, `deploy/systemd/storagej-server.service` runs
the binary directly as a dedicated, sandboxed system user (no Docker
involved). Setup steps are in the unit file's header comment:

```bash
go build -o storagej-server ./cmd/storagej-server
# then follow the setup steps at the top of:
cat deploy/systemd/storagej-server.service
```

Logs go to the systemd journal (`journalctl -u storagej-server -f`) since
`cmd/storagej-server` already logs through the stdlib `log` package —
nothing extra to wire up.

Both paths expose the same `GET /health` endpoint; point whatever's
watching the process (a load balancer, Kubernetes `livenessProbe`, or just
a cron+curl check) at that rather than guessing from process liveness alone.

## Testing

```bash
go test ./... -race -v
go test ./blog -race -v
go test ./httpapi -race -v
```

(Run from `examples/blog/` — this is a separate part of the same Go module
as the root, so `go test ./...` here only covers what's under this
directory. From the repository root, `go test ./...` covers everything,
`examples/blog` included, since it's still one module.)
