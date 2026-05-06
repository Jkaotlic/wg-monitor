package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

type SSH struct {
	client *ssh.Client
	host   string
}

func ConnectSSH(host string, port int, user, password string, kh *KnownHosts) (*SSH, error) {
	addr := fmt.Sprintf("%s:%d", host, port)
	cfg := &ssh.ClientConfig{
		User:            user,
		Auth:            []ssh.AuthMethod{ssh.Password(password)},
		HostKeyCallback: kh.HostKeyCallback,
		Timeout:         10 * time.Second,
	}
	c, err := ssh.Dial("tcp", addr, cfg)
	if err != nil {
		return nil, fmt.Errorf("ssh %s: %w", addr, err)
	}
	return &SSH{client: c, host: addr}, nil
}

func (s *SSH) Close() error {
	return s.client.Close()
}

// Run executes a command, returning stdout, stderr, exit code.
// Does NOT fail on non-zero exit — caller decides.
func (s *SSH) Run(cmd string) (string, string, int, error) {
	sess, err := s.client.NewSession()
	if err != nil {
		return "", "", -1, err
	}
	defer sess.Close()

	var sout, serr safeBuf
	sess.Stdout = &sout
	sess.Stderr = &serr

	err = sess.Run(cmd)
	rc := 0
	if err != nil {
		var ee *ssh.ExitError
		if errors.As(err, &ee) {
			rc = ee.ExitStatus()
			err = nil
		} else {
			rc = -1
		}
	}
	return sout.String(), serr.String(), rc, err
}

// MustRun runs a command and returns an error if rc != 0 or the SSH layer failed.
func (s *SSH) MustRun(cmd string) (string, error) {
	out, errS, rc, err := s.Run(cmd)
	if err != nil {
		return out, fmt.Errorf("ssh transport: %w", err)
	}
	if rc != 0 {
		return out, fmt.Errorf("cmd %q exit %d: %s", cmd, rc, errS)
	}
	return out, nil
}

// UploadStdin streams raw bytes via `cat > path` (works on dropbear without SFTP).
func (s *SSH) UploadStdin(remotePath string, data []byte) error {
	sess, err := s.client.NewSession()
	if err != nil {
		return err
	}
	defer sess.Close()

	stdin, err := sess.StdinPipe()
	if err != nil {
		return err
	}
	if err := sess.Start(fmt.Sprintf("cat > %s", remotePath)); err != nil {
		return err
	}
	view := data
	for len(view) > 0 {
		chunk := view
		if len(chunk) > 32768 {
			chunk = chunk[:32768]
		}
		if _, err := stdin.Write(chunk); err != nil {
			return err
		}
		view = view[len(chunk):]
	}
	stdin.Close()
	return sess.Wait()
}

// UploadSFTP uploads via SFTP. Use on systems with full openssh (e.g. VPS).
// Falls back to UploadStdin on dropbear (caller decides which to use).
func (s *SSH) UploadSFTP(remotePath string, data []byte) error {
	// Use scp-equivalent via ssh exec to avoid pulling github.com/pkg/sftp.
	// Pattern: open session, send `dd of=PATH bs=1M`, write to stdin.
	sess, err := s.client.NewSession()
	if err != nil {
		return err
	}
	defer sess.Close()

	stdin, err := sess.StdinPipe()
	if err != nil {
		return err
	}
	if err := sess.Start(fmt.Sprintf("dd of=%s bs=1M status=none", remotePath)); err != nil {
		return err
	}
	if _, err := io.Copy(stdin, bytesReader(data)); err != nil {
		stdin.Close()
		return err
	}
	stdin.Close()
	return sess.Wait()
}

// safeBuf is a thread-safe bytes.Buffer (ssh stdout/stderr writes can race).
type safeBuf struct {
	mu sync.Mutex
	b  []byte
}

func (s *safeBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.b = append(s.b, p...)
	return len(p), nil
}
func (s *safeBuf) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return string(s.b)
}

func bytesReader(b []byte) io.Reader {
	return &byteReader{b: b}
}

type byteReader struct {
	b   []byte
	pos int
}

func (r *byteReader) Read(p []byte) (int, error) {
	if r.pos >= len(r.b) {
		return 0, io.EOF
	}
	n := copy(p, r.b[r.pos:])
	r.pos += n
	return n, nil
}

// ----- Known hosts (TOFU) -----

type KnownHosts struct {
	path string
	mu   sync.Mutex
}

func NewKnownHosts(path string) (*KnownHosts, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY, 0o600)
		if err != nil {
			return nil, err
		}
		f.Close()
	}
	return &KnownHosts{path: path}, nil
}

// fakeAddr wraps a string as a net.Addr so we can pass a nil-safe address to knownhosts callbacks.
type fakeAddr string

func (a fakeAddr) Network() string { return "tcp" }
func (a fakeAddr) String() string  { return string(a) }

// HostKeyCallback implements ssh.HostKeyCallback with TOFU semantics:
//   - first connect to a host: append fingerprint to known_hosts, accept
//   - subsequent connects: must match
//   - mismatch: return error with clear message
func (k *KnownHosts) HostKeyCallback(hostname string, remote net.Addr, key ssh.PublicKey) error {
	k.mu.Lock()
	defer k.mu.Unlock()

	// knownhosts callbacks dereference remote.String(); provide a fallback when nil.
	if remote == nil {
		remote = fakeAddr(hostname)
	}

	cb, err := knownhosts.New(k.path)
	if err != nil {
		return err
	}
	err = cb(hostname, remote, key)
	if err == nil {
		return nil
	}
	var keyErr *knownhosts.KeyError
	if errors.As(err, &keyErr) {
		if len(keyErr.Want) == 0 {
			// First time seeing this host — append.
			line := knownhosts.Line([]string{hostname}, key)
			f, ferr := os.OpenFile(k.path, os.O_APPEND|os.O_WRONLY, 0o600)
			if ferr != nil {
				return ferr
			}
			defer f.Close()
			if _, werr := f.WriteString(line + "\n"); werr != nil {
				return werr
			}
			return nil
		}
		return fmt.Errorf("HOST KEY CHANGED for %s — possible MITM attack. "+
			"Inspect %s and remove the offending line if you trust the new key",
			hostname, k.path)
	}
	return err
}

// generateEd25519Signer is exported for tests.
func generateEd25519Signer() (ssh.Signer, error) {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	return ssh.NewSignerFromKey(priv)
}
