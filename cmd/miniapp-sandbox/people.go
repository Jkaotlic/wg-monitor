package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sort"
	"time"

	"github.com/Jkaotlic/wg-monitor/internal/backend/db"
)

// Справочник людей v0.58 (GET /v1/miniapp/people) для экрана «Доступ». Пока
// бэкенд этот адрес не знает (404), песочница отвечает сама: роли -- из
// засеянной базы, имена -- вымышленные. Когда бэкенд его заведёт, отвечает
// настоящий обработчик, фикстура молчит.

type sbPersonRouter struct {
	ID       int64  `json:"id"`
	Nickname string `json:"nickname"`
	Role     string `json:"role"`
}

type sbPerson struct {
	TelegramUserID int64            `json:"telegram_user_id"`
	Name           string           `json:"name"`
	Username       string           `json:"username"`
	LastSeenAt     *time.Time       `json:"last_seen_at"`
	IsAdmin        bool             `json:"is_admin"`
	Routers        []sbPersonRouter `json:"routers"`
}

type sbNamed struct {
	name, username string
	seenAgo        time.Duration // 0 -- не видели
}

// Вымышленные люди: зритель песочницы и трое без доступа -- «ждут».
func sandboxPeopleNames(viewer int64) map[int64]sbNamed {
	return map[int64]sbNamed{
		viewer:        {"Админ Песочный", "sandbox_admin", 2 * time.Hour},
		viewer + 1000: {"Хозяин Парка", "", 26 * time.Hour},
		777001:        {"Ольга Новикова", "olga_nov", 5 * time.Minute},
		777002:        {"Пётр Сидоров", "", 3 * 24 * time.Hour},
		777003:        {"", "rook42", 40 * time.Minute},
		777004:        {"", "", 0},
	}
}

func sandboxPeople(d *db.DB, viewer, admin int64, now time.Time) ([]sbPerson, error) {
	routers := map[int64][]sbPersonRouter{}
	users, err := d.Users().GetAll()
	if err != nil {
		return nil, err
	}
	for _, u := range users {
		if u.TelegramUserID != nil && *u.TelegramUserID > 0 {
			routers[*u.TelegramUserID] = append(routers[*u.TelegramUserID], sbPersonRouter{u.ID, u.Nickname, "owner"})
		}
		ops, err := d.RouterOperators().List(u.ID)
		if err != nil {
			return nil, err
		}
		for _, op := range ops {
			routers[op.TelegramUserID] = append(routers[op.TelegramUserID], sbPersonRouter{u.ID, u.Nickname, "operator"})
		}
	}
	names := sandboxPeopleNames(viewer)
	ids := map[int64]bool{admin: true}
	for id := range routers {
		ids[id] = true
	}
	for id := range names {
		ids[id] = true
	}
	out := make([]sbPerson, 0, len(ids))
	for id := range ids {
		n := names[id]
		p := sbPerson{TelegramUserID: id, Name: n.name, Username: n.username, IsAdmin: id == admin, Routers: routers[id]}
		if p.Routers == nil {
			p.Routers = []sbPersonRouter{}
		}
		if n.seenAgo > 0 {
			t := now.Add(-n.seenAgo).UTC()
			p.LastSeenAt = &t
		}
		out = append(out, p)
	}
	// Порядок контракта: без доступа нигде и писавшие за 7 дней -- первыми,
	// потом по давности, не виденные -- в конце по номеру.
	waiting := func(p sbPerson) bool {
		return len(p.Routers) == 0 && p.LastSeenAt != nil && now.Sub(*p.LastSeenAt) <= 7*24*time.Hour
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if waiting(a) != waiting(b) {
			return waiting(a)
		}
		if (a.LastSeenAt == nil) != (b.LastSeenAt == nil) {
			return a.LastSeenAt != nil
		}
		if a.LastSeenAt != nil && !a.LastSeenAt.Equal(*b.LastSeenAt) {
			return a.LastSeenAt.After(*b.LastSeenAt)
		}
		return a.TelegramUserID < b.TelegramUserID
	})
	return out, nil
}

// withPeopleFixture отвечает на /v1/miniapp/people, только если настоящий
// бэкенд ответил 404, и только админу -- как и настоящий адрес.
func withPeopleFixture(next http.Handler, d *db.DB, viewer, admin int64) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v1/miniapp/people" {
			next.ServeHTTP(w, r)
			return
		}
		rec := httptest.NewRecorder()
		next.ServeHTTP(rec, r)
		if rec.Code != http.StatusNotFound || viewer != admin {
			for k, v := range rec.Header() {
				w.Header()[k] = v
			}
			w.WriteHeader(rec.Code)
			_, _ = w.Write(rec.Body.Bytes())
			return
		}
		people, err := sandboxPeople(d, viewer, admin, time.Now())
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"people": people})
	})
}
