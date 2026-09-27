package backend

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/Jkaotlic/wg-monitor/internal/backend/db"
	"github.com/Jkaotlic/wg-monitor/internal/backend/state"
	"github.com/Jkaotlic/wg-monitor/pkg/wire"
)

// BUG-01: чтение роутера в приёме отчёта упало (база занята) -- отчёт нельзя
// принимать вслепую: без last_seen он считался «свежим» (двигал FSM прошлым),
// а мобильному доставался статический порог. Ответ -- 503, агент пришлёт
// следующий отчёт; событий и переходов FSM нет.
func TestReportUserLookupErrorIsNotIngestedBlind(t *testing.T) {
	d, err := db.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	tok := "5555555555555555555555555555555555555555555555555555555555555555"
	uid, _ := d.Users().InsertWithKind("mobile-fox", tok, "198.51.100.7", "awg0", db.KindMobile)

	orig := reportUserByID
	reportUserByID = func(Deps, int64) (*db.User, error) { return nil, errors.New("database is locked") }
	defer func() { reportUserByID = orig }()

	disp := &fakeDisp{db: d}
	mux := NewMux(Deps{
		Logger:     slog.New(slog.NewTextHandler(io.Discard, nil)),
		DB:         d,
		Dispatcher: disp,
		Thresholds: state.Thresholds{Fail: 3, Recovery: 2},
	})
	body, _ := json.Marshal(wire.Report{
		Timestamp: time.Now().UTC(),
		Checks:    []wire.Check{{Name: "agent_heartbeat", Status: "ok"}, {Name: "dns", Status: "fail"}},
	})
	req := httptest.NewRequest(http.MethodPost, "/v1/report", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("code=%d body=%s, want 503", rec.Code, rec.Body.String())
	}
	if latest, _ := d.Events().LatestPerUser(uid); !latest.IsZero() {
		t.Fatal("отчёт принят вслепую: события записаны")
	}
	if len(disp.calls) != 0 {
		t.Fatalf("FSM двинут вслепую: %v", disp.calls)
	}
}
