package backend

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/Jkaotlic/wg-monitor/internal/backend/db"
	"github.com/Jkaotlic/wg-monitor/internal/backend/tg"
)

// fakePeopleTG -- Telegram для дотягивания имён: знает только тех, кто в
// known; ошибки по номеру -- из errs; на остальных -- 400 «chat not found».
// block != nil -- каждый вызов ждёт close(block) или отмены контекста.
type fakePeopleTG struct {
	mu    sync.Mutex
	known map[int64]tg.ChatInfo
	errs  map[int64]error
	calls []int64
	block chan struct{}
}

func (f *fakePeopleTG) GetChat(ctx context.Context, chatID int64) (tg.ChatInfo, error) {
	f.mu.Lock()
	f.calls = append(f.calls, chatID)
	block := f.block
	f.mu.Unlock()
	if block != nil {
		select {
		case <-block:
		case <-ctx.Done():
			return tg.ChatInfo{}, ctx.Err()
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if err, ok := f.errs[chatID]; ok {
		return tg.ChatInfo{}, err
	}
	if info, ok := f.known[chatID]; ok {
		return info, nil
	}
	return tg.ChatInfo{}, &tg.APIError{Method: "getChat", Code: 400, Description: "Bad Request: chat not found"}
}

func (f *fakePeopleTG) Calls() []int64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]int64(nil), f.calls...)
}

func (f *fakePeopleTG) reset() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = nil
}

func newPeopleDB(t *testing.T) *db.DB {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "people.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return d
}

func getPeople(t *testing.T, h http.Handler, as int64) (*httptest.ResponseRecorder, miniappPeopleResp) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/v1/miniapp/people", nil)
	req.AddCookie(miniappSessionCookieFor(t, "test-bot-token", as))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	var resp miniappPeopleResp
	if rec.Code == http.StatusOK {
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatalf("json: %v body=%s", err, rec.Body.String())
		}
	}
	return rec, resp
}

func peopleIDs(resp miniappPeopleResp) []int64 {
	out := make([]int64, 0, len(resp.People))
	for _, p := range resp.People {
		out = append(out, p.TelegramUserID)
	}
	return out
}

// Вход в мини-апп кладёт человека в справочник: имя, фамилия, ник.
func TestMiniappSessionRecordsPerson(t *testing.T) {
	d := newPeopleDB(t)
	h := NewMux(Deps{DB: d, TelegramBotToken: "test-bot-token", TelegramAdminUserID: 999})
	raw := signTestInitData(t, "test-bot-token", map[string]string{
		"auth_date": strconv.FormatInt(time.Now().Add(-time.Minute).Unix(), 10),
		"user":      `{"id":4301,"first_name":"Выдуманный","last_name":"Гость","username":"made_up_guest"}`,
	})
	body, _ := json.Marshal(miniappSessionReq{InitData: raw})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/miniapp/session", bytes.NewReader(body)))
	if rec.Code != http.StatusOK {
		t.Fatalf("session %d %s", rec.Code, rec.Body.String())
	}
	list, err := d.People().List()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 {
		t.Fatalf("строк %d: %+v", len(list), list)
	}
	p := list[0]
	if p.TelegramUserID != 4301 || p.FirstName != "Выдуманный" || p.LastName != "Гость" || p.Username != "made_up_guest" ||
		p.Source != db.PersonSourceMiniapp || p.LastSeenAt == nil {
		t.Fatalf("строка = %+v", p)
	}
}

func TestMiniappPeopleForbiddenForNonAdmin(t *testing.T) {
	d, _, _, ownerTGID := seedMiniappFleet(t)
	h := NewMux(Deps{DB: d, TelegramBotToken: "test-bot-token", TelegramAdminUserID: 999})
	rec, _ := getPeople(t, h, ownerTGID)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("не-админ: want 403 как у соседних маршрутов доступа, got %d", rec.Code)
	}
}

func TestMiniappPeopleRequiresSession(t *testing.T) {
	d := newPeopleDB(t)
	h := NewMux(Deps{DB: d, TelegramBotToken: "test-bot-token", TelegramAdminUserID: 999})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/miniapp/people", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("без сессии: want 401, got %d", rec.Code)
	}
}

// Состав: справочник ∪ владельцы ∪ операторы ∪ админ; роли по всему парку;
// поля ответа -- ровно контракт спеки.
func TestMiniappPeopleCompositionAndRoles(t *testing.T) {
	d, ownedID, otherID, _ := seedMiniappFleet(t) // владельцы 100 (router-owned) и 200 (router-other)
	if err := d.RouterOperators().Add(ownedID, 300, 999); err != nil {
		t.Fatal(err)
	}
	if err := d.RouterOperators().Add(otherID, 100, 999); err != nil {
		t.Fatal(err)
	}
	seen := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
	if err := d.People().Seen(100, "Первый", "Владелец", "first_owner", db.PersonSourceMiniapp, seen); err != nil {
		t.Fatal(err)
	}
	if err := d.People().Seen(400, "Без", "Доступа", "", db.PersonSourceBot, seen); err != nil {
		t.Fatal(err)
	}
	h := NewMux(Deps{DB: d, TelegramBotToken: "test-bot-token", TelegramAdminUserID: 999})
	rec, resp := getPeople(t, h, 999)
	if rec.Code != http.StatusOK {
		t.Fatalf("admin: %d %s", rec.Code, rec.Body.String())
	}
	byID := map[int64]miniappPerson{}
	for _, p := range resp.People {
		byID[p.TelegramUserID] = p
	}
	for _, id := range []int64{100, 200, 300, 400, 999} {
		if _, ok := byID[id]; !ok {
			t.Fatalf("нет %d в %v", id, peopleIDs(resp))
		}
	}
	if len(resp.People) != 5 {
		t.Fatalf("лишние люди: %v", peopleIDs(resp))
	}

	p100 := byID[100]
	if p100.Name != "Первый Владелец" || p100.Username != "first_owner" || p100.IsAdmin {
		t.Fatalf("100 = %+v", p100)
	}
	if p100.LastSeenAt == nil || *p100.LastSeenAt != seen.Format(time.RFC3339) {
		t.Fatalf("100 last_seen_at = %v, want %s", p100.LastSeenAt, seen.Format(time.RFC3339))
	}
	want100 := []miniappPersonRouter{{ID: ownedID, Nickname: "router-owned", Role: "owner"}, {ID: otherID, Nickname: "router-other", Role: "operator"}}
	if len(p100.Routers) != 2 || p100.Routers[0] != want100[0] || p100.Routers[1] != want100[1] {
		t.Fatalf("100 routers = %+v, want %+v", p100.Routers, want100)
	}
	if r := byID[300].Routers; len(r) != 1 || r[0] != (miniappPersonRouter{ID: ownedID, Nickname: "router-owned", Role: "operator"}) {
		t.Fatalf("300 routers = %+v", r)
	}
	if !byID[999].IsAdmin {
		t.Fatalf("999 не админ: %+v", byID[999])
	}
	if byID[200].Name != "" || byID[200].Username != "" || byID[200].LastSeenAt != nil {
		t.Fatalf("200 без имени = %+v", byID[200])
	}

	// Сырой JSON: routers -- [] у ждущего, last_seen_at -- null у невиденного.
	var raw struct {
		People []map[string]json.RawMessage `json:"people"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatal(err)
	}
	for _, p := range raw.People {
		for _, k := range []string{"telegram_user_id", "name", "username", "last_seen_at", "is_admin", "routers"} {
			if _, ok := p[k]; !ok {
				t.Fatalf("нет поля %q в %v", k, p)
			}
		}
		if string(p["telegram_user_id"]) == "400" && string(p["routers"]) != "[]" {
			t.Fatalf("routers у ждущего = %s, want []", p["routers"])
		}
		if string(p["telegram_user_id"]) == "200" && string(p["last_seen_at"]) != "null" {
			t.Fatalf("last_seen_at у невиденного = %s, want null", p["last_seen_at"])
		}
	}
}

// Порядок: ждущие (без доступа, писали за 7 дней) -- по свежести; потом
// остальные по last_seen_at убыванию; без даты -- в конце по номеру.
func TestMiniappPeopleOrdering(t *testing.T) {
	d, ownedID, _, _ := seedMiniappFleet(t) // владельцы 100 и 200
	now := time.Now().UTC()
	seed := func(id int64, ago time.Duration) {
		if err := d.People().Seen(id, "Человек"+strconv.FormatInt(id, 10), "", "", db.PersonSourceBot, now.Add(-ago)); err != nil {
			t.Fatal(err)
		}
	}
	seed(100, 2*time.Hour)     // владелец, видели недавно
	seed(501, 3*time.Hour)     // ждёт, 3 часа назад
	seed(502, 5*time.Minute)   // ждёт, 5 минут назад
	seed(503, 10*24*time.Hour) // без доступа, но давно -- не «ждёт»
	seed(999, 30*time.Minute)  // админ
	if err := d.RouterOperators().Add(ownedID, 650, 999); err != nil {
		t.Fatal(err)
	}
	h := NewMux(Deps{DB: d, TelegramBotToken: "test-bot-token", TelegramAdminUserID: 999})
	rec, resp := getPeople(t, h, 999)
	if rec.Code != http.StatusOK {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	want := []int64{502, 501, 999, 100, 503, 200, 650}
	got := peopleIDs(resp)
	if len(got) != len(want) {
		t.Fatalf("порядок %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("порядок %v, want %v", got, want)
		}
	}
}

// seedOwnersWithoutNames -- n роутеров с владельцами 1001..1000+n без имён.
func seedOwnersWithoutNames(t *testing.T, d *db.DB, n int64) {
	t.Helper()
	for i := int64(1); i <= n; i++ {
		id, err := d.Users().Insert("router-"+strconv.FormatInt(i, 10), "tok-people-"+strconv.FormatInt(i, 10), "198.51.100.1", "awg11")
		if err != nil {
			t.Fatal(err)
		}
		if err := d.Users().SetTelegramUserID(id, 1000+i); err != nil {
			t.Fatal(err)
		}
	}
}

func personInResp(resp miniappPeopleResp, id int64) *miniappPerson {
	for i := range resp.People {
		if resp.People[i].TelegramUserID == id {
			return &resp.People[i]
		}
	}
	return nil
}

// Дотягивание имён getChat: только известным номерам без имени, не больше
// 10 за запрос, «чат не найден» -- номер остаётся без имени, повтор не чаще
// раза в сутки. Дотягивание идёт в фоне: имена -- со следующего открытия.
func TestMiniappPeopleBackfillsNames(t *testing.T) {
	d := newPeopleDB(t)
	seedOwnersWithoutNames(t, d, 12)
	// Ждущий без доступа и без имени -- getChat ему не положен: номер не «известный».
	if err := d.People().Seen(2001, "", "", "", db.PersonSourceBot, time.Now()); err != nil {
		t.Fatal(err)
	}
	f := &fakePeopleTG{known: map[int64]tg.ChatInfo{
		1001: {ID: 1001, Type: "private", FirstName: "Дотянут", LastName: "Первый", Username: "pulled_one"},
	}}
	bf := newMiniappPeopleBackfill()
	h := NewMux(Deps{DB: d, TelegramBotToken: "test-bot-token", TelegramAdminUserID: 999, PeopleTG: f, testPeopleBackfill: bf})

	rec, resp := getPeople(t, h, 999)
	if rec.Code != http.StatusOK {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	bf.wait()
	calls := f.Calls()
	if len(calls) != 10 {
		t.Fatalf("getChat вызван %d раз, want 10 (лимит): %v", len(calls), calls)
	}
	for _, c := range calls {
		if c == 2001 {
			t.Fatal("getChat для номера без доступа")
		}
	}
	if p := personInResp(resp, 1001); p == nil || p.Name != "" {
		t.Fatalf("первый ответ ждал дотягивания: %+v", p)
	}

	// Второе открытие: имя 1001 уже в ответе; первую десятку (999,
	// 1001..1009) пробовали сегодня -- остались 1010, 1011, 1012.
	f.reset()
	rec, resp = getPeople(t, h, 999)
	if rec.Code != http.StatusOK {
		t.Fatal(rec.Code)
	}
	bf.wait()
	if p := personInResp(resp, 1001); p == nil || p.Name != "Дотянут Первый" || p.Username != "pulled_one" || p.LastSeenAt != nil {
		t.Fatalf("1001 во втором ответе = %+v", p)
	}
	if calls := f.Calls(); len(calls) != 3 {
		t.Fatalf("второй запрос: getChat %v, want 3 непробованных", calls)
	}
	// Третий: все пробовали за сутки -- ни одного вызова.
	f.reset()
	if rec, _ := getPeople(t, h, 999); rec.Code != http.StatusOK {
		t.Fatal(rec.Code)
	}
	bf.wait()
	if calls := f.Calls(); len(calls) != 0 {
		t.Fatalf("повтор в те же сутки: %v", calls)
	}

	// Сутки спустя -- снова пробуем тех, кому не повезло.
	if err := d.People().MarkNameChecked(1002, time.Now().Add(-25*time.Hour)); err != nil {
		t.Fatal(err)
	}
	f.reset()
	if rec, _ := getPeople(t, h, 999); rec.Code != http.StatusOK {
		t.Fatal(rec.Code)
	}
	bf.wait()
	if calls := f.Calls(); len(calls) != 1 || calls[0] != 1002 {
		t.Fatalf("после суток: %v, want [1002]", calls)
	}
}

// Медленный Telegram не держит экран, а два открытия разом -- одно дотягивание.
func TestMiniappPeopleBackfillDoesNotBlockAndRunsOnce(t *testing.T) {
	d := newPeopleDB(t)
	seedOwnersWithoutNames(t, d, 3)
	f := &fakePeopleTG{block: make(chan struct{})}
	bf := newMiniappPeopleBackfill()
	h := NewMux(Deps{DB: d, TelegramBotToken: "test-bot-token", TelegramAdminUserID: 999, PeopleTG: f, testPeopleBackfill: bf})

	get := func() int {
		done := make(chan int, 1)
		go func() {
			rec, _ := getPeople(t, h, 999)
			done <- rec.Code
		}()
		select {
		case code := <-done:
			return code
		case <-time.After(2 * time.Second):
			t.Fatal("GET /people ждёт Telegram")
			return 0
		}
	}
	if code := get(); code != http.StatusOK {
		t.Fatalf("первый: %d", code)
	}
	// Ждём, пока фоновое дотягивание упрётся в первый getChat.
	deadline := time.Now().Add(2 * time.Second)
	for len(f.Calls()) == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if code := get(); code != http.StatusOK {
		t.Fatalf("второй: %d", code)
	}
	if calls := f.Calls(); len(calls) != 1 {
		t.Fatalf("пока первое висит: getChat %v, want ровно один", calls)
	}
	close(f.block)
	bf.wait()
	calls := f.Calls()
	seen := map[int64]bool{}
	for _, c := range calls {
		if seen[c] {
			t.Fatalf("номер %d спрошен дважды: %v", c, calls)
		}
		seen[c] = true
	}
	if len(calls) != 4 { // 999, 1001, 1002, 1003
		t.Fatalf("getChat %v, want 4 номера одним проходом", calls)
	}
}

// Без Telegram-клиента список всё равно отдаётся.
func TestMiniappPeopleWithoutTGClient(t *testing.T) {
	d, _, _, _ := seedMiniappFleet(t)
	h := NewMux(Deps{DB: d, TelegramBotToken: "test-bot-token", TelegramAdminUserID: 999})
	rec, resp := getPeople(t, h, 999)
	if rec.Code != http.StatusOK || len(resp.People) != 3 {
		t.Fatalf("%d %v", rec.Code, peopleIDs(resp))
	}
}
