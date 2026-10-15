package callbacks

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Jkaotlic/wg-monitor/internal/backend/alerts"
	"github.com/Jkaotlic/wg-monitor/internal/backend/db"
	"github.com/Jkaotlic/wg-monitor/internal/backend/notify"
	"github.com/Jkaotlic/wg-monitor/internal/backend/state"
	"github.com/Jkaotlic/wg-monitor/internal/backend/tg"
	"github.com/Jkaotlic/wg-monitor/pkg/wire"
)

// alertTG -- Telegram для тревоги и нити починки: помнит правки с кнопками.
type alertTG struct {
	mu      sync.Mutex
	editTxt []string
	editKB  []*tg.InlineKeyboardMarkup
}

func (a *alertTG) SendMessage(context.Context, int64, *int64, string, string, *int64) (int64, error) {
	return 501, nil
}

func (a *alertTG) SendMessageWithKeyboard(context.Context, int64, *int64, string, string, *int64, *tg.InlineKeyboardMarkup) (int64, error) {
	return 501, nil
}

func (a *alertTG) SendMessageWithReplyKeyboard(context.Context, int64, *int64, string, string, *int64, any) (int64, error) {
	return 501, nil
}

func (a *alertTG) EditMessageText(_ context.Context, _, _ int64, text, _ string, kb *tg.InlineKeyboardMarkup) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.editTxt = append(a.editTxt, text)
	a.editKB = append(a.editKB, kb)
	return nil
}

func hasSilenceButton(kb *tg.InlineKeyboardMarkup) bool {
	if kb == nil {
		return false
	}
	for _, row := range kb.InlineKeyboard {
		for _, b := range row {
			if strings.HasPrefix(b.CallbackData, "silence:") {
				return true
			}
		}
	}
	return false
}

// BOT-06: человек нажал «Тише на час» посреди автопочинки. Следующая правка
// нити починки сохраняет строку «скрыто до …» и не возвращает снятую кнопку.
func TestSilenceSurvivesRepairEdit(t *testing.T) {
	d, uid := newTestDB(t)
	if err := d.Users().SetTelegramUserID(uid, 100); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	atg := &alertTG{}
	disp := alerts.NewDispatcher(d, atg, alerts.Config{FailThreshold: 3, RecoveryThreshold: 2})
	hardSince := time.Now().Add(-time.Minute).UTC()
	tr := state.Transition{Kind: state.Hard, Next: db.IncidentState{
		UserID: uid, CheckName: "awg_handshake", CurrentStatus: "hard", ConsecutiveFails: 3, HardSince: &hardSince,
	}}
	if err := disp.Handle(ctx, uid, "vasya", "awg_handshake", tr, wire.Check{Name: "awg_handshake", Status: "fail"}); err != nil {
		t.Fatal(err)
	}
	repairs := notify.NewRepairs(notify.NewFanout(d, atg, nil, 0), atg, d, disp, time.Now)
	th := repairs.Begin(ctx, uid, "awg_handshake")

	r := NewRouter(d, &fakeRouterTG{}, Config{AdminUserID: 42})
	r.SetAlertMemory(disp)
	r.HandleCallback(ctx, silenceQuery(100, 100, uid))

	th.Progress(ctx, "Пробую другую страну")
	if len(atg.editTxt) != 1 {
		t.Fatalf("правок %d, ждали одну", len(atg.editTxt))
	}
	if !strings.Contains(atg.editTxt[0], "скрыты до") {
		t.Fatalf("правка починки стёрла строку тишины: %q", atg.editTxt[0])
	}
	if hasSilenceButton(atg.editKB[0]) {
		t.Fatalf("правка починки вернула снятую кнопку тишины: %+v", atg.editKB[0])
	}
}
