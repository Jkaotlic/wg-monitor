package backend

import (
	"bytes"
	"context"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/Jkaotlic/wg-monitor/internal/backend/db"
	"github.com/Jkaotlic/wg-monitor/internal/backend/revive"
)

type fakeAutoReviver struct {
	mu      sync.Mutex
	enabled bool
	calls   []int64
	outcome map[int64]revive.AutoOutcome
}

func (f *fakeAutoReviver) Enabled() bool { return f.enabled }

func (f *fakeAutoReviver) AutoSchedule(_ context.Context, routerID int64) (revive.AutoOutcome, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, routerID)
	if out, ok := f.outcome[routerID]; ok {
		return out, nil
	}
	return revive.AutoScheduled, nil
}

func setAgentState(t *testing.T, d *db.DB, id int64, version string, lastSeen time.Time) {
	t.Helper()
	if _, err := d.SQL().Exec(`UPDATE users SET last_deployed_version = ?, last_seen_at = ? WHERE id = ?`,
		version, lastSeen.UTC().Format(time.RFC3339Nano), id); err != nil {
		t.Fatal(err)
	}
}

// Проход зовёт авто-постановку ровно для «давно не обновлявшихся» и снимает
// бесполезное self_update у слишком старого агента.
func TestAutoRevivePass_PicksLongNotUpdated(t *testing.T) {
	orig := serverVersion
	t.Cleanup(func() { serverVersion = orig })
	serverVersion = "v0.45.0"

	d, tooOldID, freshID, _ := seedMiniappFleet(t)
	silentID, err := d.Users().Insert("router-silent", "tok-silent-000000000000000000000000000000000000000000000000000", "198.51.100.3", "awg11")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)
	setAgentState(t, d, tooOldID, "v0.12.0", now.Add(-time.Minute))
	setAgentState(t, d, freshID, "v0.44.0", now.Add(-time.Hour))
	setAgentState(t, d, silentID, "v0.44.0", now.Add(-40*24*time.Hour))
	if err := d.Users().MarkPendingDeploy(tooOldID, "v0.45.0", now.Format(time.RFC3339)); err != nil {
		t.Fatal(err)
	}
	if err := d.Users().MarkPendingDeploy(silentID, "v0.45.0", now.Format(time.RFC3339)); err != nil {
		t.Fatal(err)
	}

	fake := &fakeAutoReviver{enabled: true}
	sink := &dashboardActionSink{}
	var logs bytes.Buffer
	sum := autoRevivePass(context.Background(), AutoReviveDeps{
		DB: d, Revive: fake, CommandSink: sink, Now: func() time.Time { return now },
		Logger: slog.New(slog.NewTextHandler(&logs, nil)),
	})

	if len(fake.calls) != 2 || fake.calls[0] != tooOldID || fake.calls[1] != silentID {
		t.Fatalf("авто-постановка для %v, want [%d %d]", fake.calls, tooOldID, silentID)
	}
	if sum[revive.AutoScheduled] != 2 {
		t.Fatalf("итог прохода: %v", sum)
	}
	u, _ := d.Users().GetByID(tooOldID)
	if stringValue(u.PendingVersion) != "" || len(sink.droppedActions) != 1 || sink.droppedActions[0] != "self_update" {
		t.Fatalf("self_update слишком старого не снят: pending=%q dropped=%v", stringValue(u.PendingVersion), sink.droppedActions)
	}
	// Молчащий, но умеющий self_update: назначенное обновление остаётся --
	// проснётся живым, обновится сам (оживление тогда закроется «ожил сам»).
	s, _ := d.Users().GetByID(silentID)
	if stringValue(s.PendingVersion) != "v0.45.0" {
		t.Fatalf("self_update молчащего снят: %q", stringValue(s.PendingVersion))
	}
	if !bytes.Contains(logs.Bytes(), []byte("авто-оживление")) {
		t.Fatalf("журнал молчит: %s", logs.String())
	}
}

// Без пароля -- self_update не снимается: оживления нет, отнимать нечего.
func TestAutoRevivePass_KeepsDeployWhenNotScheduled(t *testing.T) {
	orig := serverVersion
	t.Cleanup(func() { serverVersion = orig })
	serverVersion = "v0.45.0"
	d, tooOldID, _, _ := seedMiniappFleet(t)
	now := time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)
	setAgentState(t, d, tooOldID, "v0.12.0", now.Add(-time.Minute))
	if err := d.Users().MarkPendingDeploy(tooOldID, "v0.45.0", now.Format(time.RFC3339)); err != nil {
		t.Fatal(err)
	}
	fake := &fakeAutoReviver{enabled: true, outcome: map[int64]revive.AutoOutcome{tooOldID: revive.AutoNoPassword}}
	sink := &dashboardActionSink{}
	autoRevivePass(context.Background(), AutoReviveDeps{DB: d, Revive: fake, CommandSink: sink, Now: func() time.Time { return now }})
	if u, _ := d.Users().GetByID(tooOldID); stringValue(u.PendingVersion) == "" || len(sink.droppedActions) != 0 {
		t.Fatal("self_update снят без оживления")
	}
}

func TestAutoRevivePass_DisabledDoesNothing(t *testing.T) {
	d, id, _, _ := seedMiniappFleet(t)
	setAgentState(t, d, id, "v0.12.0", time.Now())
	fake := &fakeAutoReviver{enabled: false}
	autoRevivePass(context.Background(), AutoReviveDeps{DB: d, Revive: fake})
	if len(fake.calls) != 0 {
		t.Fatalf("выключенное оживление позвано: %v", fake.calls)
	}
}

// Слишком старый агент на связи всё равно нужно переустанавливать -- этот
// признак отдаётся воркеру оживления (revive.Config.NeedsReinstall).
func TestAgentNeedsReinstall(t *testing.T) {
	v := func(s string) *string { return &s }
	if !AgentNeedsReinstall(&db.User{LastDeployedVersion: v("v0.12.0")}) {
		t.Error("слишком старый -- нужна переустановка")
	}
	for _, u := range []*db.User{{LastDeployedVersion: v("v0.44.0")}, {}, nil} {
		if AgentNeedsReinstall(u) {
			t.Errorf("%+v: переустановка не нужна", u)
		}
	}
}
