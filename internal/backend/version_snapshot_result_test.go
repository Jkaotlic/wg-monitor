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

// Ответ version_audit ложится в снимок, даже когда его никто не ждёт на экране.
//
// Снимок писался внутри отрисовки панели обслуживания, а та запускается только
// при наличии origin-ref -- то есть когда человек нажал кнопку в Telegram.
// Суточный опрос ставит команду сам, адресата у неё нет, и её ответ до записи
// не доезжал вовсе: поллер работал бы вхолостую, а версия HydraRoute Neo и
// доступная прошивка так и не появлялись бы в базе.
//
// Ответ роутера -- это данные, а не сопровождение сообщения в чате, и писать
// их надо независимо от того, смотрит ли кто-то на экран.
func TestCmdResultVersionAuditWritesSnapshotWithoutOriginRef(t *testing.T) {
	d, err := db.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	tok := "2f2f2f2f2f2f2f2f2f2f2f2f2f2f2f2f2f2f2f2f2f2f2f2f2f2f2f2f2f2f2f2f"
	uid, err := d.Users().Insert("router-a", tok, "198.51.100.10", "awg11")
	if err != nil {
		t.Fatal(err)
	}

	// Команда заведена поллером: в очереди она есть, а адресата (origin-ref)
	// у неё нет вовсе.
	sink := &fakeCmdSink{commands: map[string]wire.Command{
		"updpoll-1": {ID: "updpoll-1", Action: "version_audit"},
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

	out, _ := json.Marshal(wire.VersionAudit{
		AwgmgrVersion:   "2.18.2",
		HrneoVersion:    "3.18.3",
		FirmwareCurrent: "5.02.A.8.0-3",
		FirmwareAvail:   "5.02.A.9.0-0",
	})
	body, _ := json.Marshal(wire.CommandResult{ID: "updpoll-1", Status: "ok", Output: string(out)})
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

	row, err := d.RouterVersions().Get(uid)
	if err != nil {
		t.Fatal(err)
	}
	// Ровно те два поля, ради которых суточный опрос и существует: обычный
	// отчёт агента не приносит ни версию HydraRoute Neo, ни доступную прошивку.
	if row.HrneoVersion != "3.18.3" {
		t.Errorf("версия HydraRoute Neo не записана (%q): ответ version_audit без адресата потерян", row.HrneoVersion)
	}
	if row.FirmwareAvail != "5.02.A.9.0-0" {
		t.Errorf("доступная прошивка не записана (%q)", row.FirmwareAvail)
	}
	if row.Source != "version_audit" {
		t.Errorf("источник снимка %q, ожидался version_audit", row.Source)
	}
}

// Чужой по смыслу ответ снимок не трогает.
//
// Ответ firmware_status -- ВАЛИДНЫЙ JSON, и в wire.VersionAudit он разберётся
// без ошибки, просто всеми пустыми полями. Поэтому проверка действия обязана
// стоять ДО разбора: иначе такой ответ завёл бы пустой снимок с источником
// version_audit и затёр бы им накопленное. Ответ-простыня текстом эту границу
// не сторожит вовсе -- он не разбирается сам по себе, и тест был бы зелёным
// даже без проверки действия.
func TestCmdResultNonAuditLeavesSnapshotAlone(t *testing.T) {
	d, err := db.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	tok := "3f3f3f3f3f3f3f3f3f3f3f3f3f3f3f3f3f3f3f3f3f3f3f3f3f3f3f3f3f3f3f3f"
	uid, err := d.Users().Insert("router-a", tok, "198.51.100.10", "awg11")
	if err != nil {
		t.Fatal(err)
	}

	sink := &fakeCmdSink{commands: map[string]wire.Command{
		"fw-1": {ID: "fw-1", Action: "firmware_status"},
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

	body, _ := json.Marshal(wire.CommandResult{ID: "fw-1", Status: "ok",
		Output: `{"current":"5.02.A.8.0-3","available":"5.02.A.9.0-0"}`})
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/v1/cmd/result", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	row, err := d.RouterVersions().Get(uid)
	if err != nil {
		t.Fatal(err)
	}
	if !row.UpdatedAt.IsZero() {
		t.Errorf("снимок появился из ответа, который про версии ничего не говорит: %+v", row)
	}
}
