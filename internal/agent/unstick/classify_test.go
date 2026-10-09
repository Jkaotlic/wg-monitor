package unstick

import (
	"testing"
	"time"
)

func TestClassify_Table(t *testing.T) {
	cases := []struct {
		status  string
		enabled bool
		kind    Kind
		remedy  Remedy
	}{
		{"broken", true, KindBroken, RemedyRestart},
		{"needs_start", true, KindNeeds, RemedyStart},
		{"needs_start", false, KindNone, ""},
		{"needs_stop", false, KindNeeds, RemedyStop},
		{"needs_stop", true, KindNone, ""},
		{"starting", true, KindTransition, RemedyRestart},
		{"stopping", true, KindTransition, RemedyRestart},
		{"stopping", false, KindTransition, RemedyStop},
		{"running", true, KindNone, ""},
		{"stopped", false, KindNone, ""},
		{"disabled", false, KindNone, ""},
		{"not_created", true, KindNone, ""},
		{"connected", true, KindNone, ""},
		{"", true, KindNone, ""},
	}
	for _, c := range cases {
		k, r := Classify(c.status, c.enabled)
		if k != c.kind || r != c.remedy {
			t.Errorf("Classify(%q,%v) = %v,%q; want %v,%q", c.status, c.enabled, k, r, c.kind, c.remedy)
		}
	}
}

// Выключенный владельцем туннель сторож не поднимает никогда.
func TestClassify_DisabledNeverStarts(t *testing.T) {
	for _, s := range []string{"broken", "starting", "stopping", "needs_stop", "needs_start"} {
		_, r := Classify(s, false)
		if r == RemedyStart || r == RemedyRestart {
			t.Errorf("Classify(%q, enabled=false) = %q: выключенный туннель поднимать нельзя", s, r)
		}
	}
	if k, r := Classify("broken", false); k != KindBroken || r != RemedyStop {
		t.Errorf("broken/disabled = %v,%q; want broken,stop", k, r)
	}
	if k, r := Classify("starting", false); k != KindTransition || r != RemedyStop {
		t.Errorf("starting/disabled = %v,%q; want transition,stop", k, r)
	}
}

func TestResolved(t *testing.T) {
	cases := []struct {
		status  string
		enabled bool
		want    bool
	}{
		{"running", true, true},
		{"stopped", true, false},
		{"broken", true, false},
		{"stopped", false, true},
		{"disabled", false, true},
		{"not_created", false, true},
		{"running", false, false},
		{"needs_stop", false, false},
	}
	for _, c := range cases {
		if got := Resolved(c.status, c.enabled); got != c.want {
			t.Errorf("Resolved(%q,%v) = %v; want %v", c.status, c.enabled, got, c.want)
		}
	}
}

func TestConfig_Defaults(t *testing.T) {
	c := Config{}.withDefaults()
	if c.threshold(KindBroken) != time.Minute || c.threshold(KindNeeds) != time.Minute ||
		c.threshold(KindTransition) != 5*time.Minute {
		t.Errorf("thresholds: %v %v %v", c.threshold(KindBroken), c.threshold(KindNeeds), c.threshold(KindTransition))
	}
	if c.Poll != 30*time.Second || c.Verify1 != time.Minute || c.Verify2 != 90*time.Second ||
		c.ServiceEvery != time.Hour || c.RetryAfterGiveUp != 6*time.Hour || c.GuardWindow != 10*time.Minute {
		t.Errorf("defaults: %+v", c)
	}
	custom := Config{BrokenAfter: 7 * time.Second}.withDefaults()
	if custom.threshold(KindBroken) != 7*time.Second {
		t.Errorf("custom broken: %v", custom.threshold(KindBroken))
	}
}
