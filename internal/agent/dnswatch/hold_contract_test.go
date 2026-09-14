package dnswatch

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Jkaotlic/wg-monitor/internal/agent/dnswatchcfg"
)

// dnswatchcfg.Hold читает эту запись своей копией json-тегов. Переименованный
// тег в persisted иначе молча открыл бы выключение сторожа на запасных.
func TestHoldReadsTheRealRecord(t *testing.T) {
	dir := t.TempDir()
	for _, tc := range []struct {
		name string
		p    persisted
		want string
	}{
		{"чистый primary", persisted{Mode: ModePrimary, LastSwitch: time.Now()}, ""},
		{"fallback", persisted{Mode: ModeFallback, AppliedLines: []string{"a"}}, dnswatchcfg.HoldFallback},
		{"pending", persisted{Mode: ModePrimary, Pending: pendingReturn}, dnswatchcfg.HoldPending},
		{"leftover", persisted{Mode: ModePrimary, Leftover: []string{"a"}}, dnswatchcfg.HoldCleanup},
		{"missing", persisted{Mode: ModePrimary, Missing: []string{"a"}}, dnswatchcfg.HoldCleanup},
	} {
		body, err := json.Marshal(tc.p)
		if err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(dir, tc.name+".json")
		if err := os.WriteFile(path, body, 0o600); err != nil {
			t.Fatal(err)
		}
		if got, err := dnswatchcfg.Hold(path); err != nil || got != tc.want {
			t.Errorf("%s: Hold=%q err=%v, want %q (%s)", tc.name, got, err, tc.want, body)
		}
	}
}
