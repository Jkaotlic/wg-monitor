// Package awg3panel -- клиент и хранилище awg3-панелей оператора (спека
// docs/superpowers/specs/2026-09-29-awg3-panel-in-bot-design.md). Бэкенд
// ходит к публичному адресу панели по mTLS клиентским сертификатом оператора
// и Basic. Панель банит источник за 5 неудач за 5 минут, поэтому каждый
// запрос -- осмысленный, а класс отказа решает, можно ли следующий.
package awg3panel

import (
	"errors"
	"time"
)

type Kind string

const (
	KindBadPassword Kind = "bad_password"  // 401: следующий запрос -- только после пересохранения
	KindBanned      Kind = "paused"        // 429 или пауза предохранителя
	KindCert        Kind = "cert_rejected" // TLS: сертификат не принят или сервер не прошёл проверку
	KindUnreachable Kind = "unreachable"   // сеть, таймаут, 5xx
	KindBadResponse Kind = "bad_response"  // не JSON, не та форма, редирект, 3xx/4xx прокси
	KindReadonly    Kind = "readonly"      // сборка панели без мутаций
	KindNotFound    Kind = "not_found"     // 404 JSON: интерфейс или пир
	KindInvalid     Kind = "invalid"       // 400 JSON: панель отвергла ввод (имя)
)

// Error -- отказ панели словами. Msg не содержит ни пароля, ни тела ответа
// (кроме текста 4xx самой панели -- он описывает ввод и секретов не несёт,
// awg3-panel web/server.go:342), ни конфига.
type Error struct {
	Kind   Kind
	Status int
	Until  time.Time
	Msg    string
	cause  error
}

func (e *Error) Error() string {
	if e.Msg == "" {
		return "awg3-панель: " + string(e.Kind)
	}
	return "awg3-панель: " + e.Msg
}

func (e *Error) Unwrap() error { return e.cause }

// KindOf -- класс отказа; "" -- ошибка не от панели.
func KindOf(err error) Kind {
	var e *Error
	if errors.As(err, &e) {
		return e.Kind
	}
	return ""
}
