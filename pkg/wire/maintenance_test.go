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
