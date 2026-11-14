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

	"synclens/internal/config"
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
