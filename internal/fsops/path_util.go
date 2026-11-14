package fsops

import (
	"fmt"
	"os"
	"path/filepath"
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
