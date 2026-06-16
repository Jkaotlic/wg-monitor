package upstream

import (
	"strings"
	"testing"
)

func TestSoftwareNewerThan(t *testing.T) {
	cases := []struct {
		name      string
		installed string
		candidate string
		want      bool
	}{
		{"plain newer", "2.8.2", "2.9.0", true},
		{"plain older", "2.9.0", "2.8.2", false},
		{"equal", "2.9.0", "2.9.0", false},
		{"strip leading v installed", "v2.8.2", "2.9.0", true},
		{"strip leading v both", "v2.8.2", "v2.9.0", true},
		{"strip leading V capital", "V1.0.0", "v1.0.1", true},
		{"empty installed → no warning", "", "2.9.0", false},
		{"empty candidate → no warning", "2.8.2", "", false},
		{"junk installed → false (no false warnings)", "junk", "2.9.0", false},
		{"junk candidate → false", "2.8.2", "definitely-not-semver", false},
		{"both junk", "x", "y", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := SoftwareNewerThan(c.installed, c.candidate); got != c.want {
				t.Errorf("SoftwareNewerThan(%q,%q)=%v want %v", c.installed, c.candidate, got, c.want)
			}
		})
	}
}

func TestAwgManagerUpdateHint_NativeBoundaryNeedsReboot(t *testing.T) {
	hint := AwgManagerUpdateHint("2.10.5", "2.10.7", "native")
	for _, want := range []string{"NativeWG", "router reboot"} {
		if !strings.Contains(hint, want) {
			t.Fatalf("hint %q missing %q", hint, want)
		}
	}
}

func TestAwgManagerUpdateHint_SuppressedForKernelBackend(t *testing.T) {
	if hint := AwgManagerUpdateHint("2.10.5", "2.10.7", "kernel"); hint != "" {
		t.Fatalf("kernel backend must not get NativeWG reboot hint, got %q", hint)
	}
}

func TestFirmwareNewerThan(t *testing.T) {
	cases := []struct {
		name      string
		installed string
		candidate string
		want      bool
	}{
		// Standard dotted comparisons
		{"older numeric", "4.2.6", "5.0.1", true},
		{"newer numeric", "5.0.1", "4.2.6", false},
		{"equal", "4.2.6", "4.2.6", false},
		// Multi-level dots like the real Keenetic format
		{"keenetic 11→12 patch", "5.00.C.11.0-0", "5.00.C.12.0-0", true},
		{"keenetic 12→11 reverse", "5.00.C.12.0-0", "5.00.C.11.0-0", false},
		{"keenetic equal", "5.00.C.11.0-0", "5.00.C.11.0-0", false},
		// Alpha component (Keenetic uses letters like C/D in build IDs)
		{"alpha C→D", "5.0.A.1", "5.0.B.1", true},
		{"alpha D→C reverse", "5.0.B.1", "5.0.A.1", false},
		// Length differences
		{"longer candidate, same prefix", "5.0", "5.0.1", true},
		{"longer installed, same prefix", "5.0.1", "5.0", false},
		// Empty fallthroughs
		{"empty installed", "", "5.0.1", false},
		{"empty candidate", "4.2.6", "", false},
		{"overflowing installed segment does not look older", "5.999999999999999999999999999999", "5.1", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := FirmwareNewerThan(c.installed, c.candidate); got != c.want {
				t.Errorf("FirmwareNewerThan(%q,%q)=%v want %v", c.installed, c.candidate, got, c.want)
			}
		})
	}
}

func TestParsePosIntRejectsOverflow(t *testing.T) {
	if n, err := parsePosInt("9223372036854775808"); err == nil {
		t.Fatalf("parsePosInt overflow returned n=%d nil error, want error", n)
	}
}
