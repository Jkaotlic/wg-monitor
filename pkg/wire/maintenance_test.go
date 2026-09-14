package wire

import (
	"encoding/json"
	"testing"
)

func TestVersionAudit_RoundTrip(t *testing.T) {
	in := VersionAudit{
		AwgmgrVersion:   "2.8.2",
		HrneoVersion:    "2.4.0",
		FirmwareCurrent: "4.2.6",
		FirmwareAvail:   "5.0.1",
		HrneoUptime:     "3д 4ч",
		AwgmgrUptime:    "7д 12ч",
	}
	b, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var out VersionAudit
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out != in {
		t.Fatalf("round-trip diverged:\n  in=%+v\n out=%+v", in, out)
	}
}

// Старый агент шлёт hrneo_installed обычным bool с omitempty. Честное «стоит»
// доезжает как true и обязано остаться true: именно этот случай смена типа
// могла бы съесть молча.
func TestVersionAudit_OldAgentHrneoInstalledStillDecodes(t *testing.T) {
	var got VersionAudit
	if err := json.Unmarshal([]byte(`{"awgmgr_version":"2.8.2","hrneo_installed":true,"hrneo_version":"2.4.0"}`), &got); err != nil {
		t.Fatalf("unmarshal старой формы: %v", err)
	}
	if got.HrneoInstalled == nil {
		t.Fatal("hrneo_installed = nil: честный ответ старого агента «стоит» потерян")
	}
	if !*got.HrneoInstalled {
		t.Error("hrneo_installed = false, хотим true")
	}
}

// А ОТСУТСТВИЕ поля у старого агента -- это «опрос не дал ответа ИЛИ не
// установлен»: omitempty опускал false, и на проводе эти два случая
// неразличимы. Значит nil, а не твёрдое «не установлен».
func TestVersionAudit_AbsentHrneoInstalledIsUnknown(t *testing.T) {
	var got VersionAudit
	if err := json.Unmarshal([]byte(`{"awgmgr_version":"2.8.2"}`), &got); err != nil {
		t.Fatal(err)
	}
	if got.HrneoInstalled != nil {
		t.Errorf("hrneo_installed = %v, хотим nil (неизвестно)", *got.HrneoInstalled)
	}
}

// Новый агент, у которого опрос УДАЛСЯ и ответил «не установлен», обязан
// сказать это явно -- иначе его ответ исчезнет с провода, как у старого.
func TestVersionAudit_ExplicitHrneoFalseGoesOnTheWire(t *testing.T) {
	no := false
	b, err := json.Marshal(VersionAudit{AwgmgrVersion: "2.8.2", HrneoInstalled: &no})
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]any
	if err := json.Unmarshal(b, &raw); err != nil {
		t.Fatal(err)
	}
	if _, ok := raw["hrneo_installed"]; !ok {
		t.Fatalf("явный false не уехал на провод: %s", b)
	}
	var back VersionAudit
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatal(err)
	}
	if back.HrneoInstalled == nil {
		t.Fatal("явный false вернулся как nil")
	}
	if *back.HrneoInstalled {
		t.Error("hrneo_installed = true, хотим false")
	}
}

func TestFirmwareStatus_RoundTrip(t *testing.T) {
	in := FirmwareStatus{
		Current:   "4.2.6",
		Available: "5.0.1",
		Hint:      "system upgrade is available",
		Channel:   "release",
	}
	b, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var out FirmwareStatus
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out != in {
		t.Fatalf("round-trip diverged:\n  in=%+v\n out=%+v", in, out)
	}
}

// Перезагрузка нужна ровно тогда, когда обе версии модуля ядра известны и
// расходятся. Молчание одной из сторон -- не повод пугать владельца.
func TestRebootNeeded(t *testing.T) {
	for _, tc := range []struct {
		installed, loaded string
		want              bool
	}{
		{"3.2.20260930", "3.1.20260906", true},
		{"3.1.20260906", "3.1.20260906", false},
		{"", "3.1.20260906", false},
		{"3.1.20260906", "", false},
		{"", "", false},
	} {
		if got := RebootNeeded(tc.installed, tc.loaded); got != tc.want {
			t.Errorf("RebootNeeded(%q, %q) = %v, want %v", tc.installed, tc.loaded, got, tc.want)
		}
	}
}

func TestAwgmUpdateResultJSONShape(t *testing.T) {
	b, err := json.Marshal(AwgmUpdateResult{Updated: true, From: "2.19.0", To: "2.19.1", KmodInstalled: "3.2", KmodLoaded: "3.1", RebootNeeded: true})
	if err != nil {
		t.Fatal(err)
	}
	want := `{"updated":true,"from":"2.19.0","to":"2.19.1","kmod_installed":"3.2","kmod_loaded":"3.1","reboot_needed":true}`
	if string(b) != want {
		t.Errorf("got %s\nwant %s", b, want)
	}
	b, err = json.Marshal(HrneoUpdateResult{Updated: false, From: "3.18.3-1", To: "3.18.3-1", Running: true})
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != `{"updated":false,"from":"3.18.3-1","to":"3.18.3-1","running":true}` {
		t.Errorf("hrneo shape: %s", b)
	}
}
