package fsops

import (
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"synclens/internal/config"
)

type DirItem struct {
	Name string `json:"name"`
	Path string `json:"path"`
}

// ListDirectories returns available drives (if path is empty on Windows) or immediate subdirectories.
func ListDirectories(rawPath string) (string, []DirItem, error) {
	trimmed := strings.TrimSpace(rawPath)
	if trimmed == "" {
		if runtime.GOOS == "windows" {
			var drives []DirItem
			for c := 'A'; c <= 'Z'; c++ {
				d := string(c) + `:\`
				if _, err := os.Stat(d); err == nil {
					drives = append(drives, DirItem{Name: d, Path: d})
				}
			}
			return "", drives, nil
		}
		trimmed = "/"
	}

	resolved := config.ExpandPath(trimmed)
	entries, err := os.ReadDir(resolved)
	if err != nil {
		return resolved, nil, err
	}

	var dirs []DirItem
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		name := e.Name()
		lower := strings.ToLower(name)
		if lower == "$recycle.bin" || lower == "system volume information" {
			continue
		}
		dirs = append(dirs, DirItem{
			Name: name,
			Path: filepath.Join(resolved, name),
		})
	}

	sort.Slice(dirs, func(i, j int) bool {
		return strings.ToLower(dirs[i].Name) < strings.ToLower(dirs[j].Name)
	})
	return resolved, dirs, nil
}
