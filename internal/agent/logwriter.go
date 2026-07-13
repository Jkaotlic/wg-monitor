package agent

import (
	"os"
	"path/filepath"
	"sync"
)

// defaultAgentLogFile is where the agent writes its log on Entware routers,
// where the S99 init sends stderr to /dev/null. Baked in (not required in
// config) so already-deployed agents gain a persisted log on the next binary
// update with no config.yaml rewrite.
const defaultAgentLogFile = "/opt/var/wg-monitor/agent.log"

// defaultLogMaxBytes caps the live log file before rotation (~1 MiB).
const defaultLogMaxBytes int64 = 1 << 20

// rotatingFile is a minimal size-rotating io.Writer. At maxBytes it renames the
// live file to <path>.1 (replacing any previous .1) and reopens an empty live
// file. Concurrency-safe: slog may write from multiple goroutines. Best-effort
// on error — a rotation or write failure never panics and never crashes the
// agent (logging must never take the process down).
type rotatingFile struct {
	mu       sync.Mutex
	path     string
	maxBytes int64
	f        *os.File
	size     int64
}

func newRotatingFile(path string, maxBytes int64) (*rotatingFile, error) {
	if maxBytes <= 0 {
		maxBytes = defaultLogMaxBytes
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, err
	}
	info, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	return &rotatingFile{path: path, maxBytes: maxBytes, f: f, size: info.Size()}, nil
}

func (w *rotatingFile) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.f == nil {
		return len(p), nil // degraded after a failed rotation: drop silently
	}
	if w.size+int64(len(p)) > w.maxBytes {
		w.rotate()
		if w.f == nil {
			return len(p), nil
		}
	}
	n, err := w.f.Write(p)
	w.size += int64(n)
	return n, err
}

// Close closes the underlying file. It is not part of the brief's required
// interface, but is a minimal, harmless addition: tests (and, later, graceful
// shutdown) need a way to release the handle deterministically — on Windows a
// file cannot be removed while a process still holds it open, unlike POSIX.
func (w *rotatingFile) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.f == nil {
		return nil
	}
	err := w.f.Close()
	w.f = nil
	return err
}

// rotate closes the live file, moves it to <path>.1 (removing any prior .1 so
// os.Rename works on Windows too), and reopens an empty live file. On any
// failure it leaves w.f nil; Write then drops silently until the next process
// start reopens the file.
func (w *rotatingFile) rotate() {
	_ = w.f.Close()
	_ = os.Remove(w.path + ".1")
	_ = os.Rename(w.path, w.path+".1")
	f, err := os.OpenFile(w.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		w.f = nil
		return
	}
	w.f = f
	w.size = 0
}
