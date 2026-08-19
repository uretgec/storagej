# storagej

A high-performance, **domain-agnostic** key-value store for Go, built on
[CockroachDB's Pebble](https://github.com/cockroachdb/pebble) engine.

storagej transports raw bytes only. It has no knowledge of tenants, models,
indexes, or relations — those concerns belong to higher layers (e.g., an ORM).
This keeps it a clean, reusable storage primitive.

This repository is a **toolkit, not an application**: `storagej`, `pebble`,
`keyspace`, and `repository` (documented below) are the whole reusable
surface — none of them know about "posts," "tenants" as a concept beyond a
string, or anything domain-specific. If you're building something other
than a blog, this is everything you need; you should never need to look at
`examples/`.

For a full worked example of building a real application on top of this —
a multi-tenant blog API with auth, rate limiting, and a deployable binary —
see [`examples/blog`](examples/blog/README.md). It's a reference, not a
dependency: nothing in `storagej`/`pebble`/`keyspace`/`repository` imports
or knows about it.

## Features

- **Interface-driven** (`storagej.Store`) — swap backends, trivial to test
- **Atomic batches** — multi-key writes commit all-or-nothing
- **Range & prefix scans** — O(1) seek, cursor-based pagination ready
- **Event hooks** — background errors, write stalls, compactions (logger-agnostic)
- **Periodic metrics** — cache hit rate, disk usage, compaction stats
- **Checkpoints** — consistent, self-contained snapshots for backup
- **Minimal dependencies** — only Pebble; no logger, no framework

## Install

```bash
go get github.com/uretgec/storagej
```

Requires **Go 1.22+** (this module's `go.mod` targets 1.22; the toolkit
packages themselves use nothing newer than generics, which have been
available since 1.18 — the floor is set by `go.mod`, not by a language
feature any of these four packages actually needs).

## Quick Start

```go
package main

import (
 "context"
 "log"

 "github.com/uretgec/storagej"
 "github.com/uretgec/storagej/pebble"
)

func main() {
 db, err := pebble.Open("./data", storagej.DefaultOptions())
 if err != nil {
  log.Fatal(err)
 }
 defer db.Close()

 ctx := context.Background()

 if err := db.Set(ctx, []byte("greeting"), []byte("hello")); err != nil {
  log.Fatal(err)
 }

 val, err := db.Get(ctx, []byte("greeting"))
 if err != nil {
  log.Fatal(err)
 }
 log.Println(string(val)) // hello
}
```

## API Overview

### `storagej.Store`

| Method | Description |
|--------|-------------|
| `Get(ctx, key)` | Read a key (returns a safe copy). `ErrNotFound` if absent. |
| `GetInto(ctx, key, dst)` | Read a key into a caller-supplied buffer, reusing `dst` when its capacity allows. Skips `Get`'s allocation. |
| `Exists(ctx, key)` | Check whether a key is present, without copying its value. Zero allocations. |
| `Set(ctx, key, value)` | Write (NoSync; WAL provides durability). |
| `Delete(ctx, key)` | Delete (idempotent). |
| `Scan(ctx, start, end, fn)` | Iterate `[start, end)` in sorted order. Return `ErrStopIteration` from `fn` to stop early. |
| `PrefixScan(ctx, prefix, fn)` | Iterate all keys sharing a prefix. |
| `NewBatch()` | Start an atomic multi-key write batch. |
| `Close()` | Release resources (idempotent). |

### `storagej.Batch`

`Set`, `Delete`, `Commit`, `Count`, `Close`.

### Pebble-specific (concrete, not on the interface)

| Method | Description |
|--------|-------------|
| `Checkpoint(dir)` | Write a consistent snapshot for backup. |
| `Metrics()` | Read raw Pebble metrics. |
| `MetricsSnapshot()` | Read a `storagej.MetricsSnapshot` on demand — the same shape `OnMetrics` delivers periodically, but computed immediately. Useful for an HTTP health endpoint; see `examples/blog`. |

## Events & Metrics

storagej is **logger-agnostic**. Wire events and metrics to your own logger
through callbacks in `Options`:

```go
opts := storagej.DefaultOptions()
opts.OnEvent = func(e storagej.Event) { /* forward to your logger */ }
opts.MetricsInterval = 45 * time.Second
opts.OnMetrics = func(m storagej.MetricsSnapshot) { /* forward to metrics */ }

db, _ := pebble.Open("./data", opts)
```

See [EXAMPLES.md](EXAMPLES.md) for a full `loggerj` integration and a
cursor-based pagination recipe.

## Backup

`Checkpoint` flushes the active memtable and writes a self-contained snapshot
that can be reopened read-only to restore data:

```go
if err := db.Checkpoint("./backups/2024-01-15"); err != nil {
 log.Fatal(err)
}
```

## keyspace: building keys

storagej itself is byte-in, byte-out — it doesn't know what a "tenant" or a
"post" is. The `keyspace` subpackage is an optional, standalone codec for a
`{tenant}:{kind}:{type}:{id}` layout, so higher layers don't hand-roll
`fmt.Sprintf` keys:

```go
import "github.com/uretgec/storagej/keyspace"

id := keyspace.NewID() // sortable, 26-char, time-ordered

key, err := keyspace.Key("acme", keyspace.KindData, "post", id)
// key = "acme:d:post:01ARZ3NDEKTSV4RRFFQ69G5FAV"

db.Set(ctx, key, someJSON)
```

Iterating all records of one type for a tenant, oldest first (ids are
chronologically sortable, so no separate index is needed):

```go
prefix, _ := keyspace.Prefix("acme", keyspace.KindData, "post")
db.PrefixScan(ctx, prefix, func(key, value []byte) error {
 tenant, kind, typ, id, err := keyspace.Parse(key)
 // tenant/typ/id alias key — copy them before the scan callback returns
 // if you need to retain them past this call.
 return nil
})
```

`keyspace` has no dependency on storagej and doesn't appear on `storagej.Store`
— it's a separate, optional layer. See the package doc comment in
`keyspace/keyspace.go` for the full key-layout contract.

## repository: CRUD, indexes, and concurrency — without a domain

`repository.Core` is a byte-level CRUD + secondary-index helper scoped to
one tenant and one entity type. It knows the key layout (via `keyspace`)
but nothing about any specific struct: `Get`/`Put` take and return
`[]byte`. This is the last layer before you'd write your own domain code —
`examples/blog` shows what that next layer looks like, but `repository`
itself has no idea it exists.

```go
import "github.com/uretgec/storagej/repository"

posts := repository.NewCore(db, "acme", "post") // tenant "acme", type "post"

// PutWithIndex keeps a record and a unique-field index (e.g. a slug)
// consistent in one atomic batch — rejects with ErrIndexConflict if the
// index value is already taken by a different id.
err := posts.PutWithIndex(ctx, "01ARZ3ND...", "slug", "hello-world", someJSON)

id, err := posts.GetIndex(ctx, "slug", "hello-world")
val, err := posts.Get(ctx, id)

err = posts.List(ctx, func(id string, value []byte) error {
 return nil // oldest first; return storagej.ErrStopIteration to stop early
})
```

**Concurrency, stated plainly:** the index-uniqueness check inside
`PutWithIndex`/`ReplaceIndex`/`DeleteWithIndex` is read-then-write, not a
storagej-level compare-and-swap primitive — storagej still has no CAS. But
`Core` serializes all three with an internal mutex, and Pebble holds an
exclusive lock on its data directory (only one process can have it open for
writes), so the only possible concurrent callers are goroutines within this
process — exactly what that mutex protects against. Net effect: any two
goroutines racing to claim the same index value are safe; exactly one wins.
See `repository.TestPutWithIndexSerializesConcurrentConflicts` and its
neighbors for this proven under `-race` with real goroutines, not just
asserted in a comment.

## Performance

Defaults are tuned for low-resource systems (1-2 vCPU, 2-4GB RAM):
64MB cache, 64MB memtable, CPU/2 compaction threads, 64 open files.

```bash
go test ./pebble -bench=. -benchmem -run=^$
go test ./pebble -bench=. -benchmem -run=^$ -benchtime=2s
```

## Testing

```bash
go test ./... -race -v
go test ./pebble -race -v
go test ./keyspace -race -v
go test ./repository -race -v
```

(`examples/blog` has its own tests, run separately — see
[`examples/blog/README.md`](examples/blog/README.md#testing).)

## License

MIT — see [LICENSE](LICENSE).
