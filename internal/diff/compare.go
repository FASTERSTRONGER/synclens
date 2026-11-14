package diff

import (
	"sort"

	"synclens/internal/scanner"
)

type Status string

const (
	StatusNewLocal   Status = "NEW_LOCAL"
	StatusNewPeer    Status = "NEW_PEER"
	StatusNewerLocal Status = "NEWER_LOCAL"
	StatusNewerPeer  Status = "NEWER_PEER"
	StatusSizeDiff   Status = "SIZE_DIFF"
	StatusHashDiff   Status = "HASH_DIFF"
	StatusIdentical  Status = "IDENTICAL"
)

// Item represents one compared relative file path across Local (PC) and Peer (Laptop).
type Item struct {
	RelPath       string `json:"rel_path"`
	Status        Status `json:"status"`
	DefaultAction string `json:"default_action"` // push, pull, skip
	HasLocal      bool   `json:"has_local"`
	HasPeer       bool   `json:"has_peer"`
	LocalSize     int64  `json:"local_size"`
	PeerSize      int64  `json:"peer_size"`
	LocalMTimeNS  int64  `json:"local_mtime_ns"`
	PeerMTimeNS   int64  `json:"peer_mtime_ns"`
	LocalHash     string `json:"local_hash,omitempty"`
	PeerHash      string `json:"peer_hash,omitempty"`
}

// Summary counts items per status.
type Summary struct {
	Total      int `json:"total"`
	NewLocal   int `json:"new_local"`
	NewPeer    int `json:"new_peer"`
	NewerLocal int `json:"newer_local"`
	NewerPeer  int `json:"newer_peer"`
	SizeDiff   int `json:"size_diff"`
	HashDiff   int `json:"hash_diff"`
	Identical  int `json:"identical"`
}

// Compare compares local and peer metadata maps into sorted DiffItems and a Summary.
func Compare(local, peer map[string]scanner.FileMeta) ([]Item, Summary) {
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

	// 2-second tolerance for filesystem timestamp granularity differences
	const mtimeToleranceNS = int64(2_000_000_000)

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
			it.Status = StatusNewLocal
			it.DefaultAction = "push"
			sum.NewLocal++
		case !hasL && hasP:
			it.Status = StatusNewPeer
			it.DefaultAction = "pull"
			sum.NewPeer++
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

	return items, sum
}
