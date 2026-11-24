package transport

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"synclens/internal/auth"
	"synclens/internal/config"
	"synclens/internal/fsops"
	"synclens/internal/scanner"
	"synclens/internal/state"
)

type PeerServer struct {
	deviceName string
	pin        string
	workers    int
	stateStore *state.Store
}

func NewPeerServer(deviceName, pin string, workers int) *PeerServer {
	if workers <= 0 {
		workers = 4
	}
	return &PeerServer{
		deviceName: deviceName,
		pin:        pin,
		workers:    workers,
		stateStore: state.NewStore(state.DefaultStateDir()),
	}
}

// SetStateDir overrides the baseline state storage directory (used in tests).
func (s *PeerServer) SetStateDir(dir string) {
	s.stateStore = state.NewStore(dir)
}

func (s *PeerServer) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/info", s.withAuth(s.handleInfo))
	mux.HandleFunc("/api/v1/browse", s.withAuth(s.handleBrowse))
	mux.HandleFunc("/api/v1/disk-space", s.withAuth(s.handleDiskSpace))
	mux.HandleFunc("/api/v1/scan", s.withAuth(s.handleScan))
	mux.HandleFunc("/api/v1/scan-stream", s.withAuth(s.handleScanStream))
	mux.HandleFunc("/api/v1/hash", s.withAuth(s.handleHash))
	mux.HandleFunc("/api/v1/hash-stream", s.withAuth(s.handleHashStream))
	mux.HandleFunc("/api/v1/file", s.withAuth(s.handleFile))
	mux.HandleFunc("/api/v1/delete", s.withAuth(s.handleDelete))
	mux.HandleFunc("/api/v1/move", s.withAuth(s.handleMove))
	mux.HandleFunc("/api/v1/baseline", s.withAuth(s.handleBaseline))
	return mux
}

func (s *PeerServer) withAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !auth.VerifyHeader(s.pin, r.Method, r.URL.Path, r.Header.Get(AuthHeaderName), time.Now()) {
			http.Error(w, "unauthorized (invalid PIN)", http.StatusUnauthorized)
			return
		}
		next(w, r)
	}
}

// SafeJoin resolves relPath inside root and blocks directory traversal.
func SafeJoin(root, relPath string) (string, error) {
	resolvedRoot := config.ExpandPath(root)
	cleanRel := filepath.FromSlash(strings.TrimPrefix(relPath, "/"))
	joined := filepath.Clean(filepath.Join(resolvedRoot, cleanRel))
	cleanRoot := filepath.Clean(resolvedRoot)
	if joined == cleanRoot {
		return "", fmt.Errorf("invalid target path %q", relPath)
	}
	prefix := cleanRoot
	if !strings.HasSuffix(prefix, string(filepath.Separator)) {
		prefix += string(filepath.Separator)
	}
	if !strings.HasPrefix(strings.ToLower(joined), strings.ToLower(prefix)) {
		return "", fmt.Errorf("path traversal blocked: %q", relPath)
	}
	return joined, nil
}

func peerLog(format string, args ...any) {
	fmt.Printf("[%s] %s\n", time.Now().Format("15:04:05"), fmt.Sprintf(format, args...))
}

func (s *PeerServer) handleInfo(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	peerLog("Dashboard terhubung (%s)", r.RemoteAddr)
	procs := fsops.CheckRunningProcesses([]string{"Antigravity.exe"})
	writeJSON(w, InfoResponse{
		DeviceName:      s.deviceName,
		ActiveProcesses: procs,
	})
}

func (s *PeerServer) handleBrowse(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	reqPath := r.URL.Query().Get("path")
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

func (s *PeerServer) handleDiskSpace(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	reqPath := config.ExpandPath(r.URL.Query().Get("path"))
	sp, err := fsops.GetDiskSpace(reqPath)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, sp)
}

func (s *PeerServer) handleScan(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req ScanRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	resolved := config.ExpandPath(req.RootPath)
	entries, err := scanner.ScanMetadata(r.Context(), resolved, req.Excludes)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, ScanResponse{
		ResolvedRoot: resolved,
		Entries:      entries,
	})
}

func (s *PeerServer) handleScanStream(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req ScanRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	resolved := config.ExpandPath(req.RootPath)
	peerLog("Scan dimulai: %s", resolved)

	w.Header().Set("Content-Type", "application/x-ndjson")
	flusher, _ := w.(http.Flusher)
	enc := json.NewEncoder(w)

	entries, err := scanner.ScanMetadataWithProgress(r.Context(), resolved, req.Excludes,
		func(count int, totalBytes int64, currentPath string) {
			_ = enc.Encode(map[string]any{
				"done":    false,
				"count":   count,
				"bytes":   totalBytes,
				"current": currentPath,
			})
			if flusher != nil {
				flusher.Flush()
			}
		})
	if err != nil {
		peerLog("Scan berhenti/gagal: %v", err)
		_ = enc.Encode(map[string]any{
			"done":  true,
			"error": err.Error(),
		})
		return
	}
	peerLog("Scan selesai: %d file", len(entries))
	_ = enc.Encode(map[string]any{
		"done":          true,
		"resolved_root": resolved,
		"entries":       entries,
	})
	if flusher != nil {
		flusher.Flush()
	}
}

func (s *PeerServer) handleHash(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req HashRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	resolved := config.ExpandPath(req.RootPath)
	var hashes map[string]string
	var err error
	if req.Quick {
		hashes, err = scanner.QuickHashFiles(r.Context(), resolved, req.RelPaths, s.workers)
	} else {
		hashes, err = scanner.HashFiles(r.Context(), resolved, req.RelPaths, s.workers)
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, HashResponse{Hashes: hashes})
}

func (s *PeerServer) handleHashStream(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req HashRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	resolved := config.ExpandPath(req.RootPath)
	peerLog("Verify SHA-256 dimulai: %d file", len(req.RelPaths))

	w.Header().Set("Content-Type", "application/x-ndjson")
	flusher, _ := w.(http.Flusher)
	enc := json.NewEncoder(w)

	hashes, err := scanner.HashFilesWithProgress(r.Context(), resolved, req.RelPaths, s.workers,
		func(done, total int, currentPath string) {
			_ = enc.Encode(map[string]any{
				"done":    false,
				"count":   done,
				"total":   total,
				"current": currentPath,
			})
			if flusher != nil {
				flusher.Flush()
			}
		})
	if err != nil {
		peerLog("Verify SHA-256 gagal: %v", err)
		_ = enc.Encode(map[string]any{"done": true, "error": err.Error()})
		return
	}
	peerLog("Verify SHA-256 selesai: %d file", len(hashes))
	_ = enc.Encode(map[string]any{"done": true, "hashes": hashes})
	if flusher != nil {
		flusher.Flush()
	}
}

func (s *PeerServer) handleFile(w http.ResponseWriter, r *http.Request) {
	root := r.URL.Query().Get("root")
	relPath := r.URL.Query().Get("path")
	target, err := SafeJoin(root, relPath)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	switch r.Method {
	case http.MethodGet:
		f, err := os.Open(target)
		if err != nil {
			if os.IsNotExist(err) {
				http.Error(w, "not found", http.StatusNotFound)
				return
			}
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		defer f.Close()
		fi, err := f.Stat()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Length", strconv.FormatInt(fi.Size(), 10))
		_, _ = io.CopyBuffer(w, f, make([]byte, 1024*1024))

	case http.MethodPut:
		mtimeNS, _ := strconv.ParseInt(r.URL.Query().Get("mtime"), 10, 64)
		expectedHash := r.URL.Query().Get("hash")

		if err := fsops.EnsureParentDir(target); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		tmp := fsops.TempFilePath(target)
		out, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		h := sha256.New()
		mw := io.MultiWriter(out, h)
		_, copyErr := io.CopyBuffer(mw, r.Body, make([]byte, 1024*1024))
		closeErr := out.Close()
		if copyErr != nil || closeErr != nil {
			_ = os.Remove(tmp)
			http.Error(w, fmt.Sprintf("write failed: %v / %v", copyErr, closeErr), http.StatusInternalServerError)
			return
		}
		actualHash := hex.EncodeToString(h.Sum(nil))
		if expectedHash != "" && actualHash != expectedHash {
			_ = os.Remove(tmp)
			http.Error(w, "checksum mismatch", http.StatusBadRequest)
			return
		}
		if mtimeNS > 0 {
			t := time.Unix(0, mtimeNS)
			_ = os.Chtimes(tmp, t, t)
		}
		if err := os.Rename(tmp, target); err != nil {
			_ = os.Remove(tmp)
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)

	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *PeerServer) handleDelete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req DeleteRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	target, err := SafeJoin(req.RootPath, req.RelPath)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := fsops.DeleteFile(target, req.Permanent); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusOK)
}

func (s *PeerServer) handleMove(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req MoveRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	oldTarget, err := SafeJoin(req.RootPath, req.OldRelPath)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	newTarget, err := SafeJoin(req.RootPath, req.NewRelPath)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if strings.EqualFold(oldTarget, newTarget) {
		w.WriteHeader(http.StatusOK)
		return
	}
	if err := fsops.EnsureParentDir(newTarget); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if err := os.Rename(oldTarget, newTarget); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	fsops.CleanEmptyParents(filepath.Dir(oldTarget), config.ExpandPath(req.RootPath))
	w.WriteHeader(http.StatusOK)
}

func (s *PeerServer) handleBaseline(w http.ResponseWriter, r *http.Request) {
	if s.stateStore == nil {
		http.Error(w, "state store not initialized", http.StatusInternalServerError)
		return
	}
	key := r.URL.Query().Get("key")
	if !state.ValidKey(key) {
		http.Error(w, "invalid baseline key", http.StatusBadRequest)
		return
	}
	switch r.Method {
	case http.MethodGet:
		b, err := s.stateStore.Load(key)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if b == nil {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		gz, err := state.EncodeGzip(b)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/gzip")
		_, _ = w.Write(gz)
	case http.MethodPut:
		b, err := state.DecodeGzip(io.LimitReader(r.Body, 64<<20))
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		b.Key = key
		if err := s.stateStore.Save(b); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
	case http.MethodDelete:
		if err := s.stateStore.Reset(key); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
