package alerts

import (
	"context"
	"testing"
	"time"

	"github.com/Jkaotlic/wg-monitor/internal/backend/db"
	"github.com/Jkaotlic/wg-monitor/internal/backend/notify"
	"github.com/Jkaotlic/wg-monitor/internal/backend/state"
	"github.com/Jkaotlic/wg-monitor/internal/backend/tg"
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

// silencingSink -- рассылка, во время которой человек жмёт «Тише на час»
// (запись в базу идёт параллельно отправке, а отправка с повторами длится
// до полуминуты).
type silencingSink struct {
	recordingSink
	d       *db.DB
	uid     int64
	check   string
	until   time.Time
	sendErr error
}

func (s *silencingSink) SendTracked(ctx context.Context, routerUserID int64, checkName, text, parseMode string, kb *tg.InlineKeyboardMarkup, silent bool) (int, error) {
	st, _ := s.d.State().Get(s.uid, s.check)
	st.SilencedUntil = &s.until
	_ = s.d.State().Save(s.uid, s.check, st)
	if s.sendErr != nil {
		return 0, s.sendErr
	}
	return 1, nil
}

// Review fix 2: отметка отправки пишется точечно и не затирает тишину,
// поставленную, пока тревога отправлялась, -- ни при успехе, ни при сбое.
func TestDispatcherHardKeepsSilenceSetDuringSend(t *testing.T) {
	for name, sendErr := range map[string]error{"доставлено": nil, "не доставлено": notify.ErrNoneDelivered} {
		t.Run(name, func(t *testing.T) {
			d := newDB(t)
			uid, _ := d.Users().Insert("router-r", "6666000000000000000000000000000000000000000000000000000000000000", "1.1.1.1", "awg0")
			disp := NewDispatcher(d, &fakeTG{}, Config{FailThreshold: 3, RecoveryThreshold: 2})
			until := time.Now().Add(time.Hour).UTC().Truncate(time.Second)
			disp.SetNotifySink(&silencingSink{d: d, uid: uid, check: "awg_handshake", until: until, sendErr: sendErr})
			now := time.Now().UTC().Truncate(time.Second)
			disp.SetNow(func() time.Time { return now })
			tr := state.Apply(db.IncidentState{UserID: uid, CheckName: "awg_handshake", CurrentStatus: "fail", ConsecutiveFails: 2},
				"fail", now, state.Thresholds{Fail: 3, Recovery: 2})
			_ = disp.Handle(context.Background(), uid, "router-r", "awg_handshake", tr, chk("awg_handshake", "fail", nil))
			st, _ := d.State().Get(uid, "awg_handshake")
			if st.SilencedUntil == nil || !st.SilencedUntil.Equal(until) {
				t.Fatalf("тишина, поставленная во время отправки, затёрта: %v", st.SilencedUntil)
			}
			if sendErr == nil && (st.LastAlertAt == nil || !st.LastAlertAt.Equal(now)) {
				t.Fatalf("доставлено, а LastAlertAt=%v", st.LastAlertAt)
			}
			if sendErr != nil && st.LastAlertAt != nil {
				t.Fatalf("не доставлено, а LastAlertAt=%v", st.LastAlertAt)
			}
		})
	}
}
