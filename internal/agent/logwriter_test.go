package agent

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRotatingFile_RotatesAtMaxBytes(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sub", "agent.log") // sub dir must be created
	w, err := newRotatingFile(path, 64)
	if err != nil {
		t.Fatalf("newRotatingFile: %v", err)
	}
	// Close before TempDir's own cleanup runs (t.Cleanup is LIFO), since
	// Windows cannot remove a file this process still holds open.
	t.Cleanup(func() { _ = w.Close() })
	chunk := []byte("0123456789ABCDEF0123456789ABCDEF\n") // 33 bytes
	for i := 0; i < 3; i++ {                              // 99 bytes total => at least one rotation
		if _, err := w.Write(chunk); err != nil {
			t.Fatalf("write %d: %v", i, err)
		}
	}
	if _, err := os.Stat(path + ".1"); err != nil {
		t.Fatalf("rotated file %s.1 should exist: %v", path, err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("live file: %v", err)
	}
	if info.Size() > 64 {
		t.Fatalf("live file size = %d, want <= maxBytes (64)", info.Size())
	}
}

func TestRotatingFile_UnwritablePathDegradesGracefully(t *testing.T) {
	// A path whose parent is a regular file cannot be created; constructor errors,
	// and callers fall back to stderr-only (tested in Task 4).
	dir := t.TempDir()
	blocker := filepath.Join(dir, "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := newRotatingFile(filepath.Join(blocker, "agent.log"), 64); err == nil {
		t.Fatal("expected error creating a log file under a regular-file parent")
	}
}
