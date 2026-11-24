package diff

import (
	"testing"
	"time"

	"synclens/internal/scanner"
)

func TestCompareStatuses(t *testing.T) {
	t0 := time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC).UnixNano()
	tNew := t0 + int64(10*time.Second)

	local := map[string]scanner.FileMeta{
		"only_local.txt":  {RelPath: "only_local.txt", Size: 10, MTimeNS: t0},
		"newer_local.txt": {RelPath: "newer_local.txt", Size: 22, MTimeNS: tNew},
		"newer_peer.txt":  {RelPath: "newer_peer.txt", Size: 20, MTimeNS: t0},
		"same.txt":        {RelPath: "same.txt", Size: 30, MTimeNS: t0},
		"same_copied.txt": {RelPath: "same_copied.txt", Size: 30, MTimeNS: t0},
		"size_diff.txt":   {RelPath: "size_diff.txt", Size: 35, MTimeNS: t0},
		"hash_diff.txt":   {RelPath: "hash_diff.txt", Size: 40, MTimeNS: t0, Hash: "aaa"},
	}

	peer := map[string]scanner.FileMeta{
		"only_peer.txt":   {RelPath: "only_peer.txt", Size: 15, MTimeNS: t0},
		"newer_local.txt": {RelPath: "newer_local.txt", Size: 20, MTimeNS: t0},
		"newer_peer.txt":  {RelPath: "newer_peer.txt", Size: 25, MTimeNS: tNew},
		"same.txt":        {RelPath: "same.txt", Size: 30, MTimeNS: t0},
		"same_copied.txt": {RelPath: "same_copied.txt", Size: 30, MTimeNS: tNew},
		"size_diff.txt":   {RelPath: "size_diff.txt", Size: 36, MTimeNS: t0},
		"hash_diff.txt":   {RelPath: "hash_diff.txt", Size: 40, MTimeNS: t0, Hash: "bbb"},
	}

	items, sum := Compare(local, peer)
	if sum.Total != 8 {
		t.Fatalf("expected 8 total items, got %d", sum.Total)
	}

	got := map[string]Status{}
	for _, it := range items {
		got[it.RelPath] = it.Status
	}

	expected := map[string]Status{
		"only_local.txt":  StatusNewLocal,
		"only_peer.txt":   StatusNewPeer,
		"newer_local.txt": StatusNewerLocal,
		"newer_peer.txt":  StatusNewerPeer,
		"same.txt":        StatusIdentical,
		"same_copied.txt": StatusIdentical,
		"size_diff.txt":   StatusSizeDiff,
		"hash_diff.txt":   StatusHashDiff,
	}

	for k, want := range expected {
		if got[k] != want {
			t.Errorf("%s: expected %s, got %s", k, want, got[k])
		}
	}
}

func TestCompareWithBaselineDeletionsConflictsAndMoves(t *testing.T) {
	t0 := time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC).UnixNano()
	tNew := t0 + int64(20*time.Second)

	baseline := map[string]BaselineMeta{
		"deleted_on_pc.txt":       {Size: 100, MTimeNS: t0},
		"deleted_on_laptop.txt":   {Size: 200, MTimeNS: t0},
		"conflict_del_local.txt":  {Size: 300, MTimeNS: t0},
		"conflict_del_peer.txt":   {Size: 400, MTimeNS: t0},
		"old_folder/renamed.pdf":  {Size: 5000, MTimeNS: t0},
		"ambig/dup_a.bin":         {Size: 777, MTimeNS: t0},
		"ambig/dup_b.bin":         {Size: 777, MTimeNS: t0},
		"identical.txt":           {Size: 50, MTimeNS: t0},
	}

	local := map[string]scanner.FileMeta{
		"deleted_on_laptop.txt":  {RelPath: "deleted_on_laptop.txt", Size: 200, MTimeNS: t0},
		"conflict_del_peer.txt":  {RelPath: "conflict_del_peer.txt", Size: 450, MTimeNS: tNew},
		"new_folder/renamed.pdf": {RelPath: "new_folder/renamed.pdf", Size: 5000, MTimeNS: t0},
		"new_on_pc.txt":          {RelPath: "new_on_pc.txt", Size: 90, MTimeNS: tNew},
		"ambig/dup_1.bin":        {RelPath: "ambig/dup_1.bin", Size: 777, MTimeNS: t0},
		"ambig/dup_2.bin":        {RelPath: "ambig/dup_2.bin", Size: 777, MTimeNS: t0},
		"identical.txt":          {RelPath: "identical.txt", Size: 50, MTimeNS: t0},
	}

	peer := map[string]scanner.FileMeta{
		"deleted_on_pc.txt":      {RelPath: "deleted_on_pc.txt", Size: 100, MTimeNS: t0},
		"conflict_del_local.txt": {RelPath: "conflict_del_local.txt", Size: 305, MTimeNS: tNew},
		"old_folder/renamed.pdf": {RelPath: "old_folder/renamed.pdf", Size: 5000, MTimeNS: t0},
		"ambig/dup_a.bin":        {RelPath: "ambig/dup_a.bin", Size: 777, MTimeNS: t0},
		"ambig/dup_b.bin":        {RelPath: "ambig/dup_b.bin", Size: 777, MTimeNS: t0},
		"identical.txt":          {RelPath: "identical.txt", Size: 50, MTimeNS: t0},
	}

	items, sum := CompareWithBaseline(local, peer, baseline)
	if sum.EmptyLocalGuard || sum.EmptyPeerGuard {
		t.Fatalf("unexpected empty guard: local=%v peer=%v", sum.EmptyLocalGuard, sum.EmptyPeerGuard)
	}

	byPath := make(map[string]Item, len(items))
	for _, it := range items {
		byPath[it.RelPath] = it
	}

	checks := []struct {
		path   string
		status Status
		action string
		oldRel string
	}{
		{"deleted_on_pc.txt", StatusDeletedLocal, "delete_peer", ""},
		{"deleted_on_laptop.txt", StatusDeletedPeer, "delete_local", ""},
		{"conflict_del_local.txt", StatusConflictDeleteLocal, "skip", ""},
		{"conflict_del_peer.txt", StatusConflictDeletePeer, "skip", ""},
		{"new_folder/renamed.pdf", StatusMovePeer, "move_peer", "old_folder/renamed.pdf"},
		{"new_on_pc.txt", StatusNewLocal, "push", ""},
		{"ambig/dup_a.bin", StatusDeletedLocal, "delete_peer", ""},
		{"ambig/dup_1.bin", StatusNewLocal, "push", ""},
		{"identical.txt", StatusIdentical, "skip", ""},
	}
	for _, c := range checks {
		it, ok := byPath[c.path]
		if !ok {
			t.Errorf("missing item %s", c.path)
			continue
		}
		if it.Status != c.status || it.DefaultAction != c.action || it.OldRelPath != c.oldRel {
			t.Errorf("%s: got (%s, %s, old=%q), want (%s, %s, old=%q)",
				c.path, it.Status, it.DefaultAction, it.OldRelPath, c.status, c.action, c.oldRel)
		}
	}
	if _, stillHasOld := byPath["old_folder/renamed.pdf"]; stillHasOld {
		t.Errorf("expected old_folder/renamed.pdf merged into MOVE_PEER")
	}

	// Empty-root guard should fire when one side is completely empty with non-empty baseline.
	_, emptySum := CompareWithBaseline(map[string]scanner.FileMeta{}, peer, baseline)
	if !emptySum.EmptyLocalGuard || emptySum.MovePeer != 0 {
		t.Fatalf("expected EmptyLocalGuard=true and MovePeer=0, got %+v", emptySum)
	}
}
