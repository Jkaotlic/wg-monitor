package backend

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"
	"unicode"

	"github.com/Jkaotlic/wg-monitor/internal/backend/awg3panel"
	"github.com/Jkaotlic/wg-monitor/internal/backend/tg"
)

// awg3-панели оператора в мини-аппе (цикл v0.49, спека 2026-09-29): только
// админ. Пароль панели и .p12 приходят только телом формы и наружу не
// отдаются: в ответе -- password_set и cert_set.

// Awg3Panels -- awg3panel.Service глазами мини-аппа.
type Awg3Panels interface {
	List() ([]awg3panel.View, error)
	Create(ctx context.Context, in awg3panel.Input) (awg3panel.View, awg3panel.CheckResult, error)
	Update(ctx context.Context, id string, in awg3panel.Input) (awg3panel.View, *awg3panel.CheckResult, error)
	Delete(id string) error
	Peers(ctx context.Context, id, iface string) (awg3panel.Page, error)
	IssueDevice(ctx context.Context, id, iface, name string) (awg3panel.Issued, error)
	ConfigForRouter(ctx context.Context, id, iface, nickname string) (awg3panel.RouterConfig, error)
}

var _ Awg3Panels = (*awg3panel.Service)(nil)

// miniappAwg3MaxBody -- форма несёт .p12 в base64: до 64 КБ файла ≈ 88 КБ текста.
const miniappAwg3MaxBody = 128 << 10

type miniappAwg3Panel struct {
	ID           string `json:"id"`
	Label        string `json:"label"`
	BaseURL      string `json:"base_url"`
	User         string `json:"user"`
	Enabled      bool   `json:"enabled"`
	PasswordSet  bool   `json:"password_set"`
	CertSet      bool   `json:"cert_set"`
	CertSubject  string `json:"cert_subject"`
	CertNotAfter string `json:"cert_not_after,omitempty"`
	State        string `json:"state"`
	PausedUntil  string `json:"paused_until,omitempty"`
	Readonly     bool   `json:"readonly"`
}

func rfc3339(t time.Time) string { return t.UTC().Format(time.RFC3339) }

func miniappAwg3View(v awg3panel.View, now time.Time) miniappAwg3Panel {
	out := miniappAwg3Panel{
		ID: v.ID, Label: v.Label, BaseURL: v.BaseURL, User: v.User, Enabled: v.Enabled,
		PasswordSet: v.PasswordSet, CertSet: v.CertSet, CertSubject: v.CertSubject, State: "ok", Readonly: v.Readonly,
	}
	if !v.CertNotAfter.IsZero() {
		out.CertNotAfter = rfc3339(v.CertNotAfter)
	}
	switch {
	case !v.Enabled:
		out.State = "disabled"
	case v.Lock == awg3panel.LockBadPassword:
		out.State = "bad_password"
	case v.Lock == awg3panel.LockCert:
		out.State = "cert_rejected"
	case v.Lock == awg3panel.LockServerCert:
		out.State = "server_cert_rejected"
	case now.Before(v.PausedUntil):
		out.State = "paused"
		out.PausedUntil = rfc3339(v.PausedUntil)
	}
	return out
}

type miniappAwg3Check struct {
	Ran     bool   `json:"ran"`
	OK      bool   `json:"ok"`
	Code    string `json:"code,omitempty"`
	Message string `json:"message"`
	RetryAt string `json:"retry_at,omitempty"`
}

func miniappAwg3CheckView(r awg3panel.CheckResult) *miniappAwg3Check {
	if r.OK {
		return &miniappAwg3Check{Ran: true, OK: true, Message: fmt.Sprintf("Панель ответила: интерфейсов — %d.", r.Ifaces)}
	}
	code := miniappAwg3KindCode(r.Kind)
	out := &miniappAwg3Check{Ran: r.Ran, Code: code, Message: miniappCabinetErrorText(code)}
	if !r.Until.IsZero() {
		out.RetryAt = rfc3339(r.Until)
	}
	return out
}

// miniappAwg3Req -- форма панели. Несёт пароль и .p12: печать -- «[скрыто]».
type miniappAwg3Req struct {
	ID          string `json:"id"`
	Label       string `json:"label"`
	BaseURL     string `json:"base_url"`
	User        string `json:"user"`
	Password    string `json:"password"`
	P12Base64   string `json:"p12_base64"`
	P12Password string `json:"p12_password"`
	Enabled     *bool  `json:"enabled"`
}

func (miniappAwg3Req) String() string       { return miniappHiddenValue }
func (miniappAwg3Req) GoString() string     { return miniappHiddenValue }
func (miniappAwg3Req) LogValue() slog.Value { return slog.StringValue(miniappHiddenValue) }

func (req miniappAwg3Req) input() (awg3panel.Input, error) {
	in := awg3panel.Input{
		ID: req.ID, Label: req.Label, BaseURL: req.BaseURL, User: req.User,
		Password: req.Password, P12Password: req.P12Password, Enabled: req.Enabled,
	}
	if s := strings.TrimSpace(req.P12Base64); s != "" {
		raw, err := base64.StdEncoding.DecodeString(s)
		if err != nil {
			return in, &awg3panel.FieldError{Field: "p12", Reason: "Файл .p12 не дошёл целиком — выберите его заново"}
		}
		in.P12 = raw
	}
	return in, nil
}

// miniappAwg3Gate -- только админ (404 остальным, до тела), затем 503, если
// панели на сервере не подключены.
func miniappAwg3Gate(d Deps, w http.ResponseWriter, r *http.Request) bool {
	tgUser, _ := miniappUserFromContext(r.Context())
	if !miniappIsAdmin(tgUser, d.TelegramAdminUserID) {
		writeMiniappDeployError(w, http.StatusNotFound, "not_found", "Не найдено")
		return false
	}
	if d.Awg3Panels == nil {
		writeMiniappCabinetError(w, http.StatusServiceUnavailable, "awg3_not_configured")
		return false
	}
	return true
}

func miniappAwg3PathID(w http.ResponseWriter, r *http.Request) (string, bool) {
	id := strings.ToLower(strings.TrimSpace(r.PathValue("panel")))
	if !awg3panel.ValidInstanceID(id) {
		writeMiniappCabinetError(w, http.StatusNotFound, "awg3_not_found")
		return "", false
	}
	return id, true
}

func decodeMiniappAwg3Body(w http.ResponseWriter, r *http.Request, dst any) bool {
	if ct := strings.TrimSpace(strings.SplitN(r.Header.Get("Content-Type"), ";", 2)[0]); ct != "" && !strings.EqualFold(ct, "application/json") {
		writeMiniappCabinetError(w, http.StatusUnsupportedMediaType, errCodeUnsupportedCT)
		return false
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, miniappAwg3MaxBody)).Decode(dst); err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			writeMiniappCabinetError(w, http.StatusRequestEntityTooLarge, "awg3_too_large")
			return false
		}
		writeMiniappCabinetError(w, http.StatusBadRequest, errCodeBadJSON)
		return false
	}
	return true
}

func miniappAwg3KindCode(k awg3panel.Kind) string {
	switch k {
	case awg3panel.KindBadPassword:
		return "awg3_bad_password"
	case awg3panel.KindBanned:
		return "awg3_paused"
	case awg3panel.KindCert:
		return "awg3_cert_rejected"
	case awg3panel.KindServerCert:
		return "awg3_server_cert_rejected"
	case awg3panel.KindReadonly:
		return "awg3_readonly"
	case awg3panel.KindInvalid:
		return "awg3_invalid_name"
	case awg3panel.KindBadResponse:
		return "awg3_bad_response"
	case awg3panel.KindNotFound:
		return "awg3_iface_not_found"
	default:
		return "awg3_unreachable"
	}
}

type miniappAwg3ErrorBody struct {
	Code    string `json:"code"`
	Error   string `json:"error"`
	Message string `json:"message"`
	Field   string `json:"field,omitempty"`
	RetryAt string `json:"retry_at,omitempty"`
}

// writeMiniappAwg3Error -- отказ словами. Текст ошибки панели -- только в
// журнал (в нём нет секретов, но есть внутренности); человеку -- таблица.
func writeMiniappAwg3Error(d Deps, w http.ResponseWriter, op string, err error) {
	var fe *awg3panel.FieldError
	var pe *awg3panel.Error
	switch {
	case errors.As(err, &fe):
		writeMiniappCabinetJSON(w, http.StatusBadRequest, miniappAwg3ErrorBody{Code: "invalid_field", Error: "invalid_field", Message: fe.Reason, Field: fe.Field})
	case errors.Is(err, awg3panel.ErrInstanceNotFound):
		writeMiniappCabinetError(w, http.StatusNotFound, "awg3_not_found")
	case errors.Is(err, awg3panel.ErrInstanceExists):
		writeMiniappCabinetError(w, http.StatusConflict, "awg3_exists")
	case errors.Is(err, awg3panel.ErrInstanceDisabled):
		writeMiniappCabinetError(w, http.StatusConflict, "awg3_disabled")
	case errors.Is(err, awg3panel.ErrIfaceNotFound):
		writeMiniappCabinetError(w, http.StatusNotFound, "awg3_iface_not_found")
	case errors.Is(err, awg3panel.ErrNameTaken):
		writeMiniappCabinetError(w, http.StatusConflict, "awg3_name_taken")
	case errors.As(err, &pe):
		code := miniappAwg3KindCode(pe.Kind)
		status := http.StatusConflict
		switch pe.Kind {
		case awg3panel.KindUnreachable, awg3panel.KindBadResponse:
			status = http.StatusBadGateway
		case awg3panel.KindInvalid:
			status = http.StatusBadRequest
		case awg3panel.KindNotFound:
			status = http.StatusNotFound
		}
		body := miniappAwg3ErrorBody{Code: code, Error: code, Message: miniappCabinetErrorText(code)}
		if !pe.Until.IsZero() {
			body.RetryAt = rfc3339(pe.Until)
		}
		miniappCabinetLogger(d).Warn("awg3-панель: "+op+" не удалось", "kind", pe.Kind, "status", pe.Status, "err", pe.Error())
		writeMiniappCabinetJSON(w, status, body)
	default:
		miniappCabinetLogger(d).Error("awg3-панель: "+op+" не удалось", "err", err)
		writeMiniappCabinetError(w, http.StatusInternalServerError, errCodeInternal)
	}
}

func miniappAwg3ListHandler(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !miniappAwg3Gate(d, w, r) {
			return
		}
		views, err := d.Awg3Panels.List()
		if err != nil {
			writeMiniappAwg3Error(d, w, "список", err)
			return
		}
		now := time.Now()
		resp := struct {
			Panels []miniappAwg3Panel `json:"panels"`
		}{Panels: make([]miniappAwg3Panel, 0, len(views))}
		for _, v := range views {
			resp.Panels = append(resp.Panels, miniappAwg3View(v, now))
		}
		writeMiniappCabinetJSON(w, http.StatusOK, resp)
	}
}

type miniappAwg3SaveResp struct {
	Panel miniappAwg3Panel  `json:"panel"`
	Check *miniappAwg3Check `json:"check"`
}

func miniappAwg3CreateHandler(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !miniappAwg3Gate(d, w, r) {
			return
		}
		var req miniappAwg3Req
		if !decodeMiniappAwg3Body(w, r, &req) {
			return
		}
		in, err := req.input()
		if err != nil {
			writeMiniappAwg3Error(d, w, "добавление", err)
			return
		}
		v, res, err := d.Awg3Panels.Create(r.Context(), in)
		if err != nil {
			writeMiniappAwg3Error(d, w, "добавление", err)
			return
		}
		miniappCabinetLogger(d).Info("awg3-панель добавлена", "panel", v.ID, "check_ok", res.OK, "check_kind", res.Kind)
		writeMiniappCabinetJSON(w, http.StatusCreated, miniappAwg3SaveResp{Panel: miniappAwg3View(v, time.Now()), Check: miniappAwg3CheckView(res)})
	}
}

func miniappAwg3UpdateHandler(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !miniappAwg3Gate(d, w, r) {
			return
		}
		id, ok := miniappAwg3PathID(w, r)
		if !ok {
			return
		}
		var req miniappAwg3Req
		if !decodeMiniappAwg3Body(w, r, &req) {
			return
		}
		in, err := req.input()
		if err != nil {
			writeMiniappAwg3Error(d, w, "изменение", err)
			return
		}
		v, res, err := d.Awg3Panels.Update(r.Context(), id, in)
		if err != nil {
			writeMiniappAwg3Error(d, w, "изменение", err)
			return
		}
		out := miniappAwg3SaveResp{Panel: miniappAwg3View(v, time.Now())}
		if res != nil {
			out.Check = miniappAwg3CheckView(*res)
		}
		miniappCabinetLogger(d).Info("awg3-панель изменена", "panel", id, "password_changed", req.Password != "", "p12_changed", req.P12Base64 != "")
		writeMiniappCabinetJSON(w, http.StatusOK, out)
	}
}

func miniappAwg3DeleteHandler(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !miniappAwg3Gate(d, w, r) {
			return
		}
		id, ok := miniappAwg3PathID(w, r)
		if !ok {
			return
		}
		var body struct {
			Confirm string `json:"confirm"`
		}
		if !decodeMiniappCabinetBody(w, r, &body) {
			return
		}
		views, err := d.Awg3Panels.List()
		if err != nil {
			writeMiniappAwg3Error(d, w, "удаление", err)
			return
		}
		label := ""
		for _, v := range views {
			if v.ID == id {
				label = v.Label
				if label == "" {
					label = v.ID
				}
			}
		}
		if label == "" {
			writeMiniappCabinetError(w, http.StatusNotFound, "awg3_not_found")
			return
		}
		if !confirmPhraseMatches(body.Confirm, label) {
			writeMiniappCabinetError(w, http.StatusBadRequest, "confirm_mismatch")
			return
		}
		if err := d.Awg3Panels.Delete(id); err != nil {
			writeMiniappAwg3Error(d, w, "удаление", err)
			return
		}
		miniappCabinetLogger(d).Info("awg3-панель удалена", "panel", id)
		w.WriteHeader(http.StatusNoContent)
	}
}

type miniappAwg3Iface struct {
	ID        string `json:"id"`
	Title     string `json:"title"`
	Interface string `json:"interface"`
}

type miniappAwg3Summary struct {
	PeersTotal  int   `json:"peers_total"`
	PeersOnline int   `json:"peers_online"`
	PeersStale  int   `json:"peers_stale"`
	PeersNever  int   `json:"peers_never"`
	RxBytes     int64 `json:"rx_bytes"`
	TxBytes     int64 `json:"tx_bytes"`
}

type miniappAwg3Router struct {
	ID       int64  `json:"id"`
	Nickname string `json:"nickname"`
}

type miniappAwg3Peer struct {
	ID              string             `json:"id"`
	Name            string             `json:"name"`
	Address         string             `json:"address"`
	Enabled         bool               `json:"enabled"`
	State           string             `json:"state"`
	HandshakeAgeSec int64              `json:"handshake_age_sec"`
	RxBytes         int64              `json:"rx_bytes"`
	TxBytes         int64              `json:"tx_bytes"`
	Router          *miniappAwg3Router `json:"router"`
}

type miniappAwg3PeersResp struct {
	Panel     miniappAwg3Panel   `json:"panel"`
	Ifaces    []miniappAwg3Iface `json:"ifaces"`
	Iface     string             `json:"iface"`
	Summary   miniappAwg3Summary `json:"summary"`
	Peers     []miniappAwg3Peer  `json:"peers"`
	FetchedAt string             `json:"fetched_at"`
}

// miniappAwg3Routers -- «wgmon-<ник>» → роутер парка. Сбой базы экран не
// валит: ярлыков просто не будет.
func miniappAwg3Routers(d Deps) map[string]miniappAwg3Router {
	out := map[string]miniappAwg3Router{}
	if d.DB == nil {
		return out
	}
	users, err := d.DB.Users().GetAll()
	if err != nil {
		miniappCabinetLogger(d).Warn("awg3-панель: список роутеров не прочитан", "err", err)
		return out
	}
	for _, u := range users {
		out[awg3panel.RouterPeerPrefix+u.Nickname] = miniappAwg3Router{ID: u.ID, Nickname: u.Nickname}
	}
	return out
}

func miniappAwg3PeersHandler(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !miniappAwg3Gate(d, w, r) {
			return
		}
		id, ok := miniappAwg3PathID(w, r)
		if !ok {
			return
		}
		page, err := d.Awg3Panels.Peers(r.Context(), id, r.URL.Query().Get("iface"))
		if err != nil {
			writeMiniappAwg3Error(d, w, "пиры", err)
			return
		}
		now := time.Now()
		routers := miniappAwg3Routers(d)
		resp := miniappAwg3PeersResp{
			Panel: miniappAwg3View(page.Panel, now), Iface: page.Iface,
			Ifaces: make([]miniappAwg3Iface, 0, len(page.Ifaces)), Peers: make([]miniappAwg3Peer, 0, len(page.Peers)),
			Summary: miniappAwg3Summary{
				PeersTotal: page.Summary.PeersTotal, PeersOnline: page.Summary.PeersOnline, PeersStale: page.Summary.PeersStale,
				PeersNever: page.Summary.PeersNever, RxBytes: page.Summary.RxBytes, TxBytes: page.Summary.TxBytes,
			},
		}
		if !page.FetchedAt.IsZero() {
			resp.FetchedAt = rfc3339(page.FetchedAt)
		}
		for _, i := range page.Ifaces {
			resp.Ifaces = append(resp.Ifaces, miniappAwg3Iface{ID: i.ID, Title: i.Title, Interface: i.Interface})
		}
		for _, p := range page.Peers {
			state, age := awg3panel.PeerState(p, now)
			view := miniappAwg3Peer{ID: p.ID, Name: p.Name, Address: p.Address, Enabled: p.Enabled, State: state, HandshakeAgeSec: age, RxBytes: p.RxBytes, TxBytes: p.TxBytes}
			if rt, ok := routers[strings.TrimSpace(p.Name)]; ok {
				view.Router = &rt
			}
			resp.Peers = append(resp.Peers, view)
		}
		w.Header().Set("Cache-Control", "no-store")
		writeMiniappCabinetJSON(w, http.StatusOK, resp)
	}
}

type miniappAwg3DeviceResp struct {
	Name        string `json:"name"`
	Address     string `json:"address"`
	QRPNGBase64 string `json:"qr_png_base64"`
	DM          string `json:"dm"`
}

// miniappAwg3ConfFilename -- имя .conf из имени устройства: буквы, цифры,
// «.», «_», «-»; прочее -- «-».
func miniappAwg3ConfFilename(name string) string {
	var b strings.Builder
	for _, r := range strings.TrimSpace(name) {
		switch {
		case unicode.IsLetter(r), unicode.IsDigit(r), r == '_', r == '-', r == '.':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	out := strings.Trim(b.String(), "-.")
	if out == "" {
		out = "device"
	}
	return out + ".conf"
}

// miniappAwg3DeviceHandler -- «Конфиг на устройство» (спека, решение 8): новый
// пир на панели, QR на экран, .conf и QR в личку нажавшему. Сам конфиг
// в ответ не идёт -- только в личку.
func miniappAwg3DeviceHandler(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !miniappAwg3Gate(d, w, r) {
			return
		}
		id, ok := miniappAwg3PathID(w, r)
		if !ok {
			return
		}
		var req struct {
			Iface string `json:"iface"`
			Name  string `json:"name"`
		}
		if !decodeMiniappCabinetBody(w, r, &req) {
			return
		}
		tgUser, _ := miniappUserFromContext(r.Context())
		issued, err := d.Awg3Panels.IssueDevice(r.Context(), id, req.Iface, req.Name)
		if err != nil {
			writeMiniappAwg3Error(d, w, "устройство", err)
			return
		}
		resp := miniappAwg3DeviceResp{
			Name: issued.Name, Address: issued.Address, QRPNGBase64: issued.QRPNGBase64,
			DM: miniappAwg3SendDevice(d, r, tgUser, id, issued),
		}
		w.Header().Set("Cache-Control", "no-store")
		writeMiniappCabinetJSON(w, http.StatusCreated, resp)
	}
}

// miniappAwg3SendDevice -- .conf документом и QR фото в личку. Пир уже
// выпущен: отказ Telegram не отменяет ответ, экран скажет словами.
func miniappAwg3SendDevice(d Deps, r *http.Request, tgUser int64, panelID string, issued awg3panel.Issued) string {
	if d.MiniappDocs == nil {
		return "not_configured"
	}
	filename := miniappAwg3ConfFilename(issued.Name)
	caption := "«" + issued.Name + "» — панель " + panelID + miniappSendConfCaption
	if _, err := d.MiniappDocs.SendDocument(r.Context(), tgUser, nil, filename, []byte(issued.Config), caption); err != nil {
		if tg.IsUnreachableChat(err) {
			return "unreachable"
		}
		miniappCabinetLogger(d).Warn("awg3-панель: .conf в личку не ушёл", "panel", panelID, "err", err)
		return "failed"
	}
	if png, err := base64.StdEncoding.DecodeString(issued.QRPNGBase64); err == nil && len(png) > 0 {
		if _, err := d.MiniappDocs.SendPhoto(r.Context(), tgUser, nil, strings.TrimSuffix(filename, ".conf")+".png", png, "QR «"+issued.Name+"»"+miniappSendConfCaption); err != nil {
			miniappCabinetLogger(d).Warn("awg3-панель: QR в личку не ушёл", "panel", panelID, "err", err)
		}
	}
	miniappCabinetLogger(d).Info("awg3-панель: устройство в личку", "panel", panelID, "tg_user", tgUser)
	return "sent"
}
