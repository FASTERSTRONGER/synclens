//go:build windows

package fsops

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"unsafe"
)

var (
	kernel32               = syscall.NewLazyDLL("kernel32.dll")
	procGetDiskFreeSpaceEx = kernel32.NewProc("GetDiskFreeSpaceExW")
)

// DiskSpace holds total and caller-available free bytes for a filesystem.
type DiskSpace struct {
	TotalBytes uint64 `json:"total_bytes"`
	FreeBytes  uint64 `json:"free_bytes"`
}

// GetDiskSpace returns total and available free bytes for the volume containing targetPath.
func GetDiskSpace(targetPath string) (DiskSpace, error) {
	abs, err := filepath.Abs(targetPath)
	if err != nil {
		return DiskSpace{}, err
	}
	probe := abs
	for {
		if _, err := os.Stat(probe); err == nil {
			break
		}
		parent := filepath.Dir(probe)
		if parent == probe {
			break
		}
		probe = parent
	}

	u16, err := syscall.UTF16PtrFromString(probe)
	if err != nil {
		return DiskSpace{}, err
	}
	var freeAvail, totalBytes, totalFree uint64
	r1, _, callErr := procGetDiskFreeSpaceEx.Call(
		uintptr(unsafe.Pointer(u16)),
		uintptr(unsafe.Pointer(&freeAvail)),
		uintptr(unsafe.Pointer(&totalBytes)),
		uintptr(unsafe.Pointer(&totalFree)),
	)
	if r1 == 0 {
		return DiskSpace{}, fmt.Errorf("GetDiskFreeSpaceExW(%s): %w", probe, callErr)
	}
	return DiskSpace{
		TotalBytes: totalBytes,
		FreeBytes:  freeAvail,
	}, nil
}
