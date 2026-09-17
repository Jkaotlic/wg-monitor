package backend

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"github.com/Jkaotlic/wg-monitor/internal/releaseorigin"
)

// Админские операции мини-аппа цикла 2 (веб-управление = мини-апп): то, что
// раньше умел только старый дашборд. Ядра общие с дашбордом (B1), здесь --
// общие части обёрток: тело, отказы словами, версия по умолчанию.

// miniappAdminOpsMaxBody -- предел тела. Самое крупное тело -- установка с
// паролями и адресом панели, это сотни байт; всё крупнее -- не наш клиент.
const miniappAdminOpsMaxBody = 64 << 10

// miniappAdminOpsTexts -- отказ словами для человека. Ключ -- код ответа,
// кроме backend_downgrade_rejected: у раскатки бэкенда тот же wire-код
// downgrade_rejected, но откат там не подтверждается, а запрещён.
// #nosec G101 -- тексты отказов для человека, не учётные данные
var miniappAdminOpsTexts = map[string]string{
	errCodeBadJSON:                  "Не удалось прочитать запрос",
	errCodeInternal:                 "Не получилось на стороне сервера — повторите позже",
	"not_found":                     "Роутер не найден",
	"db_not_configured":             "У сервера не настроена база данных",
	"confirm_mismatch":              "Подтверждение не совпало",
	"provision_not_configured":      "Установка агентов на сервере не настроена",
	"root_password_required":        "Нужен пароль root",
	"no_awgm_url":                   "Нужен адрес панели awg-manager",
	"invalid_awgm_url":              "Адрес панели awg-manager должен начинаться с http:// или https://",
	"invalid_nickname":              "Имя роутера: латиница, цифры и дефис",
	"invalid_kind":                  "Тип роутера: дома (стационарный) или в машине (мобильный)",
	"nickname_taken":                "Роутер с таким именем уже выходил на связь — для него есть «Переустановить агент»",
	"provision_already_running":     "Установка на этот роутер уже идёт",
	"latest_version_failed":         "Не удалось узнать последнюю версию — повторите через минуту",
	"checksums_failed":              "Не удалось скачать контрольные суммы релиза",
	"downgrade_rejected":            "Это откат версии — подтвердите откат",
	"backend_downgrade_rejected":    "Откатить бэкенд из приложения нельзя — выберите версию не старше текущей",
	"no_public_base_url":            "У сервера не задан публичный адрес — агенту некуда отправлять отчёты",
	"backend_update_not_configured": "Раскатка бэкенда на этом сервере не настроена",
	"job_not_found":                 "Задание не найдено или истекло",
	"router_offline":                "Роутер не на связи — переустановить сейчас нельзя, есть «Оживить агент»",
	"invalid_backend_url":           "Адрес бэкенда: https://имя-сервера, локальные адреса не подходят",
	"invalid_arch":                  "Архитектура: arm64 или mipsle",
}

func miniappAdminOpsErrorText(code string) string {
	if text, ok := miniappAdminOpsTexts[code]; ok {
		return text
	}
	return miniappAdminOpsTexts[errCodeInternal]
}

func writeMiniappOpsError(w http.ResponseWriter, status int, code string) {
	writeMiniappDeployError(w, status, code, miniappAdminOpsErrorText(code))
}

// writeMiniappStartError -- отказ ядра (B1) словами. Английский Message ядра
// наружу не идёт: в нём бывает текст сетевой ошибки и имена путей.
func writeMiniappStartError(w http.ResponseWriter, serr *repairStartError) {
	code := serr.Code
	// Движок отвечает already_running, замок до выпуска токена --
	// provision_already_running; для человека это одно и то же.
	if code == "already_running" {
		code = "provision_already_running"
	}
	writeMiniappOpsError(w, serr.Status, code)
}

func decodeMiniappOpsBody(w http.ResponseWriter, r *http.Request, dst any) bool {
	if err := json.NewDecoder(io.LimitReader(r.Body, miniappAdminOpsMaxBody)).Decode(dst); err != nil {
		writeMiniappOpsError(w, http.StatusBadRequest, errCodeBadJSON)
		return false
	}
	return true
}

// miniappAgentVersionOrServer -- версия агента для установки из приложения.
// Пусто -- версия самого бэкенда: зеркало выпусков раздаёт её наверняка, и
// агент не обгонит сервер (то же правило, что у «Обновить агент»). Бэкенд без
// тега (сборка разработчика) -- пусто, ядро возьмёт последний выпуск.
func miniappAgentVersionOrServer(requested string) string {
	if v := strings.TrimSpace(requested); v != "" {
		return v
	}
	if tag, err := releaseorigin.ValidateReleaseTag(serverVersion); err == nil {
		return tag
	}
	return ""
}

// miniappCredentialKinds -- какие виды входа пришли, без значений: для журнала.
func miniappCredentialKinds(root, login, password, apiKey string) string {
	var kinds []string
	if strings.TrimSpace(root) != "" {
		kinds = append(kinds, "root")
	}
	if strings.TrimSpace(login) != "" && password != "" {
		kinds = append(kinds, "panel_login")
	}
	if strings.TrimSpace(apiKey) != "" {
		kinds = append(kinds, "panel_key")
	}
	if len(kinds) == 0 {
		return "none"
	}
	return strings.Join(kinds, "+")
}
