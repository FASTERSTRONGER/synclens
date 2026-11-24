package web

import (
	"bytes"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"synclens/internal/config"
	"synclens/internal/diff"
	"synclens/internal/discovery"
	"synclens/internal/transport"
)

func TestDashboardPeerEndToEnd(t *testing.T) {
	localDir := t.TempDir()
	peerDir := t.TempDir()
	pairsPath := filepath.Join(t.TempDir(), "pairs.json")

	if err := os.WriteFile(filepath.Join(localDir, "from_pc.txt"), []byte("pc-content"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(peerDir, "from_laptop.txt"), []byte("laptop-content"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(peerDir, "obsolete.txt"), []byte("delete-me"), 0644); err != nil {
		t.Fatal(err)
	}

	pin := "123456"
	peerSrv := transport.NewPeerServer("Laptop-Test", pin, 2)
	peerTS := httptest.NewServer(peerSrv.Handler())
	defer peerTS.Close()

	host, portStr, _ := net.SplitHostPort(peerTS.Listener.Addr().String())
	port, _ := strconv.Atoi(portStr)

	dash, err := NewDashboardServer("PC-Test", pairsPath)
	if err != nil {
		t.Fatal(err)
	}
	// Wire peerClient directly to httptest peer port
	dash.mu.Lock()
	dash.peerClient = transport.NewPeerClient(host, port, pin)
	dash.peerInfo = &discovery.PeerInfo{DeviceName: "Laptop-Test", IP: host, HTTPPort: port}
	dash.mu.Unlock()

	dashTS := httptest.NewServer(dash.Handler())
	defer dashTS.Close()

	// 1. Compare
	pair := config.FolderPair{ID: "test", LocalPath: localDir, RemotePath: peerDir}
	body, _ := json.Marshal(pair)
	resp, err := http.Post(dashTS.URL+"/api/local/compare", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("compare status %d", resp.StatusCode)
	}

	// 2. Execute push, pull, and permanent delete on peer
	execReq := map[string]any{
		"permanent_delete": true,
		"actions": []ExecuteAction{
			{RelPath: "from_pc.txt", Action: "push"},
			{RelPath: "from_laptop.txt", Action: "pull"},
			{RelPath: "obsolete.txt", Action: "delete_peer"},
		},
	}
	execBody, _ := json.Marshal(execReq)
	execResp, err := http.Post(dashTS.URL+"/api/local/execute", "application/json", bytes.NewReader(execBody))
	if err != nil {
		t.Fatal(err)
	}
	defer execResp.Body.Close()
	if execResp.StatusCode != http.StatusOK {
		t.Fatalf("execute status %d", execResp.StatusCode)
	}

	if _, err := os.Stat(filepath.Join(peerDir, "from_pc.txt")); err != nil {
		t.Fatalf("expected from_pc.txt on peer: %v", err)
	}
	if _, err := os.Stat(filepath.Join(localDir, "from_laptop.txt")); err != nil {
		t.Fatalf("expected from_laptop.txt on local: %v", err)
	}
	if _, err := os.Stat(filepath.Join(peerDir, "obsolete.txt")); !os.IsNotExist(err) {
		t.Fatalf("expected obsolete.txt deleted on peer")
	}
}

func TestThreeWayDeletionMoveAndPeerReplication(t *testing.T) {
	localDir := t.TempDir()
	peerDir := t.TempDir()
	pcConfigDir := t.TempDir()
	laptopStateDir := t.TempDir()
	pairsPath := filepath.Join(pcConfigDir, "pairs.json")

	writeSame := func(rel string, data []byte) {
		t.Helper()
		for _, root := range []string{localDir, peerDir} {
			p := filepath.Join(root, filepath.FromSlash(rel))
			if err := os.MkdirAll(filepath.Dir(p), 0755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(p, data, 0644); err != nil {
				t.Fatal(err)
			}
			ts := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
			_ = os.Chtimes(p, ts, ts)
		}
	}

	writeSame("keep.txt", []byte("keep-content"))
	writeSame("del_pc.txt", []byte("delete-from-pc"))
	writeSame("conflict.txt", []byte("conflict-orig"))
	writeSame("old_dir/move_me.txt", []byte("move-folder-content-unique"))
	writeSame("swap_role.txt", []byte("swap-role-delete-test"))

	pin := "654321"
	peerSrv := transport.NewPeerServer("Laptop-Test", pin, 2)
	peerSrv.SetStateDir(laptopStateDir)
	peerTS := httptest.NewServer(peerSrv.Handler())
	defer peerTS.Close()

	host, portStr, _ := net.SplitHostPort(peerTS.Listener.Addr().String())
	port, _ := strconv.Atoi(portStr)

	dash, err := NewDashboardServer("PC-Test", pairsPath)
	if err != nil {
		t.Fatal(err)
	}
	dash.mu.Lock()
	dash.peerClient = transport.NewPeerClient(host, port, pin)
	dash.peerInfo = &discovery.PeerInfo{DeviceName: "Laptop-Test", IP: host, HTTPPort: port}
	dash.mu.Unlock()

	dashTS := httptest.NewServer(dash.Handler())
	defer dashTS.Close()

	doCompare := func(url, lPath, rPath string) struct {
		Summary       diff.Summary `json:"summary"`
		BaselineCount int          `json:"baseline_count"`
		Items         []diff.Item  `json:"items"`
	} {
		t.Helper()
		pair := config.FolderPair{ID: "p1", LocalPath: lPath, RemotePath: rPath}
		body, _ := json.Marshal(pair)
		resp, err := http.Post(url+"/api/local/compare", "application/json", bytes.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("compare status %d", resp.StatusCode)
		}
		var out struct {
			Summary       diff.Summary `json:"summary"`
			BaselineCount int          `json:"baseline_count"`
			Items         []diff.Item  `json:"items"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
			t.Fatal(err)
		}
		return out
	}

	// 1. Initial compare records all 5 identical files into baseline and replicates to Peer.
	res1 := doCompare(dashTS.URL, localDir, peerDir)
	if res1.BaselineCount != 5 {
		t.Fatalf("expected 5 baseline entries, got %d", res1.BaselineCount)
	}
	replicatedFiles, _ := filepath.Glob(filepath.Join(laptopStateDir, "*.json.gz"))
	if len(replicatedFiles) != 1 {
		t.Fatalf("expected 1 replicated baseline file on Peer, got %d", len(replicatedFiles))
	}

	// 2. Delete del_pc.txt on PC, delete conflict.txt on PC + edit on Laptop, rename old_dir/move_me.txt on PC.
	_ = os.Remove(filepath.Join(localDir, "del_pc.txt"))
	_ = os.Remove(filepath.Join(localDir, "conflict.txt"))
	conflictPeerPath := filepath.Join(peerDir, "conflict.txt")
	_ = os.WriteFile(conflictPeerPath, []byte("conflict-edited-on-laptop-longer"), 0644)
	tNew := time.Date(2026, 11, 21, 12, 0, 0, 0, time.UTC)
	_ = os.Chtimes(conflictPeerPath, tNew, tNew)

	newDir := filepath.Join(localDir, "new_dir")
	_ = os.MkdirAll(newDir, 0755)
	_ = os.Rename(filepath.Join(localDir, "old_dir", "move_me.txt"), filepath.Join(newDir, "move_me.txt"))
	_ = os.Remove(filepath.Join(localDir, "old_dir"))

	res2 := doCompare(dashTS.URL, localDir, peerDir)
	byPath := map[string]diff.Item{}
	for _, it := range res2.Items {
		byPath[it.RelPath] = it
	}
	if byPath["del_pc.txt"].Status != diff.StatusDeletedLocal || byPath["del_pc.txt"].DefaultAction != "delete_peer" {
		t.Fatalf("expected del_pc.txt DELETED_LOCAL/delete_peer, got %+v", byPath["del_pc.txt"])
	}
	if byPath["conflict.txt"].Status != diff.StatusConflictDeleteLocal || byPath["conflict.txt"].DefaultAction != "skip" {
		t.Fatalf("expected conflict.txt CONFLICT_DELETE_LOCAL/skip, got %+v", byPath["conflict.txt"])
	}
	if byPath["new_dir/move_me.txt"].Status != diff.StatusMovePeer || byPath["new_dir/move_me.txt"].DefaultAction != "move_peer" || byPath["new_dir/move_me.txt"].OldRelPath != "old_dir/move_me.txt" {
		t.Fatalf("expected new_dir/move_me.txt MOVE_PEER from old_dir/move_me.txt, got %+v", byPath["new_dir/move_me.txt"])
	}

	// 3. Execute move_peer + permanent delete_peer.
	execReq := map[string]any{
		"permanent_delete": true,
		"actions": []ExecuteAction{
			{RelPath: "new_dir/move_me.txt", OldRelPath: "old_dir/move_me.txt", Action: "move_peer"},
			{RelPath: "del_pc.txt", Action: "delete_peer"},
		},
	}
	execBody, _ := json.Marshal(execReq)
	execResp, err := http.Post(dashTS.URL+"/api/local/execute", "application/json", bytes.NewReader(execBody))
	if err != nil {
		t.Fatal(err)
	}
	execResp.Body.Close()

	if _, err := os.Stat(filepath.Join(peerDir, "new_dir", "move_me.txt")); err != nil {
		t.Fatalf("expected moved file on peer: %v", err)
	}
	if _, err := os.Stat(filepath.Join(peerDir, "old_dir")); !os.IsNotExist(err) {
		t.Fatalf("expected empty old_dir cleaned up on peer")
	}
	if _, err := os.Stat(filepath.Join(peerDir, "del_pc.txt")); !os.IsNotExist(err) {
		t.Fatalf("expected del_pc.txt deleted on peer")
	}

	// 4. Role reversal: run Dashboard from Laptop (using laptopStateDir!) and PeerServer from PC.
	pcAsPeer := transport.NewPeerServer("PC-Test", pin, 2)
	pcAsPeerTS := httptest.NewServer(pcAsPeer.Handler())
	defer pcAsPeerTS.Close()
	pHost, pPortStr, _ := net.SplitHostPort(pcAsPeerTS.Listener.Addr().String())
	pPort, _ := strconv.Atoi(pPortStr)

	// Point Laptop's pairsPath inside parent of laptopStateDir so Store reads laptopStateDir!
	laptopPairsDir := filepath.Dir(laptopStateDir)
	laptopPairsPath := filepath.Join(laptopPairsDir, "pairs.json")
	if err := os.Rename(laptopStateDir, filepath.Join(laptopPairsDir, "synclens-state")); err != nil {
		t.Fatal(err)
	}

	laptopDash, err := NewDashboardServer("Laptop-Test", laptopPairsPath)
	if err != nil {
		t.Fatal(err)
	}
	laptopDash.mu.Lock()
	laptopDash.peerClient = transport.NewPeerClient(pHost, pPort, pin)
	laptopDash.peerInfo = &discovery.PeerInfo{DeviceName: "PC-Test", IP: pHost, HTTPPort: pPort}
	laptopDash.mu.Unlock()
	laptopDashTS := httptest.NewServer(laptopDash.Handler())
	defer laptopDashTS.Close()

	_ = os.Remove(filepath.Join(peerDir, "swap_role.txt"))
	resSwap := doCompare(laptopDashTS.URL, peerDir, localDir)
	swapMap := map[string]diff.Item{}
	for _, it := range resSwap.Items {
		swapMap[it.RelPath] = it
	}
	if swapMap["swap_role.txt"].Status != diff.StatusDeletedLocal || swapMap["swap_role.txt"].DefaultAction != "delete_peer" {
		t.Fatalf("role reversal failed to detect deletion from replicated baseline: %+v", swapMap["swap_role.txt"])
	}

	// 5. Empty root guard: emptying all files in the paired root triggers EmptyLocalGuard and preserves baseline.
	entries, _ := os.ReadDir(peerDir)
	for _, e := range entries {
		_ = os.RemoveAll(filepath.Join(peerDir, e.Name()))
	}
	resEmpty := doCompare(laptopDashTS.URL, peerDir, localDir)
	if !resEmpty.Summary.EmptyLocalGuard {
		t.Fatalf("expected EmptyLocalGuard=true when paired local dir is empty")
	}
	if resEmpty.BaselineCount == 0 {
		t.Fatalf("expected baseline preserved under EmptyLocalGuard, got 0")
	}
}
