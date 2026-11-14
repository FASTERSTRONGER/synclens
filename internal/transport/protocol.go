package transport

import "synclens/internal/scanner"

const AuthHeaderName = "X-SyncLens-Auth"

type InfoResponse struct {
	DeviceName      string          `json:"device_name"`
	ActiveProcesses map[string]bool `json:"active_processes"`
}

type ScanRequest struct {
	RootPath string   `json:"root_path"`
	Excludes []string `json:"excludes"`
}

type ScanResponse struct {
	ResolvedRoot string                      `json:"resolved_root"`
	Entries      map[string]scanner.FileMeta `json:"entries"`
}

type HashRequest struct {
	RootPath string   `json:"root_path"`
	RelPaths []string `json:"rel_paths"`
	Quick    bool     `json:"quick,omitempty"`
}

type HashResponse struct {
	Hashes map[string]string `json:"hashes"`
}

type DeleteRequest struct {
	RootPath  string `json:"root_path"`
	RelPath   string `json:"rel_path"`
	Permanent bool   `json:"permanent"`
}
