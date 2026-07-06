package provision

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// --- (a) streamLines --------------------------------------------------------
//
// streamLines is the one piece of DefaultRelay's streaming loop that can be
// unit-tested without exec (no python3 dependency, portable to Windows CI
// where python3 isn't guaranteed) — see relay.go's doc comment.

func TestStreamLines(t *testing.T) {
	var got []string
	streamLines(strings.NewReader("line1\nline2\n"), func(s string) { got = append(got, s) })
	if len(got) != 2 || got[0] != "line1" || got[1] != "line2" {
		t.Fatalf("got %v", got)
	}
}

// bufio.Scanner's default ScanLines split strips a trailing '\r', so a
// CRLF-emitting script (e.g. one that ends up running through a Windows
// python3, or a router shell that echoes CRLF) behaves identically to an
// LF-only one.
func TestStreamLines_StripsTrailingCR(t *testing.T) {
	var got []string
	streamLines(strings.NewReader("a\r\nb\n"), func(s string) { got = append(got, s) })
	if len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Fatalf("got %v", got)
	}
}

// A final line with no trailing newline must still reach onLine — relay
// output isn't guaranteed to end with a newline before the process exits.
func TestStreamLines_NoTrailingNewlineStillDeliversLastLine(t *testing.T) {
	var got []string
	streamLines(strings.NewReader("only-line"), func(s string) { got = append(got, s) })
	if len(got) != 1 || got[0] != "only-line" {
		t.Fatalf("got %v", got)
	}
}

func TestStreamLines_EmptyReaderCallsOnLineZeroTimes(t *testing.T) {
	called := 0
	streamLines(strings.NewReader(""), func(string) { called++ })
	if called != 0 {
		t.Fatalf("onLine called %d times for an empty reader, want 0", called)
	}
}

// --- (b) exitCode ------------------------------------------------------------

func TestExitCode_NilErrIsZero(t *testing.T) {
	if got := exitCode(nil); got != 0 {
		t.Fatalf("exitCode(nil) = %d, want 0", got)
	}
}

// A non-ExitError (the process never produced its own exit status: e.g. it
// never started) has no real rc to report — noExitCode, not a fabricated 0
// or 1, so a caller can't mistake it for a genuine process exit code.
func TestExitCode_NonExitErrorIsNoExitCode(t *testing.T) {
	if got := exitCode(errors.New("boom")); got != noExitCode {
		t.Fatalf("exitCode(generic error) = %d, want %d", got, noExitCode)
	}
}

// --- (c) resolveRelayScript --------------------------------------------------
//
// Ported from backend.resolveRelayScript (internal/backend/agent_revive.go)
// for the import-cycle reason documented in relay.go; these two cases mirror
// agent_revive_test.go's TestResolveRelayScriptSelfProvisionsEmbedded /
// TestResolveRelayScriptUsesInstalledOverride byte-for-byte in intent, just
// against this package's own copy.

func TestResolveRelayScriptSelfProvisionsEmbedded(t *testing.T) {
	path, cleanup, err := resolveRelayScript(filepath.Join(t.TempDir(), "absent.py"))
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("temp relay not readable: %v", err)
	}
	if !strings.HasPrefix(string(b), "#!/usr/bin/env python3") {
		t.Fatalf("temp file is not the embedded relay: %.40q", string(b))
	}
	cleanup()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("cleanup should remove the temp relay, stat err=%v", err)
	}
}

func TestResolveRelayScriptUsesInstalledOverride(t *testing.T) {
	override := filepath.Join(t.TempDir(), "awgm-relay.py")
	if err := os.WriteFile(override, []byte("# installed copy\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	path, cleanup, err := resolveRelayScript(override)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	if path != override {
		t.Fatalf("should use the installed override, got %q", path)
	}
}

// --- (d) writeJobFile ---------------------------------------------------------

func TestWriteJobFile_WritesContentAndCleansUp(t *testing.T) {
	path, cleanup, err := writeJobFile([]byte(`{"a":1}`))
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != `{"a":1}` {
		t.Fatalf("job file content = %q, want %q", b, `{"a":1}`)
	}
	cleanup()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("cleanup should remove the job file, stat err=%v", err)
	}
}

// --- (e) DefaultRelay ----------------------------------------------------------
//
// DefaultRelay itself is not exercised end-to-end against a real python3
// process here: python3 is not guaranteed present (this dev box's "python3"
// is a Windows App-Execution-Alias stub that isn't real Python), and even
// where it is, running the actual embedded relay would attempt real network
// I/O — neither is a deterministic, portable unit test. The ctx-cancellation
// contract below IS fully portable: DefaultRelay checks ctx.Err() before
// touching the filesystem or exec at all, so this exercises real production
// code with no dependency on python3/PATH/OS.

func TestDefaultRelay_HonorsAlreadyCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	var got []string
	rc, err := DefaultRelay(ctx, "", []byte(`{}`), func(s string) { got = append(got, s) })

	if err == nil {
		t.Fatal("want a non-nil error for an already-canceled context")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("want context.Canceled, got %v", err)
	}
	if rc != noExitCode {
		t.Fatalf("rc = %d, want %d", rc, noExitCode)
	}
	if len(got) != 0 {
		t.Fatalf("onLine should never be called: %v", got)
	}
}
