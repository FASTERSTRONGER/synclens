package fsops

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"
)

var tmpSeq uint64

// TempFilePath returns a short, unique temporary file path in the same directory
// as targetPath so atomic rename stays within NTFS 255-char filename limits.
func TempFilePath(targetPath string) string {
	seq := atomic.AddUint64(&tmpSeq, 1)
	name := fmt.Sprintf(".synclens_tmp_%x_%x", time.Now().UnixNano(), seq)
	return filepath.Join(filepath.Dir(targetPath), name)
}

// EnsureParentDir creates parent directories for targetPath and reports a clear
// error if an ancestor path component already exists as a regular file.
func EnsureParentDir(targetPath string) error {
	parent := filepath.Dir(targetPath)
	err := os.MkdirAll(parent, 0o755)
	if err == nil {
		return nil
	}
	// Walk ancestors to detect a file-vs-directory or broken junction/symlink collision.
	cur := parent
	for {
		if fi, statErr := os.Lstat(cur); statErr == nil {
			if fi.Mode()&(os.ModeSymlink|os.ModeIrregular) != 0 {
				if _, targetErr := os.Stat(cur); os.IsNotExist(targetErr) {
					// Dead/dangling junction or symlink: safe to remove the reparse point and retry MkdirAll.
					if remErr := os.Remove(cur); remErr == nil || os.IsNotExist(remErr) {
						if retryErr := os.MkdirAll(parent, 0o755); retryErr == nil {
							return nil
						}
					}
				}
				return fmt.Errorf("konflik junction/symlink rusak: %q adalah link/junction yang targetnya tidak ada, hapus link tersebut agar folder asli dapat dibuat (%w)", cur, err)
			}
			if !fi.IsDir() {
				return fmt.Errorf("konflik file vs folder: %q sudah ada sebagai file di tujuan sehingga folder tidak bisa dibuat (%w)", cur, err)
			}
		}
		next := filepath.Dir(cur)
		if next == cur {
			break
		}
		cur = next
	}
	return err
}

// CleanEmptyParents removes empty directories upward starting from dir,
// stopping cleanly before removing stopRoot itself.
func CleanEmptyParents(dir, stopRoot string) {
	cleanDir := filepath.Clean(dir)
	cleanRoot := filepath.Clean(stopRoot)
	rootLower := strings.ToLower(cleanRoot)
	rootPrefix := cleanRoot
	if !strings.HasSuffix(rootPrefix, string(filepath.Separator)) {
		rootPrefix += string(filepath.Separator)
	}
	rootPrefixLower := strings.ToLower(rootPrefix)

	cur := cleanDir
	for {
		curLower := strings.ToLower(cur)
		if curLower == rootLower || !strings.HasPrefix(curLower, rootPrefixLower) {
			break
		}
		if err := os.Remove(cur); err != nil {
			break
		}
		next := filepath.Dir(cur)
		if next == cur {
			break
		}
		cur = next
	}
}
