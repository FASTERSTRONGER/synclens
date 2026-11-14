package scanner

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// FileMeta holds file metadata collected during Stage 1 scan (and optional Stage 2 hash).
type FileMeta struct {
	RelPath string `json:"rel_path"`
	Size    int64  `json:"size"`
	MTimeNS int64  `json:"mtime_ns"`
	IsDir   bool   `json:"is_dir"`
	Hash    string `json:"hash,omitempty"`
}

// IsExcluded checks whether a forward-slash relative path matches any exclude pattern.
func IsExcluded(relPath string, isDir bool, patterns []string) bool {
	if relPath == "" || relPath == "." {
		return false
	}
	norm := strings.TrimPrefix(relPath, "/")
	lowerPath := strings.ToLower(norm)
	baseName := strings.ToLower(path.Base(norm))

	for _, raw := range patterns {
		pat := strings.ToLower(strings.TrimSpace(strings.ReplaceAll(raw, `\`, `/`)))
		pat = strings.TrimPrefix(pat, "/")
		if pat == "" {
			continue
		}
		if strings.HasSuffix(pat, "/*") {
			prefix := strings.TrimSuffix(pat, "/*")
			if lowerPath == prefix || strings.HasPrefix(lowerPath, prefix+"/") {
				return true
			}
		}
		if lowerPath == pat || baseName == pat {
			return true
		}
		if matched, err := path.Match(pat, baseName); err == nil && matched {
			return true
		}
		if matched, err := path.Match(pat, lowerPath); err == nil && matched {
			return true
		}
	}
	return false
}

// ScanMetadata performs a fast Stage 1 walk collecting size and modification time without hashing.
func ScanMetadata(ctx context.Context, rootPath string, excludes []string) (map[string]FileMeta, error) {
	return ScanMetadataWithProgress(ctx, rootPath, excludes, nil)
}

// ScanMetadataWithProgress performs Stage 1 walk and invokes onProgress periodically.
func ScanMetadataWithProgress(
	ctx context.Context,
	rootPath string,
	excludes []string,
	onProgress func(count int, totalBytes int64, currentPath string),
) (map[string]FileMeta, error) {
	info, err := os.Stat(rootPath)
	if err != nil {
		if os.IsNotExist(err) {
			return make(map[string]FileMeta), nil
		}
		return nil, fmt.Errorf("stat root %s: %w", rootPath, err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("%s is not a directory", rootPath)
	}

	result := make(map[string]FileMeta)
	var totalBytes int64
	lastEmit := time.Now()

	err = filepath.WalkDir(rootPath, func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			if os.IsPermission(walkErr) {
				if d != nil && d.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
			return walkErr
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if p == rootPath {
			return nil
		}

		rel, err := filepath.Rel(rootPath, p)
		if err != nil {
			return err
		}
		relSlash := filepath.ToSlash(rel)

		if IsExcluded(relSlash, d.IsDir(), excludes) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.Type()&(os.ModeSymlink|os.ModeIrregular) != 0 {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.Contains(d.Name(), ".synclens.tmp.") || strings.HasPrefix(d.Name(), ".synclens_tmp_") {
			return nil
		}

		fi, err := d.Info()
		if err != nil {
			if os.IsPermission(err) || os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if fi.Mode()&(os.ModeSymlink|os.ModeIrregular) != 0 {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}

		if d.IsDir() {
			return nil // Compare files explicitly; directories are created as needed on copy
		}

		result[relSlash] = FileMeta{
			RelPath: relSlash,
			Size:    fi.Size(),
			MTimeNS: fi.ModTime().UnixNano(),
			IsDir:   false,
		}
		totalBytes += fi.Size()
		if onProgress != nil && time.Since(lastEmit) >= 120*time.Millisecond {
			onProgress(len(result), totalBytes, relSlash)
			lastEmit = time.Now()
		}
		return nil
	})
	if err == nil && onProgress != nil {
		onProgress(len(result), totalBytes, "")
	}
	return result, err
}

// HashFiles performs Stage 2 parallel SHA-256 hashing for the requested relative paths.
func HashFiles(ctx context.Context, rootPath string, relPaths []string, workers int) (map[string]string, error) {
	return HashFilesWithProgress(ctx, rootPath, relPaths, workers, nil)
}

// HashFilesWithProgress performs parallel SHA-256 hashing and invokes onProgress periodically.
func HashFilesWithProgress(
	ctx context.Context,
	rootPath string,
	relPaths []string,
	workers int,
	onProgress func(done, total int, currentPath string),
) (map[string]string, error) {
	if workers <= 0 {
		workers = runtime.NumCPU()
		if workers < 2 {
			workers = 2
		}
	}

	total := len(relPaths)
	hashes := make(map[string]string, total)
	if total == 0 {
		return hashes, nil
	}

	jobCh := make(chan string, total)
	for _, rp := range relPaths {
		jobCh <- rp
	}
	close(jobCh)

	var (
		mu       sync.Mutex
		wg       sync.WaitGroup
		doneCnt  int64
		lastEmit = time.Now().UnixNano()
	)

	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			buf := make([]byte, 1024*1024)
			for rp := range jobCh {
				if ctx.Err() != nil {
					return
				}
				abs := filepath.Join(rootPath, filepath.FromSlash(rp))
				h, err := hashFile(abs, buf)
				d := int(atomic.AddInt64(&doneCnt, 1))
				if err == nil {
					mu.Lock()
					hashes[rp] = h
					mu.Unlock()
				}
				if onProgress != nil {
					now := time.Now().UnixNano()
					prev := atomic.LoadInt64(&lastEmit)
					if d == total || now-prev >= int64(100*time.Millisecond) {
						if atomic.CompareAndSwapInt64(&lastEmit, prev, now) || d == total {
							onProgress(d, total, rp)
						}
					}
				}
			}
		}()
	}
	wg.Wait()
	return hashes, ctx.Err()
}

func hashFile(absPath string, buf []byte) (string, error) {
	f, err := os.Open(absPath)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.CopyBuffer(h, f, buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// QuickHashFiles hashes head, middle, and tail 64KB chunks plus file size for large files.
func QuickHashFiles(ctx context.Context, rootPath string, relPaths []string, workers int) (map[string]string, error) {
	if workers <= 0 {
		workers = runtime.NumCPU()
		if workers < 2 {
			workers = 2
		}
	}
	hashes := make(map[string]string, len(relPaths))
	if len(relPaths) == 0 {
		return hashes, nil
	}

	jobCh := make(chan string, len(relPaths))
	for _, rp := range relPaths {
		jobCh <- rp
	}
	close(jobCh)

	var mu sync.Mutex
	var wg sync.WaitGroup
	const chunkSize = int64(64 * 1024)

	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			buf := make([]byte, chunkSize)
			for rp := range jobCh {
				if ctx.Err() != nil {
					return
				}
				abs := filepath.Join(rootPath, filepath.FromSlash(rp))
				h, err := quickHashFile(abs, buf)
				if err != nil {
					continue
				}
				mu.Lock()
				hashes[rp] = "q:" + h
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	return hashes, ctx.Err()
}

func quickHashFile(absPath string, buf []byte) (string, error) {
	f, err := os.Open(absPath)
	if err != nil {
		return "", err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return "", err
	}
	size := fi.Size()
	chunk := int64(len(buf))

	h := sha256.New()
	_, _ = fmt.Fprintf(h, "%d|", size)

	offsets := []int64{0}
	if size > chunk*2 {
		offsets = append(offsets, (size/2)-(chunk/2), size-chunk)
	} else if size > chunk {
		offsets = append(offsets, size-chunk)
	}

	for _, off := range offsets {
		n, err := f.ReadAt(buf, off)
		if n > 0 {
			h.Write(buf[:n])
		}
		if err != nil && err != io.EOF {
			return "", err
		}
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
