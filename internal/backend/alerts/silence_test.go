package alerts

import (
	"context"
	"testing"
	"time"

	"github.com/Jkaotlic/wg-monitor/internal/backend/db"
	"github.com/Jkaotlic/wg-monitor/internal/backend/state"
)

// BOT-02: человек нажал «Тише на час» -- новая тревога и «восстановилось» по
// той же проверке внутри этого часа приходят без звука, а не громко.
func TestDispatcherSilencedHardAndRecoveryGoQuietly(t *testing.T) {
	for _, tc := range []struct {
		name     string
		silenced *time.Time
		want     bool
	}{
		{"тишина действует", ptrT(time.Now().Add(time.Hour)), true},
		{"тишина кончилась", ptrT(time.Now().Add(-time.Minute)), false},
		{"тишины не было", nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := newDB(t)
			uid, _ := d.Users().Insert("router-s", "7777000000000000000000000000000000000000000000000000000000000000", "1.1.1.1", "awg0")
			disp := NewDispatcher(d, &fakeTG{}, Config{FailThreshold: 3, RecoveryThreshold: 2})
			sink := &recordingSink{delivers: 1}
			disp.SetNotifySink(sink)

			hardSince := time.Now().Add(-time.Minute).UTC()
			hard := state.Transition{Kind: state.Hard, Next: db.IncidentState{
				CurrentStatus: "hard", ConsecutiveFails: 3, HardSince: &hardSince, SilencedUntil: tc.silenced,
			}}
			if err := disp.Handle(context.Background(), uid, "router-s", "awg_handshake", hard, chk("awg_handshake", "fail", nil)); err != nil {
				t.Fatal(err)
			}
			rec := state.Transition{Kind: state.Recovery, Next: db.IncidentState{
				CurrentStatus: "ok", ConsecutiveOKs: 2, SilencedUntil: tc.silenced,
			}}
			if err := disp.Handle(context.Background(), uid, "router-s", "awg_handshake", rec, chk("awg_handshake", "ok", nil)); err != nil {
				t.Fatal(err)
			}
			if len(sink.trackedSilent) != 1 || sink.trackedSilent[0] != tc.want {
				t.Fatalf("тревога тихо=%v, ждали %v", sink.trackedSilent, tc.want)
			}
			if len(sink.repliesSilent) != 1 || sink.repliesSilent[0] != tc.want {
				t.Fatalf("«восстановилось» тихо=%v, ждали %v", sink.repliesSilent, tc.want)
			}
		})
	}
}
