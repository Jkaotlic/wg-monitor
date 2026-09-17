package backend

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/Jkaotlic/wg-monitor/internal/backend/db"
)

// Кабинеты VPN роутера в мини-аппе (цикл 3): ключи Amnezia Premium и коды
// HideMy.name. Секреты вводятся только полем приложения по HTTPS и наружу не
// отдаются: в ответе -- маска, в журнале -- id.

// miniappCabinetMaxBody -- предел тела. Самое крупное тело -- форма своего
// сервера с паролем, это сотни байт.
const miniappCabinetMaxBody = 16 << 10

// miniappHiddenValue -- чем секрет печатается в журнал и строку.
const miniappHiddenValue = "[скрыто]"

// miniappCabinetTexts -- отказ словами для всех маршрутов цикла 3 (кабинеты,
// отзыв, выпуск, файл в личку, свои серверы).
// #nosec G101 -- тексты отказов для человека, не учётные данные
var miniappCabinetTexts = map[string]string{
	errCodeBadJSON:              "Не удалось прочитать запрос",
	errCodeInternal:             "Не получилось на стороне сервера — повторите позже",
	"not_found":                 "Роутер не найден",
	"cabinets_not_configured":   "Кабинеты VPN на сервере не настроены",
	"invalid_key":               "Ключ Amnezia Premium начинается с vpn:// — скопируйте его целиком",
	"invalid_code":              "Код HideMy.name — от 10 до 20 цифр",
	"invalid_label":             "Подпись — до 40 знаков, без переводов строки",
	"cabinet_rejected":          "Кабинет не принял ключ — проверьте его и повторите",
	"secret_not_found":          "Ключ или код не найден — обновите экран",
	"confirm_mismatch":          "Имя набрано неверно",
	"invalid_country":           "Страна не распознана — обновите экран",
	"cabinet_not_connected":     "Ключ кабинета не сохранён — сначала добавьте ключ",
	"cabinet_failed":            "Кабинет не ответил — повторите позже",
	"slot_busy":                 "Все слоты подписки заняты — отзовите страну, которая больше не нужна, и повторите",
	"unknown_provider":          "Такого кабинета приложение не знает",
	"missing_option":            "Не выбрана страна или сервер",
	"missing_instance":          "Не выбран свой сервер",
	"dm_not_configured":         "Отправка файлов в Telegram на сервере не настроена",
	"dm_unreachable":            "Бот не может написать вам — откройте бота и нажмите /start",
	"dm_failed":                 "Telegram не принял файл — повторите позже",
	"selfhosted_not_configured": "Свои серверы на этом сервере не настроены",
	"instance_not_found":        "Свой сервер не найден — обновите экран",
	"instance_exists":           "Сервер с таким именем уже есть",
	"instance_disabled":         "Свой сервер выключен — включите его и повторите",
	"instance_not_ready":        "У своего сервера не заполнены адрес и порт для клиентов",
	"selfhosted_failed":         "Свой сервер не выпустил конфиг — нажмите «Проверить подключение»",
	"invalid_field":             "Поле заполнено неверно",
}

func miniappCabinetErrorText(code string) string {
	if text, ok := miniappCabinetTexts[code]; ok {
		return text
	}
	return miniappCabinetTexts[errCodeInternal]
}

func writeMiniappCabinetError(w http.ResponseWriter, status int, code string) {
	writeMiniappDeployError(w, status, code, miniappCabinetErrorText(code))
}

func writeMiniappCabinetJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func decodeMiniappCabinetBody(w http.ResponseWriter, r *http.Request, dst any) bool {
	if err := json.NewDecoder(io.LimitReader(r.Body, miniappCabinetMaxBody)).Decode(dst); err != nil {
		writeMiniappCabinetError(w, http.StatusBadRequest, errCodeBadJSON)
		return false
	}
	return true
}

func miniappCabinetLogger(d Deps) *slog.Logger {
	if d.Logger != nil {
		return d.Logger
	}
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

type miniappCabinetRole int

const (
	miniappCabinetAnyAccess miniappCabinetRole = iota // админ, владелец, оператор
	miniappCabinetOwner                               // админ и владелец
)

// miniappCabinetRouter -- гейт маршрутов кабинета: роль и сам роутер, 404 до
// чтения тела. Роутер, которого нет, -- 404 и админу.
func miniappCabinetRouter(d Deps, w http.ResponseWriter, r *http.Request, role miniappCabinetRole) (*db.User, bool) {
	tgUser, _ := miniappUserFromContext(r.Context())
	routerID, ok := parseMiniappRouterID(r)
	if ok && d.DB != nil {
		if role == miniappCabinetOwner {
			ok = miniappIsOwner(d, tgUser, routerID)
		} else {
			ok = miniappRouterAllowed(d, tgUser, routerID)
		}
	} else {
		ok = false
	}
	if !ok {
		writeMiniappCabinetError(w, http.StatusNotFound, "not_found")
		return nil, false
	}
	u, err := d.DB.Users().GetByID(routerID)
	if errors.Is(err, db.ErrUserNotFound) || (err == nil && u == nil) {
		writeMiniappCabinetError(w, http.StatusNotFound, "not_found")
		return nil, false
	}
	if err != nil {
		writeMiniappCabinetError(w, http.StatusInternalServerError, errCodeInternal)
		return nil, false
	}
	return u, true
}

// miniappCabinetLabel -- подпись ключа: до 40 знаков, без управляющих.
func miniappCabinetLabel(raw string) (string, bool) {
	label := strings.TrimSpace(raw)
	if utf8.RuneCountInString(label) > 40 {
		return "", false
	}
	for _, r := range label {
		if unicode.IsControl(r) {
			return "", false
		}
	}
	return label, true
}

type miniappCabinetsResp struct {
	Amnezia    miniappCabinetKeyList  `json:"amnezia"`
	HideMy     miniappCabinetCodeList `json:"hidemy"`
	SelfHosted miniappSelfHostedFlag  `json:"selfhosted"`
}

type miniappCabinetKeyList struct {
	Keys []CabinetSecret `json:"keys"`
}

type miniappCabinetCodeList struct {
	Codes []CabinetSecret `json:"codes"`
}

type miniappSelfHostedFlag struct {
	Available bool `json:"available"`
}

func miniappCabinetsHandler(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		u, ok := miniappCabinetRouter(d, w, r, miniappCabinetAnyAccess)
		if !ok {
			return
		}
		if d.VPNCabinetKeys == nil {
			writeMiniappCabinetError(w, http.StatusServiceUnavailable, "cabinets_not_configured")
			return
		}
		keys, err := d.VPNCabinetKeys.Secrets(u.ID, "amnezia")
		if err == nil {
			var codes []CabinetSecret
			codes, err = d.VPNCabinetKeys.Secrets(u.ID, "hidemyname")
			if err == nil {
				tgUser, _ := miniappUserFromContext(r.Context())
				writeMiniappCabinetJSON(w, http.StatusOK, miniappCabinetsResp{
					Amnezia:    miniappCabinetKeyList{Keys: nonNilCabinetSecrets(keys)},
					HideMy:     miniappCabinetCodeList{Codes: nonNilCabinetSecrets(codes)},
					SelfHosted: miniappSelfHostedFlag{Available: d.SelfHosted != nil && miniappIsAdmin(tgUser, d.TelegramAdminUserID)},
				})
				return
			}
		}
		miniappCabinetLogger(d).Error("кабинеты: не прочитаны ключи", "router_id", u.ID, "err", err)
		writeMiniappCabinetError(w, http.StatusInternalServerError, errCodeInternal)
	}
}

func nonNilCabinetSecrets(in []CabinetSecret) []CabinetSecret {
	if in == nil {
		return []CabinetSecret{}
	}
	return in
}

// miniappCabinetSecretReq -- тело добавления ключа или кода. Секрет живёт
// только здесь и в вызове кабинета; печать структуры любым способом даёт
// «[скрыто]».
type miniappCabinetSecretReq struct {
	VPNKey     string `json:"vpn_key"`
	AccessCode string `json:"access_code"`
	Label      string `json:"label"`
}

func (miniappCabinetSecretReq) String() string       { return miniappHiddenValue }
func (miniappCabinetSecretReq) GoString() string     { return miniappHiddenValue }
func (miniappCabinetSecretReq) LogValue() slog.Value { return slog.StringValue(miniappHiddenValue) }

func miniappCabinetAddHandler(d Deps, provider string) http.HandlerFunc {
	invalidCode := "invalid_key"
	if provider == "hidemyname" {
		invalidCode = "invalid_code"
	}
	return func(w http.ResponseWriter, r *http.Request) {
		u, ok := miniappCabinetRouter(d, w, r, miniappCabinetAnyAccess)
		if !ok {
			return
		}
		if d.VPNCabinetKeys == nil {
			writeMiniappCabinetError(w, http.StatusServiceUnavailable, "cabinets_not_configured")
			return
		}
		var req miniappCabinetSecretReq
		if !decodeMiniappCabinetBody(w, r, &req) {
			return
		}
		label, ok := miniappCabinetLabel(req.Label)
		if !ok {
			writeMiniappCabinetError(w, http.StatusBadRequest, "invalid_label")
			return
		}
		secret := req.VPNKey
		if provider == "hidemyname" {
			secret = req.AccessCode
		}
		added, err := d.VPNCabinetKeys.AddSecret(r.Context(), u.ID, provider, secret, label)
		var rejected *CabinetRejectedError
		switch {
		case err == nil:
		case errors.Is(err, ErrCabinetSecretInvalid):
			writeMiniappCabinetError(w, http.StatusBadRequest, invalidCode)
			return
		case errors.As(err, &rejected):
			reason := rejected.Reason
			if reason == "" {
				reason = miniappCabinetErrorText("cabinet_rejected")
			}
			writeMiniappDeployError(w, http.StatusUnprocessableEntity, "cabinet_rejected", reason)
			return
		default:
			// Ошибка хранилища -- про файл, не про секрет; наружу не идёт.
			miniappCabinetLogger(d).Error("кабинет: ключ не сохранён", "router_id", u.ID, "provider", provider, "err", err)
			writeMiniappCabinetError(w, http.StatusInternalServerError, errCodeInternal)
			return
		}
		miniappCabinetLogger(d).Info("кабинет: ключ добавлен", "router_id", u.ID, "provider", provider, "id", added.ID)
		writeMiniappCabinetJSON(w, http.StatusCreated, added)
	}
}

func miniappCabinetActiveHandler(d Deps, provider string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		u, ok := miniappCabinetRouter(d, w, r, miniappCabinetAnyAccess)
		if !ok {
			return
		}
		if d.VPNCabinetKeys == nil {
			writeMiniappCabinetError(w, http.StatusServiceUnavailable, "cabinets_not_configured")
			return
		}
		var body struct {
			ID string `json:"id"`
		}
		if !decodeMiniappCabinetBody(w, r, &body) {
			return
		}
		miniappCabinetSecretResult(d, w, u.ID, provider, "активный", d.VPNCabinetKeys.SetActiveSecret(u.ID, provider, strings.TrimSpace(body.ID)))
	}
}

func miniappCabinetDeleteHandler(d Deps, provider string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		u, ok := miniappCabinetRouter(d, w, r, miniappCabinetOwner)
		if !ok {
			return
		}
		if d.VPNCabinetKeys == nil {
			writeMiniappCabinetError(w, http.StatusServiceUnavailable, "cabinets_not_configured")
			return
		}
		miniappCabinetSecretResult(d, w, u.ID, provider, "удалён", d.VPNCabinetKeys.DeleteSecret(u.ID, provider, r.PathValue("secret_id")))
	}
}

func miniappCabinetSecretResult(d Deps, w http.ResponseWriter, routerID int64, provider, what string, err error) {
	switch {
	case err == nil:
		miniappCabinetLogger(d).Info("кабинет: ключ "+what, "router_id", routerID, "provider", provider)
		w.WriteHeader(http.StatusNoContent)
	case errors.Is(err, ErrCabinetSecretNotFound):
		writeMiniappCabinetError(w, http.StatusNotFound, "secret_not_found")
	default:
		miniappCabinetLogger(d).Error("кабинет: ключ не изменён", "router_id", routerID, "provider", provider, "err", err)
		writeMiniappCabinetError(w, http.StatusInternalServerError, errCodeInternal)
	}
}
