package callbacks

import (
	"testing"
	"time"
)

func TestParseSilence(t *testing.T) {
	cases := []struct {
		data string
		ttl  time.Duration
	}{
		{"silence:42:awg_handshake:1h", 1 * time.Hour},
		{"silence:42:awg_handshake:4h", 4 * time.Hour},
		{"silence:42:awg_handshake:24h", 24 * time.Hour},
	}
	for _, c := range cases {
		a, err := Parse(c.data)
		if err != nil {
			t.Fatalf("%s: %v", c.data, err)
		}
		if a.Action != "silence" {
			t.Errorf("%s: action=%q", c.data, a.Action)
		}
		if a.UserID != 42 {
			t.Errorf("%s: uid=%d", c.data, a.UserID)
		}
		if a.CheckName != "awg_handshake" {
			t.Errorf("%s: check=%q", c.data, a.CheckName)
		}
		if a.TTL != c.ttl {
			t.Errorf("%s: ttl=%v, want %v", c.data, a.TTL, c.ttl)
		}
	}
}

func TestParseAck(t *testing.T) {
	a, err := Parse("ack:42:awg_handshake")
	if err != nil {
		t.Fatal(err)
	}
	if a.Action != "ack" {
		t.Errorf("action=%q", a.Action)
	}
	if a.UserID != 42 {
		t.Errorf("uid=%d", a.UserID)
	}
	if a.CheckName != "awg_handshake" {
		t.Errorf("check=%q", a.CheckName)
	}
}

func TestParseMute(t *testing.T) {
	a, err := Parse("mute:42:awg_handshake")
	if err != nil {
		t.Fatal(err)
	}
	if a.Action != "mute" {
		t.Errorf("action=%q", a.Action)
	}
}

func TestParseHistory(t *testing.T) {
	a, err := Parse("history:42:awg_handshake")
	if err != nil {
		t.Fatal(err)
	}
	if a.Action != "history" {
		t.Errorf("action=%q", a.Action)
	}
}

func TestParseCommandActions(t *testing.T) {
	for _, action := range []string{"restart_tunnel", "diag_now", "pingcheck_now", "force_recheck", "opkg_upgrade"} {
		data := action + ":42:tunnel_amnezia_for_awg2"
		a, err := Parse(data)
		if err != nil {
			t.Fatalf("%s: %v", data, err)
		}
		if a.Action != action {
			t.Errorf("%s: action=%q", data, a.Action)
		}
		if a.UserID != 42 {
			t.Errorf("%s: uid=%d", data, a.UserID)
		}
		if a.CheckName != "tunnel_amnezia_for_awg2" {
			t.Errorf("%s: check=%q", data, a.CheckName)
		}
		if a.TTL != 0 {
			t.Errorf("%s: command actions must not carry TTL, got %v", data, a.TTL)
		}
	}
}

func TestParseMalformed(t *testing.T) {
	cases := []string{
		"",
		"garbage",
		"silence:nan:awg:1h",
		"silence:42:awg",
		"silence:42:awg:invalid",
		"unknown:42:awg",
	}
	for _, c := range cases {
		if _, err := Parse(c); err == nil {
			t.Errorf("expected error for %q", c)
		}
	}
}

func TestParse_TunnelImportReplace(t *testing.T) {
	args, err := Parse("tunnel_import_replace:42:awg11:a1b2c3d4")
	if err != nil {
		t.Fatal(err)
	}
	if args.Action != "tunnel_import_replace" {
		t.Errorf("action: %q", args.Action)
	}
	if args.UserID != 42 {
		t.Errorf("uid: %d", args.UserID)
	}
	if args.CheckName != "awg11" {
		t.Errorf("check: %q", args.CheckName)
	}
	if args.ImportToken != "a1b2c3d4" {
		t.Errorf("token: %q", args.ImportToken)
	}
}

func TestParse_TunnelImportAdd(t *testing.T) {
	args, err := Parse("tunnel_import_add:7:new-tun:deadbeef")
	if err != nil {
		t.Fatal(err)
	}
	if args.Action != "tunnel_import_add" || args.ImportToken != "deadbeef" {
		t.Errorf("args: %+v", args)
	}
}

func TestParse_TunnelImportMissingToken(t *testing.T) {
	_, err := Parse("tunnel_import_replace:42:awg11")
	if err == nil {
		t.Error("expected error for missing token")
	}
}

func TestParse_RoutesActions(t *testing.T) {
	cases := []struct {
		data    string
		action  string
		token   string
		isPanel bool
	}{
		{"routes_open:42:_panel_", "routes_open", "", true},
		{"routes_refresh:42:_panel_", "routes_refresh", "", true},
		{"routes_rebind:42:t1", "routes_rebind", "", false},
		{"routes_pick:42:t1:t2", "routes_pick", "", false},
		{"routes_confirm:42:t1:t2:abc12345", "routes_confirm", "abc12345", false},
		{"routes_close:0:_panel_", "routes_close", "", true},
		{"routes_back:42:_panel_", "routes_back", "", true},
	}
	for _, tc := range cases {
		args, err := Parse(tc.data)
		if err != nil {
			t.Errorf("%s: %v", tc.data, err)
			continue
		}
		if args.Action != tc.action {
			t.Errorf("%s: action=%s", tc.data, args.Action)
		}
		if tc.token != "" && args.RebindToken != tc.token {
			t.Errorf("%s: token=%q want %q", tc.data, args.RebindToken, tc.token)
		}
		if args.IsPanel != tc.isPanel {
			t.Errorf("%s: isPanel=%v want %v", tc.data, args.IsPanel, tc.isPanel)
		}
	}
}

func TestParse_RoutesConfirm_MissingToken(t *testing.T) {
	_, err := Parse("routes_confirm:42:t1:t2")
	if err == nil {
		t.Errorf("expected error for missing token")
	}
}
