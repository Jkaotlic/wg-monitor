package backend

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	cmdpkg "github.com/Jkaotlic/wg-monitor/internal/backend/cmd"
	"github.com/Jkaotlic/wg-monitor/internal/backend/db"
	"github.com/Jkaotlic/wg-monitor/pkg/wire"
)

// Прод 18.09: толпа у прокси релизов исчерпывала три попытки здорового
// роутера, и обновление ему сдавалось. Отказ «занято» -- не неудача роутера:
// попытку он не тратит, причина пишется, а повтор идёт после короткой паузы.
func TestCmdResult_ReleaseProxyBusyDoesNotConsumeAttempt(t *testing.T) {
	outputs := map[string]string{
		"новый агент":          wire.SelfUpdateBusyMarker + ": download checksums.txt: HTTP 503 for https://backend.example.com/v1/releases/download/v0.45.0/checksums.txt; github: dial tcp: i/o timeout",
		"старый агент":         "download checksums.txt: HTTP 503 for https://backend.example.com/v1/releases/download/v0.45.0/checksums.txt",
		"старый агент, бинарь": "download wg-monitor-agent-linux-mipsle: HTTP 503 for https://backend.example.com/v1/releases/download/v0.45.0/wg-monitor-agent-linux-mipsle",
	}
	for name, output := range outputs {
		t.Run(name, func(t *testing.T) {
			env := newBusyEnv(t)
			first := env.poll(t, "self_update")
			if st := env.pending(t); st.Attempts != 1 {
				t.Fatalf("после выдачи попыток %d, ждали 1", st.Attempts)
			}
			env.result(t, "err", output)
			st := env.pending(t)
			if st.Version != "v0.45.0" {
				t.Fatalf("отметка обязана остаться: %+v", st)
			}
			if st.Attempts != 0 {
				t.Fatalf("занятость прокси потратила попытку: %d", st.Attempts)
			}
			if !strings.Contains(st.LastError, "HTTP 503") {
				t.Fatalf("причина не записана: %q", st.LastError)
			}
			// Пауза ноль: следующий же контакт кладёт новую команду.
			second := env.poll(t, "self_update")
			if second.ID == first.ID {
				t.Fatal("выдана та же команда вместо новой")
			}
		})
	}
}

// Чужая неудача по-прежнему тратит попытку и держит команду до TTL.
func TestCmdResult_OtherSelfUpdateFailureStillCounts(t *testing.T) {
	env := newBusyEnv(t)
	env.poll(t, "self_update")
	env.result(t, "err", "download checksums.txt: HTTP 502 for https://backend.example.com/v1/releases/download/v0.45.0/checksums.txt")
	if st := env.pending(t); st.Attempts != 1 {
		t.Fatalf("попыток %d, ждали 1", st.Attempts)
	}
	if !env.q.HasActiveCommand(env.uid, "self_update") {
		t.Fatal("обычная неудача не должна отпускать команду раньше TTL")
	}
}

// На последней попытке «занято» не приводит к сдаче.
func TestCmdResult_ReleaseProxyBusyOnLastAttemptDoesNotGiveUp(t *testing.T) {
	env := newBusyEnv(t)
	for i := 0; i < pendingDeployMaxAttempts-1; i++ {
		if _, _, err := env.d.Users().IncrementPendingAttempts(env.uid, "v0.45.0"); err != nil {
			t.Fatal(err)
		}
	}
	env.poll(t, "self_update")
	env.result(t, "err", wire.SelfUpdateBusyMarker+": backend busy")
	st := env.pending(t)
	if st.Version != "v0.45.0" || st.Attempts != pendingDeployMaxAttempts-1 {
		t.Fatalf("ждали отметку с %d попытками: %+v", pendingDeployMaxAttempts-1, st)
	}
}

func TestIsReleaseProxyBusyFailure(t *testing.T) {
	cases := map[string]bool{
		wire.SelfUpdateBusyMarker + ": x": true,
		"download checksums.txt: HTTP 503 for https://h.example.com/v1/releases/download/v0.43.0/checksums.txt":         true,
		"download checksums.txt.sig: HTTP 503 for https://h.example.com/v1/releases/download/v0.43.0/checksums.txt.sig": true,
		// 503 от GitHub -- не наш прокси.
		"download checksums.txt: HTTP 503 for https://github.com/Jkaotlic/wg-monitor/releases/download/v0.43.0/checksums.txt": false,
		"download checksums.txt: HTTP 502 for https://h.example.com/v1/releases/download/v0.43.0/checksums.txt":               false,
		"sha256 mismatch: want a got b": false,
		"":                              false,
	}
	for in, want := range cases {
		if got := isReleaseProxyBusyFailure(in); got != want {
			t.Errorf("isReleaseProxyBusyFailure(%q)=%v, ждали %v", in, got, want)
		}
	}
	if txt := deployFailureText(wire.SelfUpdateBusyMarker + ": x"); !strings.Contains(txt, "занят") {
		t.Errorf("текст причины: %q", txt)
	}
}

type busyEnv struct {
	d      *db.DB
	q      *cmdpkg.Queue
	h      http.Handler
	uid    int64
	tok    string
	lastID string
}

func newBusyEnv(t *testing.T) *busyEnv {
	t.Helper()
	old := deployBusyCooldown
	deployBusyCooldown = func() time.Duration { return 0 }
	t.Cleanup(func() { deployBusyCooldown = old })
	d, err := db.Open(filepath.Join(t.TempDir(), "busy.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	tok := "b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0"
	uid, err := d.Users().InsertWithKind("busy-router", tok, "198.51.100.7", "awg0", db.KindMobile)
	if err != nil {
		t.Fatal(err)
	}
	if err := d.Users().MarkPendingDeploy(uid, "v0.45.0", time.Now().UTC().Format(time.RFC3339)); err != nil {
		t.Fatal(err)
	}
	q := cmdpkg.New()
	h := NewMux(Deps{
		Logger:        slog.New(slog.NewTextHandler(io.Discard, nil)),
		DB:            d,
		CommandSink:   q,
		PublicBaseURL: "https://backend.example.com",
	})
	return &busyEnv{d: d, q: q, h: h, uid: uid, tok: tok}
}

func (e *busyEnv) poll(t *testing.T, wantAction string) wire.Command {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/v1/cmd?wait=0", nil)
	req.Header.Set("Authorization", "Bearer "+e.tok)
	w := httptest.NewRecorder()
	e.h.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("опрос: статус %d, ждали команду", w.Code)
	}
	var c wire.Command
	if err := json.Unmarshal(w.Body.Bytes(), &c); err != nil {
		t.Fatal(err)
	}
	if c.Action != wantAction {
		t.Fatalf("action=%q", c.Action)
	}
	e.lastID = c.ID
	return c
}

func (e *busyEnv) result(t *testing.T, status, output string) {
	t.Helper()
	body, _ := json.Marshal(wire.CommandResult{ID: e.lastID, Status: status, Output: output})
	req := httptest.NewRequest(http.MethodPost, "/v1/cmd/result", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+e.tok)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	e.h.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("результат: статус %d %s", w.Code, w.Body.String())
	}
}

func (e *busyEnv) pending(t *testing.T) db.PendingDeployState {
	t.Helper()
	st, err := e.d.Users().PendingDeploy(e.uid)
	if err != nil {
		t.Fatal(err)
	}
	return st
}
