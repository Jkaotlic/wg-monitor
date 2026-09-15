package revive

import "net/http"

// Error -- отказ с кодом для маршрутов мини-аппа. Message -- русский текст
// для экрана; ни секретов, ни внутренних имён в нём нет.
type Error struct {
	Code    string
	Status  int
	Message string
}

func (e *Error) Error() string { return e.Message }

var (
	ErrDisabled       = &Error{"revive_disabled", http.StatusServiceUnavailable, "оживление не настроено на сервере"}
	ErrNoAWGMURL      = &Error{"no_awgm_url", http.StatusBadRequest, "у роутера нет адреса панели — укажите его"}
	ErrURLAlreadySet  = &Error{"awgm_url_already_set", http.StatusConflict, "адрес панели у роутера уже записан, поменять его можно в веб-дашборде"}
	ErrNoCredentials  = &Error{"no_credentials", http.StatusBadRequest, "нужен пароль root роутера"}
	ErrAgentAlive     = &Error{"agent_alive", http.StatusConflict, "агент на связи — оживлять нечего"}
	ErrInvalidURL     = &Error{"invalid_awgm_url", http.StatusBadRequest, "нужен внешний адрес панели с https — например, имя KeenDNS; локальные адреса не подходят"}
	ErrRunning        = &Error{"revive_running", http.StatusConflict, "переустановка уже идёт — дождитесь итога"}
	ErrRouterNotFound = &Error{"router_not_found", http.StatusNotFound, "роутер не найден"}
)
