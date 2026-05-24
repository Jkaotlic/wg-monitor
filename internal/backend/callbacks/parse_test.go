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
	for _, action := range []string{"restart_tunnel", "diag_now", "pingcheck_now", "force_recheck", "opkg_upgrade", "router_doctor"} {
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
	args, err := Parse("tunnel_import_replace:42:_panel_:a1b2c3d4")
	if err != nil {
		t.Fatal(err)
	}
	if args.Action != "tunnel_import_replace" {
		t.Errorf("action: %q", args.Action)
	}
	if args.UserID != 42 {
		t.Errorf("uid: %d", args.UserID)
	}
	if args.CheckName != panelSentinel {
		t.Errorf("check: %q", args.CheckName)
	}
	if !args.IsPanel {
		t.Error("expected import callback to be panel scoped")
	}
	if args.ImportToken != "a1b2c3d4" {
		t.Errorf("token: %q", args.ImportToken)
	}
}

func TestParse_TunnelImportAdd(t *testing.T) {
	args, err := Parse("tunnel_import_add:7:_panel_:deadbeef")
	if err != nil {
		t.Fatal(err)
	}
	if args.Action != "tunnel_import_add" || args.CheckName != panelSentinel || !args.IsPanel || args.ImportToken != "deadbeef" {
		t.Errorf("args: %+v", args)
	}
}

func TestParse_TunnelImportMissingToken(t *testing.T) {
	_, err := Parse("tunnel_import_replace:42:_panel_")
	if err == nil {
		t.Error("expected error for missing token")
	}
}

func TestParse_TunnelImportRejectsNameInCallbackData(t *testing.T) {
	_, err := Parse("tunnel_import_replace:42:long-tunnel-name:a1b2c3d4")
	if err == nil {
		t.Error("expected error when import callback carries tunnel name instead of panel sentinel")
	}
}

func TestParse_TunnelImportTokenOnlyShapeFitsTelegramLimit(t *testing.T) {
	data := "tunnel_import_replace:9223372036854775807:_panel_:deadbeef"
	if len(data) > 64 {
		t.Fatalf("callback_data length=%d, want <=64: %s", len(data), data)
	}
	if _, err := Parse(data); err != nil {
		t.Fatal(err)
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

func TestParse_RouteAddDeletePreviewCallbacks(t *testing.T) {
	cases := []struct {
		data         string
		action       string
		draftToken   string
		confirmToken string
		routeToken   string
	}{
		{"routes_add_confirm:42:_panel_:draft1:confirm1", "routes_add_confirm", "draft1", "confirm1", ""},
		{"routes_add_cancel:42:_panel_:draft1", "routes_add_cancel", "draft1", "", ""},
		{"routes_del:42:_panel_:rt1", "routes_del", "", "", "rt1"},
		{"routes_del_confirm:42:_panel_:draft2:confirm2", "routes_del_confirm", "draft2", "confirm2", ""},
		{"routes_del_cancel:42:_panel_:draft2", "routes_del_cancel", "draft2", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.data, func(t *testing.T) {
			got, err := Parse(tc.data)
			if err != nil {
				t.Fatalf("Parse failed: %v", err)
			}
			if got.Action != tc.action || got.RouteDraftToken != tc.draftToken || got.RouteConfirmToken != tc.confirmToken || got.RouteToken != tc.routeToken {
				t.Fatalf("got %+v", got)
			}
		})
	}
}

func TestParse_MaintActions(t *testing.T) {
	cases := []struct {
		data    string
		want    Args
		wantErr bool
	}{
		{data: "maint_open:42:_panel_", want: Args{Action: "maint_open", UserID: 42, CheckName: "_panel_", IsPanel: true}},
		{data: "maint_close:42:_panel_", want: Args{Action: "maint_close", UserID: 42, CheckName: "_panel_", IsPanel: true}},
		{data: "maint_restart:42:hrneo", want: Args{Action: "maint_restart", UserID: 42, CheckName: "hrneo", MaintName: "hrneo"}},
		{data: "maint_restart:42:awgmgr", want: Args{Action: "maint_restart", UserID: 42, CheckName: "awgmgr", MaintName: "awgmgr"}},
		{data: "maint_restart:42:router", want: Args{Action: "maint_restart", UserID: 42, CheckName: "router", MaintName: "router"}},
		{data: "maint_confirm:42:hrneo:a1b2c3d4", want: Args{Action: "maint_confirm", UserID: 42, CheckName: "hrneo", MaintName: "hrneo", MaintToken: "a1b2c3d4"}},
		{data: "maint_fw_open:42:_panel_", want: Args{Action: "maint_fw_open", UserID: 42, CheckName: "_panel_", IsPanel: true}},
		{data: "maint_fw_check:42:_panel_", want: Args{Action: "maint_fw_check", UserID: 42, CheckName: "_panel_", IsPanel: true}},
		{data: "maint_fw_install:42:_panel_", want: Args{Action: "maint_fw_install", UserID: 42, CheckName: "_panel_", IsPanel: true}},
		{data: "maint_fw_confirm:42:_panel_:deadbeef", want: Args{Action: "maint_fw_confirm", UserID: 42, CheckName: "_panel_", IsPanel: true, MaintName: "firmware", MaintToken: "deadbeef"}},
		// negative cases
		{data: "maint_restart:42", wantErr: true},             // missing name segment
		{data: "maint_restart:42:_panel_", wantErr: true},     // sentinel as name is rejected
		{data: "maint_confirm:42:hrneo", wantErr: true},       // missing token
		{data: "maint_fw_confirm:42:_panel_", wantErr: true},  // missing token
		{data: "maint_fw_confirm:42:_panel_:", wantErr: true}, // empty token
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

func TestParse_PanelHome(t *testing.T) {
	a, err := Parse("panel:0:home")
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if a.Action != "panel" || a.PanelScreen != "home" || a.UserID != 0 {
		t.Errorf("got %+v", a)
	}
}

func TestParse_PanelKindMaint(t *testing.T) {
	a, err := Parse("panel:0:kind:maint")
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if a.PanelScreen != "kind" || a.PanelKind != "maint" {
		t.Errorf("got %+v", a)
	}
}

func TestParse_PanelPush(t *testing.T) {
	a, err := Parse("panel:42:push:routes")
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if a.PanelScreen != "push" || a.PanelKind != "routes" || a.UserID != 42 {
		t.Errorf("got %+v", a)
	}
}

func TestParse_PanelKindTunnels(t *testing.T) {
	a, err := Parse("panel:0:kind:tunnels")
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if a.PanelScreen != "kind" || a.PanelKind != "tunnels" {
		t.Errorf("got %+v", a)
	}
}

func TestParse_PanelNoTopic(t *testing.T) {
	a, err := Parse("panel:7:no_topic")
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if a.PanelScreen != "no_topic" || a.UserID != 7 {
		t.Errorf("got %+v", a)
	}
}

func TestParse_PanelAwakenConfirm(t *testing.T) {
	a, err := Parse("panel:0:awaken_confirm")
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if a.PanelScreen != "awaken_confirm" {
		t.Errorf("got %+v", a)
	}
}

func TestParse_PanelAwakenDo(t *testing.T) {
	a, err := Parse("panel:0:awaken_do")
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if a.PanelScreen != "awaken_do" {
		t.Errorf("got %+v", a)
	}
}

func TestParse_PanelMobile(t *testing.T) {
	a, err := Parse("panel:0:mobile")
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if a.PanelScreen != "mobile" {
		t.Errorf("got %+v", a)
	}
}

func TestParse_PanelClose(t *testing.T) {
	a, err := Parse("panel:0:close")
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if a.PanelScreen != "close" {
		t.Errorf("got %+v", a)
	}
}

func TestParse_PanelDoctorAll(t *testing.T) {
	a, err := Parse("panel:0:doctor_all")
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if a.PanelScreen != "doctor_all" {
		t.Errorf("got %+v", a)
	}
}

func TestParse_PanelRejectsUnknownScreen(t *testing.T) {
	if _, err := Parse("panel:0:wat"); err == nil {
		t.Error("expected error for unknown screen")
	}
}

func TestParse_PanelRejectsUnknownKind(t *testing.T) {
	if _, err := Parse("panel:0:kind:lol"); err == nil {
		t.Error("expected error for unknown kind")
	}
}

func TestParse_PanelKindRequiresKind(t *testing.T) {
	if _, err := Parse("panel:0:kind"); err == nil {
		t.Error("expected error for kind screen without kind value")
	}
}

func TestParse_OpkgDisable_Valid(t *testing.T) {
	a, err := Parse("opkg_disable:12345:_menu:abcd1234")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if a.Action != "opkg_disable" {
		t.Errorf("Action=%q", a.Action)
	}
	if a.UserID != 12345 {
		t.Errorf("UserID=%d", a.UserID)
	}
	if a.OpkgRepairToken != "abcd1234" {
		t.Errorf("OpkgRepairToken=%q", a.OpkgRepairToken)
	}
}

func TestParse_OpkgDisable_MissingToken(t *testing.T) {
	_, err := Parse("opkg_disable:12345:_menu:")
	if err == nil {
		t.Error("expected error for empty token")
	}
}

func TestParse_OpkgDisable_NoTokenSegment(t *testing.T) {
	_, err := Parse("opkg_disable:12345:_menu")
	if err == nil {
		t.Error("expected error for missing token segment")
	}
}

func TestParse_Access_Home(t *testing.T) {
	a, err := Parse("access:0:home")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if a.Action != "access" || a.AccessScreen != "home" {
		t.Errorf("a=%+v", a)
	}
}

func TestParse_Access_Router(t *testing.T) {
	a, err := Parse("access:0:router:42")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if a.AccessScreen != "router" || a.AccessRouterID != 42 {
		t.Errorf("a=%+v", a)
	}
}

func TestParse_Access_Add(t *testing.T) {
	a, err := Parse("access:0:add:42")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if a.AccessScreen != "add" || a.AccessRouterID != 42 {
		t.Errorf("a=%+v", a)
	}
}

func TestParse_Access_RemoveOp(t *testing.T) {
	a, err := Parse("access:0:remove_op:42:1234567890")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if a.AccessScreen != "remove_op" || a.AccessRouterID != 42 || a.AccessOperatorTGID != 1234567890 {
		t.Errorf("a=%+v", a)
	}
}

func TestParse_Access_UnbindOwner(t *testing.T) {
	a, err := Parse("access:0:unbind_owner:42")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if a.AccessScreen != "unbind_owner" || a.AccessRouterID != 42 {
		t.Errorf("a=%+v", a)
	}
}

func TestParse_Access_CancelAdd(t *testing.T) {
	a, err := Parse("access:0:cancel_add")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if a.Action != "access" || a.AccessScreen != "cancel_add" {
		t.Errorf("a=%+v", a)
	}
}

func TestParse_Access_Errors(t *testing.T) {
	for _, bad := range []string{
		"access:0:bogus",            // unknown screen
		"access:0:router",           // missing router id
		"access:0:router:",          // empty router id
		"access:0:router:abc",       // non-numeric
		"access:0:remove_op:42",     // missing tg id
		"access:0:remove_op:42:abc", // non-numeric tg id
		"access:0:add",              // missing router id
		"access:0:unbind_owner",     // missing router id
	} {
		if _, err := Parse(bad); err == nil {
			t.Errorf("%q should have errored", bad)
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

func TestParse_RoutesSnapshot(t *testing.T) {
	a, err := Parse("routes_snapshot:42:_panel_")
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if a.Action != "routes_snapshot" || a.UserID != 42 || !a.IsPanel {
		t.Errorf("got %+v", a)
	}
}

func TestParse_RoutesHRNeoDoctor(t *testing.T) {
	a, err := Parse("routes_hrneo_doctor:42:_panel_")
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if a.Action != "routes_hrneo_doctor" || a.UserID != 42 || !a.IsPanel {
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

func TestParse_PanelKindPingCheck(t *testing.T) {
	a, err := Parse("panel:0:kind:pingcheck")
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if a.PanelKind != "pingcheck" {
		t.Errorf("got %+v", a)
	}
}

func TestParse_PanelKindDoctor(t *testing.T) {
	a, err := Parse("panel:0:kind:doctor")
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if a.PanelKind != "doctor" {
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

func TestParse_AmneziaDownload(t *testing.T) {
	a, err := Parse("amz_dl:42:_panel_:keyabc:DE")
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if a.Action != "amz_dl" || !a.IsPanel || a.AmneziaKeyID != "keyabc" || a.AmneziaCountryCode != "de" {
		t.Errorf("got %+v", a)
	}
}

func TestParse_AmneziaDownloadRejectsBadCountry(t *testing.T) {
	if _, err := Parse("amz_dl_confirm:42:_panel_:keyabc:bad country"); err == nil {
		t.Error("expected err on bad country code")
	}
}

func TestParse_AmneziaDownloadLegacyActiveKey(t *testing.T) {
	a, err := Parse("amz_dl:42:_panel_:DE")
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if a.AmneziaKeyID != "" || a.AmneziaCountryCode != "de" {
		t.Errorf("got %+v", a)
	}
}

func TestParse_AmneziaKeyActions(t *testing.T) {
	for _, data := range []string{
		"amz_open:42:_panel_:keyabc",
		"amz_delete:42:_panel_:keyabc",
		"amz_delete_confirm:42:_panel_:keyabc",
	} {
		t.Run(data, func(t *testing.T) {
			a, err := Parse(data)
			if err != nil {
				t.Fatalf("unexpected: %v", err)
			}
			if a.AmneziaKeyID != "keyabc" {
				t.Fatalf("key id = %q", a.AmneziaKeyID)
			}
		})
	}
}

func TestParse_HideMyDownload(t *testing.T) {
	a, err := Parse("hmn_dl:42:_panel_:codeabc:srv123")
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if a.Action != "hmn_dl" || !a.IsPanel || a.HideMyCodeID != "codeabc" || a.HideMyServerID != "srv123" {
		t.Errorf("got %+v", a)
	}
}

func TestParse_HideMyPage(t *testing.T) {
	a, err := Parse("hmn_page:42:_panel_:codeabc:2")
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if a.HideMyCodeID != "codeabc" || a.HideMyPage != 2 {
		t.Errorf("got %+v", a)
	}
}

func TestParse_HideMyActionsRequireCodeID(t *testing.T) {
	for _, data := range []string{
		"hmn_open:42:_panel_",
		"hmn_delete:42:_panel_:bad code",
		"hmn_dl_confirm:42:_panel_:codeabc:bad server",
	} {
		if _, err := Parse(data); err == nil {
			t.Fatalf("expected error for %q", data)
		}
	}
}
