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
| Go         | 1.21+                    |
| Pebble     | cockroachdb/pebble v1.x  |
| Run date   | 2026-07-24               |

## Results

| Benchmark              | ns/op   | B/op | allocs/op |
|------------------------|---------|------|-----------|
| `Set`                  | 600.1   | 5    | **0**     |
| `Get`                  | 223.5   | 128  | 1         |
| `Exists`               | *TBD*   | *TBD*| *TBD*     |
| `GetIntoReused`        | *TBD*   | *TBD*| *TBD*     |
| `Delete`               | 586.1   | 4    | **0**     |
| `BatchCommit` (100 ops)| 24623   | 4626 | 300       |
| `PrefixScan` (10 keys) | 1007    | 24   | 2         |
| `ScanFull` (100 keys)  | 3814    | 24   | 3         |

## Analysis

### Write path — zero allocation

`Set` and `Delete` allocate **nothing** on the hot path (0 allocs/op). The
small `B/op` figures (5 and 4 bytes) come from Pebble's internal buffers being
reused, not from new allocations. Writes use `pebble.NoSync`; the WAL provides
durability without an `fsync` per operation.

### Read path — one intentional allocation

`Get` shows exactly **1 alloc/op (128 B)** — this is the **safe copy** of the
value. Pebble returns a slice bound to an internal closer; copying it before
closing guarantees the caller can retain the bytes safely. This allocation is
deliberate and documented. ### Exists and GetInto (added, numbers pending)

`Exists` inspects Pebble's returned value and closes it immediately without
copying — expected to land close to `Get`'s ns/op minus the copy, at
**0 allocs/op**, matching `Set`/`Delete`.

`GetInto` reuses the caller's buffer when its capacity is sufficient, so a
warmed-up buffer (`BenchmarkGetIntoReused`) is expected at **0 allocs/op**
once the buffer has grown to fit the value once. The *TBD* rows above need a
real run — this environment has no Go toolchain or network access to fetch
the Pebble module, so run it locally and fill these in:

```bash
go test ./pebble -bench='Exists|GetInto' -benchmem -run=^$
```

### Batch commit

`BatchCommit` commits a 100-operation batch atomically (~246 ns per operation
amortized). The ~300 allocations are dominated by the benchmark's own key
construction (`fmt.Sprintf` + `[]byte` conversion ≈ 2 allocs/op); Pebble's
batch internals add roughly one more per op. In real workloads with pre-built
keys, the batch path is effectively allocation-light. Batching also collapses
many writes into a single WAL commit, reducing I/O overhead.

### Scans

Both scans allocate a **constant** number of times regardless of how many keys
are visited (2–3 allocs total), because the iterator is reused across the loop:

- `PrefixScan` stops after 10 keys via `ErrStopIteration` (~100 ns/key).
- `ScanFull` walks all 100 keys (~38 ns/key).

This makes cursor-based pagination cheap: the cost is driven by the page size,
not the total dataset size (O(1) seek, no OFFSET scanning).

## Running Benchmarks

```bash
# All benchmarks with allocation reporting
go test ./pebble -bench=. -benchmem -run=^$

# A single benchmark, longer run for stability
go test ./pebble -bench=BenchmarkSet -benchmem -run=^$ -benchtime=5s

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
- Each benchmark opens an isolated store in a fresh temp directory
  (`b.TempDir()`), so runs do not interfere with each other.
- Defaults under test: 64MB cache, 64MB memtable, CPU/2 compaction threads,
  64 open files (`storagej.DefaultOptions()`).
