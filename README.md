# storagej

A high-performance, **domain-agnostic** key-value store for Go, built on
[CockroachDB's Pebble](https://github.com/cockroachdb/pebble) engine.

storagej transports raw bytes only. It has no knowledge of tenants, models,
indexes, or relations — those concerns belong to higher layers (e.g., an ORM).
This keeps it a clean, reusable storage primitive.

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

Requires **Go 1.21+**.

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
```

## License

MIT — see [LICENSE](LICENSE).
