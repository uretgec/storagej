# storagej Benchmarks

Performance benchmarks for the Pebble-backed `storagej.Store` implementation.
The focus is the hot path: single-key reads/writes and scans. All benchmarks
report allocations via `-benchmem`.

## Environment

|            |                          |
|------------|--------------------------|
| OS         | darwin (macOS)           |
| Arch       | arm64                    |
| CPU        | Apple M1 Pro             |
| Go         | 1.26                     |
| Pebble     | cockroachdb/pebble/v2 v2.1.7 |
| Run date   | 2026-10-24               |

## Results

| Benchmark              | ns/op   | B/op | allocs/op |
|------------------------|---------|------|-----------|
| `Set`                  | 545.0   | 6    | **0**     |
| `Get`                  | 283.9   | 128  | 1         |
| `Exists`               | 249.1   | 0    | **0**     |
| `GetIntoReused`        | 254.4   | 0    | **0**     |
| `Delete`               | 497.5   | 7    | **0**     |
| `BatchCommit` (100 ops)| 21956   | 3047 | 200       |
| `PrefixScan` (10 keys) | 907.8   | 16   | 1         |
| `ScanFull` (100 keys)  | 3823    | 16   | 2         |
| `Snapshot` (+`Close`)  | 609.3   | 224  | 14        |
| `OpenMem`              | 104525  | 277200 | 411     |
| `OpenDisk`             | 42900180 | 285524 | 535    |

## Analysis

### Write path — zero allocation

`Set` and `Delete` allocate **nothing** on the hot path (0 allocs/op). The
small `B/op` figures (6 and 7 bytes) come from Pebble's internal buffers being
reused, not from new allocations. Writes use `pebble.NoSync`; the WAL provides
durability without an `fsync` per operation.

### Read path — one intentional allocation

`Get` shows exactly **1 alloc/op (128 B)** — this is the **safe copy** of the
value. Pebble returns a slice bound to an internal closer; copying it before
closing guarantees the caller can retain the bytes safely. This allocation is
deliberate and documented. Latency is ~283 ns/op (five runs: 281.6–289.0 ns),
about 27% slower than the ~224 ns measured under Pebble v1; allocations are
unchanged. The cause was not isolated: Pebble v2 and Go 1.26 changed
together.

### Exists and GetInto

`Exists` inspects Pebble's returned value and closes it immediately without
copying: **0 allocs/op**, ~249 ns/op, about 35 ns cheaper than `Get` (the
copy).

`GetInto` reuses the caller's buffer when its capacity is sufficient, so a
warmed-up buffer (`BenchmarkGetIntoReused`) is also **0 allocs/op**
(~254 ns/op). The buffer grows once to fit the value, then stays put.

### Batch commit

`BatchCommit` commits a 100-operation batch atomically (~220 ns per operation
amortized). The ~200 allocations are the benchmark's own key construction
(`fmt.Sprintf` + `[]byte` conversion ≈ 2 allocs/op); under Pebble v2 the batch
internals add no measurable per-op allocation on top (it was ~300 allocs under
Pebble v1). In real workloads with pre-built keys, the batch path is
effectively allocation-light. Batching also collapses
many writes into a single WAL commit, reducing I/O overhead.

### Scans

Both scans allocate a **constant** number of times regardless of how many keys
are visited (1–2 allocs total), because the iterator is reused across the loop:

- `PrefixScan` stops after 10 keys via `ErrStopIteration` (~90 ns/key).
- `ScanFull` walks all 100 keys (~38 ns/key).

This makes cursor-based pagination cheap: the cost is driven by the page size,
not the total dataset size (O(1) seek, no OFFSET scanning).

### Snapshot

A `Snapshot` + `Close` pair costs ~609 ns and 14 allocs (224 B). A memory
profile puts the allocations inside Pebble, not in `SnapshotReader`: it
accounts for 8 of the 14 per-op allocations, 7 of them on the `Close` path
(`Snapshot.Close` → `maybeScheduleCompaction` → `pickAnyCompaction`, which
allocates while recomputing level scores) and 1 in taking the snapshot. The
same pair measured ~54 ns / 1 alloc under Pebble v1. Rule of thumb: one
snapshot per request, several reads through it.

## Running Benchmarks

```bash
# All benchmarks with allocation reporting
go test ./pebble -bench=. -benchmem -run=^$

# A single benchmark, longer run for stability
go test ./pebble -bench=BenchmarkSet -benchmem -run=^$ -benchtime=5s

# Where do the allocations come from? (every allocation sampled)
go test ./pebble -run='^$' -bench='BenchmarkSnapshot$' -benchmem \
  -memprofile=mem.out -memprofilerate=1 -o pebble.test
go tool pprof -sample_index=alloc_objects -top -nodecount=20 pebble.test mem.out

# Compare before/after a change (requires benchstat)
go test ./pebble -bench=. -benchmem -run=^$ -count=5 > old.txt
# ... make change ...
go test ./pebble -bench=. -benchmem -run=^$ -count=5 > new.txt
benchstat old.txt new.txt
```

## Methodology Notes

- **`Set`/`Delete`** reuse a pre-allocated key buffer (`key[:0]` +
  `strconv.AppendInt`), so measured allocations are Pebble's, not key
  construction's.
- **`Get`** reads a 128-byte value; the reported 128 B/op is the safe copy.
- **`BatchCommit`** builds keys with `fmt.Sprintf` inside the loop; those
  allocations belong to the benchmark, not to the batch commit itself.
- **`PrefixScan`/`ScanFull`** iterate over a pre-populated keyspace
  (1000 and 100 keys respectively).
- **`Snapshot`** measures a `Snapshot()` + `Close()` pair on a 100-key store;
  **`OpenMem`/`OpenDisk`** measure store construction plus `Close`.
- Each benchmark opens an isolated store in a fresh temp directory
  (`b.TempDir()`), so runs do not interfere with each other.
- Defaults under test: 64MB cache, 64MB memtable, CPU/2 compaction threads,
  64 open files (`storagej.DefaultOptions()`).
