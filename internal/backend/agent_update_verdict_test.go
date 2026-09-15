package backend

import (
	"strings"
	"testing"
)

func TestAgentUpdateVerdictFor(t *testing.T) {
	const backend = "v0.33.0"
	cases := []struct {
		agent       string
		behind      bool
		tooOld      bool
		unknown     bool
		warnHas     []string
		warnMissing []string
	}{
		{agent: "v0.33.0"},
		{agent: "v0.34.0-rc1"},
		{agent: "v0.32.0", behind: true, warnHas: []string{"адрес загрузки"}, warnMissing: []string{"места"}},
		{agent: "v0.25.0", behind: true, warnHas: []string{"адрес загрузки"}, warnMissing: []string{"места"}},
		{agent: "v0.24.3", behind: true, warnHas: []string{"≈10% раздела /opt", "адрес загрузки"}},
		{agent: "v0.14.4", behind: true, warnHas: []string{"≈10% раздела /opt", "адрес загрузки"}, warnMissing: []string{"64 МБ"}},
		{agent: "v0.14.1", behind: true, warnHas: []string{"64 МБ", "адрес загрузки"}},
		{agent: "v0.13.0-rc60", behind: true, warnHas: []string{"64 МБ"}, warnMissing: []string{"адрес загрузки"}},
		{agent: "v0.13.0-rc32", behind: true, warnHas: []string{"64 МБ"}},
		// rc4 < rc32 числом, хотя строкой "rc4" > "rc32" -- ради этого случая
		// сравнение не через x/mod/semver.
		{agent: "v0.13.0-rc4", behind: true, tooOld: true},
		{agent: "v0.12.9", behind: true, tooOld: true},
		{agent: "", unknown: true},
		{agent: "dev", unknown: true},
	}
	for _, c := range cases {
		got := agentUpdateVerdictFor(c.agent, backend)
		if got.Behind != c.behind || got.TooOld != c.tooOld || got.Unknown != c.unknown {
			t.Errorf("%q: %+v, ждали behind=%v tooOld=%v unknown=%v", c.agent, got, c.behind, c.tooOld, c.unknown)
		}
		for _, s := range c.warnHas {
			if !strings.Contains(got.Warning, s) {
				t.Errorf("%q: в предупреждении %q нет %q", c.agent, got.Warning, s)
			}
		}
		for _, s := range c.warnMissing {
			if strings.Contains(got.Warning, s) {
				t.Errorf("%q: в предупреждении %q лишнее %q", c.agent, got.Warning, s)
			}
		}
		if (!c.behind || c.tooOld) && got.Warning != "" {
			t.Errorf("%q: предупреждение там, где обновлять нечего или нельзя: %q", c.agent, got.Warning)
		}
		for _, banned := range []string{"self_update", "pending", "repo_base"} {
			if strings.Contains(got.Warning, banned) {
				t.Errorf("%q: внутреннее имя %q в тексте", c.agent, banned)
			}
		}
	}
}

// Бэкенд собран без тега (dev): сравнивать не с чем -- никто не «отстал».
func TestAgentUpdateVerdictWithUnknownBackend(t *testing.T) {
	got := agentUpdateVerdictFor("v0.20.0", "unknown")
	if got.Behind || got.Warning != "" {
		t.Fatalf("бэкенд без версии: %+v", got)
	}
	if got := agentUpdateVerdictFor("v0.12.0", "unknown"); !got.TooOld {
		t.Fatalf("слишком старый агент остаётся слишком старым и без версии бэкенда: %+v", got)
	}
}
