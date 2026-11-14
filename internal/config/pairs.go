package config

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

const (
	DefaultWebPort       = 9850
	DefaultPeerPort      = 9857
	DefaultDiscoveryPort = 9858
)

// FolderPair defines a local-to-peer directory pair and its exclude patterns.
type FolderPair struct {
	ID           string   `json:"id"`
	Name         string   `json:"name"`
	LocalPath    string   `json:"local_path"`
	RemotePath   string   `json:"remote_path"`
	Excludes     []string `json:"excludes"`
	CheckProcess []string `json:"check_process,omitempty"`
}

// PairsConfig stores saved folder pairs on the dashboard device.
type PairsConfig struct {
	Pairs []FolderPair `json:"pairs"`
}

var winEnvRegex = regexp.MustCompile(`%([^%]+)%`)

// ExpandPath resolves Windows (%VAR%) and POSIX ($VAR) environment variables.
func ExpandPath(p string) string {
	if p == "" {
		return ""
	}
	expanded := winEnvRegex.ReplaceAllStringFunc(p, func(match string) string {
		key := strings.Trim(match, "%")
		if val, ok := os.LookupEnv(key); ok {
			return val
		}
		return match
	})
	expanded = os.ExpandEnv(expanded)
	return filepath.Clean(expanded)
}

// DefaultPairs returns initial folder pair presets standardized on D:\.
func DefaultPairs() *PairsConfig {
	return &PairsConfig{
		Pairs: []FolderPair{
			{
				ID:         "gemini",
				Name:       ".gemini (Agent Config & State)",
				LocalPath:  `%USERPROFILE%\.gemini`,
				RemotePath: `%USERPROFILE%\.gemini`,
				Excludes: []string{
					"*.lock",
					"*.tmp",
					"*.log",
					"antigravity/browser_sandbox/*",
					"antigravity/cache/*",
				},
				CheckProcess: []string{"Antigravity.exe"},
			},
			{
				ID:         "projects",
				Name:       "Projects (D:\\Projects)",
				LocalPath:  `D:\Projects`,
				RemotePath: `D:\Projects`,
				Excludes: []string{
					"*.tmp",
					".git/index.lock",
				},
			},
			{
				ID:         "support-drive",
				Name:       "Drive Pendukung (D:\\)",
				LocalPath:  `D:\`,
				RemotePath: `D:\`,
				Excludes: []string{
					"$RECYCLE.BIN/*",
					"$RECYCLE.BIN",
					"System Volume Information/*",
					"System Volume Information",
					"pagefile.sys",
					"hiberfil.sys",
					"swapfile.sys",
					"DumpStack.log*",
					"*.tmp",
				},
			},
		},
	}
}

// DefaultPairsFilePath returns the path to synclens-pairs.json next to the executable.
func DefaultPairsFilePath() string {
	if exe, err := os.Executable(); err == nil {
		return filepath.Join(filepath.Dir(exe), "synclens-pairs.json")
	}
	return "synclens-pairs.json"
}

// LoadOrInitPairs loads saved pairs or creates defaults if missing.
func LoadOrInitPairs(path string) (*PairsConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			cfg := DefaultPairs()
			_ = cfg.Save(path)
			return cfg, nil
		}
		return nil, err
	}
	var cfg PairsConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// Save writes pairs configuration to disk.
func (c *PairsConfig) Save(path string) error {
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0644)
}
