package main

import (
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestPathReport_ChosenNil_WhenNoCandidatesRespond(t *testing.T) {
	r := &PathReport{
		Target: "192.168.31.1:222",
		Candidates: []PathCandidate{
			{Iface: "Ethernet", Kind: PathLAN, Err: errors.New("timeout")},
			{Iface: "tun0", Kind: PathP2P, Err: errors.New("timeout")},
		},
	}
	r.Decide()
	if r.Chosen != nil {
		t.Fatalf("want nil Chosen when all candidates have Err, got %+v", r.Chosen)
	}
	if r.Multiple {
		t.Fatal("want Multiple=false on all-fail")
	}
}

func TestPathReport_AutoPicksOnlyResponder(t *testing.T) {
	r := &PathReport{
		Candidates: []PathCandidate{
			{Iface: "Ethernet", Kind: PathLAN, Err: errors.New("timeout")},
			{Iface: "tun0", Kind: PathP2P, Latency: 142 * time.Millisecond},
		},
	}
	r.Decide()
	if r.Chosen == nil || r.Chosen.Iface != "tun0" {
		t.Fatalf("want Chosen=tun0, got %+v", r.Chosen)
	}
	if r.Multiple {
		t.Fatal("only one responder — Multiple must be false")
	}
}

func TestPathReport_MultipleResponders_SetsMultiple(t *testing.T) {
	r := &PathReport{
		Candidates: []PathCandidate{
			{Iface: "Ethernet", Kind: PathLAN, Latency: 5 * time.Millisecond},
			{Iface: "tun0", Kind: PathP2P, Latency: 142 * time.Millisecond},
		},
	}
	r.Decide()
	if !r.Multiple {
		t.Fatal("two responders — Multiple must be true")
	}
	if r.Chosen == nil || r.Chosen.Kind != PathP2P {
		t.Fatalf("auto-pick should prefer P2P, got %+v", r.Chosen)
	}
}

func TestPathKind_Distinct(t *testing.T) {
	if PathLAN == PathP2P {
		t.Fatal("PathLAN must differ from PathP2P")
	}
}

type fakeProber struct {
	ifaces    []net.Interface
	addRoutes map[int]error               // ifIdx → error (nil → success)
	dials     map[string]fakeDialResult   // key → result
	addCalls  []int
	delCalls  []RouteToken
	mu        sync.Mutex
}

type fakeDialResult struct {
	localIP string
	err     error
	latency time.Duration
}

func (f *fakeProber) Interfaces() ([]net.Interface, error) { return f.ifaces, nil }

func (f *fakeProber) AddRoute(ip string, ifIdx int) (RouteToken, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.addCalls = append(f.addCalls, ifIdx)
	if err, ok := f.addRoutes[ifIdx]; ok && err != nil {
		return RouteToken{}, err
	}
	return RouteToken{TargetIP: ip, IfIndex: ifIdx}, nil
}

func (f *fakeProber) DelRoute(t RouteToken) error {
	f.mu.Lock()
	f.delCalls = append(f.delCalls, t)
	f.mu.Unlock()
	return nil
}

func (f *fakeProber) Dial(target string, timeout time.Duration) (string, error) {
	f.mu.Lock()
	key := "default"
	if len(f.addCalls) > 0 {
		key = fmt.Sprintf("ifIdx=%d", f.addCalls[len(f.addCalls)-1])
	}
	res := f.dials[key]
	f.mu.Unlock()
	if res.latency > 0 {
		time.Sleep(res.latency)
	}
	return res.localIP, res.err
}

func mockIface(idx int, name string, p2p bool, _ string) net.Interface {
	flags := net.FlagUp
	if p2p {
		flags |= net.FlagPointToPoint
	}
	return net.Interface{
		Index:        idx,
		Name:         name,
		Flags:        flags,
		HardwareAddr: nil,
	}
}

func TestStepFindReachablePath_SinglePathResponds(t *testing.T) {
	f := &fakeProber{
		ifaces: []net.Interface{
			mockIface(1, "Ethernet", false, "192.168.31.5"),
			mockIface(2, "SSTP-Client", true, "10.0.0.5"),
		},
		dials: map[string]fakeDialResult{
			"default": {err: errors.New("i/o timeout")},
			"ifIdx=2": {localIP: "10.0.0.5", latency: 50 * time.Millisecond},
		},
	}
	rep, cleanup, err := stepFindReachablePath(f, "192.168.31.1:222", 5*time.Second)
	defer cleanup()
	if err != nil {
		t.Fatal(err)
	}
	if rep.Chosen == nil || rep.Chosen.Iface != "SSTP-Client" {
		t.Fatalf("want chosen=SSTP-Client, got %+v", rep.Chosen)
	}
	if rep.Multiple {
		t.Fatal("only one responder — Multiple must be false")
	}
}

func TestStepFindReachablePath_NoResponse(t *testing.T) {
	f := &fakeProber{
		ifaces: []net.Interface{
			mockIface(1, "Ethernet", false, "192.168.31.5"),
			mockIface(2, "SSTP-Client", true, "10.0.0.5"),
		},
		dials: map[string]fakeDialResult{
			"default": {err: errors.New("i/o timeout")},
			"ifIdx=2": {err: errors.New("i/o timeout")},
		},
	}
	rep, cleanup, err := stepFindReachablePath(f, "192.168.31.1:222", 5*time.Second)
	defer cleanup()
	if err != nil {
		t.Fatal(err)
	}
	if rep.Chosen != nil {
		t.Fatalf("want Chosen=nil on all-timeout, got %+v", rep.Chosen)
	}
}

func TestStepFindReachablePath_InvalidTarget(t *testing.T) {
	f := &fakeProber{}
	_, cleanup, err := stepFindReachablePath(f, "not-an-ip:222", 5*time.Second)
	defer cleanup()
	if err == nil {
		t.Fatal("want error for non-IP target")
	}
}

func TestDescribePath_Renders(t *testing.T) {
	r := &PathReport{
		Target: "192.168.31.1:222",
		Candidates: []PathCandidate{
			{Iface: "SSTP-Client", LocalIP: "10.0.0.5", Kind: PathP2P, Latency: 142 * time.Millisecond},
			{Iface: "Ethernet", LocalIP: "192.168.31.5", Kind: PathLAN, Err: errors.New("i/o timeout")},
		},
	}
	out := describePath(r)
	if !strings.Contains(out, "SSTP-Client") || !strings.Contains(out, "142") {
		t.Errorf("missing P2P responder: %q", out)
	}
	if !strings.Contains(out, "Ethernet") || !strings.Contains(out, "timeout") {
		t.Errorf("missing LAN fail row: %q", out)
	}
}
