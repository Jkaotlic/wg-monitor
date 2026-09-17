package backend

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Jkaotlic/wg-monitor/internal/backend/db"
	"github.com/Jkaotlic/wg-monitor/internal/backend/upstream"
)

// Сводка парка говорит админу, кому пора обновляться.
//
// Дашборд не сравнивал роутерный софт с апстримом ни в одной строке -- админ
// жал fleet-кнопку наугад. Механизм для версии самого бэкенда там уже работает
// («доступна X»), и новость про роутер собирается тем же рисунком, только
// источник -- снимок версий в базе, а не GitHub по каждому роутеру.
func TestDashboardSummaryCarriesUpdateNewsFromSnapshot(t *testing.T) {
	d, err := db.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	uid, err := d.Users().Insert("router-a", "tok-dash-00000000000000000000000000000000", "198.51.100.10", "awg11")
	if err != nil {
		t.Fatal(err)
	}
	// Прошивка: стоит 5.02.A.8.0-3, роутер предлагает 5.02.A.9.0-0 (живые
	// числа рабочего роутера, 12.09.2026).
	if err := d.RouterVersions().Upsert(uid, db.RouterVersionSnapshot{
		AwgmgrVersion:   "2.18.2+r2",
		FirmwareCurrent: "5.02.A.8.0-3",
		FirmwareAvail:   "5.02.A.9.0-0",
		Source:          "report",
	}); err != nil {
		t.Fatal(err)
	}

	h := NewMux(Deps{
		DB:             d,
		DashboardToken: "secret",
		// Источник апстрима выключен -- как на парке сегодня. Про панель
		// новости быть не должно, про прошивку -- должна: её приносит роутер.
		Upstream: upstream.NewCache(time.Hour, nil),
	})

	loginReq := httptest.NewRequest(http.MethodPost, "/v1/dashboard/login", strings.NewReader(`{"token":"secret"}`))
	loginReq.Header.Set("Content-Type", "application/json")
	loginRec := httptest.NewRecorder()
	h.ServeHTTP(loginRec, loginReq)
	if loginRec.Code != http.StatusOK {
		t.Fatalf("login: %d %s", loginRec.Code, loginRec.Body.String())
	}
	cookies := loginRec.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("нет cookie сессии: %+v", cookies)
	}

	req := httptest.NewRequest(http.MethodGet, "/v1/dashboard/summary", nil)
	req.AddCookie(cookies[0])
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("summary: %d %s", rec.Code, rec.Body.String())
	}

	var got struct {
		Agents []struct {
			Nickname string `json:"nickname"`
			Updates  []struct {
				Component string `json:"component"`
				Installed string `json:"installed"`
				Available string `json:"available"`
			} `json:"updates"`
		} `json:"agents"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Agents) != 1 {
		t.Fatalf("роутеров в сводке %d, ожидался один", len(got.Agents))
	}
	agent := got.Agents[0]
	var firmware bool
	for _, u := range agent.Updates {
		if u.Component == "firmware" {
			firmware = true
			if u.Installed != "5.02.A.8.0-3" || u.Available != "5.02.A.9.0-0" {
				t.Errorf("новость о прошивке описана неверно: %+v", u)
			}
		}
		// Выключенный источник апстрима не имеет права превращаться в новость:
		// «мы не знаем» -- это не «вышло обновление».
		if u.Component == "awgmgr" {
			t.Errorf("при выключенном источнике выдумана новость о панели: %+v", u)
		}
	}
	if !firmware {
		t.Errorf("сводка не говорит, что роутеру пора обновить прошивку: %+v", agent.Updates)
	}
}
