# storagej Examples

All examples assume these imports:

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
no separate COUNT query:

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
