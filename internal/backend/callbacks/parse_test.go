package callbacks

import (
	"strings"
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
	for _, action := range []string{"diag_now", "pingcheck_now", "force_recheck", "router_doctor"} {
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
		"routes_router:42:_panel_",
	}
	for _, c := range cases {
		if _, err := Parse(c); err == nil {
			t.Errorf("expected error for %q", c)
		}
	}
}

func TestParse_MaintActions(t *testing.T) {
	cases := []struct {
		data    string
		want    Args
		wantErr bool
	}{
		// Перезапуск служб ушёл в приложение вместе с панелями (цикл 4).
		{data: "maint_restart:42:hrneo", wantErr: true},
		{data: "maint_restart:42:awgmgr", wantErr: true},
		{data: "maint_confirm:42:hrneo:a1b2c3d4", wantErr: true},
		// negative cases
		{data: "maint_restart:42", wantErr: true},
		{data: "maint_restart:42:_panel_", wantErr: true},
		{data: "maint_confirm:42:hrneo", wantErr: true},
		{data: "maint_open:42:_panel_", wantErr: true},
		{data: "maint_close:42:_panel_", wantErr: true},
		{data: "maint_fw_open:42:_panel_", wantErr: true},
		{data: "maint_fw_check:42:_panel_", wantErr: true},
		{data: "maint_fw_install:42:_panel_", wantErr: true},
		{data: "maint_fw_confirm:42:_panel_:deadbeef", wantErr: true},
	}
	for _, c := range cases {
		t.Run(c.data, func(t *testing.T) {
			got, err := Parse(c.data)
			if c.wantErr {
				if err == nil {
					t.Errorf("expected error for %q, got Args=%+v", c.data, got)
				}
				return
			}
			if err != nil {
				t.Errorf("Parse(%q): %v", c.data, err)
				return
			}
			if got != c.want {
				t.Errorf("Parse(%q):\n  got=%+v\n want=%+v", c.data, got, c.want)
			}
		})
	}
}

func TestParse_PanelKindMaintRemoved(t *testing.T) {
	for _, data := range []string{"panel:0:kind:maint", "panel:42:push:maint", "panel:0:help:maint"} {
		if _, err := Parse(data); err == nil {
			t.Errorf("%q: вид обслуживания удалён из хаба, разбор обязан отказать", data)
		}
	}
}

func TestParse_PanelRejectsUnknownScreen(t *testing.T) {
	if _, err := Parse("panel:0:wat"); err == nil {
		t.Error("expected error for unknown screen")
	}
}

func TestParse_MaintOpkgDiagTokensRejectMalformedCodes(t *testing.T) {
	for _, bad := range []string{
		"diag_raw:42:_panel_:bad.token",
		"diag_back:42:_panel_:bad/token",
		"diag_test:bad.token:mtu",
	} {
		if _, err := Parse(bad); err == nil {
			t.Errorf("%q should reject malformed callback token", bad)
		}
	}
}

func TestParse_DiagRaw(t *testing.T) {
	a, err := Parse("diag_raw:42:_panel_:deadbeef")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if a.Action != "diag_raw" {
		t.Errorf("action=%q", a.Action)
	}
	if a.UserID != 42 {
		t.Errorf("UserID=%d", a.UserID)
	}
	if a.DiagRawToken != "deadbeef" {
		t.Errorf("DiagRawToken=%q", a.DiagRawToken)
	}
}

func TestParse_DiagRaw_MissingToken(t *testing.T) {
	if _, err := Parse("diag_raw:42:_panel_:"); err == nil {
		t.Errorf("expected error on empty token")
	}
}

func TestParse_PanelHelpDiag(t *testing.T) {
	a, err := Parse("panel:0:help:diag")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if a.Action != "panel" || a.PanelScreen != "help" || a.PanelKind != "diag" {
		t.Errorf("got Action=%q Screen=%q Kind=%q", a.Action, a.PanelScreen, a.PanelKind)
	}
}

func TestParse_PanelHelp_UnknownScreen(t *testing.T) {
	if _, err := Parse("panel:0:help:totally_made_up"); err == nil {
		t.Error("expected error on unknown help screen")
	}
}

func TestParse_PanelHelp_MissingScreen(t *testing.T) {
	if _, err := Parse("panel:0:help:"); err == nil {
		t.Error("expected error on missing help screen arg")
	}
}

func TestParse_PingCheckOpen(t *testing.T) {
	a, err := Parse("pingcheck_open:42:_panel_")
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if a.Action != "pingcheck_open" || a.UserID != 42 || !a.IsPanel {
		t.Errorf("got %+v", a)
	}
}

func TestParse_PingCheckToggle_OK(t *testing.T) {
	a, err := Parse("pingcheck_toggle:42:awg10:Wireguard0:0")
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if a.Action != "pingcheck_toggle" || a.UserID != 42 ||
		a.PingCheckTunnelID != "awg10" || a.NDMSName != "Wireguard0" || a.PingCheckEnable != false {
		t.Errorf("got %+v", a)
	}
}

func TestParse_PingCheckToggle_RejectsBadNDMS(t *testing.T) {
	_, err := Parse("pingcheck_toggle:42:awg10:bad name with spaces:1")
	if err == nil {
		t.Error("expected validation err on space-containing ndms_name")
	}
}

func TestParse_DiagTest(t *testing.T) {
	a, err := Parse("diag_test:42:abcd1234:mtu")
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if a.Action != "diag_test" || a.DiagRawToken != "abcd1234" || a.DiagTestID != "mtu" {
		t.Errorf("got %+v", a)
	}
}

func TestParse_PanelHelpPingCheck(t *testing.T) {
	a, err := Parse("panel:0:help:pingcheck")
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if a.PanelKind != "pingcheck" {
		t.Errorf("got %+v", a)
	}
}

func TestParse_PanelHelpDoctor(t *testing.T) {
	a, err := Parse("panel:0:help:doctor")
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if a.PanelKind != "doctor" {
		t.Errorf("got %+v", a)
	}
}

func TestParse_PanelHelpOperatorAndPremium(t *testing.T) {
	for _, screen := range []string{"operator", "premium", "alerts", "fleet", "mobile"} {
		a, err := Parse("panel:0:help:" + screen)
		if err != nil {
			t.Fatalf("%s: unexpected: %v", screen, err)
		}
		if a.PanelScreen != "help" || a.PanelKind != screen {
			t.Errorf("%s: got %+v", screen, a)
		}
	}
}

func TestParse_DiagBack(t *testing.T) {
	a, err := Parse("diag_back:42:_panel_:abcd1234")
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if a.Action != "diag_back" || a.DiagRawToken != "abcd1234" {
		t.Errorf("got %+v", a)
	}
}

func TestParse_DiagBack_RequiresToken(t *testing.T) {
	_, err := Parse("diag_back:42:_panel_:")
	if err == nil {
		t.Error("expected err on empty token")
	}
}

// От хаба /panel в разборе осталась одна справка; доступы -- целиком в приложении.
func TestParse_PanelOnlyHelpSurvives(t *testing.T) {
	for _, data := range []string{
		"panel:0:home", "panel:0:kind:tunnels", "panel:42:push:routes", "panel:7:no_topic",
		"panel:0:awaken_confirm", "panel:0:awaken_do", "panel:0:mobile", "panel:0:close",
		"panel:0:doctor_all", "panel:0:audit_all", "panel:0:update_all_confirm",
		"panel:0:update_all_do", "panel:0:weblink",
		"access:0:home", "access:0:router:42", "access:0:cancel_add",
	} {
		if _, err := Parse(data); err == nil {
			t.Errorf("Parse(%q) принят, а экрана больше нет", data)
		}
	}
	if a, err := Parse("panel:0:help:pingcheck"); err != nil || a.PanelScreen != "help" || a.PanelKind != "pingcheck" {
		t.Fatalf("справка перестала разбираться: %+v, %v", a, err)
	}
}

func TestParse_CabinetCallbacksAreUnknown(t *testing.T) {
	for _, data := range []string{
		"amz_refresh:42:_panel_", "amz_open:42:_panel_:abc123", "amz_dl:42:_panel_:key1:de",
		"amz_revoke_confirm:42:_panel_:key1:de:tok", "amz_selfhosted_manage:42:_panel_",
		"amz_selfhosted_issue:42:_panel_:home", "amz_selfhosted_cancel:42:_panel_",
		"hmn_refresh:42:_panel_", "hmn_dl_confirm:42:_panel_:code1:srv1:tok",
	} {
		if _, err := Parse(data); err == nil || !strings.Contains(err.Error(), "unknown action") {
			t.Errorf("%s: err=%v", data, err)
		}
	}
	// Справка premium остаётся: кнопки с ней висят в старых сообщениях.
	if _, err := Parse("panel:0:help:premium"); err != nil {
		t.Fatalf("panel:0:help:premium: %v", err)
	}
}
