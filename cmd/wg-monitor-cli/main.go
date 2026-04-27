// cmd/wg-monitor-cli/main.go
package main

import (
	"crypto/rand"
	"encoding/hex"
	"flag"
	"fmt"
	"io"
	"os"
	"regexp"

	"github.com/anex/wg-monitor/internal/backend/db"
)

var Version = "0.2.0-stage1-dev"

var nicknameRegexp = regexp.MustCompile(`^[a-z][a-z0-9_-]{1,15}$`)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, usage())
		os.Exit(2)
	}
	cmd := os.Args[1]
	switch cmd {
	case "add-user":
		fs := flag.NewFlagSet("add-user", flag.ExitOnError)
		dbPath := fs.String("db", "/var/lib/wg-monitor/state.db", "path to SQLite DB")
		nick := fs.String("nickname", "", "user nickname (regexp ^[a-z][a-z0-9_-]{1,15}$)")
		iface := fs.String("awg-iface", "", "AWG interface name on the router (per-user, see spec Q4)")
		exitIP := fs.String("expected-exit-ip", "", "expected exit IPv4 when probing through the tunnel")
		backendURL := fs.String("backend-url", "https://wgmonitor.jkaotlic.duckdns.org", "backend HTTPS URL printed in install hint")
		_ = fs.Parse(os.Args[2:])
		if err := runAddUser(addUserOpts{
			DBPath: *dbPath, Nickname: *nick, AWGIface: *iface,
			ExpectedExitIP: *exitIP, BackendURL: *backendURL, Out: os.Stdout,
		}); err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
	case "show-discovered-dns":
		cmdShowDiscoveredDNS(os.Args[2:])
	case "version":
		fmt.Println(Version)
	default:
		fmt.Fprintln(os.Stderr, usage())
		os.Exit(2)
	}
}

func usage() string {
	return `wg-monitor-cli — onboarding CLI

Usage:
  wg-monitor-cli add-user --nickname=NAME --awg-iface=IFACE --expected-exit-ip=IP [--db PATH] [--backend-url URL]
  wg-monitor-cli show-discovered-dns [--awg-manager-url URL] [--ndmc PATH]
  wg-monitor-cli version
`
}

type addUserOpts struct {
	DBPath         string
	Nickname       string
	AWGIface       string
	ExpectedExitIP string
	BackendURL     string
	Out            io.Writer
}

func runAddUser(o addUserOpts) error {
	if !nicknameRegexp.MatchString(o.Nickname) {
		return fmt.Errorf("nickname %q must match %s", o.Nickname, nicknameRegexp)
	}
	if o.AWGIface == "" {
		return fmt.Errorf("--awg-iface is required (no default — per-user)")
	}
	if o.ExpectedExitIP == "" {
		return fmt.Errorf("--expected-exit-ip is required (no default — per-user)")
	}
	d, err := db.Open(o.DBPath)
	if err != nil {
		return fmt.Errorf("open db %s: %w", o.DBPath, err)
	}
	defer d.Close()

	rawToken, err := generateToken()
	if err != nil {
		return err
	}
	id, err := d.Users().Insert(o.Nickname, rawToken, o.ExpectedExitIP, o.AWGIface)
	if err != nil {
		return fmt.Errorf("insert user: %w", err)
	}
	fmt.Fprintf(o.Out, "User created: id=%d nickname=%s awg_iface=%s expected_exit_ip=%s\n",
		id, o.Nickname, o.AWGIface, o.ExpectedExitIP)
	fmt.Fprintf(o.Out, "Token (raw, save now — only shown once): %s\n\n", rawToken)
	fmt.Fprintf(o.Out, "Place this in /opt/etc/wg-monitor/config.yaml on the router (chmod 600):\n\n")
	fmt.Fprintf(o.Out, `backend:
  url: %s
  token: %s

agent:
  nickname: %s
  interval_sec: 60

checks:
  awg:
    interface: %s
    handshake_max_age_sec: 180
    expected_exit_ip: %s
    marker_url: https://www.youtube.com/-/manifest
  dns:
    test_domain: example.com
    fail_threshold: 2
    providers:
      - { name: cloudflare, host: 1.1.1.1 }
      - { name: google,     host: 8.8.8.8 }
      - { name: quad9,      host: 9.9.9.9 }
`, o.BackendURL, rawToken, o.Nickname, o.AWGIface, o.ExpectedExitIP)
	fmt.Fprintf(o.Out, "\nThe Telegram topic for this user will be created automatically on the first HARD alert.\n")
	return nil
}

func generateToken() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("rand: %w", err)
	}
	return hex.EncodeToString(buf), nil
}
