package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sort"
	"strings"
	"sync"
	"time"
)

// PathKind classifies a network interface for the purpose of routing
// decisions. We split because operator intent differs: a P2P interface
// (SSTP/PPP/OpenVPN/WG) being UP usually means "this is the path I want
// to deploy through"; a LAN interface being UP means "my normal default
// route". Layer-1 auto-pick prefers P2P on ambiguity.
type PathKind int

const (
	PathLAN PathKind = iota
	PathP2P
)

func (k PathKind) String() string {
	switch k {
	case PathLAN:
		return "LAN"
	case PathP2P:
		return "P2P"
	default:
		return "?"
	}
}

// PathCandidate captures one probe attempt: which iface, what came back.
// Err == nil → reachable; Err != nil → failed (Latency is meaningless then).
type PathCandidate struct {
	Iface   string        // net.Interface.Name
	Index   int           // OS iface index — passed to addTempHostRoute
	LocalIP string        // address bound on the iface, "" if multiple
	Kind    PathKind
	Latency time.Duration // TCP handshake latency
	Err     error
}

// Responded is sugar — "did this probe succeed?". Used by Decide and tests.
func (c PathCandidate) Responded() bool { return c.Err == nil }

// PathReport aggregates the result of probing target via every UP interface.
// Decide() must be called before consumers read Chosen / Multiple.
type PathReport struct {
	Target     string
	Candidates []PathCandidate
	Chosen     *PathCandidate
	Multiple   bool
}

// Decide sets Chosen + Multiple based on Candidates. Rules:
//   - no responders → Chosen=nil, Multiple=false
//   - exactly one responder → Chosen=that one
//   - >1 responders → Multiple=true, default-pick = first P2P responder,
//     fallback to first responder of any kind. Operator may override
//     interactively in the caller.
func (r *PathReport) Decide() {
	var firstAny, firstP2P *PathCandidate
	respCount := 0
	for i := range r.Candidates {
		c := &r.Candidates[i]
		if !c.Responded() {
			continue
		}
		respCount++
		if firstAny == nil {
			firstAny = c
		}
		if c.Kind == PathP2P && firstP2P == nil {
			firstP2P = c
		}
	}
	if respCount == 0 {
		r.Chosen = nil
		r.Multiple = false
		return
	}
	if respCount == 1 {
		r.Chosen = firstAny
		r.Multiple = false
		return
	}
	r.Multiple = true
	if firstP2P != nil {
		r.Chosen = firstP2P
	} else {
		r.Chosen = firstAny
	}
}

// RouteToken is the opaque handle returned by AddRoute. Holds enough info
// for DelRoute to undo the exact add even if the same target was probed
// via several interfaces in the same session.
type RouteToken struct {
	TargetIP string
	IfIndex  int
}

// Prober abstracts the OS-side primitives Layer-1 needs. Production wires
// the real `net.Interfaces` + `route` CLI; tests inject a fake.
type Prober interface {
	Interfaces() ([]net.Interface, error)
	// AddRoute installs a temporary /32 host route for ip via the interface
	// at ifIdx. May return an error on permission denied or syntax issues —
	// caller treats this as "can't force, fall through to default route".
	AddRoute(ip string, ifIdx int) (RouteToken, error)
	// DelRoute removes a previously-added route. Best-effort: callers ignore
	// the error (we just print a warning and move on).
	DelRoute(RouteToken) error
	// Dial performs a TCP handshake to target with timeout. On success
	// returns the local IP the OS bound; on failure returns the error
	// verbatim so callers can surface "connection refused" vs "i/o timeout"
	// to the operator.
	Dial(target string, timeout time.Duration) (localIP string, err error)
}

// realProber is the production implementation.
type realProber struct{}

func (*realProber) Interfaces() ([]net.Interface, error) { return net.Interfaces() }

func (*realProber) AddRoute(ip string, ifIdx int) (RouteToken, error) {
	return addTempHostRoute(ip, ifIdx)
}

func (*realProber) DelRoute(tok RouteToken) error { return delTempHostRoute(tok) }

func (*realProber) Dial(target string, timeout time.Duration) (string, error) {
	d := net.Dialer{Timeout: timeout}
	conn, err := d.DialContext(context.Background(), "tcp", target)
	if err != nil {
		return "", err
	}
	defer conn.Close()
	host, _, _ := net.SplitHostPort(conn.LocalAddr().String())
	return host, nil
}

// NewRealProber returns a Prober wired to the actual OS — used by deploy
// actions in production. Tests construct their own implementation.
func NewRealProber() Prober { return &realProber{} }

// classifyIface returns PathP2P when iface has FlagPointToPoint set
// (SSTP / WG / OpenVPN / PPP), else PathLAN. Interfaces flagged neither
// UP nor loopback are filtered by callers — this function only classifies.
func classifyIface(iface net.Interface) PathKind {
	if iface.Flags&net.FlagPointToPoint != 0 {
		return PathP2P
	}
	return PathLAN
}

// firstIPv4 returns the first IPv4 address configured on iface as a plain
// dotted-quad, or "" if iface has none. Used in candidate display so the
// operator can spot which physical NIC is which.
func firstIPv4(iface net.Interface) string {
	addrs, err := iface.Addrs()
	if err != nil {
		return ""
	}
	for _, a := range addrs {
		ipnet, ok := a.(*net.IPNet)
		if !ok {
			continue
		}
		if v4 := ipnet.IP.To4(); v4 != nil {
			return v4.String()
		}
	}
	return ""
}

// stepFindReachablePath probes target via each UP interface in parallel
// and returns what answered. Cleanup func removes every temporary /32
// route Layer-1 installed during probing — caller MUST defer it. Returns
// error only on fatal enumeration failure; an empty Chosen is signalled
// via PathReport, not error.
func stepFindReachablePath(p Prober, target string, totalTimeout time.Duration) (*PathReport, func(), error) {
	ip, _, err := net.SplitHostPort(target)
	if err != nil {
		return nil, func() {}, fmt.Errorf("target %q: %w", target, err)
	}
	if net.ParseIP(ip) == nil {
		return nil, func() {}, fmt.Errorf("target host %q is not an IPv4 literal", ip)
	}

	ifaces, err := p.Interfaces()
	if err != nil {
		return nil, func() {}, fmt.Errorf("enumerate interfaces: %w", err)
	}

	type probe struct {
		iface net.Interface
		kind  PathKind
		force bool // false → use default route, true → temp /32 via this iface
	}
	var probes []probe
	probes = append(probes, probe{force: false}) // default-route probe (iface field unused)
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 {
			continue
		}
		if iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		if classifyIface(iface) == PathP2P {
			probes = append(probes, probe{iface: iface, kind: PathP2P, force: true})
		}
		// LAN ifaces are normally reached via default-route probe — we don't
		// force per-LAN probes (would be redundant unless operator has two
		// LAN ifaces in same /24, which is rare and not in scope).
	}

	// Track tokens added so cleanup removes them all.
	var (
		mu     sync.Mutex
		tokens []RouteToken
	)
	addTok := func(t RouteToken) {
		mu.Lock()
		tokens = append(tokens, t)
		mu.Unlock()
	}

	// Per-probe budget — total budget is divided among probes so a wedged
	// interface can't starve the rest. Floor at 1s so default-route always
	// gets a fair shake even with many ifaces.
	perProbe := totalTimeout / time.Duration(len(probes))
	if perProbe < time.Second {
		perProbe = time.Second
	}

	results := make([]PathCandidate, len(probes))
	var wg sync.WaitGroup
	for i, pr := range probes {
		wg.Add(1)
		go func(i int, pr probe) {
			defer wg.Done()
			c := PathCandidate{
				Iface: "default",
				Kind:  PathLAN,
			}
			if pr.force {
				c.Iface = pr.iface.Name
				c.Index = pr.iface.Index
				c.Kind = pr.kind
				c.LocalIP = firstIPv4(pr.iface)
				tok, addErr := p.AddRoute(ip, pr.iface.Index)
				if addErr != nil {
					c.Err = fmt.Errorf("addRoute %s via %s: %w", ip, pr.iface.Name, addErr)
					results[i] = c
					return
				}
				addTok(tok)
			}
			start := time.Now()
			localIP, dialErr := p.Dial(target, perProbe)
			c.Latency = time.Since(start)
			if dialErr != nil {
				c.Err = dialErr
				results[i] = c
				return
			}
			c.Err = nil
			if !pr.force {
				c.LocalIP = localIP
				// Resolve which iface localIP belongs to so operator sees
				// "default route → Ethernet 5".
				if name, idx := resolveIfaceForLocalIP(ifaces, localIP); name != "" {
					c.Iface = name
					c.Index = idx
					c.Kind = classifyIfaceByIndex(ifaces, idx)
				}
			}
			results[i] = c
		}(i, pr)
	}
	wg.Wait()

	cleanup := func() {
		mu.Lock()
		toks := append([]RouteToken(nil), tokens...)
		tokens = nil
		mu.Unlock()
		for _, t := range toks {
			if err := p.DelRoute(t); err != nil {
				PrintWarn(fmt.Sprintf("cleanup route %s: %v", t.TargetIP, err))
			}
		}
	}

	rep := &PathReport{Target: target, Candidates: results}
	sort.SliceStable(rep.Candidates, func(i, j int) bool {
		// P2P responders first, then LAN responders, then failures.
		ci, cj := rep.Candidates[i], rep.Candidates[j]
		if ci.Responded() != cj.Responded() {
			return ci.Responded()
		}
		if ci.Kind != cj.Kind {
			return ci.Kind == PathP2P
		}
		return ci.Latency < cj.Latency
	})
	rep.Decide()
	return rep, cleanup, nil
}

// resolveIfaceForLocalIP scans ifaces, finds the one that owns localIP,
// returns (name, index). Empty name → not found (rare — possible if iface
// was just brought down between AddRoute and Dial).
func resolveIfaceForLocalIP(ifaces []net.Interface, localIP string) (string, int) {
	target := net.ParseIP(localIP)
	if target == nil {
		return "", 0
	}
	for _, iface := range ifaces {
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			ipnet, ok := a.(*net.IPNet)
			if !ok {
				continue
			}
			if ipnet.IP.Equal(target) {
				return iface.Name, iface.Index
			}
		}
	}
	return "", 0
}

// classifyIfaceByIndex looks up iface kind by its OS index.
func classifyIfaceByIndex(ifaces []net.Interface, idx int) PathKind {
	for _, iface := range ifaces {
		if iface.Index == idx {
			return classifyIface(iface)
		}
	}
	return PathLAN
}

// describePath renders a PathReport into operator-readable text for the
// "Поиск роутера" step. Multi-line, ready for fmt.Print.
func describePath(r *PathReport) string {
	if r == nil {
		return ""
	}
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Поиск %s:\n", r.Target))
	for _, c := range r.Candidates {
		marker := "✗"
		detail := ""
		if c.Responded() {
			marker = "✓"
			detail = fmt.Sprintf("%dмс", c.Latency.Milliseconds())
		} else {
			detail = trimDialErr(c.Err)
		}
		ipBit := ""
		if c.LocalIP != "" {
			ipBit = " (" + c.LocalIP + ")"
		}
		sb.WriteString(fmt.Sprintf("  %s %-20s%s %s [%s]\n",
			marker, c.Iface, ipBit, detail, c.Kind.String()))
	}
	return sb.String()
}

// trimDialErr shortens dialer errors into a single recognisable token —
// "timeout" / "refused" / "no route" / raw err otherwise. Keeps output
// scannable in the multi-candidate table.
func trimDialErr(err error) string {
	if err == nil {
		return ""
	}
	s := strings.ToLower(err.Error())
	switch {
	case strings.Contains(s, "i/o timeout") || errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	case strings.Contains(s, "refused"):
		return "refused"
	case strings.Contains(s, "no route to host"), strings.Contains(s, "network unreachable"):
		return "no route"
	default:
		return strings.TrimSpace(err.Error())
	}
}
