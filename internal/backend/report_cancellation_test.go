package backend

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Jkaotlic/wg-monitor/internal/backend/state"
	"github.com/Jkaotlic/wg-monitor/pkg/wire"
)

func TestReportIngestFinishesAtomicallyAfterClientCancels(t *testing.T) {
	d, ownedID, _, _ := seedMiniappFleet(t)
	tok := "tok-owned-0000000000000000000000000000000000000000000000000000"
	h := NewMux(Deps{Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), DB: d, Dispatcher: &fakeDisp{db: d}, Thresholds: state.Thresholds{Fail: 3, Recovery: 2}})
	ts := time.Now().UTC().Truncate(time.Second)
	body, _ := json.Marshal(wire.Report{Timestamp: ts, Checks: []wire.Check{{Name: "agent_heartbeat", Status: "ok"}}})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	req := httptest.NewRequest(http.MethodPost, "/v1/report", bytes.NewReader(body)).WithContext(ctx)
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	got, err := d.Events().LatestPerUser(ownedID)
	if err != nil || !got.Equal(ts) {
		t.Fatalf("cancelled client lost atomic ingest: got=%v err=%v response=%d %s", got, err, rec.Code, rec.Body.String())
	}
}
