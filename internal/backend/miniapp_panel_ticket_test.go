package backend

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"
)

const testPanelURL = "https://panel.example.com/admin"

func panelTicketIssue(t *testing.T, h http.Handler, routerID, telegramUserID int64) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/v1/miniapp/routers/%d/panel/ticket", routerID), nil)
	req.AddCookie(miniappSessionCookieFor(t, "test-bot-token", telegramUserID))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// Внешний браузер -- без cookie сессии: так и открывает его tg.openLink.
func panelTicketVisit(h http.Handler, method, path string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, nil)
	req.RemoteAddr = "198.51.100.30:5555"
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func openPathOf(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var resp struct {
		OpenPath string `json:"open_path"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("ответ выдачи: %v: %s", err, rec.Body.String())
	}
	if !regexp.MustCompile(`^/v1/panel/[0-9a-f]{64}$`).MatchString(resp.OpenPath) {
		t.Fatalf("open_path = %q", resp.OpenPath)
	}
	return resp.OpenPath
}

// Полный путь: выдача в сессии -> страница во внешнем браузере -> нажатие ->
// редирект на панель. Адрес появляется только в Location последнего шага.
func TestPanelTicketFullPathAndSingleUse(t *testing.T) {
	d, ownedID, _, ownerTG := seedMiniappFleet(t)
	setAWGMURL(t, d, ownedID, testPanelURL)
	h := NewMux(Deps{DB: d, TelegramBotToken: "test-bot-token", TelegramAdminUserID: 999})

	issued := panelTicketIssue(t, h, ownedID, ownerTG)
	if issued.Code != http.StatusOK {
		t.Fatalf("выдача: код %d тело %s", issued.Code, issued.Body.String())
	}
	if strings.Contains(issued.Body.String(), "panel.example.com") {
		t.Errorf("адрес панели в ответе выдачи: %s", issued.Body.String())
	}
	if !strings.Contains(issued.Header().Get("Cache-Control"), "no-store") {
		t.Errorf("выдача без no-store")
	}
	path := openPathOf(t, issued)

	// Страница билет не тратит: браузер вправе прогреть адрес заранее.
	for i := 0; i < 2; i++ {
		page := panelTicketVisit(h, http.MethodGet, path)
		if page.Code != http.StatusOK || !strings.Contains(page.Body.String(), `method="post"`) {
			t.Fatalf("страница %d: код %d тело %s", i, page.Code, page.Body.String())
		}
		if strings.Contains(page.Body.String(), "panel.example.com") {
			t.Errorf("адрес панели на странице: %s", page.Body.String())
		}
		if page.Header().Get("Referrer-Policy") != "no-referrer" {
			t.Errorf("Referrer-Policy = %q", page.Header().Get("Referrer-Policy"))
		}
	}

	go1 := panelTicketVisit(h, http.MethodPost, path)
	if go1.Code != http.StatusFound || go1.Header().Get("Location") != testPanelURL {
		t.Fatalf("переход: код %d Location %q тело %s", go1.Code, go1.Header().Get("Location"), go1.Body.String())
	}
	if !strings.Contains(go1.Header().Get("Cache-Control"), "no-store") || go1.Header().Get("Referrer-Policy") != "no-referrer" {
		t.Errorf("переход без no-store/no-referrer: %v", go1.Header())
	}

	// Одноразовый: второе нажатие и новая страница -- «устарела».
	if again := panelTicketVisit(h, http.MethodPost, path); again.Code != http.StatusNotFound || again.Header().Get("Location") != "" {
		t.Errorf("повторный переход: код %d Location %q", again.Code, again.Header().Get("Location"))
	}
	if page := panelTicketVisit(h, http.MethodGet, path); page.Code != http.StatusNotFound {
		t.Errorf("страница после перехода: код %d", page.Code)
	}
}

// Выдаёт только владелец и админ; оператору и незнакомцу -- 404 до всего.
func TestPanelTicketIssueGate(t *testing.T) {
	d, ownedID, _, ownerTG := seedMiniappFleet(t)
	setAWGMURL(t, d, ownedID, testPanelURL)
	if err := d.RouterOperators().Add(ownedID, 555, 999); err != nil {
		t.Fatal(err)
	}
	h := NewMux(Deps{DB: d, TelegramBotToken: "test-bot-token", TelegramAdminUserID: 999})
	for _, tg := range []int64{555, 4242} {
		if rec := panelTicketIssue(t, h, ownedID, tg); rec.Code != http.StatusNotFound || strings.Contains(rec.Body.String(), "panel.example.com") {
			t.Errorf("tg %d: код %d тело %s, хотим 404", tg, rec.Code, rec.Body.String())
		}
	}
	for _, tg := range []int64{ownerTG, 999} {
		if rec := panelTicketIssue(t, h, ownedID, tg); rec.Code != http.StatusOK {
			t.Errorf("tg %d: код %d, хотим 200", tg, rec.Code)
		}
	}
	setAWGMURL(t, d, ownedID, "")
	if rec := panelTicketIssue(t, h, ownedID, ownerTG); rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "panel_unknown") {
		t.Errorf("без адреса: код %d тело %s, хотим 409 panel_unknown", rec.Code, rec.Body.String())
	}
}

// Запрет на входе и на выходе: права перепроверяются в момент перехода, а
// адрес читается заново. Разжалованный владелец и стёртый адрес не открывают
// панель по билету, выданному раньше.
func TestPanelTicketRechecksOnRedeem(t *testing.T) {
	d, ownedID, _, ownerTG := seedMiniappFleet(t)
	setAWGMURL(t, d, ownedID, testPanelURL)
	h := NewMux(Deps{DB: d, TelegramBotToken: "test-bot-token", TelegramAdminUserID: 999})

	path := openPathOf(t, panelTicketIssue(t, h, ownedID, ownerTG))
	if _, err := d.SQL().Exec(`UPDATE users SET telegram_user_id=NULL WHERE id=?`, ownedID); err != nil {
		t.Fatal(err)
	}
	if rec := panelTicketVisit(h, http.MethodPost, path); rec.Code != http.StatusNotFound || rec.Header().Get("Location") != "" {
		t.Errorf("разжалованный владелец: код %d Location %q", rec.Code, rec.Header().Get("Location"))
	}

	path = openPathOf(t, panelTicketIssue(t, h, ownedID, 999))
	setAWGMURL(t, d, ownedID, "javascript:alert(1)")
	if rec := panelTicketVisit(h, http.MethodPost, path); rec.Code != http.StatusConflict || rec.Header().Get("Location") != "" {
		t.Errorf("негодный адрес на переходе: код %d Location %q", rec.Code, rec.Header().Get("Location"))
	}
}

// Билет живёт минуту.
func TestPanelTicketExpires(t *testing.T) {
	now := time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC)
	prev := panelTicketNow
	panelTicketNow = func() time.Time { return now }
	t.Cleanup(func() { panelTicketNow = prev })

	d, ownedID, _, ownerTG := seedMiniappFleet(t)
	setAWGMURL(t, d, ownedID, testPanelURL)
	h := NewMux(Deps{DB: d, TelegramBotToken: "test-bot-token", TelegramAdminUserID: 999})
	path := openPathOf(t, panelTicketIssue(t, h, ownedID, ownerTG))

	now = now.Add(61 * time.Second)
	if rec := panelTicketVisit(h, http.MethodGet, path); rec.Code != http.StatusNotFound {
		t.Errorf("страница просроченного билета: код %d", rec.Code)
	}
	if rec := panelTicketVisit(h, http.MethodPost, path); rec.Code != http.StatusNotFound || rec.Header().Get("Location") != "" {
		t.Errorf("переход по просроченному: код %d Location %q", rec.Code, rec.Header().Get("Location"))
	}
}

// Выдумать билет нельзя, а подбор упирается в общий лимит входов.
func TestPanelTicketUnknownAndRateLimited(t *testing.T) {
	d, _, _, _ := seedMiniappFleet(t)
	h := NewMux(Deps{DB: d, TelegramBotToken: "test-bot-token", TelegramAdminUserID: 999})
	fake := "/v1/panel/" + strings.Repeat("ab", 32)
	if rec := panelTicketVisit(h, http.MethodPost, fake); rec.Code != http.StatusNotFound {
		t.Fatalf("выдуманный билет: код %d", rec.Code)
	}
	limited := false
	for i := 0; i < entranceBurst+5; i++ {
		if panelTicketVisit(h, http.MethodPost, fake).Code == http.StatusTooManyRequests {
			limited = true
			break
		}
	}
	if !limited {
		t.Error("подбор билетов не упёрся в лимит входов")
	}
}

// Билет -- секрет в ПУТИ. Отказ по лимиту пишет путь в журнал, и живой билет
// лежал бы в логах контейнера целиком, пока не истечёт.
func TestPanelTicketNeverLoggedOnRateLimit(t *testing.T) {
	d, ownedID, _, ownerTG := seedMiniappFleet(t)
	setAWGMURL(t, d, ownedID, testPanelURL)
	var logs bytes.Buffer
	h := NewMux(Deps{DB: d, TelegramBotToken: "test-bot-token", TelegramAdminUserID: 999,
		Logger: slog.New(slog.NewTextHandler(&logs, nil))})
	path := openPathOf(t, panelTicketIssue(t, h, ownedID, ownerTG))
	ticket := strings.TrimPrefix(path, "/v1/panel/")

	limited := false
	for i := 0; i < entranceBurst+5; i++ {
		if panelTicketVisit(h, http.MethodGet, path).Code == http.StatusTooManyRequests {
			limited = true
		}
	}
	if !limited {
		t.Fatal("лимит не сработал — тест прошёл бы вхолостую")
	}
	if strings.Contains(logs.String(), ticket) {
		t.Errorf("билет попал в журнал:\n%s", logs.String())
	}
}
