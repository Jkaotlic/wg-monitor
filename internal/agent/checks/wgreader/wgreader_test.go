// internal/agent/checks/wgreader/wgreader_test.go
package wgreader

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

type fakeReader struct {
	name string
	age  time.Duration
	err  error
}

func (f *fakeReader) Name() string { return f.name }
func (f *fakeReader) LatestHandshakeAge(_ context.Context, _ string) (time.Duration, error) {
	return f.age, f.err
}

func TestMulti_NoReaders(t *testing.T) {
	m := &Multi{}
	_, err := m.LatestHandshakeAge(context.Background(), "wg0")
	if !errors.Is(err, ErrNoStrategies) {
		t.Fatalf("got %v, want ErrNoStrategies", err)
	}
}

func TestMulti_FirstSucceeds(t *testing.T) {
	m := &Multi{readers: []Reader{
		&fakeReader{name: "first", age: 30 * time.Second},
		&fakeReader{name: "second", err: errors.New("must not be called")},
	}}
	age, err := m.LatestHandshakeAge(context.Background(), "wg0")
	if err != nil {
		t.Fatal(err)
	}
	if age != 30*time.Second {
		t.Fatalf("age=%v", age)
	}
}

func TestMulti_FallbackOnFirstError(t *testing.T) {
	m := &Multi{readers: []Reader{
		&fakeReader{name: "first", err: errors.New("not found")},
		&fakeReader{name: "second", age: 99 * time.Second},
	}}
	age, _ := m.LatestHandshakeAge(context.Background(), "wg0")
	if age != 99*time.Second {
		t.Fatalf("age=%v", age)
	}
}

func TestMulti_NoPeersIsAuthoritative(t *testing.T) {
	// ErrNoPeers means reader successfully found the iface but no peer
	// has ever handshook. Falling through to a different reader could
	// LIE about a different state — so ErrNoPeers must short-circuit.
	m := &Multi{readers: []Reader{
		&fakeReader{name: "first", err: ErrNoPeers},
		&fakeReader{name: "second", age: 99 * time.Second},
	}}
	_, err := m.LatestHandshakeAge(context.Background(), "wg0")
	if !errors.Is(err, ErrNoPeers) {
		t.Fatalf("got %v, want ErrNoPeers", err)
	}
}

func TestMulti_AllFail(t *testing.T) {
	m := &Multi{readers: []Reader{
		&fakeReader{name: "first", err: errors.New("boom1")},
		&fakeReader{name: "second", err: errors.New("boom2")},
	}}
	_, err := m.LatestHandshakeAge(context.Background(), "wg0")
	if err == nil {
		t.Fatal("expected error")
	}
	msg := err.Error()
	for _, want := range []string{"first", "second", "boom1", "boom2"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("missing %q in %q", want, msg)
		}
	}
}

func TestMulti_StrategiesNamesPreserveOrder(t *testing.T) {
	m := &Multi{readers: []Reader{
		&fakeReader{name: "alpha"},
		&fakeReader{name: "beta"},
	}}
	got := m.Strategies()
	if len(got) != 2 || got[0] != "alpha" || got[1] != "beta" {
		t.Fatalf("strategies=%v", got)
	}
}

type fakeRunner struct {
	out string
	err error
}

func (f *fakeRunner) Run(_ context.Context, _ string, _ ...string) (string, error) {
	return f.out, f.err
}

func TestShellReader_Fresh(t *testing.T) {
	now := time.Now().Unix()
	r := newShellReader(&fakeRunner{out: "abcdEF=\t" + strconv.FormatInt(now-30, 10) + "\n"}, "wg")
	age, err := r.LatestHandshakeAge(context.Background(), "wg0")
	if err != nil {
		t.Fatal(err)
	}
	if age < 28*time.Second || age > 32*time.Second {
		t.Fatalf("age=%v", age)
	}
}

func TestShellReader_NoPeers(t *testing.T) {
	r := newShellReader(&fakeRunner{out: "abcdEF=\t0\n"}, "wg")
	_, err := r.LatestHandshakeAge(context.Background(), "wg0")
	if !errors.Is(err, ErrNoPeers) {
		t.Fatalf("got %v, want ErrNoPeers", err)
	}
}

func TestShellReader_TwoPeersTakeMin(t *testing.T) {
	now := time.Now().Unix()
	out := "aa==\t0\nbb==\t" + strconv.FormatInt(now-10, 10) + "\n"
	r := newShellReader(&fakeRunner{out: out}, "wg")
	age, _ := r.LatestHandshakeAge(context.Background(), "wg0")
	if age < 8*time.Second || age > 12*time.Second {
		t.Fatalf("age=%v", age)
	}
}

func TestShellReader_BinaryError(t *testing.T) {
	r := newShellReader(&fakeRunner{out: "stderr blob", err: fmt.Errorf("exit 1")}, "wg")
	_, err := r.LatestHandshakeAge(context.Background(), "wg0")
	if err == nil {
		t.Fatal("want error")
	}
	if !strings.Contains(err.Error(), "wg show") {
		t.Fatalf("err msg lacks context: %v", err)
	}
}

func TestShellReader_Name(t *testing.T) {
	r := newShellReader(&fakeRunner{}, "/opt/sbin/awg")
	if r.Name() != "shell:/opt/sbin/awg" {
		t.Fatalf("name=%q", r.Name())
	}
}

func TestAwgManagerReader_Found(t *testing.T) {
	body := `{"success":true,"data":{"tunnels":[
		{"id":"awg11","interfaceName":"nwg1","status":"running","lastHandshake":"` + time.Now().UTC().Add(-30*time.Second).Format(time.RFC3339) + `"},
		{"id":"awg12","interfaceName":"nwg0","status":"running","lastHandshake":"` + time.Now().UTC().Add(-5*time.Second).Format(time.RFC3339) + `"}
	]}}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/tunnels/all" {
			t.Fatalf("path: %s", r.URL.Path)
		}
		if r.Header.Get("X-Requested-With") != "XMLHttpRequest" {
			t.Fatalf("missing X-Requested-With header")
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(body))
	}))
	defer srv.Close()

	r := newAwgManagerReaderForTest(srv.URL)
	age, err := r.LatestHandshakeAge(context.Background(), "nwg0")
	if err != nil {
		t.Fatal(err)
	}
	if age < 3*time.Second || age > 8*time.Second {
		t.Fatalf("age=%v want ~5s", age)
	}
}

func TestAwgManagerReader_TunnelNotFound(t *testing.T) {
	body := `{"success":true,"data":{"tunnels":[
		{"id":"awg11","interfaceName":"nwg1","status":"running","lastHandshake":"2026-04-27T09:41:18Z"}
	]}}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(body))
	}))
	defer srv.Close()

	r := newAwgManagerReaderForTest(srv.URL)
	_, err := r.LatestHandshakeAge(context.Background(), "nwg0")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Fatalf("expected 'not found' in err: %v", err)
	}
}

func TestAwgManagerReader_TunnelStopped(t *testing.T) {
	// status != "running" should return ErrNoPeers (authoritative: tunnel exists but is down)
	body := `{"success":true,"data":{"tunnels":[
		{"id":"awg11","interfaceName":"nwg1","status":"stopped","lastHandshake":"0001-01-01T00:00:00Z"}
	]}}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(body))
	}))
	defer srv.Close()

	r := newAwgManagerReaderForTest(srv.URL)
	_, err := r.LatestHandshakeAge(context.Background(), "nwg1")
	if !errors.Is(err, ErrNoPeers) {
		t.Fatalf("got %v, want ErrNoPeers", err)
	}
}

func TestAwgManagerReader_ZeroHandshake(t *testing.T) {
	// running but lastHandshake is the zero time — peer never handshook
	body := `{"success":true,"data":{"tunnels":[
		{"id":"awg11","interfaceName":"nwg1","status":"running","lastHandshake":"0001-01-01T00:00:00Z"}
	]}}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(body))
	}))
	defer srv.Close()

	r := newAwgManagerReaderForTest(srv.URL)
	_, err := r.LatestHandshakeAge(context.Background(), "nwg1")
	if !errors.Is(err, ErrNoPeers) {
		t.Fatalf("got %v, want ErrNoPeers", err)
	}
}

func TestAwgManagerReader_HTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(500)
		w.Write([]byte("internal error"))
	}))
	defer srv.Close()

	r := newAwgManagerReaderForTest(srv.URL)
	_, err := r.LatestHandshakeAge(context.Background(), "nwg1")
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestAwgManagerReader_BadJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte("<html>not json</html>"))
	}))
	defer srv.Close()

	r := newAwgManagerReaderForTest(srv.URL)
	_, err := r.LatestHandshakeAge(context.Background(), "nwg1")
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestAwgManagerReader_Name(t *testing.T) {
	r := newAwgManagerReaderForTest("http://x")
	if r.Name() != "awg-manager" {
		t.Fatalf("name=%q", r.Name())
	}
}
