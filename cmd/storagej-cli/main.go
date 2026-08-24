// Command storagej-cli is a minimal, domain-agnostic command-line tool for
// inspecting and manipulating any storagej-backed Pebble store directly —
// the redis-cli equivalent for this toolkit. It depends only on
// storagej.Store (plus two Pebble-specific methods for checkpoint/metrics,
// see checkpointer below); it knows nothing about blog, tenants, or any
// other domain, matching the rest of the toolkit.
//
// Pebble takes an exclusive lock on its data directory — this tool can't
// open a store that a running storagej-server (or any other process) has
// open at the same time. Stop the server first, or point this at a
// checkpoint/copy instead.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/uretgec/storagej"
	"github.com/uretgec/storagej/pebble"
)

func main() {
	dataDir := flag.String("data", "./data", "Pebble data directory")
	readOnly := flag.Bool("readonly", false, "open the store read-only (set/delete fail; safe for pure inspection)")
	flag.Usage = usage
	flag.Parse()

	args := flag.Args()
	if len(args) < 1 {
		usage()
		os.Exit(2)
	}

	opts := storagej.DefaultOptions()
	opts.ReadOnly = *readOnly
	db, err := pebble.Open(*dataDir, opts)
	if err != nil {
		fmt.Fprintf(os.Stderr, "storagej-cli: open %q: %v\n", *dataDir, err)
		os.Exit(1)
	}
	defer db.Close()

	if err := run(context.Background(), db, os.Stdout, args[0], args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "storagej-cli: %v\n", err)
		os.Exit(1)
	}
}

// checkpointer is satisfied by *pebble.Store. Kept as a small local
// interface — matching the rest of this toolkit's preference for
// interfaces over concrete types — so run only depends on the two methods
// it actually needs, not the whole pebble.Store type.
type checkpointer interface {
	Checkpoint(dir string) error
	MetricsSnapshot() storagej.MetricsSnapshot
}

// run dispatches one command. db satisfying both storagej.Store and
// checkpointer (which *pebble.Store always does) rather than taking the
// concrete type means this is testable against pebble.OpenMem() — no real
// disk needed for the test suite.
func run(ctx context.Context, db interface {
	storagej.Store
	checkpointer
}, out io.Writer, cmd string, args []string) error {
	switch cmd {
	case "get":
		return runGet(ctx, db, out, args)
	case "set":
		return runSet(ctx, db, args)
	case "delete", "del":
		return runDelete(ctx, db, args)
	case "exists":
		return runExists(ctx, db, out, args)
	case "scan":
		return runScan(ctx, db, out, args)
	case "checkpoint":
		return runCheckpoint(db, args)
	case "metrics":
		return runMetrics(db, out)
	default:
		return fmt.Errorf("unknown command %q — see -h", cmd)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `storagej-cli — inspect and manipulate a storagej Pebble store directly.

Usage:
  storagej-cli -data <dir> get <key>
  storagej-cli -data <dir> set <key> <value>
  storagej-cli -data <dir> delete <key>
  storagej-cli -data <dir> exists <key>
  storagej-cli -data <dir> scan [prefix]
  storagej-cli -data <dir> checkpoint <dest-dir>
  storagej-cli -data <dir> metrics

Keys and values are treated as raw strings — this tool doesn't know about
JSON, tenants, or any higher-level key layout; it only depends on
storagej.Store, the same as everything else in this toolkit.

Pass -readonly to open the store without allowing set/delete — useful when
you only want to look, not touch.

Pebble locks its data directory exclusively: stop anything else that has
<dir> open (e.g. storagej-server) before running this.
`)
}

func runGet(ctx context.Context, db storagej.Store, out io.Writer, args []string) error {
	if len(args) != 1 {
		return errors.New("usage: get <key>")
	}
	val, err := db.Get(ctx, []byte(args[0]))
	if errors.Is(err, storagej.ErrNotFound) {
		return fmt.Errorf("key %q not found", args[0])
	}
	if err != nil {
		return err
	}
	fmt.Fprintln(out, string(val))
	return nil
}

func runSet(ctx context.Context, db storagej.Store, args []string) error {
	if len(args) != 2 {
		return errors.New("usage: set <key> <value>")
	}
	return db.Set(ctx, []byte(args[0]), []byte(args[1]))
}

func runDelete(ctx context.Context, db storagej.Store, args []string) error {
	if len(args) != 1 {
		return errors.New("usage: delete <key>")
	}
	return db.Delete(ctx, []byte(args[0]))
}

func runExists(ctx context.Context, db storagej.Store, out io.Writer, args []string) error {
	if len(args) != 1 {
		return errors.New("usage: exists <key>")
	}
	ok, err := db.Exists(ctx, []byte(args[0]))
	if err != nil {
		return err
	}
	fmt.Fprintln(out, ok)
	return nil
}

// runScan scans a prefix (if given) or the entire keyspace (nil start/end
// on storagej.Store.Scan is unbounded — this passes straight through to
// Pebble's own IterOptions, which treats nil LowerBound/UpperBound as "no
// bound" natively).
func runScan(ctx context.Context, db storagej.Store, out io.Writer, args []string) error {
	if len(args) > 1 {
		return errors.New("usage: scan [prefix]")
	}
	w := bufio.NewWriter(out)
	defer w.Flush()

	fn := func(key, value []byte) error {
		fmt.Fprintf(w, "%s\t%s\n", key, value)
		return nil
	}

	if len(args) == 1 {
		return db.PrefixScan(ctx, []byte(args[0]), fn)
	}
	return db.Scan(ctx, nil, nil, fn)
}

func runCheckpoint(db checkpointer, args []string) error {
	if len(args) != 1 {
		return errors.New("usage: checkpoint <dest-dir>")
	}
	return db.Checkpoint(args[0])
}

func runMetrics(db checkpointer, out io.Writer) error {
	enc := json.NewEncoder(out)
	enc.SetIndent("", "  ")
	return enc.Encode(db.MetricsSnapshot())
}
