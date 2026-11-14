package web

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"math"
	"net/http"
	"path"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"synclens/internal/config"
	"synclens/internal/diff"
	"synclens/internal/discovery"
	"synclens/internal/fsops"
	"synclens/internal/scanner"
	"synclens/internal/transport"
)

//go:embed static/*
var staticFiles embed.FS

type DashboardServer struct {
	deviceName string
	pairsPath  string
	pairsCfg   *config.PairsConfig

	mu         sync.RWMutex
	peerClient *transport.PeerClient
	peerInfo   *discovery.PeerInfo
	lastLocal       map[string]scanner.FileMeta
	lastPeer        map[string]scanner.FileMeta
	lastItems       []diff.Item
	lastSum         diff.Summary
	textVerifyPaths []string
	lastPair        config.FolderPair

	execMu       sync.Mutex
	opMu         sync.Mutex
	activeCancel context.CancelFunc
	sseSubs      map[chan string]struct{}
	sseSubsMu    sync.Mutex
}

func dashLog(format string, args ...any) {
	fmt.Printf("[%s] %s\n", time.Now().Format("15:04:05"), fmt.Sprintf(format, args...))
}

func (s *DashboardServer) beginOp(parent context.Context) (context.Context, context.CancelFunc) {
	s.opMu.Lock()
	defer s.opMu.Unlock()
	if s.activeCancel != nil {
		s.activeCancel()
	}
	ctx, cancel := context.WithCancel(parent)
	s.activeCancel = cancel
	return ctx, cancel
}

func (s *DashboardServer) endOp() {
	s.opMu.Lock()
	s.activeCancel = nil
	s.opMu.Unlock()
}

func NewDashboardServer(deviceName, pairsPath string) (*DashboardServer, error) {
	cfg, err := config.LoadOrInitPairs(pairsPath)
	if err != nil {
		return nil, err
	}
	return &DashboardServer{
		deviceName: deviceName,
		pairsPath:  pairsPath,
		pairsCfg:   cfg,
		sseSubs:    make(map[chan string]struct{}),
	}, nil
}

func (s *DashboardServer) Handler() http.Handler {
	mux := http.NewServeMux()
	sub, _ := fs.Sub(staticFiles, "static")
	mux.Handle("/", http.FileServer(http.FS(sub)))

	mux.HandleFunc("/api/local/pairs", s.handlePairs)
	mux.HandleFunc("/api/local/browse", s.handleBrowse)
	mux.HandleFunc("/api/local/connect", s.handleConnect)
	mux.HandleFunc("/api/local/compare", s.handleCompare)
	mux.HandleFunc("/api/local/items", s.handleItems)
	mux.HandleFunc("/api/local/tree", s.handleTree)
	mux.HandleFunc("/api/local/verify-hash", s.handleVerifyHash)
	mux.HandleFunc("/api/local/execute", s.handleExecute)
	mux.HandleFunc("/api/local/cancel", s.handleCancel)
	mux.HandleFunc("/api/local/events", s.handleEvents)
	return mux
}

func (s *DashboardServer) handlePairs(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		s.mu.RLock()
		client := s.peerClient
		peer := s.peerInfo
		pairs := s.pairsCfg.Pairs
		s.mu.RUnlock()

		resp := map[string]any{
			"device_name": s.deviceName,
			"pairs":       pairs,
			"connected":   false,
		}
		if client != nil && peer != nil {
			ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
			info, err := client.Info(ctx)
			cancel()
			if err == nil {
				resp["connected"] = true
				resp["peer"] = peer
				resp["peer_processes"] = info.ActiveProcesses
				resp["local_processes"] = fsops.CheckRunningProcesses([]string{"Antigravity.exe"})
			}
		}
		writeJSON(w, resp)
	case http.MethodPost:
		var pair config.FolderPair
		if err := json.NewDecoder(r.Body).Decode(&pair); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if pair.ID == "" {
			pair.ID = fmt.Sprintf("pair-%d", time.Now().Unix())
		}
		if pair.Name == "" {
			pair.Name = pair.LocalPath
		}
		s.mu.Lock()
		updated := false
		for i := range s.pairsCfg.Pairs {
			if s.pairsCfg.Pairs[i].ID == pair.ID {
				s.pairsCfg.Pairs[i] = pair
				updated = true
				break
			}
		}
		if !updated {
			s.pairsCfg.Pairs = append(s.pairsCfg.Pairs, pair)
		}
		err := s.pairsCfg.Save(s.pairsPath)
		s.mu.Unlock()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, pair)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *DashboardServer) handleBrowse(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	side := r.URL.Query().Get("side")
	reqPath := r.URL.Query().Get("path")

	if side == "peer" {
		s.mu.RLock()
		client := s.peerClient
		s.mu.RUnlock()
		if client == nil {
			http.Error(w, "belum terhubung ke peer (Laptop)", http.StatusPreconditionFailed)
			return
		}
		resp, err := client.Browse(r.Context(), reqPath)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		writeJSON(w, resp)
		return
	}

	resolved, dirs, err := fsops.ListDirectories(reqPath)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, map[string]any{
		"current_path": resolved,
		"dirs":         dirs,
	})
}

func (s *DashboardServer) handleConnect(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		IP  string `json:"ip"`
		PIN string `json:"pin"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if req.PIN == "" {
		http.Error(w, "PIN wajib diisi", http.StatusBadRequest)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	var peer *discovery.PeerInfo
	var err error
	if req.IP == "" {
		peer, err = discovery.DiscoverPeer(ctx, config.DefaultDiscoveryPort, req.PIN, s.deviceName, 3*time.Second)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
	} else {
		peer = &discovery.PeerInfo{IP: req.IP, HTTPPort: config.DefaultPeerPort}
	}

	client := transport.NewPeerClient(peer.IP, peer.HTTPPort, req.PIN)
	info, err := client.Info(ctx)
	if err != nil {
		http.Error(w, fmt.Sprintf("gagal terhubung ke peer %s: %v", peer.IP, err), http.StatusBadGateway)
		return
	}
	peer.DeviceName = info.DeviceName

	s.mu.Lock()
	s.peerClient = client
	s.peerInfo = peer
	s.mu.Unlock()

	dashLog("Terhubung ke Peer: %s (%s)", peer.DeviceName, peer.IP)

	localProcs := fsops.CheckRunningProcesses([]string{"Antigravity.exe"})
	writeJSON(w, map[string]any{
		"peer":             peer,
		"peer_processes":   info.ActiveProcesses,
		"local_processes":  localProcs,
	})
}

func (s *DashboardServer) handleCancel(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	s.opMu.Lock()
	cancel := s.activeCancel
	s.opMu.Unlock()
	if cancel != nil {
		cancel()
	}
	writeJSON(w, map[string]any{"cancelled": cancel != nil})
}

func (s *DashboardServer) handleCompare(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var pair config.FolderPair
	if err := json.NewDecoder(r.Body).Decode(&pair); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	s.mu.RLock()
	client := s.peerClient
	s.mu.RUnlock()
	if client == nil {
		http.Error(w, "belum terhubung ke peer (Laptop)", http.StatusPreconditionFailed)
		return
	}

	ctx, cancel := s.beginOp(r.Context())
	defer func() {
		cancel()
		s.endOp()
	}()

	localRoot := config.ExpandPath(pair.LocalPath)
	dashLog("Compare dimulai: %s <-> %s", localRoot, pair.RemotePath)
	var (
		localMeta map[string]scanner.FileMeta
		peerResp  *transport.ScanResponse
		localErr  error
		peerErr   error
		wg        sync.WaitGroup
	)

	wg.Add(2)
	go func() {
		defer wg.Done()
		localMeta, localErr = scanner.ScanMetadataWithProgress(ctx, localRoot, pair.Excludes,
			func(count int, totalBytes int64, currentPath string) {
				s.broadcastSSE(map[string]any{
					"type":    "scan_progress",
					"side":    "local",
					"count":   count,
					"bytes":   totalBytes,
					"current": currentPath,
				})
			})
	}()
	go func() {
		defer wg.Done()
		peerResp, peerErr = client.ScanWithProgress(ctx, pair.RemotePath, pair.Excludes,
			func(count int, totalBytes int64, currentPath string) {
				s.broadcastSSE(map[string]any{
					"type":    "scan_progress",
					"side":    "peer",
					"count":   count,
					"bytes":   totalBytes,
					"current": currentPath,
				})
			})
	}()
	wg.Wait()

	if ctx.Err() != nil {
		http.Error(w, "pemindaian dibatalkan", 499)
		return
	}
	if localErr != nil {
		http.Error(w, "local scan error: "+localErr.Error(), http.StatusInternalServerError)
		return
	}
	if peerErr != nil {
		http.Error(w, "peer scan error: "+peerErr.Error(), http.StatusBadGateway)
		return
	}

	items, summary := diff.Compare(localMeta, peerResp.Entries)

	const maxTextVerifySize = int64(16 * 1024 * 1024)
	const mtimeTol = int64(2_000_000_000)
	var textCandidates []string
	for rel, l := range localMeta {
		if p, ok := peerResp.Entries[rel]; ok && l.Size == p.Size && l.Size <= maxTextVerifySize && isTextCodeFile(rel) {
			delta := l.MTimeNS - p.MTimeNS
			if delta > mtimeTol || delta < -mtimeTol {
				textCandidates = append(textCandidates, rel)
			}
		}
	}

	s.mu.Lock()
	s.lastLocal = localMeta
	s.lastPeer = peerResp.Entries
	s.lastItems = items
	s.lastSum = summary
	s.textVerifyPaths = textCandidates
	s.lastPair = pair
	s.mu.Unlock()

	localSpace, _ := fsops.GetDiskSpace(localRoot)
	peerSpace, _ := client.GetDiskSpace(ctx, peerResp.ResolvedRoot)

	diffTotal := summary.NewLocal + summary.NewPeer + summary.NewerLocal + summary.NewerPeer + summary.SizeDiff + summary.HashDiff
	dashLog("Compare selesai: %d berbeda, %d identik", diffTotal, summary.Identical)

	writeJSON(w, map[string]any{
		"resolved_local":    localRoot,
		"resolved_peer":     peerResp.ResolvedRoot,
		"summary":           summary,
		"text_verify_count": len(textCandidates),
		"local_space":       localSpace,
		"peer_space":        peerSpace,
		"items":             filterItems(items, "DIFF", "", 2000),
	})
}

func isTextCodeFile(relPath string) bool {
	base := strings.ToLower(path.Base(relPath))
	switch base {
	case "dockerfile", "makefile", ".gitignore", ".gitattributes", ".env":
		return true
	}
	ext := strings.ToLower(path.Ext(base))
	switch ext {
	case ".txt", ".md", ".json", ".jsonl", ".yaml", ".yml", ".toml", ".xml", ".csv",
		".ini", ".conf", ".cfg", ".env", ".sql", ".sh", ".ps1", ".bat", ".cmd",
		".go", ".mod", ".sum", ".py", ".js", ".mjs", ".cjs", ".ts", ".tsx", ".jsx",
		".html", ".htm", ".css", ".scss", ".less", ".c", ".h", ".cpp", ".hpp",
		".cs", ".java", ".kt", ".rs", ".php", ".rb", ".swift", ".vue", ".svelte":
		return true
	default:
		return false
	}
}

func (s *DashboardServer) handleItems(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	filter := r.URL.Query().Get("filter")
	if filter == "" {
		filter = "DIFF"
	}
	q := r.URL.Query().Get("q")
	prefix := strings.Trim(strings.TrimSpace(r.URL.Query().Get("prefix")), "/")

	s.mu.RLock()
	items := s.lastItems
	summary := s.lastSum
	s.mu.RUnlock()

	var result []diff.Item
	if prefix != "" {
		result = filterItems(items, filter, q, 0)
		filtered := result[:0]
		pfx := prefix + "/"
		for _, it := range result {
			if strings.HasPrefix(it.RelPath, pfx) {
				filtered = append(filtered, it)
			}
		}
		result = filtered
	} else {
		result = filterItems(items, filter, q, 2000)
	}

	writeJSON(w, map[string]any{
		"summary": summary,
		"items":   result,
	})
}

type treeFolder struct {
	Name       string `json:"name"`
	RelPath    string `json:"rel_path"`
	TotalFiles int    `json:"total_files"`
	DiffCount  int    `json:"diff_count"`
	NewLocal   int    `json:"new_local"`
	NewPeer    int    `json:"new_peer"`
	NewerLocal int    `json:"newer_local"`
	NewerPeer  int    `json:"newer_peer"`
	LocalSize  int64  `json:"local_size"`
	PeerSize   int64  `json:"peer_size"`
}

func (s *DashboardServer) handleTree(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	dir := strings.Trim(strings.TrimSpace(r.URL.Query().Get("dir")), "/")
	filter := r.URL.Query().Get("filter")
	if filter == "" {
		filter = "DIFF"
	}

	s.mu.RLock()
	items := s.lastItems
	s.mu.RUnlock()

	prefix := ""
	if dir != "" {
		prefix = dir + "/"
	}

	folderMap := make(map[string]*treeFolder)
	var folders []*treeFolder
	var files []diff.Item

	for _, it := range items {
		if filter == "DIFF" && it.Status == diff.StatusIdentical {
			continue
		}
		if filter != "DIFF" && filter != "ALL" && string(it.Status) != filter {
			continue
		}
		if prefix != "" && !strings.HasPrefix(it.RelPath, prefix) {
			continue
		}

		rest := strings.TrimPrefix(it.RelPath, prefix)
		slashIdx := strings.IndexByte(rest, '/')
		if slashIdx == -1 {
			files = append(files, it)
			continue
		}

		subName := rest[:slashIdx]
		subRel := subName
		if prefix != "" {
			subRel = prefix + subName
		}
		tf, exists := folderMap[subName]
		if !exists {
			tf = &treeFolder{Name: subName, RelPath: subRel}
			folderMap[subName] = tf
			folders = append(folders, tf)
		}
		tf.TotalFiles++
		if it.HasLocal {
			tf.LocalSize += it.LocalSize
		}
		if it.HasPeer {
			tf.PeerSize += it.PeerSize
		}
		if it.Status != diff.StatusIdentical {
			tf.DiffCount++
		}
		switch it.Status {
		case diff.StatusNewLocal:
			tf.NewLocal++
		case diff.StatusNewPeer:
			tf.NewPeer++
		case diff.StatusNewerLocal:
			tf.NewerLocal++
		case diff.StatusNewerPeer:
			tf.NewerPeer++
		}
	}

	writeJSON(w, map[string]any{
		"dir":     dir,
		"folders": folders,
		"files":   files,
	})
}

func filterItems(items []diff.Item, filter, q string, limit int) []diff.Item {
	q = strings.ToLower(strings.TrimSpace(q))
	out := make([]diff.Item, 0, min(len(items), limit))
	for _, it := range items {
		if filter == "DIFF" && it.Status == diff.StatusIdentical {
			continue
		}
		if filter != "DIFF" && filter != "ALL" && string(it.Status) != filter {
			continue
		}
		if q != "" && !strings.Contains(strings.ToLower(it.RelPath), q) {
			continue
		}
		out = append(out, it)
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	return out
}

func (s *DashboardServer) handleVerifyHash(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		RelPaths []string `json:"rel_paths"`
		Mode     string   `json:"mode"` // "" or "text_candidates"
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	s.mu.RLock()
	client := s.peerClient
	pair := s.lastPair
	localMeta := s.lastLocal
	peerMeta := s.lastPeer
	textCandidates := s.textVerifyPaths
	s.mu.RUnlock()

	targetPaths := req.RelPaths
	if req.Mode == "text_candidates" {
		targetPaths = textCandidates
	}

	if client == nil || localMeta == nil || peerMeta == nil {
		http.Error(w, "jalankan Scan & Bandingkan terlebih dahulu", http.StatusPreconditionFailed)
		return
	}
	if len(targetPaths) == 0 {
		http.Error(w, "tidak ada file target untuk diverifikasi", http.StatusBadRequest)
		return
	}

	ctx, cancel := s.beginOp(r.Context())
	defer func() {
		cancel()
		s.endOp()
	}()

	localRoot := config.ExpandPath(pair.LocalPath)
	dashLog("Verify SHA-256 dimulai: %d file", len(targetPaths))
	hashStart := time.Now()
	var (
		localHashes map[string]string
		peerHashes  map[string]string
		lErr, pErr  error
		wg          sync.WaitGroup
	)
	wg.Add(2)
	go func() {
		defer wg.Done()
		localHashes, lErr = scanner.HashFilesWithProgress(ctx, localRoot, targetPaths, 4,
			func(done, total int, currentPath string) {
				pct := 0
				if total > 0 {
					pct = (done * 100) / total
				}
				s.broadcastSSE(map[string]any{
					"type":    "hash_progress",
					"side":    "local",
					"done":    done,
					"total":   total,
					"percent": pct,
					"current": currentPath,
					"eta_sec": estimateETA(hashStart, int64(done), int64(total)),
				})
			})
	}()
	go func() {
		defer wg.Done()
		peerHashes, pErr = client.HashWithProgress(ctx, pair.RemotePath, targetPaths,
			func(done, total int, currentPath string) {
				pct := 0
				if total > 0 {
					pct = (done * 100) / total
				}
				s.broadcastSSE(map[string]any{
					"type":    "hash_progress",
					"side":    "peer",
					"done":    done,
					"total":   total,
					"percent": pct,
					"current": currentPath,
					"eta_sec": estimateETA(hashStart, int64(done), int64(total)),
				})
			})
	}()
	wg.Wait()

	if ctx.Err() != nil {
		dashLog("Verify SHA-256 dibatalkan")
		http.Error(w, "verifikasi SHA-256 dibatalkan", 499)
		return
	}
	if lErr != nil || pErr != nil {
		http.Error(w, fmt.Sprintf("hash error: local=%v peer=%v", lErr, pErr), http.StatusInternalServerError)
		return
	}

	s.mu.Lock()
	for k, h := range localHashes {
		if m, ok := s.lastLocal[k]; ok {
			m.Hash = h
			s.lastLocal[k] = m
		}
	}
	for k, h := range peerHashes {
		if m, ok := s.lastPeer[k]; ok {
			m.Hash = h
			s.lastPeer[k] = m
		}
	}
	items, summary := diff.Compare(s.lastLocal, s.lastPeer)
	s.lastItems = items
	s.lastSum = summary
	if req.Mode == "text_candidates" {
		s.textVerifyPaths = nil
	}
	s.mu.Unlock()
	dashLog("Verify SHA-256 selesai: %d file", len(targetPaths))

	writeJSON(w, map[string]any{
		"summary": summary,
		"items":   filterItems(items, "DIFF", "", 2000),
	})
}

type ExecuteAction struct {
	RelPath string `json:"rel_path"`
	Action  string `json:"action"` // push, pull, delete_local, delete_peer
}

func (s *DashboardServer) handleExecute(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		PermanentDelete bool            `json:"permanent_delete"`
		Actions         []ExecuteAction `json:"actions"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	s.mu.RLock()
	client := s.peerClient
	pair := s.lastPair
	localMeta := s.lastLocal
	peerMeta := s.lastPeer
	s.mu.RUnlock()

	if client == nil {
		http.Error(w, "belum terhubung ke peer", http.StatusPreconditionFailed)
		return
	}

	s.execMu.Lock()
	defer s.execMu.Unlock()

	ctx, cancel := s.beginOp(r.Context())
	defer func() {
		cancel()
		s.endOp()
	}()

	localRoot := config.ExpandPath(pair.LocalPath)
	total := len(req.Actions)
	var totalBytes int64
	for _, a := range req.Actions {
		switch a.Action {
		case "push":
			if m, ok := localMeta[a.RelPath]; ok {
				totalBytes += m.Size
			}
		case "pull":
			if m, ok := peerMeta[a.RelPath]; ok {
				totalBytes += m.Size
			}
		}
	}
	dashLog("Eksekusi dimulai: %d aksi", total)
	var doneCount int64
	var bytesMoved int64
	var errList []string
	var errMu sync.Mutex

	jobCh := make(chan ExecuteAction, total)
	for _, a := range req.Actions {
		jobCh <- a
	}
	close(jobCh)

	start := time.Now()
	workers := 4
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for act := range jobCh {
				if ctx.Err() != nil {
					return
				}
				var moved int64
				var err error

				switch act.Action {
				case "push":
					abs, joinErr := transport.SafeJoin(localRoot, act.RelPath)
					if joinErr != nil {
						err = joinErr
					} else {
						var mtime int64
						if m, ok := localMeta[act.RelPath]; ok {
							mtime = m.MTimeNS
						}
						moved, err = client.UploadFile(ctx, pair.RemotePath, act.RelPath, abs, mtime)
					}
				case "pull":
					abs, joinErr := transport.SafeJoin(localRoot, act.RelPath)
					if joinErr != nil {
						err = joinErr
					} else {
						var mtime int64
						if m, ok := peerMeta[act.RelPath]; ok {
							mtime = m.MTimeNS
						}
						moved, err = client.DownloadFile(ctx, pair.RemotePath, act.RelPath, abs, mtime)
					}
				case "delete_local":
					abs, joinErr := transport.SafeJoin(localRoot, act.RelPath)
					if joinErr != nil {
						err = joinErr
					} else {
						err = fsops.DeleteFile(abs, req.PermanentDelete)
					}
				case "delete_peer":
					err = client.DeleteRemote(ctx, pair.RemotePath, act.RelPath, req.PermanentDelete)
				}

				if err != nil {
					errMu.Lock()
					errList = append(errList, fmt.Sprintf("%s (%s): %v", act.RelPath, act.Action, err))
					errMu.Unlock()
				}

				dc := atomic.AddInt64(&doneCount, 1)
				bm := atomic.AddInt64(&bytesMoved, moved)
				elapsed := time.Since(start).Seconds()
				mbps := 0.0
				if elapsed > 0 {
					mbps = (float64(bm) / (1024 * 1024)) / elapsed
				}
				etaSec := int64(-1)
				if totalBytes > 0 && bm > 0 {
					etaSec = estimateETA(start, bm, totalBytes)
				} else if totalBytes == 0 {
					etaSec = estimateETA(start, dc, int64(total))
				}
				s.broadcastSSE(map[string]any{
					"done":        dc,
					"total":       total,
					"bytes_moved": bm,
					"bytes_total": totalBytes,
					"mbps":        mbps,
					"eta_sec":     etaSec,
					"current":     act.RelPath,
				})
			}
		}()
	}
	wg.Wait()

	dashLog("Eksekusi selesai: %d/%d aksi (%.2f MB)", doneCount, total, float64(bytesMoved)/(1024*1024))
	writeJSON(w, map[string]any{
		"completed":   doneCount,
		"bytes_moved": bytesMoved,
		"duration_ms": time.Since(start).Milliseconds(),
		"errors":      errList,
	})
}

func (s *DashboardServer) handleEvents(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	ch := make(chan string, 32)
	s.sseSubsMu.Lock()
	s.sseSubs[ch] = struct{}{}
	s.sseSubsMu.Unlock()
	defer func() {
		s.sseSubsMu.Lock()
		delete(s.sseSubs, ch)
		s.sseSubsMu.Unlock()
		close(ch)
	}()

	flusher, ok := w.(http.Flusher)
	if !ok {
		return
	}
	for {
		select {
		case <-r.Context().Done():
			return
		case msg := <-ch:
			fmt.Fprintf(w, "data: %s\n\n", msg)
			flusher.Flush()
		}
	}
}

func (s *DashboardServer) broadcastSSE(payload any) {
	data, err := json.Marshal(payload)
	if err != nil {
		return
	}
	msg := string(data)
	s.sseSubsMu.Lock()
	defer s.sseSubsMu.Unlock()
	for ch := range s.sseSubs {
		select {
		case ch <- msg:
		default:
		}
	}
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func estimateETA(start time.Time, done, total int64) int64 {
	if done <= 0 || total <= done {
		return -1
	}
	elapsed := time.Since(start).Seconds()
	if elapsed < 0.2 {
		return -1
	}
	rem := float64(total-done) * elapsed / float64(done)
	return int64(math.Ceil(rem))
}
