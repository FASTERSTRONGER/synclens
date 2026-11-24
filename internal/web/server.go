package web

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"math"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"synclens/internal/config"
	"synclens/internal/diff"
	"synclens/internal/discovery"
	"synclens/internal/fsops"
	"synclens/internal/scanner"
	"synclens/internal/state"
	"synclens/internal/transport"
)

//go:embed static/*
var staticFiles embed.FS

type DashboardServer struct {
	deviceName string
	pairsPath  string
	pairsCfg   *config.PairsConfig
	appVersion string
	developer  string
	stateStore *state.Store

	mu                sync.RWMutex
	peerClient        *transport.PeerClient
	peerInfo          *discovery.PeerInfo
	lastLocal         map[string]scanner.FileMeta
	lastPeer          map[string]scanner.FileMeta
	lastItems         []diff.Item
	lastSum           diff.Summary
	lastBaseline      *state.Baseline
	lastBaselineKey   string
	lastResolvedLocal string
	lastResolvedPeer  string
	textVerifyPaths   []string
	lastPair          config.FolderPair

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
	stateDir := filepath.Join(filepath.Dir(pairsPath), "synclens-state")
	return &DashboardServer{
		deviceName: deviceName,
		pairsPath:  pairsPath,
		pairsCfg:   cfg,
		appVersion: "v2.0",
		developer:  "OK Muhammad Majid Maulana",
		stateStore: state.NewStore(stateDir),
		sseSubs:    make(map[chan string]struct{}),
	}, nil
}

// SetAppInfo overrides application version and developer metadata exposed to the web UI.
func (s *DashboardServer) SetAppInfo(version, developer string) {
	s.mu.Lock()
	if version != "" {
		s.appVersion = version
	}
	if developer != "" {
		s.developer = developer
	}
	s.mu.Unlock()
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
	mux.HandleFunc("/api/local/reset-baseline", s.handleResetBaseline)
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
		ver := s.appVersion
		dev := s.developer
		s.mu.RUnlock()

		resp := map[string]any{
			"device_name": s.deviceName,
			"version":     ver,
			"developer":   dev,
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

	s.mu.RLock()
	peerInfo := s.peerInfo
	s.mu.RUnlock()
	peerName := "peer"
	if peerInfo != nil && peerInfo.DeviceName != "" {
		peerName = peerInfo.DeviceName
	}

	pairKey := state.PairKey(s.deviceName, localRoot, peerName, peerResp.ResolvedRoot)
	localBase, _ := s.stateStore.Load(pairKey)
	peerBase, _ := client.GetBaseline(ctx, pairKey)

	var activeBase *state.Baseline
	var peerNeedsSync bool
	switch {
	case peerBase != nil && (localBase == nil || peerBase.UpdatedAt > localBase.UpdatedAt):
		activeBase = peerBase
		_ = s.stateStore.Save(activeBase)
	case localBase != nil:
		activeBase = localBase
		if peerBase == nil || localBase.UpdatedAt > peerBase.UpdatedAt {
			peerNeedsSync = true
		}
	default:
		activeBase = &state.Baseline{
			Version: state.CurrentVersion,
			Key:     pairKey,
			Entries: make(map[string]state.Entry),
		}
	}

	items, summary := diff.CompareWithBaseline(localMeta, peerResp.Entries, toBaselineMeta(activeBase))

	if !summary.EmptyLocalGuard && !summary.EmptyPeerGuard {
		if updateBaselineFromCompare(activeBase, localMeta, peerResp.Entries, items) {
			activeBase.UpdatedAt = time.Now().UnixNano()
			_ = s.stateStore.Save(activeBase)
			peerNeedsSync = true
		}
		if peerNeedsSync && len(activeBase.Entries) > 0 {
			_ = client.PutBaseline(ctx, pairKey, activeBase)
		}
	}

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
	s.lastBaseline = activeBase
	s.lastBaselineKey = pairKey
	s.lastResolvedLocal = localRoot
	s.lastResolvedPeer = peerResp.ResolvedRoot
	s.textVerifyPaths = textCandidates
	s.lastPair = pair
	s.mu.Unlock()

	localSpace, _ := fsops.GetDiskSpace(localRoot)
	peerSpace, _ := client.GetDiskSpace(ctx, peerResp.ResolvedRoot)

	diffTotal := summary.Total - summary.Identical
	dashLog("Compare selesai: %d berbeda, %d identik (baseline: %d)", diffTotal, summary.Identical, len(activeBase.Entries))

	writeJSON(w, map[string]any{
		"resolved_local":    localRoot,
		"resolved_peer":     peerResp.ResolvedRoot,
		"summary":           summary,
		"total_diff":        diffTotal,
		"baseline_count":    len(activeBase.Entries),
		"text_verify_count": len(textCandidates),
		"local_space":       localSpace,
		"peer_space":        peerSpace,
		"items":             filterItems(items, "DIFF", "", 20000),
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
	allFlag := r.URL.Query().Get("all") == "1"

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
		limit := 20000
		if allFlag {
			limit = 0
		}
		result = filterItems(items, filter, q, limit)
	}

	writeJSON(w, map[string]any{
		"summary": summary,
		"items":   result,
	})
}

type treeFolder struct {
	Name         string `json:"name"`
	RelPath      string `json:"rel_path"`
	TotalFiles   int    `json:"total_files"`
	DiffCount    int    `json:"diff_count"`
	NewLocal     int    `json:"new_local"`
	NewPeer      int    `json:"new_peer"`
	NewerLocal   int    `json:"newer_local"`
	NewerPeer    int    `json:"newer_peer"`
	DeletedLocal int    `json:"deleted_local"`
	DeletedPeer  int    `json:"deleted_peer"`
	MoveCount    int    `json:"move_count"`
	Conflict     int    `json:"conflict"`
	LocalSize    int64  `json:"local_size"`
	PeerSize     int64  `json:"peer_size"`
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
		if !matchesFilter(it, filter) {
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
		case diff.StatusDeletedLocal:
			tf.DeletedLocal++
		case diff.StatusDeletedPeer:
			tf.DeletedPeer++
		case diff.StatusMoveLocal, diff.StatusMovePeer:
			tf.MoveCount++
		case diff.StatusConflictDeleteLocal, diff.StatusConflictDeletePeer, diff.StatusSizeDiff, diff.StatusHashDiff:
			tf.Conflict++
		}
	}

	writeJSON(w, map[string]any{
		"dir":     dir,
		"folders": folders,
		"files":   files,
	})
}

func matchesFilter(it diff.Item, filter string) bool {
	switch filter {
	case "", "DIFF":
		return it.Status != diff.StatusIdentical
	case "ALL":
		return true
	case "MOVE":
		return it.Status == diff.StatusMoveLocal || it.Status == diff.StatusMovePeer
	case "CONFLICT":
		return it.Status == diff.StatusConflictDeleteLocal ||
			it.Status == diff.StatusConflictDeletePeer ||
			it.Status == diff.StatusSizeDiff ||
			it.Status == diff.StatusHashDiff
	default:
		return string(it.Status) == filter
	}
}

func filterItems(items []diff.Item, filter, q string, limit int) []diff.Item {
	q = strings.ToLower(strings.TrimSpace(q))
	capHint := len(items)
	if limit > 0 && limit < capHint {
		capHint = limit
	}
	out := make([]diff.Item, 0, capHint)
	for _, it := range items {
		if !matchesFilter(it, filter) {
			continue
		}
		if q != "" && !strings.Contains(strings.ToLower(it.RelPath), q) &&
			!strings.Contains(strings.ToLower(it.OldRelPath), q) {
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
	items, summary := diff.CompareWithBaseline(s.lastLocal, s.lastPeer, toBaselineMeta(s.lastBaseline))
	s.lastItems = items
	s.lastSum = summary
	if req.Mode == "text_candidates" {
		s.textVerifyPaths = nil
	}
	baseCount := 0
	var baseToSync *state.Baseline
	baseKey := s.lastBaselineKey
	if s.lastBaseline != nil && !summary.EmptyLocalGuard && !summary.EmptyPeerGuard {
		if updateBaselineFromCompare(s.lastBaseline, s.lastLocal, s.lastPeer, items) {
			s.lastBaseline.UpdatedAt = time.Now().UnixNano()
			_ = s.stateStore.Save(s.lastBaseline)
			baseToSync = s.lastBaseline
		}
		baseCount = len(s.lastBaseline.Entries)
	} else if s.lastBaseline != nil {
		baseCount = len(s.lastBaseline.Entries)
	}
	s.mu.Unlock()
	if baseToSync != nil && baseKey != "" {
		_ = client.PutBaseline(ctx, baseKey, baseToSync)
	}

	diffTotal := summary.Total - summary.Identical
	dashLog("Verify SHA-256 selesai: %d file", len(targetPaths))

	writeJSON(w, map[string]any{
		"summary":        summary,
		"total_diff":     diffTotal,
		"baseline_count": baseCount,
		"items":          filterItems(items, "DIFF", "", 20000),
	})
}

type ExecuteAction struct {
	RelPath    string `json:"rel_path"`
	OldRelPath string `json:"old_rel_path,omitempty"`
	Action     string `json:"action"` // push, pull, delete_local, delete_peer, move_local, move_peer
}

func (s *DashboardServer) runOneAction(
	ctx context.Context,
	client *transport.PeerClient,
	localRoot, remoteRoot string,
	act ExecuteAction,
	localMeta, peerMeta map[string]scanner.FileMeta,
	permanentDelete bool,
) (int64, error) {
	switch act.Action {
	case "push":
		abs, joinErr := transport.SafeJoin(localRoot, act.RelPath)
		if joinErr != nil {
			return 0, joinErr
		}
		var mtime int64
		if m, ok := localMeta[act.RelPath]; ok {
			mtime = m.MTimeNS
		}
		return client.UploadFile(ctx, remoteRoot, act.RelPath, abs, mtime)
	case "pull":
		abs, joinErr := transport.SafeJoin(localRoot, act.RelPath)
		if joinErr != nil {
			return 0, joinErr
		}
		var mtime int64
		if m, ok := peerMeta[act.RelPath]; ok {
			mtime = m.MTimeNS
		}
		return client.DownloadFile(ctx, remoteRoot, act.RelPath, abs, mtime)
	case "delete_local":
		abs, joinErr := transport.SafeJoin(localRoot, act.RelPath)
		if joinErr != nil {
			return 0, joinErr
		}
		return 0, fsops.DeleteFile(abs, permanentDelete)
	case "delete_peer":
		return 0, client.DeleteRemote(ctx, remoteRoot, act.RelPath, permanentDelete)
	case "move_local":
		oldAbs, joinErr := transport.SafeJoin(localRoot, act.OldRelPath)
		if joinErr != nil {
			return 0, joinErr
		}
		newAbs, joinErr := transport.SafeJoin(localRoot, act.RelPath)
		if joinErr != nil {
			return 0, joinErr
		}
		if !strings.EqualFold(oldAbs, newAbs) {
			if err := fsops.EnsureParentDir(newAbs); err != nil {
				return 0, err
			}
			if err := os.Rename(oldAbs, newAbs); err != nil {
				return 0, err
			}
			fsops.CleanEmptyParents(filepath.Dir(oldAbs), localRoot)
		}
		return 0, nil
	case "move_peer":
		return 0, client.MoveRemote(ctx, remoteRoot, act.OldRelPath, act.RelPath)
	default:
		return 0, nil
	}
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
	var moves []ExecuteAction
	var rest []ExecuteAction
	for _, a := range req.Actions {
		switch a.Action {
		case "push":
			if m, ok := localMeta[a.RelPath]; ok {
				totalBytes += m.Size
			}
			rest = append(rest, a)
		case "pull":
			if m, ok := peerMeta[a.RelPath]; ok {
				totalBytes += m.Size
			}
			rest = append(rest, a)
		case "move_local", "move_peer":
			moves = append(moves, a)
		default:
			rest = append(rest, a)
		}
	}
	dashLog("Eksekusi dimulai: %d aksi (%d move)", total, len(moves))
	var doneCount int64
	var bytesMoved int64
	var errList []string
	var errMu sync.Mutex
	var okMu sync.Mutex
	var succeeded []ExecuteAction

	start := time.Now()
	reportProgress := func(act ExecuteAction, moved int64, err error) {
		if err != nil {
			errMu.Lock()
			errList = append(errList, fmt.Sprintf("%s (%s): %v", act.RelPath, act.Action, err))
			errMu.Unlock()
		} else {
			okMu.Lock()
			succeeded = append(succeeded, act)
			okMu.Unlock()
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

	// Phase 1: run move_local / move_peer sequentially before parallel copy/delete workers.
	for _, act := range moves {
		if ctx.Err() != nil {
			break
		}
		moved, err := s.runOneAction(ctx, client, localRoot, pair.RemotePath, act, localMeta, peerMeta, req.PermanentDelete)
		reportProgress(act, moved, err)
	}

	// Phase 2: run push / pull / delete actions concurrently.
	jobCh := make(chan ExecuteAction, len(rest))
	for _, a := range rest {
		jobCh <- a
	}
	close(jobCh)

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
				moved, err := s.runOneAction(ctx, client, localRoot, pair.RemotePath, act, localMeta, peerMeta, req.PermanentDelete)
				reportProgress(act, moved, err)
			}
		}()
	}
	wg.Wait()

	// Update and replicate baseline for succeeded actions.
	if len(succeeded) > 0 {
		s.mu.Lock()
		base := s.lastBaseline
		baseKey := s.lastBaselineKey
		var baseToSave *state.Baseline
		if base != nil && baseKey != "" && applySucceededActionsToBaseline(base, localMeta, peerMeta, succeeded) {
			base.UpdatedAt = time.Now().UnixNano()
			_ = s.stateStore.Save(base)
			baseToSave = base
		}
		s.mu.Unlock()
		if baseToSave != nil && ctx.Err() == nil {
			_ = client.PutBaseline(ctx, baseKey, baseToSave)
		}
	}

	dashLog("Eksekusi selesai: %d/%d aksi (%.2f MB)", doneCount, total, float64(bytesMoved)/(1024*1024))
	writeJSON(w, map[string]any{
		"completed":   doneCount,
		"bytes_moved": bytesMoved,
		"duration_ms": time.Since(start).Milliseconds(),
		"errors":      errList,
	})
}

func (s *DashboardServer) handleResetBaseline(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	s.mu.Lock()
	client := s.peerClient
	key := s.lastBaselineKey
	if s.lastBaseline != nil {
		s.lastBaseline.Entries = make(map[string]state.Entry)
		s.lastBaseline.UpdatedAt = 0
	}
	s.mu.Unlock()

	if key != "" && s.stateStore != nil {
		if err := s.stateStore.Reset(key); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if client != nil {
			_ = client.DeleteBaseline(r.Context(), key)
		}
	}
	dashLog("Baseline direset untuk pasangan aktif")
	writeJSON(w, map[string]any{"reset": true})
}

func toBaselineMeta(b *state.Baseline) map[string]diff.BaselineMeta {
	if b == nil || len(b.Entries) == 0 {
		return nil
	}
	out := make(map[string]diff.BaselineMeta, len(b.Entries))
	for k, e := range b.Entries {
		out[k] = diff.BaselineMeta{Size: e.Size, MTimeNS: e.MTimeNS, Hash: e.Hash}
	}
	return out
}

func updateBaselineFromCompare(
	b *state.Baseline,
	localMeta, peerMeta map[string]scanner.FileMeta,
	items []diff.Item,
) bool {
	if b.Entries == nil {
		b.Entries = make(map[string]state.Entry)
	}
	changed := false
	for _, it := range items {
		if it.Status == diff.StatusIdentical {
			cur, exists := b.Entries[it.RelPath]
			next := state.Entry{Size: it.LocalSize, MTimeNS: it.LocalMTimeNS, Hash: it.LocalHash}
			if next.Hash == "" {
				next.Hash = it.PeerHash
			}
			if next.Hash == "" && exists {
				next.Hash = cur.Hash
			}
			if !exists || cur != next {
				b.Entries[it.RelPath] = next
				changed = true
			}
		}
	}
	for k := range b.Entries {
		_, hasL := localMeta[k]
		_, hasP := peerMeta[k]
		if !hasL && !hasP {
			delete(b.Entries, k)
			changed = true
		}
	}
	return changed
}

func applySucceededActionsToBaseline(
	b *state.Baseline,
	localMeta, peerMeta map[string]scanner.FileMeta,
	succeeded []ExecuteAction,
) bool {
	if b.Entries == nil {
		b.Entries = make(map[string]state.Entry)
	}
	hasL := make(map[string]bool, len(localMeta))
	for k := range localMeta {
		hasL[k] = true
	}
	hasP := make(map[string]bool, len(peerMeta))
	for k := range peerMeta {
		hasP[k] = true
	}

	changed := false
	for _, act := range succeeded {
		switch act.Action {
		case "push":
			if m, ok := localMeta[act.RelPath]; ok {
				b.Entries[act.RelPath] = state.Entry{Size: m.Size, MTimeNS: m.MTimeNS, Hash: m.Hash}
				hasP[act.RelPath] = true
				changed = true
			}
		case "pull":
			if m, ok := peerMeta[act.RelPath]; ok {
				b.Entries[act.RelPath] = state.Entry{Size: m.Size, MTimeNS: m.MTimeNS, Hash: m.Hash}
				hasL[act.RelPath] = true
				changed = true
			}
		case "move_local":
			delete(b.Entries, act.OldRelPath)
			hasL[act.OldRelPath] = false
			if m, ok := peerMeta[act.RelPath]; ok {
				b.Entries[act.RelPath] = state.Entry{Size: m.Size, MTimeNS: m.MTimeNS, Hash: m.Hash}
				hasL[act.RelPath] = true
			}
			changed = true
		case "move_peer":
			delete(b.Entries, act.OldRelPath)
			hasP[act.OldRelPath] = false
			if m, ok := localMeta[act.RelPath]; ok {
				b.Entries[act.RelPath] = state.Entry{Size: m.Size, MTimeNS: m.MTimeNS, Hash: m.Hash}
				hasP[act.RelPath] = true
			}
			changed = true
		case "delete_local":
			hasL[act.RelPath] = false
			if !hasP[act.RelPath] {
				delete(b.Entries, act.RelPath)
				changed = true
			}
		case "delete_peer":
			hasP[act.RelPath] = false
			if !hasL[act.RelPath] {
				delete(b.Entries, act.RelPath)
				changed = true
			}
		}
	}
	return changed
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
