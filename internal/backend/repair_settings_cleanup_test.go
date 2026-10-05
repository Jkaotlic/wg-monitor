package backend

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/Jkaotlic/wg-monitor/internal/backend/db"
	"github.com/Jkaotlic/wg-monitor/internal/backend/state"
	"github.com/Jkaotlic/wg-monitor/pkg/wire"
)

// Агент подтвердил удаление VPN-туннеля -- настройка его автопочинки уходит
// вместе с ним: awg-manager может отдать тот же id новому туннелю, и
// согласие на автопочинку к нему не переходит. Неудачное удаление настройку
// не трогает: туннель остался.
func TestCmdResultTunnelDeleteDropsRepairSetting(t *testing.T) {
	d, err := db.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	tok := "3a3a3a3a3a3a3a3a3a3a3a3a3a3a3a3a3a3a3a3a3a3a3a3a3a3a3a3a3a3a3a3a"
	uid, err := d.Users().Insert("router-a", tok, "198.51.100.10", "awg11")
	if err != nil {
		t.Fatal(err)
	}
	for _, tid := range []string{"awg12", "awg13", "awg14"} {
		if err := d.TunnelRepairSettings().Put(db.TunnelRepairSetting{UserID: uid, TunnelID: tid, Enabled: true}); err != nil {
			t.Fatal(err)
		}
	}
	sink := &fakeCmdSink{commands: map[string]wire.Command{
		"del-ok":    {ID: "del-ok", Action: "tunnel_delete", Args: map[string]any{"tunnel_id": "awg12"}},
		"del-check": {ID: "del-check", Action: "tunnel_delete", Args: map[string]any{"check_name": "tunnel_awg14"}},
		"del-fail":  {ID: "del-fail", Action: "tunnel_delete", Args: map[string]any{"tunnel_id": "awg13"}},
	}}
	mux := NewMux(Deps{
		Logger:      slog.New(slog.NewTextHandler(io.Discard, nil)),
		DB:          d,
		Dispatcher:  &fakeDisp{},
		CommandSink: sink,
		Thresholds:  state.Thresholds{Fail: 3, Recovery: 2},
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	post := func(res wire.CommandResult) {
		t.Helper()
		body, _ := json.Marshal(res)
		req, _ := http.NewRequest(http.MethodPost, srv.URL+"/v1/cmd/result", bytes.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+tok)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status: %d", resp.StatusCode)
		}
	}
	post(wire.CommandResult{ID: "del-ok", Status: "ok", Output: "удалён"})
	post(wire.CommandResult{ID: "del-check", Status: "ok", Output: "удалён"})
	post(wire.CommandResult{ID: "del-fail", Status: "error", Output: "не удалён"})

	for tid, want := range map[string]bool{"awg12": false, "awg14": false, "awg13": true} {
		_, ok, err := d.TunnelRepairSettings().Get(uid, tid)
		if err != nil {
			t.Fatal(err)
		}
		if ok != want {
			t.Errorf("настройка %s есть=%v, ждали %v", tid, ok, want)
		}
	}
}
