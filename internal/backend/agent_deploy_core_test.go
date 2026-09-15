package backend

import (
	"errors"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Jkaotlic/wg-monitor/internal/backend/db"
)

func coreTestRouter(t *testing.T, installed string) (*db.DB, *db.User) {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	uid, err := d.Users().Insert("bronya", strings.Repeat("9", 64), "198.51.100.7", "awg0")
	if err != nil {
		t.Fatal(err)
	}
	if installed != "" {
		if err := d.Users().UpdateLastSeenAgentVersion(uid, installed); err != nil {
			t.Fatal(err)
		}
	}
	u, err := d.Users().GetByID(uid)
	if err != nil {
		t.Fatal(err)
	}
	return d, u
}

func TestAgentDeployCoreQueuesAndMarks(t *testing.T) {
	d, u := coreTestRouter(t, "v0.31.0")
	sink := &fakeCmdSink{}
	now := time.Date(2026, 9, 15, 10, 0, 0, 0, time.UTC)
	res, derr := agentDeployCore(Deps{DB: d, CommandSink: sink}, u, "v0.32.0", agentDeployOpts{
		RepoBaseURL: "https://backend.example.com",
		ResolveIP:   func() string { return "203.0.113.5" },
		Now:         now,
		Source:      "miniapp",
	})
	if derr != nil {
		t.Fatalf("ошибка ядра: %+v", derr)
	}
	if res.TargetVersion != "v0.32.0" || res.CmdID == "" {
		t.Fatalf("результат: %+v", res)
	}
	q := sink.snapshotEnqueued()
	if len(q) != 1 || q[0].Action != "self_update" || q[0].ID != res.CmdID {
		t.Fatalf("очередь: %+v", q)
	}
	if q[0].Args["version"] != "v0.32.0" ||
		q[0].Args["repo_base"] != "https://backend.example.com/v1/releases/download" ||
		q[0].Args["repo_resolve_ip"] != "203.0.113.5" {
		t.Fatalf("аргументы: %+v", q[0].Args)
	}
	st, _ := d.Users().PendingDeploy(u.ID)
	if st.Version != "v0.32.0" || st.Since != now.Format(time.RFC3339) || st.Attempts != 0 {
		t.Fatalf("отметка: %+v", st)
	}
}

func TestAgentDeployCoreRefusals(t *testing.T) {
	resolveCalls := 0
	opts := agentDeployOpts{
		RepoBaseURL: "https://backend.example.com",
		ResolveIP:   func() string { resolveCalls++; return "" },
	}

	d, u := coreTestRouter(t, "v0.31.0")
	sink := &fakeCmdSink{}
	if _, derr := agentDeployCore(Deps{DB: d, CommandSink: sink}, u, "unknown", opts); derr == nil ||
		derr.Code != deployErrNoRelease || derr.Status != http.StatusConflict {
		t.Fatalf("тег: %+v", derr)
	}
	if _, derr := agentDeployCore(Deps{DB: d, CommandSink: sink}, u, "v0.30.0", opts); derr == nil ||
		derr.Code != deployErrDowngrade || derr.Status != http.StatusBadRequest {
		t.Fatalf("даунгрейд: %+v", derr)
	}
	if err := d.Users().MarkPendingDeploy(u.ID, "v0.32.0", "2026-09-15T10:00:00Z"); err != nil {
		t.Fatal(err)
	}
	u, _ = d.Users().GetByID(u.ID)
	if _, derr := agentDeployCore(Deps{DB: d, CommandSink: sink}, u, "v0.32.0", opts); derr == nil ||
		derr.Code != deployErrPending || derr.Status != http.StatusConflict {
		t.Fatalf("уже назначено: %+v", derr)
	}
	if len(sink.snapshotEnqueued()) != 0 {
		t.Fatal("отказ поставил команду")
	}
	if resolveCalls != 0 {
		t.Fatalf("адрес загрузки разрешался %d раз при отказах", resolveCalls)
	}
}

func TestAgentDeployCoreAllowsDowngradeWhenAsked(t *testing.T) {
	d, u := coreTestRouter(t, "v0.31.0")
	sink := &fakeCmdSink{}
	if _, derr := agentDeployCore(Deps{DB: d, CommandSink: sink}, u, "v0.30.0", agentDeployOpts{
		RepoBaseURL: "https://backend.example.com", AllowDowngrade: true,
	}); derr != nil {
		t.Fatalf("разрешённый даунгрейд: %+v", derr)
	}
}

func TestAgentDeployCoreRollsBackMarkWhenEnqueueFails(t *testing.T) {
	d, u := coreTestRouter(t, "v0.31.0")
	_, derr := agentDeployCore(Deps{DB: d, CommandSink: failingEnqueueSink{err: errors.New("queue closed")}}, u, "v0.32.0",
		agentDeployOpts{RepoBaseURL: "https://backend.example.com"})
	if derr == nil || derr.Status != http.StatusInternalServerError || derr.Code != errCodeInternal {
		t.Fatalf("ошибка постановки: %+v", derr)
	}
	if st, _ := d.Users().PendingDeploy(u.ID); st.Version != "" {
		t.Fatalf("отметка осталась после сорванной постановки: %+v", st)
	}
}

func TestCancelAgentDeployDropsQueueAndMark(t *testing.T) {
	d, u := coreTestRouter(t, "v0.31.0")
	if err := d.Users().MarkPendingDeploy(u.ID, "v0.32.0", "2026-09-15T10:00:00Z"); err != nil {
		t.Fatal(err)
	}
	sink := &dashboardActionSink{}
	cleared, _, err := cancelAgentDeploy(Deps{DB: d, CommandSink: sink}, u)
	if err != nil || !cleared {
		t.Fatalf("отмена: cleared=%v err=%v", cleared, err)
	}
	if len(sink.droppedActions) != 1 || sink.droppedActions[0] != "self_update" {
		t.Fatalf("очередь не почищена: %v", sink.droppedActions)
	}
	cleared, _, err = cancelAgentDeploy(Deps{DB: d, CommandSink: sink}, u)
	if err != nil || cleared {
		t.Fatalf("повторная отмена: cleared=%v err=%v", cleared, err)
	}
}
