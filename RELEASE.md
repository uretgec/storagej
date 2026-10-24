release: storagej v2.0.0

First stable release. storagej is a domain-agnostic key-value toolkit on
top of CockroachDB's Pebble (v2.1.7): byte-in, byte-out at the core, with optional
opt-in layers for key layout, CRUD+indexing, encryption, and live change
notifications — plus a full worked reference application (a multi-tenant
blog API) proving the whole stack holds up end to end.

Core toolkit (storagej / pebble)
---------------------------------

- storagej.Store: Get/GetInto/Exists/Set/Delete/Scan/PrefixScan/NewBatch/
  Close. Logger-agnostic OnEvent/OnMetrics callbacks.
- storagej.Reader: new, purely additive read-only interface (Store already
  satisfies it structurally) — the return type of a point-in-time
  snapshot, where a "Set" wouldn't mean anything coherent.
- pebble.Open: the on-disk Store implementation, zero allocations on the
  Set/Delete hot path.
- pebble.OpenMem: purely in-memory store, same interface as Open — no real
  file I/O, ~410x faster to construct than Open+tempdir in this repo's own
  benchmarks. WALDir is ignored; Checkpoint writes into the same in-memory
  filesystem, not real disk.
- pebble.Store.Snapshot: point-in-time consistent multi-key reads via
  Pebble's own NewSnapshot — ~610ns/op, 14 allocs for a Snapshot+Close
  pair (Pebble v2.1.7, Apple M1 Pro).
- pebble.Store.MetricsSnapshot: on-demand storagej.MetricsSnapshot, same
  shape OnMetrics delivers periodically, computed immediately (e.g. for an
  HTTP health endpoint). storagej.MetricsSnapshot now carries snake_case
  json tags.

Optional layers
----------------

- keyspace: {tenant}:{kind}:{type}:{id} key codec plus NewID/Time, a
  26-char lexicographically-sortable id embedding a millisecond timestamp
  (so no separate created_at field or index is ever needed).
- repository.Core: byte-level CRUD + secondary-index helper (PutWithIndex/
  GetIndex/ReplaceIndex/DeleteWithIndex/List/ListFrom). Index-affecting
  operations are serialized by an internal mutex; combined with Pebble's
  own exclusive single-process directory lock, this makes concurrent
  index-uniqueness races provably safe, not just documented as unsafe —
  see TestPutWithIndexSerializesConcurrentConflicts and neighbors under
  -race.
- encrypted.Store: transparent AES-256-GCM value encryption. Keys are
  never encrypted (hard requirement — key ordering guarantees depend on
  it); a fresh random nonce per Set so identical plaintexts never produce
  identical ciphertext.
- watchable.Store: Watch(key) / WatchPrefix(prefix) live Set/Delete
  notifications over a channel. Delivery is best-effort by design —
  Set/Delete never blocks on a slow subscriber, a full buffer drops new
  events instead. Batch notifications fire only after Commit succeeds, in
  order.

Both decorators implement storagej.Store and compose freely; stacking
order changes what a watcher observes (plaintext vs ciphertext) — see
EXAMPLES.md.

Tooling
-------

- cmd/storagej-cli: get/set/delete/exists/scan/checkpoint/metrics against
  any storagej store, entirely through storagej.Store plus Pebble's two
  concrete extras. No knowledge of tenants, posts, or any domain concept.

Reference application (examples/blog)
--------------------------------------

- blog.Entity + blog.Repository[T, PT]: a generic CRUD/slug-index/
  pagination layer: Post and Page are each a struct plus four one-line
  methods on top of it.
- httpapi: framework-free net/http (stdlib ServeMux method+wildcard
  routing). Mount for single-tenant resources; MountMultiTenant for
  /t/{tenant}/... routing with a lazily-built, cached per-tenant
  repository.
- httpapi auth + rate limiting: per-tenant static API keys
  (crypto/subtle.ConstantTimeCompare, no JWT dependency) and a hand-written
  per-tenant token-bucket rate limiter (no golang.org/x/time/rate). Auth is
  checked before the rate limit, so failed auth can't drain a real
  tenant's quota.
- cmd/storagej-server: single binary wiring every layer together, with
  graceful shutdown and -multi-tenant/-api-keys/-rate-limit flags.
- deploy/: multi-stage Dockerfile (distroless runtime) and a sandboxed
  systemd unit.

Repository layout
------------------

blog/httpapi/cmd/deploy moved under examples/blog/, leaving the repository
root strictly to the domain-agnostic toolkit (storagej, pebble, keyspace,
repository, encrypted, watchable, cmd/storagej-cli). Nothing at the root
imports anything under examples/; deleting examples/blog entirely does not
affect the toolkit.

BREAKING CHANGES
-----------------

- Minimum Go version is now 1.26 (go.mod's go directive). Pebble v2.1.7's
  resolved dependency graph (golang.org/x/sys, x/text, ...) requires it;
  httpapi's routing alone would only need 1.22 (stdlib ServeMux
  method+wildcard routing).
- examples/blog's import paths changed from github.com/uretgec/storagej/
  {blog,httpapi} to github.com/uretgec/storagej/examples/blog/{blog,httpapi}.
- httpapi.NewServer no longer takes a repository argument; mount resources
  explicitly with Mount/MountMultiTenant after construction.
- httpapi list responses use {"items": [...]} instead of a resource-named
  field (previously {"posts": [...]}).
- storagej.MetricsSnapshot's JSON field names are now snake_case
  (disk_mb, cache_hit_rate_percent, ...) instead of the Go field names.
- Pebble is upgraded from v1.1.2 to v2.1.7 (import path
  github.com/cockroachdb/pebble/v2). Pebble v2 refuses databases in the
  oldest on-disk formats, including anything generated by RocksDB; see
  Pebble's own README for the format-major-version upgrade path. This
  repo's tests always start from a fresh store, so opening a data
  directory written under Pebble v1 is not exercised here.
- Options.WALDir: an empty value now means the WAL lives in the data
  directory (Pebble's own default); it used to default to {path}/wal.
  Pebble v2 rejects an unannounced WALDir change, so a data directory
  that already has {path}/wal must set WALDir: filepath.Join(path, "wal")
  explicitly. The new default also lets a Checkpoint be reopened
  read-only with default Options; a checkpoint of a store that uses a
  custom WALDir must be reopened with that same WALDir.

Full test suite green under `go test ./... -race`, including
examples/blog's own suite from its own directory.
