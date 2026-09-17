package backend

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Jkaotlic/wg-monitor/internal/backend/provision"
)

// Цель оператора: «дашборд и миниапп должны иметь одинаковый набор функций».
// Установка роутера из браузера -- настоящий mux, настоящая кука входа в
// веб-управление, опрос хода до успеха, роутер в базе.
func TestWebEntryCanInstallRouterAndFollowJob(t *testing.T) {
	old := serverVersion
	SetVersion("v0.36.0")
	t.Cleanup(func() { SetVersion(old) })
	d, _, _, _ := seedMiniappFleet(t)
	relay := &fakeProvisionRelay{rc: 0, lines: []string{"__WG_STEP__ terminal_connected", "__WG_STEP__ config_written"}}
	var asked []string
	h := NewMux(Deps{
		DB:                  d,
		DashboardToken:      webTestDash,
		TelegramBotToken:    "test-bot-token",
		TelegramAdminUserID: webTestAdmin,
		PublicBaseURL:       "https://backend.example.com",
		// Как у песочницы: сеть не нужна, и видно, что ядро берёт подмену из Deps.
		ReleaseChecksumsOverride: func(_ context.Context, base, version string) (map[string]string, error) {
			asked = append(asked, base+"@"+version)
			return map[string]string{"wg-monitor-agent-linux-arm64": "cafebabe"}, nil
		},
		Provision: provision.Deps{Store: provision.NewStore(), BaseCtx: context.Background(), Relay: relay.run, LastSeen: freshLastSeen},
	})

	login := httptest.NewRequest(http.MethodPost, "/v1/dashboard/login", strings.NewReader(`{"token":"`+webTestDash+`"}`))
	login.Header.Set("Content-Type", "application/json")
	loginRec := httptest.NewRecorder()
	h.ServeHTTP(loginRec, login)
	if loginRec.Code != http.StatusOK || len(loginRec.Result().Cookies()) != 1 {
		t.Fatalf("вход: код %d", loginRec.Code)
	}
	cookie := loginRec.Result().Cookies()[0]
	do := func(method, path, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		req.AddCookie(cookie)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}

	rec := do(http.MethodPost, "/v1/miniapp/provision", `{"kind":"provision","nickname":"router-web","agent_kind":"static",`+
		`"awgm_url":"https://panel.example.com","awgm_auth":"web","root_password":"`+miniappReviveRoot+`","version":"","confirm":"router-web"}`)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("установка из браузера: код %d (%s)", rec.Code, rec.Body.String())
	}
	var start struct {
		JobID string `json:"job_id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &start); err != nil || start.JobID == "" {
		t.Fatalf("ответ %s err=%v", rec.Body.String(), err)
	}
	if len(asked) != 1 || asked[0] != releaseDownloadBase+"@v0.36.0" {
		t.Fatalf("checksums спрошены %v", asked)
	}

	var job struct {
		State    string `json:"state"`
		RouterID *int64 `json:"router_id"`
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		rec = do(http.MethodGet, "/v1/miniapp/jobs/"+start.JobID, "")
		if rec.Code != http.StatusOK {
			t.Fatalf("опрос: код %d (%s)", rec.Code, rec.Body.String())
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &job); err != nil {
			t.Fatal(err)
		}
		if job.State != "running" || time.Now().After(deadline) {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if job.State != "success" || job.RouterID == nil {
		t.Fatalf("итог задания: %+v", job)
	}
	u, err := d.Users().GetByNickname("router-web")
	if err != nil || u.ID != *job.RouterID {
		t.Fatalf("роутер в базе: %+v err=%v", u, err)
	}
}

// Подмена проверки подписи checksums -- только для песочницы и тестов. В
// бинаре бэкенда её быть не должно ни в каком виде.
func TestBackendMainNeverOverridesReleaseChecksums(t *testing.T) {
	files, err := filepath.Glob(filepath.Join("..", "..", "cmd", "backend", "*.go"))
	if err != nil || len(files) == 0 {
		t.Fatalf("не нашёл cmd/backend: %v", err)
	}
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(raw, []byte("ReleaseChecksumsOverride")) {
			t.Errorf("%s заполняет ReleaseChecksumsOverride: в проде подпись checksums перестала бы проверяться", f)
		}
	}
}
