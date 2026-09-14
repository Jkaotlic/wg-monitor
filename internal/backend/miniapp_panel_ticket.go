package backend

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"html"
	"net/http"
	"sync"
	"time"

	"github.com/Jkaotlic/wg-monitor/internal/backend/db"
)

// Одноразовый билет на панель awg-manager роутера (решение оператора 14.09).
//
// Сессия мини-аппа -- cookie внутри вебвью Telegram, а tg.openLink открывает
// ВНЕШНИЙ браузер, где этой cookie нет. Поэтому переход идёт по билету:
//
//  1. мини-апп в своей сессии просит билет (гейт -- владелец и админ);
//  2. внешний браузер открывает /v1/panel/<билет> -- страницу с кнопкой.
//     GET билет НЕ тратит: браузер вправе прогреть адрес заранее, и
//     одноразовая ссылка сгорела бы до нажатия;
//  3. нажатие -- POST на тот же адрес: билет сгорает, права и адрес
//     перепроверяются в этот момент, ответ -- 302 на панель.
//
// Адрес панели не попадает ни в один ответ API: только в Location последнего
// шага, у того, кто сам нажал. Хранилище в памяти: билет живёт минуту, и
// потерять его при рестарте бэкенда -- дешевле, чем заводить таблицу.

const (
	panelTicketTTL   = time.Minute
	panelTicketBytes = 32
	// panelTicketMax -- потолок живых билетов на весь бэкенд. Каждый выдаётся
	// только по сессии владельца или админа, так что потолок -- страховка от
	// зацикленного клиента, а не от подбора.
	panelTicketMax = 256
)

var panelTicketNow = time.Now

type panelTicket struct {
	routerID       int64
	telegramUserID int64
	expires        time.Time
}

type panelTicketStore struct {
	mu      sync.Mutex
	tickets map[string]panelTicket // ключ -- sha256 билета, сам билет не хранится
}

func newPanelTicketStore() *panelTicketStore {
	return &panelTicketStore{tickets: map[string]panelTicket{}}
}

var errPanelTicketsFull = errors.New("too many live panel tickets")

func (s *panelTicketStore) issue(routerID, telegramUserID int64) (string, error) {
	b := make([]byte, panelTicketBytes)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	raw := hex.EncodeToString(b)
	now := panelTicketNow()
	s.mu.Lock()
	defer s.mu.Unlock()
	for k, t := range s.tickets {
		if !now.Before(t.expires) {
			delete(s.tickets, k)
		}
	}
	if len(s.tickets) >= panelTicketMax {
		return "", errPanelTicketsFull
	}
	s.tickets[webLinkHash(raw)] = panelTicket{routerID: routerID, telegramUserID: telegramUserID, expires: now.Add(panelTicketTTL)}
	return raw, nil
}

// live -- жив ли билет, не тратя его.
func (s *panelTicketStore) live(raw string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.tickets[webLinkHash(raw)]
	return ok && panelTicketNow().Before(t.expires)
}

// take тратит билет: второй раз он не найдётся, даже если первый переход
// закончился отказом.
func (s *panelTicketStore) take(raw string) (panelTicket, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := webLinkHash(raw)
	t, ok := s.tickets[key]
	delete(s.tickets, key)
	if !ok || !panelTicketNow().Before(t.expires) {
		return panelTicket{}, false
	}
	return t, true
}

func miniappPanelTicketHandler(d Deps, store *panelTicketStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		telegramUserID, _ := miniappUserFromContext(r.Context())
		routerID, ok := parseMiniappRouterID(r)
		// Гейт -- владелец и админ (решение оператора № 9). Оператор роутера
		// и незнакомец получают 404 раньше, чем узнают о роутере.
		if !ok || !miniappIsOwner(d, telegramUserID, routerID) {
			writeJSONError(w, http.StatusNotFound, "not_found", "router not found")
			return
		}
		u, err := d.DB.Users().GetByID(routerID)
		if errors.Is(err, db.ErrUserNotFound) {
			writeJSONError(w, http.StatusNotFound, "not_found", "router not found")
			return
		}
		if err != nil {
			writeJSONError(w, http.StatusInternalServerError, errCodeInternal, "router lookup failed")
			return
		}
		if _, ok := panelAddress(u.AWGMURL); !ok {
			writeJSONError(w, http.StatusConflict, "panel_unknown", "panel address is not stored")
			return
		}
		raw, err := store.issue(routerID, telegramUserID)
		if err != nil {
			writeJSONError(w, http.StatusServiceUnavailable, "try_later", "too many panel links right now")
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_ = json.NewEncoder(w).Encode(struct {
			OpenPath     string `json:"open_path"`
			ExpiresInSec int    `json:"expires_in_sec"`
		}{OpenPath: "/v1/panel/" + raw, ExpiresInSec: int(panelTicketTTL / time.Second)})
	}
}

func panelPageHeaders(w http.ResponseWriter) {
	h := w.Header()
	h.Set("Cache-Control", "no-store")
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("X-Robots-Tag", "noindex")
	h.Set("X-Frame-Options", "DENY")
	h.Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'")
}

func writePanelPage(w http.ResponseWriter, status int, title, body string, button bool) {
	panelPageHeaders(w)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	form := ""
	if button {
		form = `<form method="post"><button type="submit">Открыть панель роутера</button></form>`
	}
	_, _ = w.Write([]byte(`<!doctype html><html lang="ru"><head><meta charset="utf-8">` +
		`<meta name="viewport" content="width=device-width,initial-scale=1"><title>` + html.EscapeString(title) + `</title>` +
		`<style>body{font:16px system-ui,sans-serif;margin:0;padding:24px 16px;max-width:32rem}` +
		`button{font:inherit;padding:12px 20px;border-radius:10px;border:0;background:#2481cc;color:#fff;width:100%}</style>` +
		`</head><body><h1>` + html.EscapeString(title) + `</h1><p>` + html.EscapeString(body) + `</p>` + form + `</body></html>`))
}

const panelLinkDead = "Ссылка одноразовая и живёт минуту. Вернитесь в приложение и нажмите «Открыть панель роутера» ещё раз."

// panelTicketPageHandler -- страница во внешнем браузере. Билет не тратит.
func panelTicketPageHandler(store *panelTicketStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !store.live(r.PathValue("ticket")) {
			writePanelPage(w, http.StatusNotFound, "Ссылка устарела", panelLinkDead, false)
			return
		}
		writePanelPage(w, http.StatusOK, "Панель роутера",
			"Панель спросит свой логин и пароль — мы их не знаем и не храним.", true)
	}
}

// panelTicketRedeemHandler -- нажатие. Билет сгорает, права и адрес
// перепроверяются сейчас, а не на выдаче: за минуту владельца могли
// разжаловать, а адрес -- стереть.
func panelTicketRedeemHandler(d Deps, store *panelTicketStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		raw := r.PathValue("ticket")
		t, ok := store.take(raw)
		if !ok || !miniappIsOwner(d, t.telegramUserID, t.routerID) {
			writePanelPage(w, http.StatusNotFound, "Ссылка устарела", panelLinkDead, false)
			return
		}
		u, err := d.DB.Users().GetByID(t.routerID)
		if err != nil {
			writePanelPage(w, http.StatusNotFound, "Ссылка устарела", panelLinkDead, false)
			return
		}
		addr, ok := panelAddress(u.AWGMURL)
		if !ok {
			writePanelPage(w, http.StatusConflict, "Адрес панели не сохранён",
				"Мы больше не знаем адрес панели этого роутера, поэтому открыть её нельзя.", false)
			return
		}
		if d.Logger != nil {
			d.Logger.Info("панель роутера: переход по билету",
				"router_id", t.routerID, "telegram_user_id", t.telegramUserID, "ticket_hash_prefix", webLinkHashPrefix(raw))
		}
		panelPageHeaders(w)
		http.Redirect(w, r, addr, http.StatusFound)
	}
}
