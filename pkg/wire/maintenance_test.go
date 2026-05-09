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
	if err != nil { t.Fatalf("marshal: %v", err) }
	var out VersionAudit
	if err := json.Unmarshal(b, &out); err != nil { t.Fatalf("unmarshal: %v", err) }
	if out != in { t.Fatalf("round-trip diverged:\n  in=%+v\n out=%+v", in, out) }
}

func TestFirmwareStatus_RoundTrip(t *testing.T) {
	in := FirmwareStatus{
		Current:   "4.2.6",
		Available: "5.0.1",
		Hint:      "system upgrade is available",
		Channel:   "release",
	}
	b, _ := json.Marshal(in)
	var out FirmwareStatus
	if err := json.Unmarshal(b, &out); err != nil { t.Fatalf("unmarshal: %v", err) }
	if out != in { t.Fatalf("round-trip diverged:\n  in=%+v\n out=%+v", in, out) }
}
