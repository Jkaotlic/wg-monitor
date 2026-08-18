package actions

import (
	"testing"

	"github.com/Jkaotlic/wg-monitor/internal/agent/awgmgr"
	"github.com/Jkaotlic/wg-monitor/pkg/wire"
)

// Живая раскладка домашнего роутера 2.17.2: политика называет интерфейсы
// NDMS-именами, туннели знают себя по id и ядерному iface.
func liveResolverFixture() *policyIfaceResolver {
	tunnels := []wire.TunnelMeta{
		{ID: "awg10", Name: "awg3-main-work", Iface: "opkgtun10"},
		{ID: "awg11", Name: "awg3-work-via-ru1", Iface: "opkgtun11"},
		{ID: "awg20", Name: "NetherlandsKerkradeS24", Iface: "nwg0", NDMSName: "Wireguard0"},
		{ID: "wan:eth3", Name: "Подключение Ethernet", Iface: "eth3"},
	}
	ifaces := []awgmgr.PolicyInterface{
		{Name: "OpkgTun11", Label: "awg3-work-via-ru1", Up: true},
		{Name: "GigabitEthernet1", Label: "Подключение Ethernet", Up: true},
		{Name: "OpkgTun10", Label: "awg3-main-work", Up: false},
		{Name: "Wireguard0", Label: "NetherlandsKerkradeS24", Up: false},
		{Name: "Bridge1", Label: "Guest network", Up: false},
	}
	return newPolicyIfaceResolver(tunnels, ifaces)
}

func TestPolicyIfaceResolver_TunnelForNames(t *testing.T) {
	r := liveResolverFixture()
	cases := []struct {
		name, label string
		wantID      string
		wantOK      bool
	}{
		{"OpkgTun11", "awg3-work-via-ru1", "awg11", true},   // регистр iface
		{"Wireguard0", "NetherlandsKerkradeS24", "awg20", true}, // ndmsName
		{"OpkgTun10", "awg3-main-work", "awg10", true},
		{"GigabitEthernet1", "Подключение Ethernet", "wan:eth3", true}, // только по label
		{"Bridge1", "Guest network", "", false},                        // не наш интерфейс
	}
	for _, c := range cases {
		gotID, gotOK := r.tunnelForNames(c.name, c.label)
		if gotID != c.wantID || gotOK != c.wantOK {
			t.Errorf("tunnelForNames(%q,%q) = (%q,%v), want (%q,%v)", c.name, c.label, gotID, gotOK, c.wantID, c.wantOK)
		}
	}
}

func TestPolicyIfaceResolver_UpUnknownIsDown(t *testing.T) {
	r := liveResolverFixture()
	if !r.up("OpkgTun11") {
		t.Error("OpkgTun11 must be up")
	}
	if r.up("Wireguard0") {
		t.Error("Wireguard0 must be down")
	}
	// Звено, которого роутер не перечислил, не может нести трафик: угадав
	// "up", мы бы назначили его активным и соврали про текущий выход.
	if r.up("SomethingElse0") {
		t.Error("unknown interface must be reported down")
	}
}

func TestPolicyIfaceResolver_KnownInterface(t *testing.T) {
	r := liveResolverFixture()
	// WAN политике доступен, но нашим туннелем не является.
	if !r.knownInterface("GigabitEthernet1") {
		t.Error("GigabitEthernet1 must be known")
	}
	if !r.knownInterface("Bridge1") {
		t.Error("Bridge1 must be known: the router offers it to policies")
	}
	if r.knownInterface("Wireguard9") {
		t.Error("Wireguard9 is not offered by this router")
	}
}

func TestPolicyIfaceResolver_PermitName(t *testing.T) {
	r := liveResolverFixture()
	// permit принимает NDMS-имя; агент знает туннель по iface и по имени.
	if got, ok := r.permitName("opkgtun11", "awg11", "awg3-work-via-ru1"); !ok || got != "OpkgTun11" {
		t.Errorf("permitName(opkg) = (%q,%v), want (OpkgTun11,true)", got, ok)
	}
	if got, ok := r.permitName("nwg0", "awg20", "NetherlandsKerkradeS24"); !ok || got != "Wireguard0" {
		t.Errorf("permitName(nwg0) = (%q,%v), want (Wireguard0,true)", got, ok)
	}
	// Роутер не предлагает такой интерфейс политикам -- вызывающий обязан
	// упасть, а не молча "успешно" ничего не сделать.
	if got, ok := r.permitName("nwg9", "awg99"); ok {
		t.Errorf("permitName(unknown) = (%q,true), want ok=false", got)
	}
}
