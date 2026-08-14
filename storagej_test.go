package storagej

import "testing"

// -----------------------------------------------------------------------------
// Options.Normalize
// -----------------------------------------------------------------------------

func TestOptionsNormalizeDefaults(t *testing.T) {
	o := Options{}.Normalize()
	d := DefaultOptions()

	if o.CacheSizeMB != d.CacheSizeMB {
		t.Errorf("CacheSizeMB = %d, want %d", o.CacheSizeMB, d.CacheSizeMB)
	}
	if o.MemTableSizeMB != d.MemTableSizeMB {
		t.Errorf("MemTableSizeMB = %d, want %d", o.MemTableSizeMB, d.MemTableSizeMB)
	}
	if o.MaxOpenFiles != d.MaxOpenFiles {
		t.Errorf("MaxOpenFiles = %d, want %d", o.MaxOpenFiles, d.MaxOpenFiles)
	}
	if o.CompactionThreads != d.CompactionThreads {
		t.Errorf("CompactionThreads = %d, want %d", o.CompactionThreads, d.CompactionThreads)
	}
}

func TestOptionsNormalizeNegativeValuesFallBackToDefaults(t *testing.T) {
	o := Options{
		CacheSizeMB:       -10,
		MemTableSizeMB:    -1,
		MaxOpenFiles:      -5,
		CompactionThreads: -2,
	}.Normalize()
	d := DefaultOptions()

	if o.CacheSizeMB != d.CacheSizeMB {
		t.Errorf("negative CacheSizeMB not defaulted: got %d", o.CacheSizeMB)
	}
	if o.MemTableSizeMB != d.MemTableSizeMB {
		t.Errorf("negative MemTableSizeMB not defaulted: got %d", o.MemTableSizeMB)
	}
	if o.MaxOpenFiles != d.MaxOpenFiles {
		t.Errorf("negative MaxOpenFiles not defaulted: got %d", o.MaxOpenFiles)
	}
	if o.CompactionThreads != d.CompactionThreads {
		t.Errorf("negative CompactionThreads not defaulted: got %d", o.CompactionThreads)
	}
}

func TestOptionsNormalizeZeroValuesFallBackToDefaults(t *testing.T) {
	// Zero is the Go zero-value for an unset int field, so it must be treated
	// the same as "not configured", exactly like negative values.
	o := Options{}.Normalize()
	d := DefaultOptions()

	if o.CacheSizeMB != d.CacheSizeMB || o.MemTableSizeMB != d.MemTableSizeMB ||
		o.MaxOpenFiles != d.MaxOpenFiles || o.CompactionThreads != d.CompactionThreads {
		t.Fatalf("zero-value Options did not fully default: %+v", o)
	}
}

func TestOptionsNormalizePreservesExplicitValues(t *testing.T) {
	o := Options{
		CacheSizeMB:       128,
		MemTableSizeMB:    32,
		MaxOpenFiles:      256,
		CompactionThreads: 4,
		WALDir:            "/custom/wal",
		ReadOnly:          true,
	}.Normalize()

	if o.CacheSizeMB != 128 {
		t.Errorf("CacheSizeMB overwritten: got %d, want 128", o.CacheSizeMB)
	}
	if o.MemTableSizeMB != 32 {
		t.Errorf("MemTableSizeMB overwritten: got %d, want 32", o.MemTableSizeMB)
	}
	if o.MaxOpenFiles != 256 {
		t.Errorf("MaxOpenFiles overwritten: got %d, want 256", o.MaxOpenFiles)
	}
	if o.CompactionThreads != 4 {
		t.Errorf("CompactionThreads overwritten: got %d, want 4", o.CompactionThreads)
	}
	if o.WALDir != "/custom/wal" {
		t.Errorf("WALDir not preserved: got %q", o.WALDir)
	}
	if !o.ReadOnly {
		t.Error("ReadOnly not preserved")
	}
}

func TestOptionsNormalizeDoesNotMutateReceiver(t *testing.T) {
	// Normalize has a value receiver, so it must return a modified copy
	// without touching the original. This guards against a future change
	// accidentally switching it to a pointer receiver.
	o := Options{}
	_ = o.Normalize()

	if o.CacheSizeMB != 0 || o.MemTableSizeMB != 0 || o.MaxOpenFiles != 0 {
		t.Fatalf("Normalize mutated the original receiver: %+v", o)
	}
}

// -----------------------------------------------------------------------------
// EventType
// -----------------------------------------------------------------------------

func TestEventTypeString(t *testing.T) {
	cases := []struct {
		et   EventType
		want string
	}{
		{EventBackgroundError, "BACKGROUND_ERROR"},
		{EventWriteStallBegin, "WRITE_STALL_BEGIN"},
		{EventWriteStallEnd, "WRITE_STALL_END"},
		{EventCompactionBegin, "COMPACTION_BEGIN"},
		{EventCompactionEnd, "COMPACTION_END"},
		{EventWALCreated, "WAL_CREATED"},
	}
	for _, c := range cases {
		if got := c.et.String(); got != c.want {
			t.Errorf("%d.String() = %q, want %q", c.et, got, c.want)
		}
	}
}

func TestEventTypeStringUnknown(t *testing.T) {
	unknown := EventType(255)
	if got := unknown.String(); got != "UNKNOWN" {
		t.Errorf("String() = %q, want UNKNOWN", got)
	}
}
