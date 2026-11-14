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
