package pingruns

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/Jkaotlic/wg-monitor/internal/agent/awgmgr"
)

type fakeLogs struct {
	entries []awgmgr.PingCheckLogEntry
	err     error
}

func (f *fakeLogs) PingCheckLogs(context.Context) ([]awgmgr.PingCheckLogEntry, error) {
	return f.entries, f.err
}

func entry(ts, id string, ok bool, failCount int, stateChange, errText string) awgmgr.PingCheckLogEntry {
	return awgmgr.PingCheckLogEntry{Timestamp: ts, TunnelID: id, TunnelName: "NL", Success: ok,
		FailCount: failCount, Threshold: 3, StateChange: stateChange, Error: errText}
}

// Живой workrouter 28.09: одни успехи раз в ~45 с -- ни одной серии.
func TestLiveShapeAllSuccessGivesNoRuns(t *testing.T) {
	tr := &Tracker{Src: &fakeLogs{entries: []awgmgr.PingCheckLogEntry{
		entry("2026-09-28T07:44:50Z", "awg11", true, 0, "", ""),
		entry("2026-09-28T07:44:50Z", "awg10", true, 0, "", ""),
		entry("2026-09-28T07:44:05Z", "awg11", true, 0, "", ""),
		entry("2026-09-28T07:44:05Z", "awg10", true, 0, "", ""),
	}}}
	tr.Poll(context.Background())
	if got := tr.Pending(); len(got) != 0 {
		t.Fatalf("серии из одних успехов: %+v", got)
	}
	log := tr.Log()
	if log == nil || log.State != StateOK || fmt.Sprint(log.Tunnels) != "[awg10 awg11]" {
		t.Fatalf("log = %+v", log)
	}
}

func TestFailRunThenRecoveryIsOneClosedRun(t *testing.T) {
	tr := &Tracker{Src: &fakeLogs{entries: []awgmgr.PingCheckLogEntry{ // свежие сверху, как у API
		entry("2026-09-28T03:17:00Z", "awg11", true, 0, "up", ""),
		entry("2026-09-28T03:16:15Z", "awg11", false, 3, "down", "timeout"),
		entry("2026-09-28T03:15:30Z", "awg11", false, 2, "", "timeout"),
		entry("2026-09-28T03:14:45Z", "awg11", false, 1, "", "timeout"),
		entry("2026-09-28T03:14:00Z", "awg11", true, 0, "", ""),
	}}}
	tr.Poll(context.Background())
	got := tr.Pending()
	if len(got) != 1 {
		t.Fatalf("серий %d, хотим 1: %+v", len(got), got)
	}
	r := got[0]
	if r.From.Format("15:04:05") != "03:14:45" || r.To.Format("15:04:05") != "03:17:00" ||
		r.Fails != 3 || !r.WentDown || !r.Recovered || r.Error != "timeout" {
		t.Fatalf("серия = %+v", r)
	}
	if _, down := tr.DownSince("awg11"); down {
		t.Fatal("закрытая серия -- уже не «упал сейчас»")
	}
}

func TestOpenRunIsPendingAndDownSince(t *testing.T) {
	tr := &Tracker{Src: &fakeLogs{entries: []awgmgr.PingCheckLogEntry{
		entry("2026-09-28T03:16:15Z", "awg11", false, 3, "", "timeout"),
		entry("2026-09-28T03:15:30Z", "awg11", false, 2, "", "timeout"),
		entry("2026-09-28T03:14:45Z", "awg11", false, 1, "", "timeout"),
	}}}
	tr.Poll(context.Background())
	got := tr.Pending()
	if len(got) != 1 || got[0].Recovered || !got[0].WentDown || got[0].Fails != 3 {
		t.Fatalf("открытая серия = %+v", got)
	}
	since, down := tr.DownSince("awg11")
	if !down || since.Format("15:04:05") != "03:14:45" {
		t.Fatalf("DownSince = %v %v", since, down)
	}
}

// Буфер awg-manager не меняется между отчётами -- та же серия второй раз не едет.
func TestTrackerDoesNotResendCommittedRuns(t *testing.T) {
	src := &fakeLogs{entries: []awgmgr.PingCheckLogEntry{
		entry("2026-09-28T03:17:00Z", "awg11", true, 0, "", ""),
		entry("2026-09-28T03:16:15Z", "awg11", false, 1, "", "timeout"),
	}}
	tr := &Tracker{Src: src}
	tr.Poll(context.Background())
	sent := tr.Pending()
	if len(sent) != 1 {
		t.Fatalf("pending = %+v", sent)
	}
	tr.Commit(sent)
	tr.Poll(context.Background())
	if got := tr.Pending(); len(got) != 0 {
		t.Fatalf("принятая серия ушла повторно: %+v", got)
	}
}

func TestUncommittedRunsStayPending(t *testing.T) {
	tr := &Tracker{Src: &fakeLogs{entries: []awgmgr.PingCheckLogEntry{
		entry("2026-09-28T03:17:00Z", "awg11", true, 0, "", ""),
		entry("2026-09-28T03:16:15Z", "awg11", false, 1, "", "timeout"),
	}}}
	tr.Poll(context.Background())
	tr.Poll(context.Background()) // отчёт не дошёл, Commit не было
	if got := tr.Pending(); len(got) != 1 {
		t.Fatalf("серия потерялась без подтверждения: %+v", got)
	}
}

func TestUnsupportedLogState(t *testing.T) {
	tr := &Tracker{Src: &fakeLogs{err: fmt.Errorf("wrap: %w", awgmgr.ErrUnsupportedByRouter)}}
	tr.Poll(context.Background())
	if log := tr.Log(); log == nil || log.State != StateUnsupported {
		t.Fatalf("log = %+v", log)
	}
	if got := tr.Pending(); len(got) != 0 {
		t.Fatalf("pending = %+v", got)
	}
}

func TestPendingCapped(t *testing.T) {
	var entries []awgmgr.PingCheckLogEntry
	base := time.Date(2026, 9, 28, 1, 0, 0, 0, time.UTC)
	for i := 29; i >= 0; i-- { // свежие сверху: 30 серий «неудача -> успех»
		entries = append(entries,
			entry(base.Add(time.Duration(2*i+1)*time.Minute).Format(time.RFC3339), "awg11", true, 0, "", ""),
			entry(base.Add(time.Duration(2*i)*time.Minute).Format(time.RFC3339), "awg11", false, 1, "", "timeout"))
	}
	tr := &Tracker{Src: &fakeLogs{entries: entries}}
	tr.Poll(context.Background())
	if got := tr.Pending(); len(got) != 20 {
		t.Fatalf("pending %d, хотим потолок 20", len(got))
	}
}
