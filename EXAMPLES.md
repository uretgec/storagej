# storagej Examples

A cookbook covering the core toolkit end to end: the base `storagej.Store`,
`pebble`'s two ways to open one, the optional `keyspace`/`repository`
layers, and the two `Store` decorators (`encrypted`, `watchable`). Every
example here is runnable as shown, using only packages that live at this
repository's root — nothing under `examples/blog` is required for any of
this.

## Table of Contents

**storagej + pebble — the base store*

- [storagej Examples](#storagej-examples)
  - [Table of Contents](#table-of-contents)
  - [Basic Operations](#basic-operations)
  - [Atomic Batches](#atomic-batches)
  - [Range Scan with Early Stop](#range-scan-with-early-stop)
  - [Prefix Scan](#prefix-scan)
  - [Cursor-Based Pagination](#cursor-based-pagination)
  - [In-Memory Store for Tests (OpenMem)](#in-memory-store-for-tests-openmem)
  - [Point-in-Time Consistent Reads (Snapshot)](#point-in-time-consistent-reads-snapshot)
  - [Event Hook → loggerj](#event-hook--loggerj)
  - [Periodic Metrics → loggerj](#periodic-metrics--loggerj)
  - [Backup \& Restore](#backup--restore)
  - [Building Sortable Keys with keyspace](#building-sortable-keys-with-keyspace)
  - [CRUD + a Unique Index with repository.Core](#crud--a-unique-index-with-repositorycore)
  - [Transparent Encryption at Rest (encrypted.Store)](#transparent-encryption-at-rest-encryptedstore)
  - [Live Change Notifications (watchable.Store)](#live-change-notifications-watchablestore)
  - [Combining Decorators: Encrypted + Watchable, Stacked](#combining-decorators-encrypted--watchable-stacked)
  - [Inspecting a Store from the Command Line (storagej-cli)](#inspecting-a-store-from-the-command-line-storagej-cli)
  - [See Also](#see-also)

**keyspace + repository — optional layers above the base store*

- [Building Sortable Keys with keyspace](#building-sortable-keys-with-keyspace)
- [CRUD + a Unique Index with repository.Core](#crud--a-unique-index-with-repositorycore)

**Store decorators — wrap any storagej.Store, transparently*

- [Transparent Encryption at Rest (encrypted.Store)](#transparent-encryption-at-rest-encryptedstore)
- [Live Change Notifications (watchable.Store)](#live-change-notifications-watchablestore)
- [Combining Decorators: Encrypted + Watchable, Stacked](#combining-decorators-encrypted--watchable-stacked)

**Tooling*

- [Inspecting a Store from the Command Line (storagej-cli)](#inspecting-a-store-from-the-command-line-storagej-cli)

**See also*

- [examples/blog](#see-also)

All examples assume these imports unless a section adds its own:

```go
import (
 "context"

 "github.com/uretgec/storagej"
 "github.com/uretgec/storagej/pebble"
)
```

## Basic Operations

```go
db, _ := pebble.Open("./data", storagej.DefaultOptions())
defer db.Close()
ctx := context.Background()

db.Set(ctx, []byte("user:1"), []byte("alice"))

val, err := db.Get(ctx, []byte("user:1"))
if errors.Is(err, storagej.ErrNotFound) {
 // handle missing key
}

db.Delete(ctx, []byte("user:1")) // idempotent
```

## Atomic Batches

All operations in a batch commit together or not at all:

```go
batch := db.NewBatch()
defer batch.Close()

batch.Set([]byte("order:1"), []byte("pending"))
batch.Set([]byte("stock:sku"), []byte("9"))
batch.Delete([]byte("cart:1"))

if err := batch.Commit(); err != nil {
 // none of the above was applied
}
```

## Range Scan with Early Stop

```go
// Iterate keys in ["a", "m") and stop after 5.
n := 0
db.Scan(ctx, []byte("a"), []byte("m"), func(key, value []byte) error {
 n++
 if n >= 5 {
  return storagej.ErrStopIteration // clean early exit
 }
 return nil
})
```

## Prefix Scan

```go
// All keys starting with "user:".
db.PrefixScan(ctx, []byte("user:"), func(key, value []byte) error {
 fmt.Println(string(key))
 return nil
})
```

## Cursor-Based Pagination

The `limit+1` technique reports `hasMore`/`nextCursor` in a single scan —
no separate COUNT query. (If you're using `repository.Core`, its own
`ListFrom` already does exactly this — see
[CRUD + a Unique Index with repository.Core](#crud--a-unique-index-with-repositorycore)
below. This manual version is for when you're using `storagej.Store`
directly, without that layer.)

```go
func paginate(ctx context.Context, db storagej.Store, prefix []byte, cursor string, limit int) (
 keys []string, nextCursor string, hasMore bool, err error,
) {
 lower := prefix
 if cursor != "" {
  lower = []byte(cursor)
 }
 upper := append(append([]byte{}, prefix...), 0xFF)

 fetch := limit + 1
 count := 0
 var cursorKey []byte

 err = db.Scan(ctx, lower, upper, func(key, value []byte) error {
  if cursor != "" && bytes.Equal(key, []byte(cursor)) {
   return nil // skip the previous page's last key
  }
  if count >= fetch {
   return storagej.ErrStopIteration
  }
  keys = append(keys, string(key))
  count++
  if count == limit {
   cursorKey = append(cursorKey[:0], key...) // last shown key
  }
  return nil
 })
 if err != nil {
  return nil, "", false, err
 }
 if count == fetch {
  hasMore = true
  nextCursor = string(cursorKey)
  keys = keys[:limit]
 }
 return keys, nextCursor, hasMore, nil
}
```

## In-Memory Store for Tests (OpenMem)

`OpenMem` shares `Open`'s exact interface and Options handling — the only
difference is the backing filesystem, an in-memory one that never touches
disk. Much faster for tests than `Open` with a temp directory:

```go
func TestSomething(t *testing.T) {
 db, err := pebble.OpenMem(storagej.DefaultOptions())
 if err != nil {
  t.Fatal(err)
 }
 defer db.Close()

 ctx := context.Background()
 db.Set(ctx, []byte("k"), []byte("v"))
 // ... exercise whatever you're testing against db, same as a real Open
}
```

Two things carry over from `Open` a little differently: `Options.WALDir`
is ignored (a separate WAL directory is a real-filesystem concept), and
`Checkpoint` still works but writes into the same in-memory filesystem
rather than real disk — fine for a test asserting a checkpoint was
written, not useful for anything that needs to survive process exit.

## Point-in-Time Consistent Reads (Snapshot)

`Snapshot` is for reading several keys "as of one moment" — e.g. rendering
a page that shows a post, its author, and a comment count, all needing to
agree with each other even if a write lands on one of them mid-request:

```go
snap := db.Snapshot()
defer snap.Close() // pins resources until closed — don't forget this

post, err := snap.Get(ctx, postKey)
author, err := snap.Get(ctx, authorKey)
commentCount, err := snap.Get(ctx, commentCountKey)
// all three reflect the exact same moment, even if a write lands on one
// of them between these three calls
```

`Snapshot` returns a `*pebble.SnapshotReader` satisfying `storagej.Reader`
(`Get`/`GetInto`/`Exists`/`Scan`/`PrefixScan` — everything but the writes,
which wouldn't mean anything coherent on a moment that's already passed).
It's cheap in Pebble — pins a sequence number, no data copy — so this is
for an ordinary request path, not something to reserve for rare
operations.

This is different from `Checkpoint`: `Checkpoint` is a durable backup you
can reopen after a restart; `Snapshot` is a consistency tool that lives
only as long as your process does and only as long as you keep it open.

## Event Hook → loggerj

storagej emits events; you decide where they go. Here is a `loggerj` wiring:

```go
logger := loggerj.NewLogger(loggerj.DefaultConfig())
logger.RegisterSub("STORAGEJ", loggerj.WithFields("service", "myapp"))

opts := storagej.DefaultOptions()
opts.OnEvent = func(e storagej.Event) {
 switch e.Type {
 case storagej.EventBackgroundError, storagej.EventWriteStallBegin:
  logger.Warn("STORAGEJ", []byte(e.Message), e.Fields...)
 default:
  logger.Info("STORAGEJ", []byte(e.Message), e.Fields...)
 }
}
```

## Periodic Metrics → loggerj

```go
opts.MetricsInterval = 45 * time.Second
opts.OnMetrics = func(m storagej.MetricsSnapshot) {
 logger.InfoString("STORAGEJ", "db metrics",
  "disk_mb", strconv.FormatInt(m.DiskMB, 10),
  "cache_hit", strconv.FormatFloat(m.CacheHitRatePercent, 'f', 0, 64),
  "files", strconv.FormatInt(m.TotalFiles, 10),
  "compactions", strconv.FormatInt(m.CompactionCount, 10),
  "wal_mb", strconv.FormatInt(m.WALSizeMB, 10),
 )
}

db, _ := pebble.Open("./data", opts)
```

Need a metrics snapshot on demand instead of on a timer — e.g. for an HTTP
health endpoint? `db.MetricsSnapshot()` returns the same
`storagej.MetricsSnapshot` shape immediately, without setting up
`MetricsInterval`/`OnMetrics` at all.

## Backup & Restore

```go
// Backup: flush + self-contained snapshot.
if err := db.Checkpoint("./backups/snap-1"); err != nil {
 log.Fatal(err)
}

// Restore: open the snapshot read-only and read data back.
restored, err := pebble.Open("./backups/snap-1", storagej.Options{ReadOnly: true})
if err != nil {
 log.Fatal(err)
}
defer restored.Close()

val, _ := restored.Get(ctx, []byte("user:1"))
```

## Building Sortable Keys with keyspace

```go
import "github.com/uretgec/storagej/keyspace"

id := keyspace.NewID() // sortable, 26-char, time-ordered

key, err := keyspace.Key("acme", keyspace.KindData, "post", id)
// key = "acme:d:post:01ARZ3NDEKTSV4RRFFQ69G5FAV"

db.Set(ctx, key, someJSON)
```

Iterating all records of one type for a tenant, oldest first — ids sort
chronologically, so no separate creation-time index is needed:

```go
prefix, _ := keyspace.Prefix("acme", keyspace.KindData, "post")
db.PrefixScan(ctx, prefix, func(key, value []byte) error {
 tenant, kind, typ, id, err := keyspace.Parse(key)
 // tenant/typ/id alias key — copy them before returning from this
 // callback if you need to retain them past this call.
 return nil
})
```

Deriving a record's creation time back out of its id — no stored
`created_at` field needed:

```go
created, err := keyspace.Time(id) // time.Time
```

## CRUD + a Unique Index with repository.Core

`repository.Core` is byte-level: it knows the key layout (via `keyspace`)
but nothing about any specific struct.

```go
import "github.com/uretgec/storagej/repository"

posts := repository.NewCore(db, "acme", "post") // tenant "acme", type "post"

// PutWithIndex keeps a record and a unique-field index (e.g. a slug)
// consistent in one atomic batch — rejects with ErrIndexConflict if the
// index value is already taken by a different id.
id := keyspace.NewID()
err := posts.PutWithIndex(ctx, id, "slug", "hello-world", someJSON)

// Look a record up by that index instead of by id.
foundID, err := posts.GetIndex(ctx, "slug", "hello-world")
val, err := posts.Get(ctx, foundID)

// Renaming the slug: swap the index atomically along with the data.
err = posts.ReplaceIndex(ctx, id, "slug", "hello-world", "hello-world-v2", updatedJSON)

// Cursor-based pagination, built in — the manual recipe above in one call.
err = posts.ListFrom(ctx, cursor, func(id string, value []byte) error {
 return nil // oldest first; return storagej.ErrStopIteration to stop early
})
```

Concurrent callers racing to claim the same index value are safe: `Core`
serializes `PutWithIndex`/`ReplaceIndex`/`DeleteWithIndex` with an internal
mutex, and Pebble's own exclusive lock on its data directory means those
are the only concurrent writers that can exist in the first place. Exactly
one caller wins; the rest get `repository.ErrIndexConflict`.

For a complete worked example of a domain type (`Post`) built on `Core` —
JSON encoding, field validation, and a generic `Repository[T, PT]` that a
second entity type (`Page`) reuses almost entirely — see
[`examples/blog`](examples/blog/README.md).

## Transparent Encryption at Rest (encrypted.Store)

```go
import (
 "crypto/rand"

 "github.com/uretgec/storagej/encrypted"
)

var key [32]byte
rand.Read(key[:]) // generate once; store it outside this package entirely

db, _ := pebble.Open("./data", storagej.DefaultOptions())
enc, err := encrypted.New(db, key)

enc.Set(ctx, []byte("secret"), []byte("sensitive value")) // encrypted at rest
val, _ := enc.Get(ctx, []byte("secret"))                  // decrypted transparently
```

`enc` implements `storagej.Store` itself, so it's a drop-in replacement
anywhere one is expected — including as the store passed to
`repository.NewCore` or `keyspace`-based code, which need no changes at
all to get encrypted values.

**Keys are never encrypted, only values** — this is a hard requirement,
not a limitation to work around: `keyspace`'s sortable ids and
`Scan`/`PrefixScan`'s ordering both depend on keys sorting as plain bytes.

## Live Change Notifications (watchable.Store)

```go
import "github.com/uretgec/storagej/watchable"

w := watchable.New(db)

ch, cancel := w.Watch([]byte("post:123")) // or WatchPrefix for a whole group
defer cancel()

go func() {
 for evt := range ch {
  fmt.Println(evt.Type, string(evt.Key), string(evt.Value))
 }
}()

w.Set(ctx, []byte("post:123"), []byte("new content")) // -> SET post:123 new content
```

Delivery is best-effort by design: each watch channel is buffered, and a
`Set`/`Delete` that finds a subscriber's buffer full drops that
notification rather than blocking the write. A watch channel is a live
tail, not a durable event log.

## Combining Decorators: Encrypted + Watchable, Stacked

Both decorators just wrap and return a `storagej.Store`, so they compose —
but **stacking order changes what watchers see**, which is worth
understanding rather than discovering by accident:

```go
// Watchable OUTSIDE, encrypted INSIDE: watchers see plaintext.
enc, _ := encrypted.New(db, key)
w := watchable.New(enc)

ch, cancel := w.Watch([]byte("secret"))
defer cancel()

w.Set(ctx, []byte("secret"), []byte("plaintext value"))
// evt.Value on ch is "plaintext value" — watchable notifies with the
// value IT received, before that value ever reaches encrypted.Set.
// Meanwhile db (on disk) only ever has the ciphertext.
```

```go
// Encrypted OUTSIDE, watchable INSIDE: watchers see ciphertext.
w := watchable.New(db)
enc, _ := encrypted.New(w, key)

ch, cancel := w.Watch([]byte("secret"))
defer cancel()

enc.Set(ctx, []byte("secret"), []byte("plaintext value"))
// evt.Value on ch is the ciphertext: encrypted.Set encrypts first, then
// calls watchable.Set with the already-encrypted bytes — watchable never
// sees the plaintext at all.
```

Neither order is "more correct" — they're different guarantees. The first
gives in-process watchers the real value while still encrypting at rest
(the more common intent). The second means even your own watchers never
see plaintext, useful if the watcher itself shouldn't be trusted with it.
Reads (`Get`) are unaffected by ordering either way — both stacks decrypt
correctly regardless of which decorator is outermost, since neither
`Get` implementation does anything unusual.

## Inspecting a Store from the Command Line (storagej-cli)

```bash
go build -o storagej-cli ./cmd/storagej-cli

./storagej-cli -data ./data set some-key some-value
./storagej-cli -data ./data get some-key
./storagej-cli -data ./data exists some-key
./storagej-cli -data ./data scan post:              # prefix scan
./storagej-cli -data ./data scan                    # (almost) the whole store
./storagej-cli -data ./data checkpoint ./backup
./storagej-cli -data ./data metrics                 # MetricsSnapshot as JSON
./storagej-cli -data ./data -readonly get some-key  # no accidental writes
```

Pebble locks its data directory exclusively — stop anything else that has
`-data`'s directory open (e.g. a running server) before running this.

## See Also

Everything above is the core toolkit: `storagej`, `pebble`, `keyspace`,
`repository`, `encrypted`, `watchable` — domain-agnostic, each optional,
none requiring any of the others.

For a complete worked application built on top of all of it — a
multi-tenant blog API with a generic domain layer, a framework-free HTTP
server, per-tenant auth and rate limiting, and Docker/systemd deploy
configs — see [`examples/blog`](examples/blog/README.md). It's a
reference, not a dependency: nothing above imports or knows about it.
