package backend

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/Jkaotlic/wg-monitor/internal/backend/alerts"
	"github.com/Jkaotlic/wg-monitor/internal/backend/db"
	"github.com/Jkaotlic/wg-monitor/internal/backend/state"
	"github.com/Jkaotlic/wg-monitor/internal/backend/tg"
	"github.com/Jkaotlic/wg-monitor/pkg/wire"
)

// stuckTelegram -- рассылка тревог, застрявшая на Telegram: SendTracked ждёт,
// пока тест не отпустит release (так ведёт себя sendWithRetry, который
// пережидает retry_after при 429 -- до полуминуты).
type stuckTelegram struct {
	release   chan struct{}
	delivered chan string
}

func (s *stuckTelegram) Send(context.Context, int64, string, string) (int, error) { return 1, nil }

func (s *stuckTelegram) SendTracked(ctx context.Context, _ int64, checkName, text, _ string, _ *tg.InlineKeyboardMarkup, _ bool) (int, error) {
	select {
	case <-s.release:
	case <-ctx.Done():
		return 0, ctx.Err()
	}
	s.delivered <- checkName
	return 1, nil
}

func (s *stuckTelegram) ReplyToEach(context.Context, int64, string, string, string, bool) error {
	return nil
}

// FRESH-04: отчёт роутера не ждёт рассылки тревоги. Агент держит запрос 10 с
// и при медленном Telegram писал «send report failed», хотя отчёт принят.
// Автомат пишется синхронно, доставка -- в фоне и всё равно доходит.
func TestReportDoesNotWaitForAlertDelivery(t *testing.T) {
	d, err := db.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	tok := "f404f404f404f404f404f404f404f404f404f404f404f404f404f404f404f404"
	uid, err := d.Users().Insert("slowtg", tok, "198.51.100.44", "awg0")
	if err != nil {
		t.Fatal(err)
	}
	// Одна неудача до порога (Fail:2): этот отчёт переводит dns в hard.
	if err := d.State().Save(uid, "dns", db.IncidentState{CurrentStatus: "fail", ConsecutiveFails: 1}); err != nil {
		t.Fatal(err)
	}

	stuck := &stuckTelegram{release: make(chan struct{}), delivered: make(chan string, 1)}
	disp := alerts.NewDispatcher(d, nil, alerts.Config{FailThreshold: 2, RecoveryThreshold: 2})
	disp.SetNotifySink(stuck)
	mux := NewMux(Deps{
		Logger:      slog.New(slog.NewTextHandler(io.Discard, nil)),
		DB:          d,
		Dispatcher:  disp,
		CommandSink: &fakeCmdSink{},
		Thresholds:  state.Thresholds{Fail: 2, Recovery: 2},
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	released := false
	defer func() {
		if !released {
			close(stuck.release)
		}
	}()

	body, _ := json.Marshal(wire.Report{
		Timestamp:    time.Now().UTC(),
		AgentVersion: "test",
		Checks:       []wire.Check{{Name: "dns", Status: "fail"}},
	})
	req, err := http.NewRequest(http.MethodPost, srv.URL+"/v1/report", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Content-Type", "application/json")

	type result struct {
		code int
		err  error
	}
	done := make(chan result, 1)
	go func() {
		resp, doErr := http.DefaultClient.Do(req)
		if doErr != nil {
			done <- result{err: doErr}
			return
		}
		resp.Body.Close()
		done <- result{code: resp.StatusCode}
	}()

	// Telegram ещё «висит»: ответ обязан прийти, не дожидаясь его.
	select {
	case r := <-done:
		if r.err != nil || r.code != http.StatusOK {
			t.Fatalf("ответ на отчёт: code=%d err=%v", r.code, r.err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("ответ на /v1/report ждёт рассылки тревоги: агент со своим таймаутом 10 с напишет «send report failed»")
	}

	// Автомат записан синхронно, до доставки.
	st, err := d.State().Get(uid, "dns")
	if err != nil {
		t.Fatal(err)
	}
	if st.CurrentStatus != "hard" {
		t.Fatalf("dns = %q до доставки, ждали hard", st.CurrentStatus)
	}

	// Telegram ответил -- тревога всё равно дошла.
	close(stuck.release)
	released = true
	select {
	case check := <-stuck.delivered:
		if check != "dns" {
			t.Fatalf("доставлена тревога по %q, ждали dns", check)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("тревога после разблокировки Telegram не доставлена")
	}
	// Доставка ещё может держать место в пуле relay (отметки после
	// отправки): не оставляем её следующим тестам насыщения пула.
	waitRelayPoolEmpty(t)
}
