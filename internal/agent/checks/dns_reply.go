package checks

import (
	"errors"
	"fmt"

	"golang.org/x/net/dns/dnsmessage"
)

// DNSReplyError -- сервер ОТВЕТИЛ, но не адресом: кодом ошибки (RCode) или
// пустым ответом (NoAnswer). Текст тот же, что был у fmt.Errorf прежде
// («dot: rcode …», «doh: no A answers»), -- его читают детали проверки dns.
type DNSReplyError struct {
	Prefix   string // «dot: », «doh: » или пусто
	RCode    dnsmessage.RCode
	NoAnswer bool
}

func (e *DNSReplyError) Error() string {
	if e.NoAnswer {
		return e.Prefix + "no A answers"
	}
	return fmt.Sprintf("%srcode %v", e.Prefix, e.RCode)
}

// ServerAnswered -- ошибка пробы всё же означает, что сервер имён работает:
// он сказал «такого имени нет» или «записи нет». Имени для пробы может и не
// существовать -- это не поломка сервера. Отказ (SERVFAIL, REFUSED) и
// молчание -- поломка.
func ServerAnswered(err error) bool {
	var re *DNSReplyError
	if !errors.As(err, &re) {
		return false
	}
	return re.NoAnswer || re.RCode == dnsmessage.RCodeNameError
}
