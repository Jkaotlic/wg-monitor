package awgmgr

import (
	"bytes"
	"encoding/json"
	"fmt"
	"time"
)

// nullableTime parses awg-manager's RFC3339 time fields, accepting both
// JSON `null` and the empty string `""` as sentinels for "no value".
// awg-manager's /api/tunnels/all emits `"lastHandshake":""` for tunnels
// that have never handshaken, which would otherwise crash json.Unmarshal
// into *time.Time with `parsing time "" as "2006-01-02T..."`.
type nullableTime struct {
	t *time.Time
}

func (n *nullableTime) UnmarshalJSON(b []byte) error {
	if bytes.Equal(b, []byte("null")) {
		n.t = nil
		return nil
	}
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return fmt.Errorf("nullableTime: expected string or null, got %s: %w", string(b), err)
	}
	if s == "" {
		n.t = nil
		return nil
	}
	parsed, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return fmt.Errorf("nullableTime: parse %q: %w", s, err)
	}
	if parsed.Year() <= 1 {
		n.t = nil
		return nil
	}
	n.t = &parsed
	return nil
}

func (n nullableTime) MarshalJSON() ([]byte, error) {
	if n.t == nil {
		return []byte("null"), nil
	}
	return json.Marshal(n.t.UTC().Format(time.RFC3339))
}

// Time returns the underlying *time.Time (nil if the field was empty/null).
func (n nullableTime) Time() *time.Time { return n.t }
