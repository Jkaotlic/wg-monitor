package keenetic

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// stubRunner implements CmdRunner with canned outputs keyed by joined argv.
type stubRunner struct {
	want   string // expected joined argv
	out    string
	err    error
	called bool
}

func (s *stubRunner) Run(_ context.Context, name string, args ...string) (string, error) {
	s.called = true
	got := name
	for _, a := range args {
		got += " " + a
	}
	if got != s.want {
		return "", &mismatchErr{want: s.want, got: got}
	}
	return s.out, s.err
}

type mismatchErr struct{ want, got string }

func (e *mismatchErr) Error() string { return "stub: argv mismatch want=" + e.want + " got=" + e.got }

func TestNDMC_RunsCorrectArgv(t *testing.T) {
	stub := &stubRunner{
		want: "/bin/ndmc -c show running-config",
		out:  "stub-output\n",
	}
	n := NDMC{Runner: stub}
	out, err := n.Show(context.Background(), "running-config")
	if err != nil {
		t.Fatalf("Show err: %v", err)
	}
	if out != "stub-output\n" {
		t.Fatalf("out: %q", out)
	}
	if !stub.called {
		t.Fatalf("runner not called")
	}
}

func TestNDMC_PropagatesErr(t *testing.T) {
	stub := &stubRunner{
		want: "/bin/ndmc -c show running-config",
		err:  errors.New("ndmc not found"),
	}
	n := NDMC{Runner: stub}
	_, err := n.Show(context.Background(), "running-config")
	if err == nil {
		t.Fatalf("expected error")
	}
}

func TestNDMC_ErrorCarriesShortOutput(t *testing.T) {
	stub := &stubRunner{want: "/bin/ndmc -c show running-config", out: "\x1b[KCore::Configurator: busy.\n", err: errors.New("exit status 1")}
	_, err := NDMC{Runner: stub}.Show(context.Background(), "running-config")
	if err == nil || err.Error() != "ndmc show running-config: exit status 1: Core::Configurator: busy." {
		t.Fatalf("err = %v", err)
	}
}

func TestNDMC_ErrorDropsLongOutput(t *testing.T) {
	stub := &stubRunner{want: "/bin/ndmc -c show running-config", out: "authentication wpa-psk ns3 SECRET\n" + strings.Repeat("y\n", 300), err: errors.New("exit status 1")}
	_, err := NDMC{Runner: stub}.Show(context.Background(), "running-config")
	if err == nil || strings.Contains(err.Error(), "SECRET") || err.Error() != "ndmc show running-config: exit status 1" {
		t.Fatalf("err = %v", err)
	}
}
