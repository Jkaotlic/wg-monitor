package main

import (
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

// awgmPinStore is a Trust-On-First-Use fingerprint store for AWG Manager TLS
// certificates, modeled on SSH known_hosts. It is used only on the
// AWGM_INSECURE_TLS path: rather than trusting any presented certificate, the
// leaf cert's SHA-256 is pinned on first contact and enforced on every later
// connection. A self-signed KeenDNS cert keeps working, but a man-in-the-middle
// presenting a different cert is rejected (SEC-02).
type awgmPinStore struct {
	path string
	mu   sync.Mutex
}

func awgmPinStorePath() string {
	return filepath.Join(defaultCacheDir(), "awgm_pins")
}

func newAWGMPinStore() *awgmPinStore {
	return &awgmPinStore{path: awgmPinStorePath()}
}

func (s *awgmPinStore) load() (map[string]string, error) {
	b, err := os.ReadFile(s.path)
	if os.IsNotExist(err) {
		return map[string]string{}, nil
	}
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		host, fp, ok := strings.Cut(line, " ")
		if !ok {
			continue
		}
		out[strings.ToLower(strings.TrimSpace(host))] = strings.TrimSpace(fp)
	}
	return out, nil
}

func (s *awgmPinStore) save(pins map[string]string) error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	hosts := make([]string, 0, len(pins))
	for h := range pins {
		hosts = append(hosts, h)
	}
	sort.Strings(hosts)
	var b strings.Builder
	b.WriteString("# AWG Manager TLS cert pins (TOFU): <host> sha256:<hex>\n")
	for _, h := range hosts {
		b.WriteString(h + " " + pins[h] + "\n")
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, []byte(b.String()), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

// verify implements TOFU: accept and record the fingerprint on first sight,
// enforce an exact match on every subsequent connection.
func (s *awgmPinStore) verify(host string, certDER []byte) error {
	host = strings.ToLower(strings.TrimSpace(host))
	if host == "" {
		return fmt.Errorf("awgm pin: empty host")
	}
	fp := awgmCertFingerprint(certDER)
	s.mu.Lock()
	defer s.mu.Unlock()
	pins, err := s.load()
	if err != nil {
		return fmt.Errorf("awgm pin store: %w", err)
	}
	pinned, ok := pins[host]
	if !ok {
		pins[host] = fp
		if err := s.save(pins); err != nil {
			return fmt.Errorf("awgm pin store save: %w", err)
		}
		PrintWarn(fmt.Sprintf("AWG Manager TLS pin recorded (TOFU) for %s: %s — confirm it matches your router on first use.", host, fp))
		return nil
	}
	if pinned != fp {
		return fmt.Errorf("AWG Manager TLS cert for %s changed: pinned %s, got %s — possible MITM. If you rotated the cert intentionally, remove the line from %s and retry", host, pinned, fp, s.path)
	}
	return nil
}

func awgmCertFingerprint(der []byte) string {
	sum := sha256.Sum256(der)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// awgmHost extracts the lowercase hostname used as the pin key.
func awgmHost(rawURL string) string {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || u.Hostname() == "" {
		return strings.ToLower(strings.TrimSpace(rawURL))
	}
	return strings.ToLower(u.Hostname())
}

// awgmInsecureTLSConfig builds the TLS config for the AWGM_INSECURE_TLS path:
// chain verification is skipped (self-signed routers), but VerifyConnection
// pins the leaf certificate via TOFU so a MITM is still caught.
func awgmInsecureTLSConfig(host string) *tls.Config {
	store := newAWGMPinStore()
	return &tls.Config{
		InsecureSkipVerify: true, // #nosec G402 -- TOFU leaf-cert pinning below replaces chain verification for self-signed AWG Manager.
		VerifyConnection: func(cs tls.ConnectionState) error {
			if len(cs.PeerCertificates) == 0 {
				return fmt.Errorf("awgm tls: no peer certificate presented by %s", host)
			}
			return store.verify(host, cs.PeerCertificates[0].Raw)
		},
	}
}
