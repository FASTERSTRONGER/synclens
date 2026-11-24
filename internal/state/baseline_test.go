package state

import (
	"testing"
	"time"
)

func TestBaselineStoreRoundTripAndReset(t *testing.T) {
	k1 := PairKey("PC-Home", `D:\Projects\Data`, "Laptop-Work", `D:\Projects\Data`)
	k2 := PairKey("laptop-work", `D:\Projects\Data\`, "pc-home", `d:\Projects\Data`)
	if k1 != k2 {
		t.Fatalf("expected symmetrical pair key, got %s vs %s", k1, k2)
	}
	if !ValidKey(k1) {
		t.Fatalf("expected valid 16-hex key, got %q", k1)
	}

	kDiff1 := PairKey("PC-Home", `D:\FolderA`, "Laptop-Work", `E:\FolderB`)
	kDiff2 := PairKey("Laptop-Work", `E:\FolderB`, "PC-Home", `D:\FolderA`)
	if kDiff1 != kDiff2 {
		t.Fatalf("expected symmetrical key for different paths, got %s vs %s", kDiff1, kDiff2)
	}

	if a, b := PairKey("PC", `D:\`, "Laptop", `D:/`), PairKey("Laptop", `d:`, "PC", `d:\`); a != b {
		t.Fatalf("expected symmetrical drive-root key, got %s vs %s", a, b)
	}

	badKeys := []string{"", "../evil", "0123456789abcde", "0123456789ABCDEF", "zzzzzzzzzzzzzzzz"}
	for _, bk := range badKeys {
		if ValidKey(bk) {
			t.Errorf("expected ValidKey(%q) == false", bk)
		}
	}

	store := NewStore(t.TempDir())
	empty, err := store.Load(k1)
	if err != nil || empty != nil {
		t.Fatalf("expected nil baseline when missing, got (%+v, %v)", empty, err)
	}

	now := time.Now().UnixNano()
	orig := &Baseline{
		Version:   CurrentVersion,
		Key:       k1,
		UpdatedAt: now,
		Entries: map[string]Entry{
			"docs/readme.md": {Size: 123, MTimeNS: 456789, Hash: "deadbeef"},
		},
	}
	if err := store.Save(orig); err != nil {
		t.Fatalf("Save failed: %v", err)
	}

	loaded, err := store.Load(k1)
	if err != nil || loaded == nil {
		t.Fatalf("Load failed: %+v, %v", loaded, err)
	}
	if loaded.UpdatedAt != now || loaded.Entries["docs/readme.md"].Size != 123 || loaded.Entries["docs/readme.md"].Hash != "deadbeef" {
		t.Fatalf("unexpected loaded baseline: %+v", loaded)
	}

	if err := store.Reset(k1); err != nil {
		t.Fatalf("Reset failed: %v", err)
	}
	afterReset, err := store.Load(k1)
	if err != nil || afterReset != nil {
		t.Fatalf("expected nil after Reset, got (%+v, %v)", afterReset, err)
	}
}
