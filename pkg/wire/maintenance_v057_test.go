package wire

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestV057ActionsAreValid(t *testing.T) {
	for _, a := range []string{"porthop_status", "porthop_install", "porthop_remove", "porthop_logs", "space_report"} {
		if !IsValidCommandAction(a) {
			t.Errorf("%s нет в validCommandActions", a)
		}
	}
}

// Имена полей -- контракт с бэкендом и мини-аппом (v0.57).
func TestV057PayloadFieldNames(t *testing.T) {
	st := PorthopStatus{
		Installed: true, Running: true, Auto: true, Ifaces: []string{"opkgtun10"}, Watched: []string{"opkgtun10"},
		Legacy:  PorthopLegacy{Found: true, Path: "/opt/etc/init.d/S99awg-porthop", Running: true, MovedTo: "/x"},
		Hops24h: 3, Recovered24h: 2, Failed24h: 1, LastEvent: "e", LogTail: "t",
		ScriptPath: "/s", ConfPath: "/c", LogPath: "/l",
	}
	b, _ := json.Marshal(st)
	for _, k := range []string{`"installed"`, `"running"`, `"auto"`, `"ifaces"`, `"watched"`, `"legacy"`, `"found"`, `"path"`, `"moved_to"`,
		`"hops_24h"`, `"recovered_24h"`, `"failed_24h"`, `"last_event"`, `"log_tail"`, `"script_path"`, `"conf_path"`, `"log_path"`} {
		if !strings.Contains(string(b), k) {
			t.Errorf("PorthopStatus lacks %s: %s", k, b)
		}
	}
	b, _ = json.Marshal(SpaceReport{FreeKB: 1, TotalKB: 2, Top: []SpaceEntry{{Path: "/opt/x", KB: 3}}})
	if string(b) != `{"free_kb":1,"total_kb":2,"top":[{"path":"/opt/x","kb":3}]}` {
		t.Errorf("SpaceReport: %s", b)
	}
	b, _ = json.Marshal(DNSResetResult{Probes: []DNSProbe{{Server: "9.9.9.9", Purpose: "foreign", OK: false, Error: "timeout"}}})
	if string(b) != `{"probes":[{"server":"9.9.9.9","purpose":"foreign","ok":false,"error":"timeout"}]}` {
		t.Errorf("DNSResetResult: %s", b)
	}
	if !(DNSResetResult{}).IsZero() || (DNSResetResult{Probes: []DNSProbe{{}}}).IsZero() {
		t.Error("DNSResetResult.IsZero")
	}
}
