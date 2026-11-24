package transport

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"synclens/internal/auth"
	"synclens/internal/fsops"
	"synclens/internal/scanner"
	"synclens/internal/state"
)

type PeerClient struct {
	baseURL    string
	pin        string
	httpClient *http.Client
}

func NewPeerClient(ip string, port int, pin string) *PeerClient {
	return &PeerClient{
		baseURL: fmt.Sprintf("http://%s:%d", ip, port),
		pin:     pin,
		httpClient: &http.Client{
			Transport: &http.Transport{
				MaxIdleConns:        16,
				MaxIdleConnsPerHost: 16,
				IdleConnTimeout:     60 * time.Second,
			},
			Timeout: 0,
		},
	}
}

func (c *PeerClient) newRequest(ctx context.Context, method, reqPath string, body io.Reader) (*http.Request, error) {
	u, err := url.Parse(c.baseURL + reqPath)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, method, u.String(), body)
	if err != nil {
		return nil, err
	}
	req.Header.Set(AuthHeaderName, auth.SignHeader(c.pin, method, u.Path, time.Now()))
	return req, nil
}

func (c *PeerClient) Info(ctx context.Context) (*InfoResponse, error) {
	req, err := c.newRequest(ctx, http.MethodGet, "/api/v1/info", nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("peer info failed (%d): %s", resp.StatusCode, string(b))
	}
	var out InfoResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *PeerClient) GetDiskSpace(ctx context.Context, reqPath string) (fsops.DiskSpace, error) {
	q := url.Values{}
	q.Set("path", reqPath)
	req, err := c.newRequest(ctx, http.MethodGet, "/api/v1/disk-space?"+q.Encode(), nil)
	if err != nil {
		return fsops.DiskSpace{}, err
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fsops.DiskSpace{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return fsops.DiskSpace{}, fmt.Errorf("disk-space failed (%d): %s", resp.StatusCode, string(b))
	}
	var out fsops.DiskSpace
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return fsops.DiskSpace{}, err
	}
	return out, nil
}

type BrowseResponse struct {
	CurrentPath string `json:"current_path"`
	Dirs        []struct {
		Name string `json:"name"`
		Path string `json:"path"`
	} `json:"dirs"`
}

func (c *PeerClient) Browse(ctx context.Context, reqPath string) (*BrowseResponse, error) {
	q := url.Values{}
	q.Set("path", reqPath)
	req, err := c.newRequest(ctx, http.MethodGet, "/api/v1/browse?"+q.Encode(), nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("browse failed (%d): %s", resp.StatusCode, string(b))
	}
	var out BrowseResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *PeerClient) Scan(ctx context.Context, rootPath string, excludes []string) (*ScanResponse, error) {
	return c.ScanWithProgress(ctx, rootPath, excludes, nil)
}

func (c *PeerClient) ScanWithProgress(
	ctx context.Context,
	rootPath string,
	excludes []string,
	onProgress func(count int, totalBytes int64, currentPath string),
) (*ScanResponse, error) {
	data, err := json.Marshal(ScanRequest{RootPath: rootPath, Excludes: excludes})
	if err != nil {
		return nil, err
	}
	req, err := c.newRequest(ctx, http.MethodPost, "/api/v1/scan-stream", bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	// Fallback to non-streaming endpoint if peer is older
	if resp.StatusCode == http.StatusNotFound {
		return c.scanLegacy(ctx, data)
	}
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("peer scan failed (%d): %s", resp.StatusCode, string(b))
	}

	dec := json.NewDecoder(resp.Body)
	allEntries := make(map[string]scanner.FileMeta)
	var resolvedRoot string

	for {
		var msg struct {
			Done         bool                        `json:"done"`
			Error        string                      `json:"error"`
			Count        int                         `json:"count"`
			Bytes        int64                       `json:"bytes"`
			Current      string                      `json:"current"`
			ResolvedRoot string                      `json:"resolved_root"`
			Entries      map[string]scanner.FileMeta `json:"entries"`
		}
		if err := dec.Decode(&msg); err != nil {
			if err == io.EOF {
				break
			}
			return nil, fmt.Errorf("decode peer scan stream: %w", err)
		}
		if msg.Error != "" {
			return nil, fmt.Errorf("peer scan error: %s", msg.Error)
		}
		if msg.ResolvedRoot != "" {
			resolvedRoot = msg.ResolvedRoot
		}
		for k, v := range msg.Entries {
			allEntries[k] = v
		}
		if !msg.Done {
			if onProgress != nil {
				onProgress(msg.Count, msg.Bytes, msg.Current)
			}
			continue
		}
		return &ScanResponse{
			ResolvedRoot: resolvedRoot,
			Entries:      allEntries,
		}, nil
	}
	return nil, fmt.Errorf("peer scan stream ended unexpectedly")
}

func (c *PeerClient) scanLegacy(ctx context.Context, data []byte) (*ScanResponse, error) {
	req, err := c.newRequest(ctx, http.MethodPost, "/api/v1/scan", bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("peer scan failed (%d): %s", resp.StatusCode, string(b))
	}
	var out ScanResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *PeerClient) Hash(ctx context.Context, rootPath string, relPaths []string) (map[string]string, error) {
	return c.hashInternal(ctx, rootPath, relPaths, false)
}

func (c *PeerClient) QuickHash(ctx context.Context, rootPath string, relPaths []string) (map[string]string, error) {
	return c.hashInternal(ctx, rootPath, relPaths, true)
}

func (c *PeerClient) HashWithProgress(
	ctx context.Context,
	rootPath string,
	relPaths []string,
	onProgress func(done, total int, currentPath string),
) (map[string]string, error) {
	data, err := json.Marshal(HashRequest{RootPath: rootPath, RelPaths: relPaths})
	if err != nil {
		return nil, err
	}
	req, err := c.newRequest(ctx, http.MethodPost, "/api/v1/hash-stream", bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return c.hashInternal(ctx, rootPath, relPaths, false)
	}
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("peer hash failed (%d): %s", resp.StatusCode, string(b))
	}

	dec := json.NewDecoder(resp.Body)
	for {
		var msg struct {
			Done    bool              `json:"done"`
			Error   string            `json:"error"`
			Count   int               `json:"count"`
			Total   int               `json:"total"`
			Current string            `json:"current"`
			Hashes  map[string]string `json:"hashes"`
		}
		if err := dec.Decode(&msg); err != nil {
			if err == io.EOF {
				break
			}
			return nil, fmt.Errorf("decode peer hash stream: %w", err)
		}
		if msg.Error != "" {
			return nil, fmt.Errorf("peer hash error: %s", msg.Error)
		}
		if !msg.Done {
			if onProgress != nil {
				onProgress(msg.Count, msg.Total, msg.Current)
			}
			continue
		}
		return msg.Hashes, nil
	}
	return nil, fmt.Errorf("peer hash stream ended unexpectedly")
}

func (c *PeerClient) hashInternal(ctx context.Context, rootPath string, relPaths []string, quick bool) (map[string]string, error) {
	data, err := json.Marshal(HashRequest{RootPath: rootPath, RelPaths: relPaths, Quick: quick})
	if err != nil {
		return nil, err
	}
	req, err := c.newRequest(ctx, http.MethodPost, "/api/v1/hash", bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("peer hash failed (%d): %s", resp.StatusCode, string(b))
	}
	var out HashResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return out.Hashes, nil
}

func (c *PeerClient) UploadFile(ctx context.Context, remoteRoot, relPath, localAbsPath string, mtimeNS int64) (int64, error) {
	// Compute SHA-256 of local file first so peer verifies transfer integrity
	hashes, err := scanner.HashFiles(ctx, filepath.Dir(localAbsPath), []string{filepath.Base(localAbsPath)}, 1)
	if err != nil {
		return 0, err
	}
	expectedHash := hashes[filepath.Base(localAbsPath)]

	q := url.Values{}
	q.Set("root", remoteRoot)
	q.Set("path", relPath)
	q.Set("mtime", strconv.FormatInt(mtimeNS, 10))
	q.Set("hash", expectedHash)
	targetURL := "/api/v1/file?" + q.Encode()

	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		if ctx.Err() != nil {
			return 0, ctx.Err()
		}
		if attempt > 0 {
			time.Sleep(400 * time.Millisecond)
		}
		size, retryable, err := c.uploadOnce(ctx, targetURL, relPath, localAbsPath)
		if err == nil {
			return size, nil
		}
		lastErr = err
		if !retryable {
			return 0, err
		}
	}
	return 0, lastErr
}

func (c *PeerClient) uploadOnce(ctx context.Context, targetURL, relPath, localAbsPath string) (int64, bool, error) {
	f, err := os.Open(localAbsPath)
	if err != nil {
		return 0, false, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return 0, false, err
	}

	req, err := c.newRequest(ctx, http.MethodPut, targetURL, f)
	if err != nil {
		return 0, false, err
	}
	req.ContentLength = fi.Size()

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return 0, ctx.Err() == nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		retryable := resp.StatusCode >= 500
		return 0, retryable, fmt.Errorf("upload %s failed (%d): %s", relPath, resp.StatusCode, string(b))
	}
	return fi.Size(), false, nil
}

func (c *PeerClient) DownloadFile(ctx context.Context, remoteRoot, relPath, localAbsPath string, mtimeNS int64) (int64, error) {
	q := url.Values{}
	q.Set("root", remoteRoot)
	q.Set("path", relPath)
	targetURL := "/api/v1/file?" + q.Encode()

	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		if ctx.Err() != nil {
			return 0, ctx.Err()
		}
		if attempt > 0 {
			time.Sleep(400 * time.Millisecond)
		}
		written, retryable, err := c.downloadOnce(ctx, targetURL, relPath, localAbsPath, mtimeNS)
		if err == nil {
			return written, nil
		}
		lastErr = err
		if !retryable {
			return 0, err
		}
	}
	return 0, lastErr
}

func (c *PeerClient) downloadOnce(ctx context.Context, targetURL, relPath, localAbsPath string, mtimeNS int64) (int64, bool, error) {
	req, err := c.newRequest(ctx, http.MethodGet, targetURL, nil)
	if err != nil {
		return 0, false, err
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return 0, ctx.Err() == nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		retryable := resp.StatusCode >= 500
		return 0, retryable, fmt.Errorf("download %s failed (%d): %s", relPath, resp.StatusCode, string(b))
	}

	if err := fsops.EnsureParentDir(localAbsPath); err != nil {
		return 0, false, err
	}
	tmp := fsops.TempFilePath(localAbsPath)
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
	if err != nil {
		return 0, false, err
	}

	h := sha256.New()
	mw := io.MultiWriter(out, h)
	written, copyErr := io.CopyBuffer(mw, resp.Body, make([]byte, 1024*1024))
	closeErr := out.Close()
	if copyErr != nil || closeErr != nil {
		_ = os.Remove(tmp)
		return 0, copyErr != nil && ctx.Err() == nil, fmt.Errorf("write %s failed: %v / %v", relPath, copyErr, closeErr)
	}
	_ = hex.EncodeToString(h.Sum(nil))

	if mtimeNS > 0 {
		t := time.Unix(0, mtimeNS)
		_ = os.Chtimes(tmp, t, t)
	}
	if err := os.Rename(tmp, localAbsPath); err != nil {
		_ = os.Remove(tmp)
		return 0, false, err
	}
	return written, false, nil
}

func (c *PeerClient) DeleteRemote(ctx context.Context, remoteRoot, relPath string, permanent bool) error {
	data, err := json.Marshal(DeleteRequest{
		RootPath:  remoteRoot,
		RelPath:   relPath,
		Permanent: permanent,
	})
	if err != nil {
		return err
	}
	req, err := c.newRequest(ctx, http.MethodPost, "/api/v1/delete", bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("remote delete failed (%d): %s", resp.StatusCode, string(b))
	}
	return nil
}

func (c *PeerClient) MoveRemote(ctx context.Context, remoteRoot, oldRelPath, newRelPath string) error {
	data, err := json.Marshal(MoveRequest{
		RootPath:   remoteRoot,
		OldRelPath: oldRelPath,
		NewRelPath: newRelPath,
	})
	if err != nil {
		return err
	}
	req, err := c.newRequest(ctx, http.MethodPost, "/api/v1/move", bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("remote move failed (%d): %s", resp.StatusCode, string(b))
	}
	return nil
}

func (c *PeerClient) GetBaseline(ctx context.Context, key string) (*state.Baseline, error) {
	q := url.Values{}
	q.Set("key", key)
	req, err := c.newRequest(ctx, http.MethodGet, "/api/v1/baseline?"+q.Encode(), nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil, nil
	}
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("get baseline failed (%d): %s", resp.StatusCode, string(b))
	}
	return state.DecodeGzip(resp.Body)
}

func (c *PeerClient) PutBaseline(ctx context.Context, key string, b *state.Baseline) error {
	gz, err := state.EncodeGzip(b)
	if err != nil {
		return err
	}
	q := url.Values{}
	q.Set("key", key)
	req, err := c.newRequest(ctx, http.MethodPut, "/api/v1/baseline?"+q.Encode(), bytes.NewReader(gz))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/gzip")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("put baseline failed (%d): %s", resp.StatusCode, string(msg))
	}
	return nil
}

func (c *PeerClient) DeleteBaseline(ctx context.Context, key string) error {
	q := url.Values{}
	q.Set("key", key)
	req, err := c.newRequest(ctx, http.MethodDelete, "/api/v1/baseline?"+q.Encode(), nil)
	if err != nil {
		return err
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNotFound {
		msg, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("delete baseline failed (%d): %s", resp.StatusCode, string(msg))
	}
	return nil
}
