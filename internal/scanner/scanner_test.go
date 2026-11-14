package scanner

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestTwoStageScanAndHash(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "doc.txt"), []byte("hello synclens"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "temp.lock"), []byte("ignore"), 0644); err != nil {
		t.Fatal(err)
	}

	meta, err := ScanMetadata(context.Background(), root, []string{"*.lock"})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := meta["temp.lock"]; ok {
		t.Fatalf("temp.lock should be excluded")
	}
	if m, ok := meta["doc.txt"]; !ok || m.Size != 14 || m.Hash != "" {
		t.Fatalf("unexpected metadata for doc.txt: %+v", m)
	}

	hashes, err := HashFiles(context.Background(), root, []string{"doc.txt"}, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(hashes["doc.txt"]) != 64 {
		t.Fatalf("expected 64-char sha256 hex, got %q", hashes["doc.txt"])
	}

	qHashes, err := QuickHashFiles(context.Background(), root, []string{"doc.txt"}, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(qHashes["doc.txt"]) == 0 {
		t.Fatalf("expected non-empty quick hash")
	}
}
