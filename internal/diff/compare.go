package diff

import (
	"path"
	"sort"
	"strings"

	"synclens/internal/scanner"
)

type Status string

const (
	StatusNewLocal            Status = "NEW_LOCAL"
	StatusNewPeer             Status = "NEW_PEER"
	StatusNewerLocal          Status = "NEWER_LOCAL"
	StatusNewerPeer           Status = "NEWER_PEER"
	StatusSizeDiff            Status = "SIZE_DIFF"
	StatusHashDiff            Status = "HASH_DIFF"
	StatusIdentical           Status = "IDENTICAL"
	StatusDeletedLocal        Status = "DELETED_LOCAL"
	StatusDeletedPeer         Status = "DELETED_PEER"
	StatusConflictDeleteLocal Status = "CONFLICT_DELETE_LOCAL"
	StatusConflictDeletePeer  Status = "CONFLICT_DELETE_PEER"
	StatusMoveLocal           Status = "MOVE_LOCAL"
	StatusMovePeer            Status = "MOVE_PEER"
)

// BaselineMeta captures the last synchronized state of a file path.
type BaselineMeta struct {
	Size    int64
	MTimeNS int64
	Hash    string
}

// Item represents one compared relative file path (or matched rename) across Local and Peer.
type Item struct {
	RelPath       string `json:"rel_path"`
	OldRelPath    string `json:"old_rel_path,omitempty"`
	Status        Status `json:"status"`
	DefaultAction string `json:"default_action"` // push, pull, delete_local, delete_peer, move_local, move_peer, skip
	HasLocal      bool   `json:"has_local"`
	HasPeer       bool   `json:"has_peer"`
	LocalSize     int64  `json:"local_size"`
	PeerSize      int64  `json:"peer_size"`
	LocalMTimeNS  int64  `json:"local_mtime_ns"`
	PeerMTimeNS   int64  `json:"peer_mtime_ns"`
	LocalHash     string `json:"local_hash,omitempty"`
	PeerHash      string `json:"peer_hash,omitempty"`
}

// Summary counts items per status and tracks empty-root guards.
type Summary struct {
	Total               int  `json:"total"`
	NewLocal            int  `json:"new_local"`
	NewPeer             int  `json:"new_peer"`
	NewerLocal          int  `json:"newer_local"`
	NewerPeer           int  `json:"newer_peer"`
	SizeDiff            int  `json:"size_diff"`
	HashDiff            int  `json:"hash_diff"`
	Identical           int  `json:"identical"`
	DeletedLocal        int  `json:"deleted_local"`
	DeletedPeer         int  `json:"deleted_peer"`
	ConflictDeleteLocal int  `json:"conflict_delete_local"`
	ConflictDeletePeer  int  `json:"conflict_delete_peer"`
	MoveLocal           int  `json:"move_local"`
	MovePeer            int  `json:"move_peer"`
	EmptyLocalGuard     bool `json:"empty_local_guard"`
	EmptyPeerGuard      bool `json:"empty_peer_guard"`
}

// 2-second tolerance for filesystem timestamp granularity differences
const mtimeToleranceNS = int64(2_000_000_000)

func absInt64(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}

func unchangedSinceBaseline(f scanner.FileMeta, b BaselineMeta) bool {
	if f.Size != b.Size {
		return false
	}
	if absInt64(f.MTimeNS-b.MTimeNS) > mtimeToleranceNS {
		return false
	}
	if f.Hash != "" && b.Hash != "" && f.Hash != b.Hash {
		return false
	}
	return true
}

// Compare compares local and peer metadata maps (stateless 2-way).
func Compare(local, peer map[string]scanner.FileMeta) ([]Item, Summary) {
	return CompareWithBaseline(local, peer, nil)
}

// CompareWithBaseline compares local and peer metadata against the last synchronized
// baseline to distinguish deletions, delete-vs-edit conflicts, and 1-to-1 renames/moves.
func CompareWithBaseline(local, peer map[string]scanner.FileMeta, baseline map[string]BaselineMeta) ([]Item, Summary) {
	all := make(map[string]struct{}, len(local)+len(peer))
	for k := range local {
		all[k] = struct{}{}
	}
	for k := range peer {
		all[k] = struct{}{}
	}

	keys := make([]string, 0, len(all))
	for k := range all {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	items := make([]Item, 0, len(keys))
	var sum Summary
	sum.Total = len(keys)
	sum.EmptyLocalGuard = len(baseline) > 0 && len(local) == 0 && len(peer) > 0
	sum.EmptyPeerGuard = len(baseline) > 0 && len(peer) == 0 && len(local) > 0

	for _, rel := range keys {
		l, hasL := local[rel]
		p, hasP := peer[rel]

		it := Item{
			RelPath:  rel,
			HasLocal: hasL,
			HasPeer:  hasP,
		}
		if hasL {
			it.LocalSize = l.Size
			it.LocalMTimeNS = l.MTimeNS
			it.LocalHash = l.Hash
		}
		if hasP {
			it.PeerSize = p.Size
			it.PeerMTimeNS = p.MTimeNS
			it.PeerHash = p.Hash
		}

		switch {
		case hasL && !hasP:
			if b, ok := baseline[rel]; ok {
				if unchangedSinceBaseline(l, b) {
					it.Status = StatusDeletedPeer
					it.DefaultAction = "delete_local"
					sum.DeletedPeer++
				} else {
					it.Status = StatusConflictDeletePeer
					it.DefaultAction = "skip"
					sum.ConflictDeletePeer++
				}
			} else {
				it.Status = StatusNewLocal
				it.DefaultAction = "push"
				sum.NewLocal++
			}
		case !hasL && hasP:
			if b, ok := baseline[rel]; ok {
				if unchangedSinceBaseline(p, b) {
					it.Status = StatusDeletedLocal
					it.DefaultAction = "delete_peer"
					sum.DeletedLocal++
				} else {
					it.Status = StatusConflictDeleteLocal
					it.DefaultAction = "skip"
					sum.ConflictDeleteLocal++
				}
			} else {
				it.Status = StatusNewPeer
				it.DefaultAction = "pull"
				sum.NewPeer++
			}
		default:
			if l.Hash != "" && p.Hash != "" {
				if l.Hash == p.Hash && l.Size == p.Size {
					it.Status = StatusIdentical
					it.DefaultAction = "skip"
					sum.Identical++
					items = append(items, it)
					continue
				}
			}

			delta := l.MTimeNS - p.MTimeNS
			switch {
			case l.Size == p.Size && (l.Hash == "" || p.Hash == ""):
				it.Status = StatusIdentical
				it.DefaultAction = "skip"
				sum.Identical++
			case delta > mtimeToleranceNS:
				it.Status = StatusNewerLocal
				it.DefaultAction = "push"
				sum.NewerLocal++
			case delta < -mtimeToleranceNS:
				it.Status = StatusNewerPeer
				it.DefaultAction = "pull"
				sum.NewerPeer++
			case l.Size != p.Size:
				it.Status = StatusSizeDiff
				it.DefaultAction = "skip"
				sum.SizeDiff++
			default:
				it.Status = StatusHashDiff
				it.DefaultAction = "skip"
				sum.HashDiff++
			}
		}

		items = append(items, it)
	}

	if !sum.EmptyLocalGuard && !sum.EmptyPeerGuard && len(baseline) > 0 {
		items = detectUniqueMoves(items, &sum)
	}

	return items, sum
}

type moveMatch struct {
	delIdx int
	newIdx int
}

func detectUniqueMoves(items []Item, sum *Summary) []Item {
	removeIdx := make(map[int]bool)

	matchSide := func(delStatus, newStatus, moveStatus Status, moveAction string, delCounter, newCounter, moveCounter *int) {
		bySizeDel := make(map[int64][]int)
		bySizeNew := make(map[int64][]int)

		for i, it := range items {
			if it.Status == delStatus {
				sz := it.PeerSize
				if delStatus == StatusDeletedPeer {
					sz = it.LocalSize
				}
				if sz > 0 {
					bySizeDel[sz] = append(bySizeDel[sz], i)
				}
			} else if it.Status == newStatus {
				sz := it.LocalSize
				if newStatus == StatusNewPeer {
					sz = it.PeerSize
				}
				if sz > 0 {
					bySizeNew[sz] = append(bySizeNew[sz], i)
				}
			}
		}

		var pairs []moveMatch
		for sz, dIdxs := range bySizeDel {
			nIdxs := bySizeNew[sz]
			if len(nIdxs) == 0 {
				continue
			}

			usedDel := make(map[int]bool, len(dIdxs))
			usedNew := make(map[int]bool, len(nIdxs))

			// Round A: unique 1-to-1 compatible match within this size bucket.
			for _, di := range dIdxs {
				var compat []int
				for _, ni := range nIdxs {
					if itemsMoveCompatible(items[di], items[ni], delStatus) {
						compat = append(compat, ni)
					}
				}
				if len(compat) != 1 {
					continue
				}
				targetNew := compat[0]
				var revCompat []int
				for _, d2 := range dIdxs {
					if itemsMoveCompatible(items[d2], items[targetNew], delStatus) {
						revCompat = append(revCompat, d2)
					}
				}
				if len(revCompat) == 1 && revCompat[0] == di {
					pairs = append(pairs, moveMatch{delIdx: di, newIdx: targetNew})
					usedDel[di] = true
					usedNew[targetNew] = true
				}
			}

			// Round B: basename tie-breaker for unmatched items in the same size bucket (e.g. moved folder).
			baseDel := make(map[string][]int)
			baseNew := make(map[string][]int)
			for _, di := range dIdxs {
				if !usedDel[di] {
					b := strings.ToLower(path.Base(items[di].RelPath))
					baseDel[b] = append(baseDel[b], di)
				}
			}
			for _, ni := range nIdxs {
				if !usedNew[ni] {
					b := strings.ToLower(path.Base(items[ni].RelPath))
					baseNew[b] = append(baseNew[b], ni)
				}
			}
			for b, bd := range baseDel {
				bn := baseNew[b]
				if len(bd) == 1 && len(bn) == 1 && itemsMoveCompatible(items[bd[0]], items[bn[0]], delStatus) {
					pairs = append(pairs, moveMatch{delIdx: bd[0], newIdx: bn[0]})
				}
			}
		}

		for _, m := range pairs {
			delIt := items[m.delIdx]
			newIt := items[m.newIdx]
			removeIdx[m.delIdx] = true

			newIt.Status = moveStatus
			newIt.DefaultAction = moveAction
			newIt.OldRelPath = delIt.RelPath
			newIt.HasLocal = true
			newIt.HasPeer = true
			if moveStatus == StatusMovePeer {
				newIt.PeerSize = delIt.PeerSize
				newIt.PeerMTimeNS = delIt.PeerMTimeNS
				newIt.PeerHash = delIt.PeerHash
			} else {
				newIt.LocalSize = delIt.LocalSize
				newIt.LocalMTimeNS = delIt.LocalMTimeNS
				newIt.LocalHash = delIt.LocalHash
			}
			items[m.newIdx] = newIt

			*delCounter--
			*newCounter--
			*moveCounter++
			sum.Total--
		}
	}

	matchSide(StatusDeletedLocal, StatusNewLocal, StatusMovePeer, "move_peer", &sum.DeletedLocal, &sum.NewLocal, &sum.MovePeer)
	matchSide(StatusDeletedPeer, StatusNewPeer, StatusMoveLocal, "move_local", &sum.DeletedPeer, &sum.NewPeer, &sum.MoveLocal)

	if len(removeIdx) == 0 {
		return items
	}
	out := make([]Item, 0, len(items)-len(removeIdx))
	for i, it := range items {
		if !removeIdx[i] {
			out = append(out, it)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].RelPath < out[j].RelPath
	})
	return out
}

func itemsMoveCompatible(delIt, newIt Item, delStatus Status) bool {
	var delMT, newMT int64
	var delHash, newHash string
	if delStatus == StatusDeletedLocal {
		delMT, delHash = delIt.PeerMTimeNS, delIt.PeerHash
		newMT, newHash = newIt.LocalMTimeNS, newIt.LocalHash
	} else {
		delMT, delHash = delIt.LocalMTimeNS, delIt.LocalHash
		newMT, newHash = newIt.PeerMTimeNS, newIt.PeerHash
	}
	if absInt64(newMT-delMT) > mtimeToleranceNS {
		return false
	}
	if delHash != "" && newHash != "" && delHash != newHash {
		return false
	}
	return true
}
