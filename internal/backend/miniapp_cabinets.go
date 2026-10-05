package backend

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/Jkaotlic/wg-monitor/internal/backend/db"
	"github.com/Jkaotlic/wg-monitor/internal/backend/sealedfile"
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
	errCodeUnsupportedCT:        "Запрос должен быть в формате JSON",
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
	"cabinet_key_missing":       "Ключи кабинетов на сервере не прочитать: ключ шифрования не найден — напишите администратору",
	"cabinet_key_wrong":         "Ключи кабинетов на сервере не расшифровываются: ключ шифрования не тот — напишите администратору",
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
	"client_not_found":          "Подключения уже нет на сервере — обновите список",
	"selfhosted_revoke_failed":  "Свой сервер не отозвал подключение — нажмите «Проверить подключение» и повторите",
	"selfhosted_clients_failed": "Не удалось получить список подключений — нажмите «Проверить подключение»",
	"invalid_field":             "Поле заполнено неверно",
	"missing_iface":             "Не выбран интерфейс панели",
	"awg3_not_configured":       "Панели awg3 на этом сервере не настроены",
	"bad_issuer_id":             "Нужен положительный числовой Telegram ID",
	"awg3_not_found":            "Панель не найдена — обновите экран",
	"awg3_exists":               "Панель с таким коротким именем уже есть",
	"awg3_disabled":             "Панель выключена — включите её в настройках панели",
	"awg3_too_large":            "Файл .p12 слишком большой — это не сертификат",
	"awg3_bad_password":         "Неверный пароль панели — пересохраните учётные данные. До этого бот к панели не обращается",
	"awg3_paused":               "Панель ограничила вход после неудачных попыток — повтор позже",
	"awg3_cert_rejected":        "Сертификат не принят — загрузите файл .p12 заново",
	"awg3_server_cert_rejected": "Сертификат панели не прошёл проверку — проверьте адрес панели и сертификат на сервере",
	"awg3_unreachable":          "Панель недоступна — нет связи или ответа за 10 секунд",
	"awg3_bad_response":         "Панель ответила не так, как ожидалось — проверьте адрес",
	"awg3_readonly":             "Панель только для просмотра — выпускать с неё нельзя",
	"awg3_iface_not_found":      "Такого интерфейса на панели нет — обновите экран",
	"awg3_name_taken":           "Устройство с таким именем уже есть на этом интерфейсе — выберите другое имя",
	"awg3_invalid_name":         "Панель не приняла имя — до 40 знаков, без «[» и «]»",
	// awg3_dm_sent_no_qr -- не код ошибки, а расшифровка значения "dm":
	// "sent_no_qr" в ответе POST …/device для фронтенда (Task 8+): файл
	// .conf ушёл в личку, а QR — нет (QR всё равно есть на экране).
	"awg3_dm_sent_no_qr": "Файл в личке, QR не отправился — он на экране",
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

// writeMiniappCabinetKeyError -- сбой хранилища кабинетов из-за ключа
// шифрования (v0.55, B1): файл зашифрован, а ключа нет или он не тот.
// Человеку -- слова вместо «на стороне сервера», файл при этом не тронут.
// false -- причина другая, ответ не написан.
func writeMiniappCabinetKeyError(d Deps, w http.ResponseWriter, err error) bool {
	code := ""
	switch {
	case errors.Is(err, sealedfile.ErrKeyMissing):
		code = "cabinet_key_missing"
	case errors.Is(err, sealedfile.ErrUnreadable):
		code = "cabinet_key_wrong"
	default:
		return false
	}
	miniappCabinetLogger(d).Error("кабинеты: хранилище не читается без верного ключа шифрования", "code", code)
	writeMiniappCabinetError(w, http.StatusServiceUnavailable, code)
	return true
}

func writeMiniappCabinetJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// decodeMiniappCabinetBody -- тело только JSON (как у /vpn/issue: пустой
// Content-Type принимается ради старых клиентов) и не больше предела.
func decodeMiniappCabinetBody(w http.ResponseWriter, r *http.Request, dst any) bool {
	if ct := strings.TrimSpace(strings.SplitN(r.Header.Get("Content-Type"), ";", 2)[0]); ct != "" && !strings.EqualFold(ct, "application/json") {
		writeMiniappCabinetError(w, http.StatusUnsupportedMediaType, errCodeUnsupportedCT)
		return false
	}
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
		if writeMiniappCabinetKeyError(d, w, err) {
			return
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
			if writeMiniappCabinetKeyError(d, w, err) {
				return
			}
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
		if writeMiniappCabinetKeyError(d, w, err) {
			return
		}
		miniappCabinetLogger(d).Error("кабинет: ключ не изменён", "router_id", routerID, "provider", provider, "err", err)
		writeMiniappCabinetError(w, http.StatusInternalServerError, errCodeInternal)
	}
}

// miniappCountryRe -- код страны кабинета, как его пишет кнопка бота
// (callbackCodeRe), в нижнем регистре.
var miniappCountryRe = regexp.MustCompile(`^[a-z0-9_-]{2,16}$`)

// miniappCabinetRevokeHandler освобождает слот подписки Amnezia: админ,
// владелец и оператор (v0.52, спека §7: оператору всё, кроме админского;
// отзыв обратим перевыпуском), подтверждение набором имени роутера.
func miniappCabinetRevokeHandler(d Deps) http.HandlerFunc {
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
			Country string `json:"country"`
			Confirm string `json:"confirm"`
		}
		if !decodeMiniappCabinetBody(w, r, &body) {
			return
		}
		if !confirmPhraseMatches(body.Confirm, u.Nickname) {
			writeMiniappCabinetError(w, http.StatusBadRequest, "confirm_mismatch")
			return
		}
		country := strings.ToLower(strings.TrimSpace(body.Country))
		if !miniappCountryRe.MatchString(country) {
			writeMiniappCabinetError(w, http.StatusBadRequest, "invalid_country")
			return
		}
		err := d.VPNCabinetKeys.RevokeSlot(r.Context(), u.ID, country)
		switch {
		case err == nil:
			miniappCabinetLogger(d).Info("кабинет: слот отозван", "router_id", u.ID, "country", country)
			writeMiniappCabinetJSON(w, http.StatusOK, struct {
				Country string `json:"country"`
			}{Country: country})
		case errors.Is(err, ErrCabinetSecretNotFound):
			writeMiniappCabinetError(w, http.StatusConflict, "cabinet_not_connected")
		default:
			miniappCabinetLogger(d).Warn("кабинет: отзыв слота не прошёл", "router_id", u.ID, "country", country, "err", err)
			writeMiniappCabinetError(w, http.StatusBadGateway, "cabinet_failed")
		}
	}
}
