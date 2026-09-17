package backend

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// Вход в веб-управление по личной ссылке. Проверяется не «работает ли
// кнопка», а свойства входа: срок, адресат, неразличимость отказов,
// лимит живых грантов и то, что сам грант нигде не остаётся.

const testPublicBase = "https://wg.example.com"

func webLinkDeps(t *testing.T, adminID int64, logger *slog.Logger) (Deps, http.Handler) {
	t.Helper()
	d, _, _, _ := seedMiniappFleet(t)
	deps := Deps{
		DB:                  d,
		TelegramBotToken:    "test-bot-token",
		TelegramAdminUserID: adminID,
		DashboardToken:      "dashboard-secret",
		PublicBaseURL:       testPublicBase,
		Logger:              logger,
	}
	return deps, NewMux(deps)
}

// issueWebLink просит ссылку от имени telegramUserID и возвращает ответ.
func issueWebLink(t *testing.T, h http.Handler, telegramUserID int64) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/v1/miniapp/web-link", nil)
	req.AddCookie(miniappSessionCookieFor(t, "test-bot-token", telegramUserID))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func webLinkTokenFrom(t *testing.T, rec *httptest.ResponseRecorder) (token string, resp webLinkIssueResp) {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("выдача ссылки: код %d, тело %s", rec.Code, rec.Body.String())
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("разбор ответа выдачи: %v (тело %s)", err, rec.Body.String())
	}
	_, frag, found := strings.Cut(resp.URL, "#token=")
	if !found || frag == "" {
		t.Fatalf("в ссылке нет фрагмента с грантом: %q", resp.URL)
	}
	return frag, resp
}

func redeemWebLink(t *testing.T, h http.Handler, token, remote string) *httptest.ResponseRecorder {
	t.Helper()
	body := `{"token":"` + token + `"}`
	req := httptest.NewRequest(http.MethodPost, "/v1/dashboard/web-link/redeem", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if remote != "" {
		req.RemoteAddr = remote
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func dashboardCookieFrom(rec *httptest.ResponseRecorder) *http.Cookie {
	for _, c := range rec.Result().Cookies() {
		if c.Name == dashboardSessionCookieName {
			return c
		}
	}
	return nil
}

// Многоразовость -- решение оператора: одноразовость снята сознательно, и
// отказа «ссылка уже использована» нет как класса.
func TestWebLinkRedeemWorksTwiceWithinTTL(t *testing.T) {
	_, h := webLinkDeps(t, 999, nil)
	token, _ := webLinkTokenFrom(t, issueWebLink(t, h, 999))

	for i := 1; i <= 3; i++ {
		rec := redeemWebLink(t, h, token, "198.51.100.7:40001")
		if rec.Code != http.StatusOK {
			t.Fatalf("предъявление %d: код %d, тело %s", i, rec.Code, rec.Body.String())
		}
		if dashboardCookieFrom(rec) == nil {
			t.Fatalf("предъявление %d: cookie дашборда не выдана", i)
		}
	}
}

// Просроченная ссылка отвечает словами, а не 500 и не пустотой.
func TestWebLinkExpiredSpeaksWords(t *testing.T) {
	_, h := webLinkDeps(t, 999, nil)
	now := time.Date(2026, 9, 12, 10, 0, 0, 0, time.UTC)
	old := webLinkNow
	webLinkNow = func() time.Time { return now }
	t.Cleanup(func() { webLinkNow = old })

	token, _ := webLinkTokenFrom(t, issueWebLink(t, h, 999))

	webLinkNow = func() time.Time { return now.Add(webLinkTTL + time.Minute) }
	rec := redeemWebLink(t, h, token, "198.51.100.7:40001")
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("просроченная ссылка: код %d, want 401 (тело %s)", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), webLinkCopyDead) {
		t.Fatalf("отказ обязан сказать «%s», а сказал: %s", webLinkCopyDead, rec.Body.String())
	}
	if dashboardCookieFrom(rec) != nil {
		t.Fatal("просроченная ссылка не имеет права выдавать cookie дашборда")
	}
}

// Грант адресован одному человеку: выданный другому telegram_user_id он не
// работает, даже если ещё не просрочен.
func TestWebLinkIsAddressedToOneTelegramUser(t *testing.T) {
	deps, h := webLinkDeps(t, 999, nil)
	// Владелец роутера (не админ) -- грант кладём прямо в базу: через
	// эндпоинт ему такую ссылку и не выдадут.
	raw, hash, err := newWebLinkToken()
	if err != nil {
		t.Fatal(err)
	}
	if err := deps.DB.WebLinks().Issue(hash, 100, time.Now().UTC().Add(webLinkTTL)); err != nil {
		t.Fatal(err)
	}

	rec := redeemWebLink(t, h, raw, "198.51.100.7:40001")
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("чужой грант: код %d, want 401 (тело %s)", rec.Code, rec.Body.String())
	}
	if dashboardCookieFrom(rec) != nil {
		t.Fatal("чужой грант не имеет права открывать дашборд")
	}
}

// Грант открывает ровно одну дверь -- обмен на сессию дашборда, и ни одну из
// трёх остальных, куда в этом проекте предъявляют секреты.
//
// Чего тест НЕ утверждает: что мы «закрыли» Bearer-путь. Мы его и не
// открывали -- сравнение там идёт с DashboardToken и с токенами агентов.
// Тест сторожит будущую ошибку: попытку «унифицировать вход» так, чтобы
// короткоживущий грант начал приниматься там, где ждут долгоживущий токен.
// Двери взяты настоящие: сводка под Bearer, форма входа и приём отчётов.
func TestWebLinkIsNotUsableAsBearer(t *testing.T) {
	_, h := webLinkDeps(t, 999, nil)
	token, _ := webLinkTokenFrom(t, issueWebLink(t, h, 999))

	// 1. Bearer к дашборду -- там ждут dashboard-токен оператора.
	summary := httptest.NewRequest(http.MethodGet, "/v1/dashboard/summary", nil)
	summary.Header.Set("Authorization", "Bearer "+token)
	summaryRec := httptest.NewRecorder()
	h.ServeHTTP(summaryRec, summary)
	if summaryRec.Code != http.StatusUnauthorized {
		t.Errorf("грант в Authorization к сводке: код %d, want 401 (тело %s)", summaryRec.Code, summaryRec.Body.String())
	}

	// 2. Форма входа дашборда -- настоящая дверь, куда секрет вставляют
	// руками. Грант там не подходит: у неё свой токен и своё сравнение.
	login := httptest.NewRequest(http.MethodPost, "/v1/dashboard/login", strings.NewReader(`{"token":"`+token+`"}`))
	login.Header.Set("Content-Type", "application/json")
	loginRec := httptest.NewRecorder()
	h.ServeHTTP(loginRec, login)
	if loginRec.Code != http.StatusUnauthorized {
		t.Errorf("грант в форме входа: код %d, want 401 (тело %s)", loginRec.Code, loginRec.Body.String())
	}
	if dashboardCookieFrom(loginRec) != nil {
		t.Error("форма входа выдала сессию по гранту")
	}

	// 3. Bearer агента: грант не должен сойти за токен роутера -- иначе он
	// открывал бы приём отчётов от чужого имени.
	report := httptest.NewRequest(http.MethodPost, "/v1/report", strings.NewReader(`{}`))
	report.Header.Set("Authorization", "Bearer "+token)
	report.Header.Set("Content-Type", "application/json")
	reportRec := httptest.NewRecorder()
	h.ServeHTTP(reportRec, report)
	if reportRec.Code != http.StatusUnauthorized {
		t.Errorf("грант как токен агента: код %d, want 401 (тело %s)", reportRec.Code, reportRec.Body.String())
	}
}

// В базе лежит sha256, а не сам грант: база, утёкшая целиком, входа не даёт.
//
// Хэш здесь считается НЕЗАВИСИМО от нашей же webLinkHash -- иначе тест
// остался бы зелёным, замени кто-нибудь хеширование тождеством.
func TestWebLinkStoresHashNotTheGrantItself(t *testing.T) {
	deps, h := webLinkDeps(t, 999, nil)
	token, _ := webLinkTokenFrom(t, issueWebLink(t, h, 999))

	var stored string
	if err := deps.DB.SQL().QueryRow(`SELECT token_hash FROM web_links`).Scan(&stored); err != nil {
		t.Fatalf("чтение строки гранта: %v", err)
	}
	if stored == token {
		t.Fatal("в базе лежит сам грант: утёкшая база даёт вход в веб-управление")
	}
	if strings.Contains(stored, token) || strings.Contains(token, stored) {
		t.Fatalf("хранимое значение -- часть гранта: %q", stored)
	}
	sum := sha256.Sum256([]byte(token))
	if want := hex.EncodeToString(sum[:]); stored != want {
		t.Fatalf("token_hash = %q, want sha256 гранта %q", stored, want)
	}
}

// Длина и источник случайности гранта: 32 байта из crypto/rand, наружу --
// 64 шестнадцатеричных символа. Короткий грант подбирается, и никакие гейты
// вокруг этого не спасут.
func TestWebLinkTokenIsThirtyTwoRandomBytes(t *testing.T) {
	raw, hash, err := newWebLinkToken()
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) != 64 {
		t.Fatalf("длина гранта = %d символов, want 64 (32 байта в hex)", len(raw))
	}
	decoded, err := hex.DecodeString(raw)
	if err != nil {
		t.Fatalf("грант не шестнадцатеричный: %v", err)
	}
	if len(decoded) != 32 {
		t.Fatalf("в гранте %d байт случайности, want 32", len(decoded))
	}
	sum := sha256.Sum256([]byte(raw))
	if want := hex.EncodeToString(sum[:]); hash != want {
		t.Fatalf("hash = %q, want sha256 гранта", hash)
	}
	// Два гранта подряд не совпадают: источник случайный, а не счётчик.
	other, _, err := newWebLinkToken()
	if err != nil {
		t.Fatal(err)
	}
	if other == raw {
		t.Fatal("два гранта подряд совпали -- источник случайности не случаен")
	}
}

// Связку ключей ко всему парку накопить нельзя.
func TestWebLinkFourthIssueEvictsOldest(t *testing.T) {
	_, h := webLinkDeps(t, 999, nil)
	first, _ := webLinkTokenFrom(t, issueWebLink(t, h, 999))
	for i := 0; i < 3; i++ {
		webLinkTokenFrom(t, issueWebLink(t, h, 999))
	}

	rec := redeemWebLink(t, h, first, "198.51.100.7:40001")
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("самая старая ссылка после четвёртой выдачи: код %d, want 401", rec.Code)
	}
}

// Смена админа в конфиге гасит все ссылки прежнего: обмен каждый раз сверяет
// адресата гранта с текущим админом.
func TestWebLinkAdminChangeKillsOldGrants(t *testing.T) {
	deps, h := webLinkDeps(t, 999, nil)
	token, _ := webLinkTokenFrom(t, issueWebLink(t, h, 999))
	if rec := redeemWebLink(t, h, token, "198.51.100.7:40001"); rec.Code != http.StatusOK {
		t.Fatalf("до смены админа ссылка обязана работать: код %d", rec.Code)
	}

	changed := deps
	changed.TelegramAdminUserID = 1000
	after := NewMux(changed)
	rec := redeemWebLink(t, after, token, "198.51.100.7:40001")
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("после смены админа: код %d, want 401 (тело %s)", rec.Code, rec.Body.String())
	}
}

// Сам грант не появляется ни в журнале, ни в ответе обмена: в журнале --
// факт, получатель и хеш-префикс, как это делает AuthMiddleware.
func TestWebLinkNeverAppearsInLogsOrResponse(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	_, h := webLinkDeps(t, 999, logger)

	token, _ := webLinkTokenFrom(t, issueWebLink(t, h, 999))
	rec := redeemWebLink(t, h, token, "198.51.100.7:40001")
	if rec.Code != http.StatusOK {
		t.Fatalf("обмен: код %d, тело %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), token) {
		t.Fatalf("ответ обмена вернул сам грант: %s", rec.Body.String())
	}
	logged := buf.String()
	if strings.Contains(logged, token) {
		t.Fatal("грант утёк в журнал целиком")
	}
	if !strings.Contains(logged, webLinkHashPrefix(token)) {
		t.Fatalf("в журнале нет хеш-префикса гранта: %s", logged)
	}
	if !strings.Contains(logged, "999") {
		t.Fatalf("в журнале нет получателя ссылки: %s", logged)
	}
}

// Отказ не-админу -- 404, а не 403: так ведут себя роутерные эндпоинты
// мини-аппа, и по коду ответа нельзя понять, существует ли поверхность.
func TestWebLinkIssueDeniedToNonAdminWith404(t *testing.T) {
	_, h := webLinkDeps(t, 999, nil)
	rec := issueWebLink(t, h, 100) // владелец роутера, не админ
	if rec.Code != http.StatusNotFound {
		t.Fatalf("не-админ: код %d, want 404 (тело %s)", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "#token=") {
		t.Fatal("не-админу выдали ссылку")
	}
}

// Публичного адреса по https нет -- ссылку выдать нельзя, и об этом надо
// сказать словами: серая кнопка без объяснения хуже отказа.
func TestWebLinkIssueRefusesWithoutHTTPSPublicBase(t *testing.T) {
	for _, base := range []string{"", "http://wg.example.com"} {
		d, _, _, _ := seedMiniappFleet(t)
		h := NewMux(Deps{
			DB:                  d,
			TelegramBotToken:    "test-bot-token",
			TelegramAdminUserID: 999,
			DashboardToken:      "dashboard-secret",
			PublicBaseURL:       base,
		})
		rec := issueWebLink(t, h, 999)
		if rec.Code != http.StatusConflict {
			t.Fatalf("base=%q: код %d, want 409 (тело %s)", base, rec.Code, rec.Body.String())
		}
		if !strings.Contains(rec.Body.String(), WebLinkCopyNoPublicBase) {
			t.Fatalf("base=%q: отказ обязан сказать «%s», а сказал: %s", base, WebLinkCopyNoPublicBase, rec.Body.String())
		}
	}
}

// Срок жизни и лимит живых ссылок сказаны человеку в ответе, а не спрятаны
// в коде.
func TestWebLinkIssueSaysTTLAndLimitInWords(t *testing.T) {
	_, h := webLinkDeps(t, 999, nil)
	_, resp := webLinkTokenFrom(t, issueWebLink(t, h, 999))

	if resp.Notice != webLinkCopyNotice {
		t.Fatalf("notice = %q, want %q", resp.Notice, webLinkCopyNotice)
	}
	if !strings.Contains(resp.Notice, "12 часов") {
		t.Fatalf("срок жизни не сказан словами: %q", resp.Notice)
	}
	if resp.LimitNotice != webLinkCopyLimit {
		t.Fatalf("limit_notice = %q, want %q", resp.LimitNotice, webLinkCopyLimit)
	}
	if !strings.HasPrefix(resp.URL, testPublicBase+"/dashboard/login#token=") {
		t.Fatalf("ссылка = %q, want вход дашборда с фрагментом", resp.URL)
	}
	expires, err := time.Parse(time.RFC3339, resp.ExpiresAt)
	if err != nil {
		t.Fatalf("expires_at = %q: %v", resp.ExpiresAt, err)
	}
	if left := time.Until(expires); left < 11*time.Hour || left > 13*time.Hour {
		t.Fatalf("срок ссылки = %v, want около 12 часов", left)
	}
}

// Просроченная, отозванная и несуществующая ссылка снаружи неразличимы: тому,
// кто подбирает, ответ не должен подсказывать, был ли грант вообще.
func TestWebLinkRefusalsAreIndistinguishable(t *testing.T) {
	deps, h := webLinkDeps(t, 999, nil)
	now := time.Date(2026, 9, 12, 10, 0, 0, 0, time.UTC)
	old := webLinkNow
	webLinkNow = func() time.Time { return now }
	t.Cleanup(func() { webLinkNow = old })

	expiredToken, _ := webLinkTokenFrom(t, issueWebLink(t, h, 999))
	revokedToken, revokedResp := webLinkTokenFrom(t, issueWebLink(t, h, 999))
	_ = revokedResp
	if err := deps.DB.WebLinks().Delete(webLinkHash(revokedToken)); err != nil {
		t.Fatal(err)
	}
	webLinkNow = func() time.Time { return now.Add(webLinkTTL + time.Minute) }

	bodies := map[string]string{}
	for name, token := range map[string]string{
		"просроченная":   expiredToken,
		"отозванная":     revokedToken,
		"несуществующая": strings.Repeat("f", 64),
	} {
		rec := redeemWebLink(t, h, token, "198.51.100.7:40001")
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("%s: код %d, want 401", name, rec.Code)
		}
		bodies[name] = rec.Body.String()
	}
	first := ""
	for name, body := range bodies {
		if first == "" {
			first = body
			continue
		}
		if body != first {
			t.Fatalf("ответы различимы: %s = %q, а другой = %q", name, body, first)
		}
	}
}

// Обмен выдаёт ровно ту же сессию, что и обычный вход по токену: второй
// механизм сессии не заводится.
func TestWebLinkRedeemGivesTheUsualDashboardSession(t *testing.T) {
	_, h := webLinkDeps(t, 999, nil)
	token, _ := webLinkTokenFrom(t, issueWebLink(t, h, 999))
	rec := redeemWebLink(t, h, token, "198.51.100.7:40001")
	cookie := dashboardCookieFrom(rec)
	if cookie == nil {
		t.Fatal("cookie дашборда не выдана")
	}

	// Сессия та самая: мост веб-управления узнаёт её как вход админа.
	who := httptest.NewRequest(http.MethodGet, "/v1/miniapp/session", nil)
	who.AddCookie(cookie)
	whoRec := httptest.NewRecorder()
	h.ServeHTTP(whoRec, who)
	if whoRec.Code != http.StatusOK || !strings.Contains(whoRec.Body.String(), `"via":"web"`) {
		t.Fatalf("сессия по этой cookie: код %d тело %s", whoRec.Code, whoRec.Body.String())
	}
}

// Лимит попыток стоит на трёх входах сразу, а не на одном.
func TestRemoteRateLimitGuardsThreeEntrances(t *testing.T) {
	_, h := webLinkDeps(t, 999, nil)
	const remote = "203.0.113.9:40001"

	hit := func(req *http.Request) int {
		req.RemoteAddr = remote
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Code
	}
	login := func() int {
		req := httptest.NewRequest(http.MethodPost, "/v1/dashboard/login", strings.NewReader(`{"token":"wrong"}`))
		req.Header.Set("Content-Type", "application/json")
		return hit(req)
	}

	limited := false
	for i := 0; i < entranceBurst+2; i++ {
		if login() == http.StatusTooManyRequests {
			limited = true
			break
		}
	}
	if !limited {
		t.Fatalf("вход дашборда не ограничивает перебор за %d попыток", entranceBurst+2)
	}

	redeem := httptest.NewRequest(http.MethodPost, "/v1/dashboard/web-link/redeem", strings.NewReader(`{"token":"guess"}`))
	redeem.Header.Set("Content-Type", "application/json")
	if code := hit(redeem); code != http.StatusTooManyRequests {
		t.Fatalf("обмен ссылки: код %d, want 429 -- тот же лимит", code)
	}

	session := httptest.NewRequest(http.MethodPost, "/v1/miniapp/session", strings.NewReader(`{"init_data":"nope"}`))
	session.Header.Set("Content-Type", "application/json")
	if code := hit(session); code != http.StatusTooManyRequests {
		t.Fatalf("вход мини-аппа: код %d, want 429 -- тот же лимит", code)
	}
}
