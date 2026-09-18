package backend

import (
	"strings"
	"testing"
	"time"

	"github.com/Jkaotlic/wg-monitor/internal/backend/db"
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
		//
		// B6: агент ниже agentSelfUpdateFloor не умеет self_update вовсе --
		// Behind обязан остаться false (иначе кнопка «Обновить» и счётчик
		// «Обновить всех отставших (N)» на /fleet обещают то, что кончится
		// отказом agent_too_old), а предупреждение — говорить прямо, что
		// нужна переустановка, а не «отстаёт от бэкенда».
		{agent: "v0.13.0-rc4", tooOld: true, warnHas: []string{"слишком старый", "переустановка"}},
		{agent: "v0.12.9", tooOld: true, warnHas: []string{"слишком старый", "переустановка"}},
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
		// Предупреждения не бывает, только когда обновлять нечего (не
		// отстаёт) и переустановка тоже не нужна (не tooOld) -- у tooOld
		// своё предупреждение (см. warnHas выше).
		if !c.behind && !c.tooOld && got.Warning != "" {
			t.Errorf("%q: предупреждение там, где обновлять нечего: %q", c.agent, got.Warning)
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

// «Давно не обновлялся» -- один признак на весь сервер (v0.45): агент ниже
// agentSelfUpdateFloor ИЛИ молчит дольше 30 дней и отстаёт от бэкенда.
func TestAgentLongNotUpdated(t *testing.T) {
	now := time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)
	ago := func(d time.Duration) *time.Time { t := now.Add(-d); return &t }
	ver := func(v string) *string { return &v }
	const backendV = "v0.45.0"
	day := 24 * time.Hour
	cases := []struct {
		name     string
		version  *string
		lastSeen *time.Time
		want     bool
	}{
		{"слишком старый на связи", ver("v0.12.9"), ago(time.Minute), true},
		{"слишком старый, молчит", ver("v0.13.0-rc4"), ago(40 * day), true},
		{"отстаёт и молчит 31 день", ver("v0.44.0"), ago(31 * day), true},
		{"отстаёт, молчит ровно 30 дней", ver("v0.44.0"), ago(30 * day), false},
		{"отстаёт, но на связи", ver("v0.44.0"), ago(time.Hour), false},
		{"свежий и молчит 60 дней", ver("v0.45.0"), ago(60 * day), false},
		{"версия неизвестна", nil, ago(60 * day), false},
		{"мусор вместо версии", ver("dev"), ago(60 * day), false},
		{"отстаёт, ни разу не выходил на связь", ver("v0.44.0"), nil, false},
		{"на границе self_update", ver(agentSelfUpdateFloor), ago(time.Minute), false},
	}
	for _, c := range cases {
		u := &db.User{LastDeployedVersion: c.version, LastSeenAt: c.lastSeen}
		if got := agentLongNotUpdated(u, backendV, now); got != c.want {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
	}
	// Бэкенд без тега выпуска (dev-сборка) -- «отстаёт» не определить, но
	// слишком старый остаётся слишком старым.
	if agentLongNotUpdated(&db.User{LastDeployedVersion: ver("v0.44.0"), LastSeenAt: ago(60 * day)}, "unknown", now) {
		t.Error("dev-бэкенд: отставание не определено, а признак true")
	}
	if !agentLongNotUpdated(&db.User{LastDeployedVersion: ver("v0.12.0"), LastSeenAt: ago(time.Minute)}, "unknown", now) {
		t.Error("dev-бэкенд: слишком старый агент потерян")
	}
}
