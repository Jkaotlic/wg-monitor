package main

import (
	"bufio"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

type SSH struct {
	client *ssh.Client
	host   string
}

// ConnectSSH dials host:port. alias is used as the known_hosts lookup key
// instead of the real address — pass the agent's nickname (or "backend",
// "vps", etc.) so two different routers reachable through the SAME LAN IP
// (e.g. 192.168.31.1 over rotating SSTP tunnels) don't collide. Empty alias
// falls back to host:port for backward compatibility.
func ConnectSSH(host string, port int, user, password string, kh *KnownHosts, alias string) (*SSH, error) {
	addr := fmt.Sprintf("%s:%d", host, port)
	hkcb := kh.HostKeyCallback
	if alias != "" {
		hkcb = kh.HostKeyCallbackFor(alias)
	}
	cfg := &ssh.ClientConfig{
		User:            user,
		Auth:            []ssh.AuthMethod{ssh.Password(password)},
		HostKeyCallback: hkcb,
		Timeout:         10 * time.Second,
	}
	c, err := ssh.Dial("tcp", addr, cfg)
	if err != nil {
		return nil, diagnoseSSHErr(addr, err)
	}
	return &SSH{client: c, host: addr}, nil
}

// diagnoseSSHErr wraps a transport-level SSH error with a Russian operator
// hint matched against well-known failure substrings. The original err is
// preserved via %w so callers can still errors.As / errors.Is on it.
//
// HOST KEY CHANGED messages from our own KnownHosts already carry an
// actionable explanation, so we pass them through verbatim (still wrapped
// for the addr prefix) without adding a redundant hint.
func diagnoseSSHErr(addr string, err error) error {
	if err == nil {
		return nil
	}
	msg := err.Error()
	lower := strings.ToLower(msg)

	// Our own MITM message is self-explanatory — don't double-annotate.
	if strings.Contains(msg, "HOST KEY CHANGED") {
		return fmt.Errorf("ssh %s: %w", addr, err)
	}

	var hint string
	switch {
	case strings.Contains(lower, "unable to authenticate"):
		hint = "пароль не подошёл — проверь WG_VPS_PASS / WG_KEENETIC_PASS_<NICK> или disk-кэш"
	case strings.Contains(lower, "no route to host"), strings.Contains(lower, "connect: network"):
		hint = "хост недоступен — SSTP/VPN не подключён? проверь host:port в wizard.toml"
	case strings.Contains(lower, "i/o timeout"), strings.Contains(lower, "deadline exceeded"):
		hint = "таймаут SSH-handshake — медленный канал или роутер занят, попробуй ещё раз"
	case strings.Contains(lower, "connection refused"):
		hint = "порт закрыт — на роутере dropbear/sshd не слушает <port> или firewall"
	}

	if hint == "" {
		return fmt.Errorf("ssh %s: %w", addr, err)
	}
	return fmt.Errorf("ssh %s: %s: %w", addr, hint, err)
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
	prog := newProgressDots(len(data), os.Stderr)
	view := data
	for len(view) > 0 {
		chunk := view
		if len(chunk) > 32768 {
			chunk = chunk[:32768]
		}
		if _, err := stdin.Write(chunk); err != nil {
			prog.finish()
			return err
		}
		prog.add(len(chunk))
		view = view[len(chunk):]
	}
	prog.finish()
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
	prog := newProgressDots(len(data), os.Stderr)
	// Tee writes through the progress counter so dots land on stderr only,
	// never into the SSH stdin stream.
	w := io.MultiWriter(stdin, prog)
	if _, err := io.Copy(w, bytesReader(data)); err != nil {
		prog.finish()
		stdin.Close()
		return err
	}
	prog.finish()
	stdin.Close()
	return sess.Wait()
}

// progressDots prints one '.' to its writer per 1 MiB consumed, and a
// trailing '\n' on finish. It is silent for payloads under 256 KiB to
// avoid noise on small config uploads. The writer is intended to be
// stderr — never wire it into the SSH stdin pipe.
type progressDots struct {
	w        io.Writer
	total    int
	step     int
	written  int
	emitted  int
	enabled  bool
	finished bool
}

const (
	progressDotStep      = 1 << 20 // 1 MiB
	progressMinThreshold = 256 << 10
)

func newProgressDots(total int, w io.Writer) *progressDots {
	return &progressDots{
		w:       w,
		total:   total,
		step:    progressDotStep,
		enabled: total >= progressMinThreshold && w != nil,
	}
}

// add records n more bytes consumed and prints any newly-crossed dot
// boundaries. Safe to call with n == 0.
func (p *progressDots) add(n int) {
	if !p.enabled || n <= 0 {
		return
	}
	p.written += n
	target := p.written / p.step
	for p.emitted < target {
		_, _ = p.w.Write([]byte{'.'})
		p.emitted++
	}
}

// Write satisfies io.Writer so progressDots can be plugged into io.MultiWriter.
// It only counts bytes; it never echoes payload data.
func (p *progressDots) Write(b []byte) (int, error) {
	p.add(len(b))
	return len(b), nil
}

// finish prints the trailing newline (once) so subsequent log lines start
// on a fresh line. Safe to call multiple times.
func (p *progressDots) finish() {
	if !p.enabled || p.finished {
		return
	}
	p.finished = true
	if p.emitted > 0 {
		_, _ = p.w.Write([]byte{'\n'})
	}
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

// HostKeyCallback implements ssh.HostKeyCallback with TOFU semantics keyed
// by the real hostname:port. Suitable for stable endpoints (a public VPS,
// a static LAN IP) where the address itself uniquely identifies the host.
//
// For LAN IPs that may resolve to different physical hosts depending on
// which VPN is active (e.g. several Keenetic routers all on 192.168.31.1
// reachable through rotating SSTP tunnels), use HostKeyCallbackFor with a
// stable alias (the agent nickname).
func (k *KnownHosts) HostKeyCallback(hostname string, remote net.Addr, key ssh.PublicKey) error {
	return k.checkOrAppend(hostname, remote, key)
}

// HostKeyCallbackFor returns an ssh.HostKeyCallback that ignores the dialed
// hostname and looks up / records the fingerprint under the supplied alias
// instead. This decouples the TOFU record from the network address so
// reused IPs across different physical machines do not collide.
func (k *KnownHosts) HostKeyCallbackFor(alias string) ssh.HostKeyCallback {
	return func(_ string, remote net.Addr, key ssh.PublicKey) error {
		return k.checkOrAppend(alias, remote, key)
	}
}

// checkOrAppend is the shared TOFU body:
//   - first sighting under `key` → append fingerprint, accept
//   - subsequent sightings → must match
//   - mismatch → return error with the file path so the operator can inspect
func (k *KnownHosts) checkOrAppend(hostKey string, remote net.Addr, pub ssh.PublicKey) error {
	k.mu.Lock()
	defer k.mu.Unlock()

	// knownhosts.New uses net.SplitHostPort on the lookup key, which fails
	// for bare aliases without a port (e.g. "router_alice"). Normalise to a
	// dummy ":22" — this is purely an internal lookup token; it never
	// touches the network. Real "host:port" keys pass through unchanged.
	hostKey = normaliseHostKey(hostKey)

	// knownhosts callbacks dereference remote.String(); provide a fallback when nil.
	if remote == nil {
		remote = fakeAddr(hostKey)
	}

	cb, err := knownhosts.New(k.path)
	if err != nil {
		return err
	}
	err = cb(hostKey, remote, pub)
	if err == nil {
		return nil
	}
	var keyErr *knownhosts.KeyError
	if errors.As(err, &keyErr) {
		if len(keyErr.Want) == 0 {
			// First time seeing this alias. Cross-check fingerprint against
			// every other entry in known_hosts BEFORE appending: if this
			// fingerprint is already saved under a different alias we are
			// almost certainly talking to a router that was added before —
			// classic "forgot to switch on VPN, cabled into the same LAN
			// router on 192.168.31.1" scenario. Refusing here prevents the
			// caller (install-agent / add-router) from clobbering /opt/bin
			// /wg-monitor on a host that already has a different agent.
			if other := k.aliasForFingerprint(pub); other != "" && other != hostKey {
				return fmt.Errorf(
					"SSH host key fingerprint совпадает с уже сохранённым под alias %q. "+
						"Скорее всего ты цепляешься к ФИЗИЧЕСКИ ТОМУ ЖЕ роутеру под новым nickname'ом "+
						"— проверь, поднят ли VPN/SSTP. Если правда хочешь добавить тот же роутер "+
						"под двумя именами, удали строку для %q из %s.",
					other, other, k.path)
			}
			line := knownhosts.Line([]string{hostKey}, pub)
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
			hostKey, k.path)
	}
	return err
}

// aliasForFingerprint scans known_hosts for an entry whose public key has
// the same SHA256 fingerprint as `pub` and returns the alias (first host
// field) of that entry. Returns "" if nothing matches or the file can't be
// read. Used by checkOrAppend to detect alias collisions on the same
// physical SSH endpoint (typically: forgotten VPN / multiple nicknames
// resolving to one box on 192.168.31.1).
func (k *KnownHosts) aliasForFingerprint(pub ssh.PublicKey) string {
	f, err := os.Open(k.path)
	if err != nil {
		return ""
	}
	defer f.Close()
	target := ssh.FingerprintSHA256(pub)
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		// Each known_hosts line: "<hosts> <keytype> <base64-key> [comment]".
		// Reconstruct authorized_keys form ("<keytype> <base64>") and parse.
		fields := strings.Fields(line)
		if len(fields) < 3 {
			continue
		}
		host, keytype, b64 := fields[0], fields[1], fields[2]
		pk, _, _, _, perr := ssh.ParseAuthorizedKey([]byte(keytype + " " + b64))
		if perr != nil {
			continue
		}
		if ssh.FingerprintSHA256(pk) == target {
			return host
		}
	}
	return ""
}

func normaliseHostKey(s string) string {
	if strings.Contains(s, ":") {
		return s
	}
	return s + ":22"
}

// generateEd25519Signer is exported for tests.
func generateEd25519Signer() (ssh.Signer, error) {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	return ssh.NewSignerFromKey(priv)
}
