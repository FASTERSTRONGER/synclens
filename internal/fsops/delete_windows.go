//go:build windows

package fsops

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"unsafe"
)

var (
	shell32           = syscall.NewLazyDLL("shell32.dll")
	procSHFileOpW     = shell32.NewProc("SHFileOperationW")
)

const (
	foDelete          = 0x0003
	fofSilent         = 0x0004
	fofNoConfirmation = 0x0010
	fofAllowUndo      = 0x0040
	fofNoErrorUI      = 0x0400
)

type shFileOpStructW struct {
	hwnd                  uintptr
	wFunc                 uint32
	pFrom                 *uint16
	pTo                   *uint16
	fFlags                uint16
	fAnyOperationsAborted int32
	hNameMappings         uintptr
	lpszProgressTitle     *uint16
}

// DeleteFile moves targetPath to the Windows Recycle Bin unless permanent is true.
func DeleteFile(targetPath string, permanent bool) error {
	abs, err := filepath.Abs(targetPath)
	if err != nil {
		return err
	}
	if _, err := os.Stat(abs); os.IsNotExist(err) {
		return nil
	}
	if permanent {
		return os.RemoveAll(abs)
	}

	// SHFileOperationW requires a double-null-terminated UTF-16 string
	u16, err := syscall.UTF16FromString(abs)
	if err != nil {
		return err
	}
	u16 = append(u16, 0)

	op := shFileOpStructW{
		wFunc:  foDelete,
		pFrom:  &u16[0],
		fFlags: fofAllowUndo | fofNoConfirmation | fofSilent | fofNoErrorUI,
	}
	ret, _, callErr := procSHFileOpW.Call(uintptr(unsafe.Pointer(&op)))
	if ret != 0 {
		return fmt.Errorf("recycle bin delete failed (code %d): %v", ret, callErr)
	}
	if op.fAnyOperationsAborted != 0 {
		return fmt.Errorf("recycle bin delete aborted")
	}
	return nil
}

// CheckRunningProcesses checks whether any named Windows executables are currently running.
func CheckRunningProcesses(names []string) map[string]bool {
	res := make(map[string]bool, len(names))
	if len(names) == 0 {
		return res
	}
	out, err := exec.Command("tasklist", "/NH", "/FO", "CSV").Output()
	if err != nil {
		return res
	}
	lowerOut := strings.ToLower(string(out))
	for _, n := range names {
		if n != "" {
			res[n] = strings.Contains(lowerOut, `"`+strings.ToLower(n)+`"`)
		}
	}
	return res
}
