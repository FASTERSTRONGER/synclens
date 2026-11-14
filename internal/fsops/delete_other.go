//go:build !windows

package fsops

import "os"

// DeleteFile removes targetPath on non-Windows platforms.
func DeleteFile(targetPath string, permanent bool) error {
	return os.RemoveAll(targetPath)
}

// CheckRunningProcesses is a no-op on non-Windows platforms.
func CheckRunningProcesses(names []string) map[string]bool {
	res := make(map[string]bool, len(names))
	for _, n := range names {
		res[n] = false
	}
	return res
}
