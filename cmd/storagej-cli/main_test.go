package main

import (
	"bytes"
	"context"
	"os"
	"strings"
	"testing"

	"github.com/uretgec/storagej"
	"github.com/uretgec/storagej/pebble"
)

func newTestDB(t *testing.T) *pebble.Store {
	t.Helper()
	db, err := pebble.OpenMem(storagej.DefaultOptions())
	if err != nil {
		t.Fatalf("openmem: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func TestRunSetGet(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	if err := run(ctx, db, nil, "set", []string{"k", "hello"}); err != nil {
		t.Fatalf("set: %v", err)
	}

	var out bytes.Buffer
	if err := run(ctx, db, &out, "get", []string{"k"}); err != nil {
		t.Fatalf("get: %v", err)
	}
	if got := strings.TrimSpace(out.String()); got != "hello" {
		t.Fatalf("get output = %q, want %q", got, "hello")
	}
}

func TestRunGetMissingKey(t *testing.T) {
	db := newTestDB(t)
	err := run(context.Background(), db, &bytes.Buffer{}, "get", []string{"missing"})
	if err == nil {
		t.Fatal("expected error for missing key")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Fatalf(`err = %v, want it to mention "not found"`, err)
	}
}

func TestRunDelete(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	if err := run(ctx, db, nil, "set", []string{"k", "v"}); err != nil {
		t.Fatalf("set: %v", err)
	}
	if err := run(ctx, db, nil, "delete", []string{"k"}); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if err := run(ctx, db, &bytes.Buffer{}, "get", []string{"k"}); err == nil {
		t.Fatal("get after delete should error")
	}
}

func TestRunDeleteAlias(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	if err := run(ctx, db, nil, "set", []string{"k", "v"}); err != nil {
		t.Fatalf("set: %v", err)
	}
	if err := run(ctx, db, nil, "del", []string{"k"}); err != nil {
		t.Fatalf(`"del" alias: %v`, err)
	}
	if err := run(ctx, db, &bytes.Buffer{}, "get", []string{"k"}); err == nil {
		t.Fatal("get after del should error")
	}
}

func TestRunExists(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	var out bytes.Buffer
	if err := run(ctx, db, &out, "exists", []string{"k"}); err != nil {
		t.Fatalf("exists: %v", err)
	}
	if got := strings.TrimSpace(out.String()); got != "false" {
		t.Fatalf("exists (missing) = %q, want %q", got, "false")
	}

	if err := run(ctx, db, nil, "set", []string{"k", "v"}); err != nil {
		t.Fatalf("set: %v", err)
	}

	out.Reset()
	if err := run(ctx, db, &out, "exists", []string{"k"}); err != nil {
		t.Fatalf("exists: %v", err)
	}
	if got := strings.TrimSpace(out.String()); got != "true" {
		t.Fatalf("exists (present) = %q, want %q", got, "true")
	}
}

func TestRunScanAll(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	for _, kv := range [][2]string{{"a", "1"}, {"b", "2"}, {"c", "3"}} {
		if err := run(ctx, db, nil, "set", kv[:]); err != nil {
			t.Fatalf("set %v: %v", kv, err)
		}
	}

	var out bytes.Buffer
	if err := run(ctx, db, &out, "scan", nil); err != nil {
		t.Fatalf("scan: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 3 {
		t.Fatalf("scan produced %d lines, want 3: %q", len(lines), out.String())
	}
	if lines[0] != "a\t1" || lines[1] != "b\t2" || lines[2] != "c\t3" {
		t.Fatalf("scan output = %v, want sorted a/b/c lines", lines)
	}
}

func TestRunScanPrefix(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	for _, kv := range [][2]string{{"post:1", "x"}, {"post:2", "y"}, {"page:1", "z"}} {
		if err := run(ctx, db, nil, "set", kv[:]); err != nil {
			t.Fatalf("set %v: %v", kv, err)
		}
	}

	var out bytes.Buffer
	if err := run(ctx, db, &out, "scan", []string{"post:"}); err != nil {
		t.Fatalf("scan: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("scan post: produced %d lines, want 2: %q", len(lines), out.String())
	}
	for _, l := range lines {
		if !strings.HasPrefix(l, "post:") {
			t.Fatalf("scan post: leaked non-matching line %q", l)
		}
	}
}

// TestRunCheckpoint proves the on-disk checkpoint round trip. It
// deliberately does NOT use the pebble.OpenMem-backed newTestDB helper the
// other tests share: an OpenMem store's Checkpoint writes into that same
// in-memory filesystem, not real disk (see pebble.OpenMem's doc comment),
// so verifying a real-disk round trip needs a real, disk-backed source
// store — see TestRunCheckpointFromMemStoreStaysInMemory just below for
// the in-memory case this test intentionally avoids.
func TestRunCheckpoint(t *testing.T) {
	db, err := pebble.Open(t.TempDir(), storagej.DefaultOptions())
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()

	ctx := context.Background()
	if err := run(ctx, db, nil, "set", []string{"k", "v"}); err != nil {
		t.Fatalf("set: %v", err)
	}

	dir := t.TempDir() + "/checkpoint"
	if err := run(ctx, db, nil, "checkpoint", []string{dir}); err != nil {
		t.Fatalf("checkpoint: %v", err)
	}

	restored, err := pebble.Open(dir, storagej.Options{ReadOnly: true})
	if err != nil {
		t.Fatalf("open checkpoint: %v", err)
	}
	defer restored.Close()
	got, err := restored.Get(ctx, []byte("k"))
	if err != nil {
		t.Fatalf("get from checkpoint: %v", err)
	}
	if string(got) != "v" {
		t.Fatalf("checkpoint = %q, want %q", got, "v")
	}
}

// TestRunCheckpointFromMemStoreStaysInMemory locks in the OpenMem
// limitation that broke the first version of TestRunCheckpoint above when
// it used an OpenMem-backed store for a real-disk round trip: checkpointing
// an in-memory store writes into that same in-memory filesystem, so a real
// filesystem path passed to checkpoint is never actually created. The fix
// for TestRunCheckpoint was to use a real, disk-backed store instead of
// changing this behavior, which is correct and intentional — this test
// makes sure it stays that way.
func TestRunCheckpointFromMemStoreStaysInMemory(t *testing.T) {
	db := newTestDB(t) // OpenMem
	ctx := context.Background()
	if err := run(ctx, db, nil, "set", []string{"k", "v"}); err != nil {
		t.Fatalf("set: %v", err)
	}

	dir := t.TempDir() + "/checkpoint"
	if err := run(ctx, db, nil, "checkpoint", []string{dir}); err != nil {
		t.Fatalf("checkpoint: %v", err)
	}

	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("expected no real directory at %q after checkpointing an in-memory store, stat err = %v", dir, err)
	}
}

func TestRunMetrics(t *testing.T) {
	db := newTestDB(t)
	var out bytes.Buffer
	if err := run(context.Background(), db, &out, "metrics", nil); err != nil {
		t.Fatalf("metrics: %v", err)
	}
	if !strings.Contains(out.String(), "disk_mb") {
		t.Fatalf("metrics output missing expected field: %s", out.String())
	}
}

func TestRunUnknownCommand(t *testing.T) {
	db := newTestDB(t)
	err := run(context.Background(), db, &bytes.Buffer{}, "bogus", nil)
	if err == nil {
		t.Fatal("expected error for unknown command")
	}
}

func TestRunWrongArgCounts(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	cases := []struct {
		cmd  string
		args []string
	}{
		{"get", nil},
		{"get", []string{"a", "b"}},
		{"set", []string{"only-one"}},
		{"delete", nil},
		{"exists", nil},
		{"scan", []string{"a", "b"}},
		{"checkpoint", nil},
	}
	for _, c := range cases {
		if err := run(ctx, db, &bytes.Buffer{}, c.cmd, c.args); err == nil {
			t.Errorf("%s with args %v: expected a usage error", c.cmd, c.args)
		}
	}
}
