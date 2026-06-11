package awgmgr

import (
	"encoding/json"
	"testing"
	"time"
)

func TestNullableTime_UnmarshalJSON(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		wantNil bool
		wantErr bool
	}{
		{"null literal", `null`, true, false},
		{"empty string", `""`, true, false},
		{"valid RFC3339", `"2026-05-05T13:23:45Z"`, false, false},
		{"valid RFC3339Nano", `"2026-05-05T13:23:45.123456789Z"`, false, false},
		{"valid RFC3339 with offset", `"2026-05-05T16:23:45+03:00"`, false, false},
		{"garbage", `"not a date"`, false, true},
		{"zero year", `"0001-01-01T00:00:00Z"`, true, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var nt nullableTime
			err := json.Unmarshal([]byte(c.in), &nt)
			if c.wantErr {
				if err == nil {
					t.Fatalf("want error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			got := nt.Time()
			if c.wantNil && got != nil {
				t.Fatalf("want nil, got %v", got)
			}
			if !c.wantNil && got == nil {
				t.Fatalf("want non-nil, got nil")
			}
		})
	}
}

func TestNullableTime_StructDecode(t *testing.T) {
	body := `{"lastHandshake":"","startedAt":"2026-05-05T10:00:00Z"}`
	var s struct {
		LastHandshake nullableTime `json:"lastHandshake"`
		StartedAt     nullableTime `json:"startedAt"`
	}
	if err := json.Unmarshal([]byte(body), &s); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if s.LastHandshake.Time() != nil {
		t.Fatalf("LastHandshake: want nil for empty string, got %v", s.LastHandshake.Time())
	}
	if s.StartedAt.Time() == nil {
		t.Fatalf("StartedAt: want non-nil")
	}
	want := time.Date(2026, 5, 5, 10, 0, 0, 0, time.UTC)
	if !s.StartedAt.Time().Equal(want) {
		t.Fatalf("StartedAt: got %v, want %v", s.StartedAt.Time(), want)
	}
}
