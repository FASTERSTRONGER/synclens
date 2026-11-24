package state

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"synclens/internal/fsops"
)

const CurrentVersion = 1

// Entry stores the last known synchronized metadata of a single relative path.
type Entry struct {
	Size    int64  `json:"s"`
	MTimeNS int64  `json:"m"`
	Hash    string `json:"h,omitempty"`
}

// Baseline stores the last synchronized snapshot for a symmetrical folder pair.
type Baseline struct {
	Version   int              `json:"version"`
	Key       string           `json:"key"`
	UpdatedAt int64            `json:"updated_at"`
	Entries   map[string]Entry `json:"entries"`
}

// PairKey returns a symmetrical 16-hex-char identifier derived from both device
// names and cleaned paths, sorted canonically so PC<->Laptop produce the same key.
func PairKey(devA, pathA, devB, pathB string) string {
	epA := strings.ToLower(strings.TrimSpace(devA)) + "|" + strings.ToLower(filepath.Clean(pathA))
	epB := strings.ToLower(strings.TrimSpace(devB)) + "|" + strings.ToLower(filepath.Clean(pathB))
	if epA > epB {
		epA, epB = epB, epA
	}
	sum := sha256.Sum256([]byte(epA + "\x00" + epB))
	return hex.EncodeToString(sum[:8])
}

// ValidKey verifies that key is strictly 16 lowercase hexadecimal characters.
func ValidKey(key string) bool {
	if len(key) != 16 {
		return false
	}
	for i := 0; i < len(key); i++ {
		c := key[i]
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			return false
		}
	}
	return true
}

// DefaultStateDir returns the centralized synclens-state directory next to the executable.
func DefaultStateDir() string {
	if exe, err := os.Executable(); err == nil {
		return filepath.Join(filepath.Dir(exe), "synclens-state")
	}
	return "synclens-state"
}

// Store manages compressed baseline snapshots on disk.
type Store struct {
	dir string
}

// NewStore returns a baseline Store rooted at dir.
func NewStore(dir string) *Store {
	return &Store{dir: dir}
}

func (s *Store) filePath(key string) (string, error) {
	if !ValidKey(key) {
		return "", fmt.Errorf("invalid baseline key %q", key)
	}
	return filepath.Join(s.dir, key+".json.gz"), nil
}

// EncodeGzip serializes b into gzip-compressed JSON bytes.
func EncodeGzip(b *Baseline) ([]byte, error) {
	if b == nil {
		return nil, errors.New("nil baseline")
	}
	cp := *b
	if cp.Version == 0 {
		cp.Version = CurrentVersion
	}
	if cp.Entries == nil {
		cp.Entries = map[string]Entry{}
	}
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	enc := json.NewEncoder(gw)
	if err := enc.Encode(&cp); err != nil {
		_ = gw.Close()
		return nil, err
	}
	if err := gw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// DecodeGzip reads a gzip-compressed JSON Baseline from r.
func DecodeGzip(r io.Reader) (*Baseline, error) {
	gr, err := gzip.NewReader(r)
	if err != nil {
		return nil, err
	}
	defer gr.Close()
	var b Baseline
	if err := json.NewDecoder(gr).Decode(&b); err != nil {
		return nil, err
	}
	if b.Version == 0 {
		b.Version = CurrentVersion
	}
	if b.Entries == nil {
		b.Entries = make(map[string]Entry)
	}
	return &b, nil
}

// Load reads a baseline by key. Returns (nil, nil) if no snapshot exists yet.
func (s *Store) Load(key string) (*Baseline, error) {
	path, err := s.filePath(key)
	if err != nil {
		return nil, err
	}
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()
	b, err := DecodeGzip(f)
	if err != nil {
		return nil, err
	}
	if b.Key == "" {
		b.Key = key
	}
	return b, nil
}

// Save atomically writes b as gzipped JSON in the store directory.
func (s *Store) Save(b *Baseline) error {
	if b == nil {
		return errors.New("nil baseline")
	}
	path, err := s.filePath(b.Key)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(s.dir, 0o755); err != nil {
		return err
	}
	payload, err := EncodeGzip(b)
	if err != nil {
		return err
	}
	tmp := fsops.TempFilePath(path)
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	if _, err := f.Write(payload); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// Reset deletes the stored snapshot for key if it exists.
func (s *Store) Reset(key string) error {
	path, err := s.filePath(key)
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}
