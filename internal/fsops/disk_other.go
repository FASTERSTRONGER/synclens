//go:build !windows

package fsops

// DiskSpace holds total and caller-available free bytes for a filesystem.
type DiskSpace struct {
	TotalBytes uint64 `json:"total_bytes"`
	FreeBytes  uint64 `json:"free_bytes"`
}

// GetDiskSpace returns zeroed disk space on non-Windows platforms.
func GetDiskSpace(targetPath string) (DiskSpace, error) {
	return DiskSpace{}, nil
}
