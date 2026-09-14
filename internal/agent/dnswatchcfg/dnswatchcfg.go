// Package dnswatchcfg holds what both the agent's config loader, the
// update_agent_config action and the backend's dashboard mirror must agree on
// about the DNS watchdog block. It is a leaf: dnswatch imports actions, so the
// shared rules cannot live in either of them.
package dnswatchcfg

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"net/url"
	"os"
	"strings"
)

// EndpointMax bounds the DoH endpoint URL.
const EndpointMax = 512

var errEndpointShape = fmt.Errorf("must be an https:// URL of at most %d characters", EndpointMax)

// ValidateEndpoint accepts an absolute https URL with a host name, at most
// EndpointMax bytes. The caller trims. The masked "https://<host>/***" a view
// hands out is refused: echoing it back would overwrite the secret path. The
// error never repeats the value — the path is a credential.
func ValidateEndpoint(s string) error {
	if s == "" || len(s) > EndpointMax {
		return errEndpointShape
	}
	if strings.Contains(s, "***") {
		return errors.New("is the masked value from agent_config_get, not the real endpoint")
	}
	u, err := url.Parse(s)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.Hostname() == "" {
		return errEndpointShape
	}
	return nil
}

// ValidateBootstrapIP: empty (ask 77.88.8.8) or an IPv4 address — the
// router's dns-proxy bootstrap takes no IPv6 here.
func ValidateBootstrapIP(s string) error {
	if s == "" {
		return nil
	}
	if a, err := netip.ParseAddr(s); err != nil || !a.Is4() {
		return errors.New("must be an IPv4 address")
	}
	return nil
}

// ValidateCanary: empty (the default) or a plain ASCII domain of at least two
// labels. A single-label name never resolves on a public resolver: the
// watchdog would take the own resolver for dead forever.
func ValidateCanary(s string) error {
	bad := errors.New("must be a plain domain name with a dot (e.g. example.com)")
	if s == "" {
		return nil
	}
	if len(s) > 253 || !strings.Contains(s, ".") {
		return bad
	}
	for _, label := range strings.Split(s, ".") {
		if label == "" || len(label) > 63 || strings.HasPrefix(label, "-") || strings.HasSuffix(label, "-") {
			return bad
		}
		for _, r := range label {
			if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-') {
				return bad
			}
		}
	}
	return nil
}

// Why the watchdog holds the router (Hold).
const (
	HoldFallback = "fallback" // the router runs on the fallback resolvers
	HoldPending  = "pending"  // a switch is not proven on the router yet
	HoldCleanup  = "cleanup"  // lines the watchdog put or removed are not settled
)

// holdRecord is the part of dns-watchdog-state.json (dnswatch.persisted) that
// says whether the watchdog left the router in a state only it can undo. The
// tags must match dnswatch.persisted: hold_contract_test.go in dnswatch
// guards it.
type holdRecord struct {
	Mode     string   `json:"mode"`
	Pending  string   `json:"pending"`
	Leftover []string `json:"leftover"`
	Missing  []string `json:"missing"`
}

// Hold reads the watchdog's record and returns why switching the watchdog off
// (or moving its endpoint) would strand the router, or "" when it is safe. No
// file or an unparsable one is "" — the watchdog itself ignores such a record.
// Any other read error is returned: when in doubt, refuse.
func Hold(statePath string) (string, error) {
	if statePath == "" {
		return "", nil
	}
	body, err := os.ReadFile(statePath)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	var r holdRecord
	if json.Unmarshal(body, &r) != nil {
		return "", nil
	}
	switch {
	case r.Mode == "fallback":
		return HoldFallback, nil
	case r.Pending != "":
		return HoldPending, nil
	case len(r.Leftover)+len(r.Missing) > 0:
		return HoldCleanup, nil
	}
	return "", nil
}
