package linkrepair

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/Jkaotlic/wg-monitor/pkg/wire"
)

func TestAutomaticRepairCooldownSurvivesRestart(t *testing.T) {
	now := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
	kv := fakeKV{}
	a := Attempts{KV: kv, Now: func() time.Time { return now }}
	if err := a.Started("r", "tunnel_a"); err != nil {
		t.Fatal(err)
	}
	a = Attempts{KV: kv, Now: func() time.Time { return now }}
	_ = a.Clear("r", "tunnel_a")
	now = now.Add(9 * time.Minute)
	if ok, _ := a.Allow("r", "tunnel_a"); ok {
		t.Fatal("automatic repair allowed inside ten minute cooldown")
	}
	now = now.Add(time.Minute)
	if ok, why := a.Allow("r", "tunnel_a"); !ok {
		t.Fatal(why)
	}
}

func TestWorkingTunnelAvoidsMutationAndAttemptCharge(t *testing.T) {
	e := newLadder(t, &Setting{Enabled: true})
	e.cmd.hs = true
	e.cmd.via = exitTunnel
	job, call := e.run(t, ladderReq())
	if e.cmd.countOf("tunnel_restart") != 0 || len(e.cmd.promotedTo()) != 0 {
		t.Fatal("working tunnel was mutated")
	}
	if !strings.Contains(call.Text, "провер") {
		t.Fatalf("missing explanation: %s (%v)", call.Text, job.State)
	}
	log, err := e.d.Attempts.load("роутер", "tunnel_awg12")
	if err != nil || len(log.At) != 0 {
		t.Fatalf("confirmation charged: %+v %v", log, err)
	}
}

func TestRecurrentFaultKeepsVerifiedBackup(t *testing.T) {
	e := newLadder(t, &Setting{Enabled: true})
	_ = e.d.Attempts.Record("роутер", "tunnel_awg12", true)
	e.cmd.on = map[string]func(*scriptCommander, int){"tunnel_restart": func(c *scriptCommander, _ int) { c.hs = true; c.via = exitTunnel }}
	_, call := e.run(t, ladderReq())
	if got := e.cmd.promotedTo(); len(got) != 1 || got[0] != "awg10" {
		t.Fatalf("recurrent fault failed back: %v", got)
	}
	if !strings.Contains(call.Text, "запас") {
		t.Fatalf("missing backup outcome: %s", call.Text)
	}
}

func TestIncidentEvidenceBoundedAndSanitized(t *testing.T) {
	kv := fakeKV{}
	e := Evidence{KV: kv}
	check := wire.Check{Name: "tunnel_a", Status: "fail", Details: map[string]any{"failure_kind": "probe", "private_key": "SECRET", "tunnel_confirmation": map[string]any{"status": "failed", "token": "SECRET"}}}
	snap := wire.RouteSnapshot{Tunnels: []wire.TunnelMeta{{ID: "a", HasHandshake: true, HandshakeAge: 4}}, Rules: []wire.RouteRuleSummary{{}}}
	for i := 0; i < 20; i++ {
		if err := e.Record("r", "tunnel_a", check, snap); err != nil {
			t.Fatal(err)
		}
	}
	raw, _ := kv.Get(evidenceKey("r", "tunnel_a"))
	if strings.Contains(raw, "SECRET") || strings.Contains(raw, "rules") {
		t.Fatalf("unsafe evidence: %s", raw)
	}
	var rows []json.RawMessage
	if err := json.Unmarshal([]byte(raw), &rows); err != nil || len(rows) != 8 {
		t.Fatalf("retention %d: %v", len(rows), err)
	}
	if !strings.Contains(raw, "handshake_age_sec") || !strings.Contains(raw, "failure_kind") {
		t.Fatalf("missing useful evidence: %s", raw)
	}
}

func TestIncidentSavedBeforeMutation(t *testing.T) {
	e := newLadder(t, &Setting{Enabled: true})
	e.d.LatestCheck = func(int64, string) (wire.Check, error) {
		return wire.Check{Status: "fail", Details: map[string]any{"failure_kind": "data_path", "tunnel_confirmation": "unreachable", "confirmation_at": "2026-11-08T12:00:00Z", "confirmation_results": []any{map[string]any{"target": "google", "state": "unreachable", "reason": "request_failed", "latency_ms": 12, "token": "SECRET"}}}}, nil
	}
	captured := false
	e.cmd.on = map[string]func(*scriptCommander, int){"route_policy_promote": func(_ *scriptCommander, _ int) {
		raw, _ := e.d.Attempts.KV.Get(evidenceKey("роутер", "tunnel_awg12"))
		if raw != "" {
			captured = true
		}
	}}
	e.run(t, ladderReq())
	raw, _ := e.d.Attempts.KV.Get(evidenceKey("роутер", "tunnel_awg12"))
	if !captured || !strings.Contains(raw, `"target":"google"`) || !strings.Contains(raw, `"state":"unreachable"`) || !strings.Contains(raw, `"reason":"request_failed"`) || strings.Contains(raw, "SECRET") {
		t.Fatalf("missing safe pre-mutation evidence: %s", raw)
	}
}

func TestRecurrentFaultDoesNotHoldUnprovenBackup(t *testing.T) {
	e := newLadder(t, &Setting{Enabled: true})
	_ = e.d.Attempts.Record("роутер", "tunnel_awg12", true)
	e.cmd.on = map[string]func(*scriptCommander, int){
		"tunnel_restart": func(c *scriptCommander, _ int) { c.hs = true; c.via = exitTunnel },
		"exit_ip_probe": func(c *scriptCommander, _ int) {
			if c.sent[len(c.sent)-1].Args["tunnel_id"] == "awg10" {
				c.via = exitDirect
			}
		},
	}
	_, call := e.run(t, ladderReq())
	if got := e.cmd.promotedTo(); len(got) != 2 || got[1] != "awg12" {
		t.Fatalf("left traffic on unproven backup: %v %s", got, call.Text)
	}
}

func TestStructuralFailureIsNotClearedByExitProbe(t *testing.T) {
	e := newLadder(t, &Setting{Enabled: true})
	e.cmd.hs = true
	e.cmd.via = exitTunnel
	e.d.LatestCheck = func(int64, string) (wire.Check, error) {
		return wire.Check{Status: "fail", Details: map[string]any{"failure_kind": "address_conflict"}}, nil
	}
	e.run(t, ladderReq())
	if e.cmd.countOf("tunnel_restart") == 0 {
		t.Fatal("structural conflict hidden by exit probe")
	}
}
func TestMoveOnlySavesEvidenceBeforeMutation(t *testing.T) {
	e := newLadder(t, nil)
	e.d.LatestCheck = func(int64, string) (wire.Check, error) {
		return wire.Check{Status: "fail", Details: map[string]any{"failure_kind": "data_path"}}, nil
	}
	captured := false
	e.cmd.on = map[string]func(*scriptCommander, int){"route_policy_promote": func(_ *scriptCommander, _ int) {
		raw, _ := e.d.Attempts.KV.Get(evidenceKey("роутер", "tunnel_awg12"))
		captured = raw != ""
	}}
	sc, _ := ScenarioFor("tunnel_awg12")
	e.d.moveOnly(ladderReq(), sc)
	if !captured {
		t.Fatal("move-only mutation has no saved incident evidence")
	}
}
func TestIncidentFreeFormReasonCannotPersistSecrets(t *testing.T) {
	kv := fakeKV{}
	err := (Evidence{KV: kv}).Record("r", "tunnel_a", wire.Check{Details: map[string]any{"reason": "SECRET", "confirmation_results": []any{map[string]any{"reason": "SECRET", "target": "google", "state": "unknown"}}}}, wire.RouteSnapshot{})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := kv.Get(evidenceKey("r", "tunnel_a"))
	if strings.Contains(raw, "SECRET") {
		t.Fatalf("free form credential persisted: %s", raw)
	}
}

func TestFailureInsideCooldownMovesToVerifiedBackupAndHoldsAcrossRestart(t *testing.T) {
	e := newLadder(t, &Setting{Enabled: true})
	now := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
	e.d.Attempts.Now = func() time.Time { return now }
	e.d.Now = func() time.Time { return now }
	fixOn(e.cmd, "tunnel_restart", 1, true)
	e.run(t, ladderReq())
	now = now.Add(5 * time.Minute)
	e.cmd.mu.Lock()
	e.cmd.hs = false
	e.cmd.via = exitDirect
	e.cmd.on = map[string]func(*scriptCommander, int){"exit_ip_probe": func(c *scriptCommander, _ int) {
		if c.sent[len(c.sent)-1].Args["tunnel_id"] == "awg10" {
			c.via = exitTunnel
		} else {
			c.via = exitDirect
		}
	}}
	e.cmd.mu.Unlock()
	if _, err := e.d.Start(ladderReq()); err == nil {
		t.Fatal("restart ladder accepted inside cooldown")
	}
	deadline := time.Now().Add(time.Second)
	for e.d.Moves.Busy() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if e.cmd.countOf("tunnel_restart") != 1 {
		t.Fatal("cooldown restarted tunnel")
	}
	got := e.cmd.promotedTo()
	if len(got) != 3 || got[2] != "awg10" {
		t.Fatalf("cooldown stranded traffic: %v", got)
	}
	// New process memory, same persistent KV: a recovery must not auto-failback.
	d := e.d
	d.Moves = NewMoveState()
	if !d.HasMove("роутер", "tunnel_awg12") || d.ReturnDue(ladderReq(), ReportChains{"hydraroute": {"awg10", "awg12"}}) {
		t.Fatal("recurrent backup hold did not survive restart")
	}
}

func TestConfiguredPingFailureIsNotClearedByExitProbe(t *testing.T) {
	e := newLadder(t, &Setting{Enabled: true})
	e.cmd.hs = true
	e.cmd.via = exitTunnel
	e.d.LatestCheck = func(int64, string) (wire.Check, error) {
		return wire.Check{Status: "fail", Details: map[string]any{"error": "pingCheck=dead (fails 3/3)"}}, nil
	}
	e.run(t, ladderReq())
	if e.cmd.countOf("tunnel_restart") == 0 {
		t.Fatal("configured ping failure hidden by exit probe")
	}
}

func TestCooldownBackupHoldSurvivesFailedFirstMoveAndReportRetries(t *testing.T) {
	e := newLadder(t, &Setting{Enabled: true})
	now := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
	e.d.Attempts.Now = func() time.Time { return now }
	e.d.Now = func() time.Time { return now }
	if err := e.d.Attempts.Started("роутер", "tunnel_awg12"); err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Minute)
	// Exit probes fail, so the first cooldown move must decline the backup.
	if _, err := e.d.Start(ladderReq()); err == nil {
		t.Fatal("cooldown ladder accepted")
	}
	wait := func() {
		t.Helper()
		deadline := time.Now().Add(2 * time.Second)
		for e.d.Moves.Busy() && time.Now().Before(deadline) {
			time.Sleep(time.Millisecond)
		}
		if e.d.Moves.Busy() {
			t.Fatal("move did not finish")
		}
	}
	wait()
	if len(e.cmd.promotedTo()) != 0 {
		t.Fatal("first cooldown attempt used unproven backup")
	}
	// A report retry after restart must preserve both proof and hold requirements.
	now = now.Add(6 * time.Minute)
	e.d.Moves = NewMoveState()
	e.d.AfterFailedReport(ladderReq(), nil, true)
	wait()
	if len(e.cmd.promotedTo()) != 0 {
		t.Fatal("report retry used unproven backup")
	}
	// Even after repair cooldown expires, the same incident's pending hold applies.
	now = now.Add(6 * time.Minute)
	e.d.Moves = NewMoveState()
	e.cmd.mu.Lock()
	e.cmd.on = map[string]func(*scriptCommander, int){"exit_ip_probe": func(c *scriptCommander, _ int) {
		if c.sent[len(c.sent)-1].Args["tunnel_id"] == "awg10" {
			c.via = exitTunnel
		}
	}}
	e.cmd.mu.Unlock()
	e.d.AfterFailedReport(ladderReq(), nil, true)
	wait()
	if got := e.cmd.promotedTo(); len(got) != 1 || got[0] != "awg10" {
		t.Fatalf("verified retry did not move: %v", got)
	}
	rec, found, err := e.d.loadMoved("роутер", "tunnel_awg12")
	if err != nil || !found || !rec.HoldBackup {
		t.Fatalf("retry lost backup hold: %+v %v", rec, err)
	}
	e.d.Moves = NewMoveState()
	if e.d.ReturnDue(ladderReq(), ReportChains{"hydraroute": {"awg10", "awg12"}}) {
		t.Fatal("retry backup would auto-failback after recovery")
	}
}
