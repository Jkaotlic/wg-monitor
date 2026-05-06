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

var Version = "0.5.0-awgmgr-pivot"

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
		kind := fs.String("kind", db.KindStatic, "router kind: static (home/office, default) or mobile (4G in-vehicle)")
		_ = fs.Parse(os.Args[2:])
		if err := runAddUser(addUserOpts{
			DBPath: *dbPath, Nickname: *nick, AWGIface: *iface,
			ExpectedExitIP: *exitIP, BackendURL: *backendURL, Kind: *kind, Out: os.Stdout,
		}); err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
	case "show-discovered-dns":
		cmdShowDiscoveredDNS(os.Args[2:])
	case "init-menu":
		cmdInitMenu(os.Args[2:])
	case "list-users":
		cmdListUsers(os.Args[2:])
	case "set-topic":
		cmdSetTopic(os.Args[2:])
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
  wg-monitor-cli add-user --nickname=NAME --awg-iface=IFACE --expected-exit-ip=IP [--kind static|mobile] [--db PATH] [--backend-url URL]
  wg-monitor-cli list-users [--db PATH]
  wg-monitor-cli show-discovered-dns [--awg-manager-url URL] [--ndmc PATH]
  wg-monitor-cli set-topic --kind=summary|systemic --thread-id=N [--db PATH]
  wg-monitor-cli version
`
}

type addUserOpts struct {
	DBPath         string
	Nickname       string
	AWGIface       string
	ExpectedExitIP string
	BackendURL     string
	Kind           string
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
	if o.Kind == "" {
		o.Kind = db.KindStatic
	}
	if !db.IsValidKind(o.Kind) {
		return fmt.Errorf("--kind=%q must be %q or %q", o.Kind, db.KindStatic, db.KindMobile)
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
	id, err := d.Users().InsertWithKind(o.Nickname, rawToken, o.ExpectedExitIP, o.AWGIface, o.Kind)
	if err != nil {
		return fmt.Errorf("insert user: %w", err)
	}
	fmt.Fprintf(o.Out, "User created: id=%d nickname=%s kind=%s awg_iface=%s expected_exit_ip=%s\n",
		id, o.Nickname, o.Kind, o.AWGIface, o.ExpectedExitIP)
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
    expected_exit_ip: %s
    handshake_max_age_sec: 180
    # marker_url defaults to http://www.gstatic.com/generate_204 — uncomment to override
    # marker_url: http://www.gstatic.com/generate_204
    # routing_probe_url defaults to https://1.1.1.1/cdn-cgi/trace — uncomment to override
    # routing_probe_url: https://1.1.1.1/cdn-cgi/trace
  dns:
    auto_discover: true        # discover endpoints from /bin/ndmc on Keenetic
    test_domain: example.com
    fail_threshold: 1
    # Optional manual endpoints in addition to auto-discovered ones:
    # endpoints:
    #   - { type: doh, url: "https://dns.example/dns-query" }
    #   - { type: plain, host: 1.1.1.1, port: 53, ndms_name: Wireguard0 }
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
