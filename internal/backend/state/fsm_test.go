package state

import (
	"testing"
	"time"

	"github.com/anex/wg-monitor/internal/backend/db"
)

func TestFSM_OkOk_NoOp(t *testing.T) {
	prev := db.IncidentState{CurrentStatus: "ok"}
	tr := Apply(prev, "ok", time.Now(), Thresholds{Fail: 3, Recovery: 2})
	if tr.Kind != Noop {
		t.Fatalf("got %v", tr)
	}
}

func TestFSM_OkFail_StartsCounting(t *testing.T) {
	prev := db.IncidentState{CurrentStatus: "ok"}
	tr := Apply(prev, "fail", time.Now(), Thresholds{Fail: 3, Recovery: 2})
	if tr.Kind != Soft {
		t.Fatalf("kind=%v next=%+v", tr.Kind, tr.Next)
	}
	if tr.Next.ConsecutiveFails != 1 || tr.Next.CurrentStatus != "fail" {
		t.Fatalf("next: %+v", tr.Next)
	}
}

func TestFSM_FailFail_Hardens_ExactlyAtThreshold(t *testing.T) {
	prev := db.IncidentState{CurrentStatus: "fail", ConsecutiveFails: 2}
	now := time.Now()
	tr := Apply(prev, "fail", now, Thresholds{Fail: 3, Recovery: 2})
	if tr.Kind != Hard {
		t.Fatalf("kind=%v", tr.Kind)
	}
	if tr.Next.CurrentStatus != "hard" {
		t.Fatalf("next status: %s", tr.Next.CurrentStatus)
	}
	if tr.Next.HardSince == nil || !tr.Next.HardSince.Equal(now) {
		t.Fatalf("hard_since: %v", tr.Next.HardSince)
	}
}

func TestFSM_FailFail_AfterHard_StaysHard(t *testing.T) {
	prev := db.IncidentState{CurrentStatus: "hard", ConsecutiveFails: 5}
	tr := Apply(prev, "fail", time.Now(), Thresholds{Fail: 3, Recovery: 2})
	if tr.Kind != Noop {
		t.Fatalf("hard+fail must noop, got %v", tr.Kind)
	}
}

func TestFSM_FailOk_FromSoft_FlipsBackToOk(t *testing.T) {
	prev := db.IncidentState{CurrentStatus: "fail", ConsecutiveFails: 2}
	tr := Apply(prev, "ok", time.Now(), Thresholds{Fail: 3, Recovery: 2})
	if tr.Kind != SoftFlap {
		t.Fatalf("kind=%v", tr.Kind)
	}
	if tr.Next.ConsecutiveFails != 0 || tr.Next.CurrentStatus != "ok" {
		t.Fatalf("next: %+v", tr.Next)
	}
}

func TestFSM_HardOk_NeedsTwoConsecutiveOK(t *testing.T) {
	prev := db.IncidentState{CurrentStatus: "hard", ConsecutiveOKs: 0}
	tr := Apply(prev, "ok", time.Now(), Thresholds{Fail: 3, Recovery: 2})
	if tr.Kind != Noop {
		t.Fatalf("first ok in hard should noop, got %v (next=%+v)", tr.Kind, tr.Next)
	}
	if tr.Next.ConsecutiveOKs != 1 {
		t.Fatalf("oks: %d", tr.Next.ConsecutiveOKs)
	}

	prev = tr.Next
	tr = Apply(prev, "ok", time.Now(), Thresholds{Fail: 3, Recovery: 2})
	if tr.Kind != Recovery {
		t.Fatalf("second ok should recover, got %v", tr.Kind)
	}
	if tr.Next.CurrentStatus != "ok" {
		t.Fatalf("recovery should set ok, got %s", tr.Next.CurrentStatus)
	}
}

func TestFSM_HardFail_OkResetCountIfBroken(t *testing.T) {
	// hard → fail → ok → fail: after the second fail oks must reset to 0
	prev := db.IncidentState{CurrentStatus: "hard", ConsecutiveOKs: 1}
	tr := Apply(prev, "fail", time.Now(), Thresholds{Fail: 3, Recovery: 2})
	if tr.Next.ConsecutiveOKs != 0 {
		t.Fatalf("should reset oks, got %d", tr.Next.ConsecutiveOKs)
	}
}

func TestApplyHardToOKZeroesAcked(t *testing.T) {
	prev := db.IncidentState{
		CurrentStatus: "hard", ConsecutiveOKs: 1, Acked: true,
	}
	now := time.Now()
	tr := Apply(prev, "ok", now, Thresholds{Fail: 3, Recovery: 2})
	if tr.Kind != Recovery {
		t.Fatalf("expected Recovery, got %v", tr.Kind)
	}
	if tr.Next.Acked {
		t.Errorf("recovery should zero Acked, got Acked=true")
	}
}

func TestApplyHardToFailKeepsAcked(t *testing.T) {
	prev := db.IncidentState{
		CurrentStatus: "hard", ConsecutiveFails: 3, Acked: true,
	}
	now := time.Now()
	tr := Apply(prev, "fail", now, Thresholds{Fail: 3, Recovery: 2})
	if !tr.Next.Acked {
		t.Errorf("hard→fail (no transition) must preserve Acked=true")
	}
}
