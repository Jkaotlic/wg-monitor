package main

import (
	"fmt"

	"github.com/Jkaotlic/wg-monitor/internal/backend/db"
)

// Роли приёмки раскладки (v0.52, спека §9): чьими глазами открыт мини-апп.
// admin -- прежнее поведение (tg-user -- владелец и оператор парка и админ);
// остальные: парк принадлежит постороннему, зритель привязан к своим роутерам.
type viewerLink struct{ nick, role string }

var sandboxRoles = map[string][]viewerLink{
	"admin":  nil,
	"owner1": {{"sandbox-home", "owner"}},
	// Три роутера с длинными именами -- полоса «Мои роутеры»; sandbox-broken
	// с тревогой встаёт первым.
	"owner3":   {{"sandbox-broken", "owner"}, {"router4car4new", "owner"}, {"дача-северная", "owner"}},
	"operator": {{"sandbox-home", "operator"}},
	// Допущенный к панели: оператор своего роутера, допуск к main и old (awg3.go).
	"issuer": {{"sandbox-work", "operator"}},
}

func attachViewer(d *db.DB, ids map[string]int64, viewer int64, role string) error {
	links, ok := sandboxRoles[role]
	if !ok {
		return fmt.Errorf("неизвестная роль %q", role)
	}
	for _, l := range links {
		uid, ok := ids[l.nick]
		if !ok {
			return fmt.Errorf("роль %s: в seed нет роутера %s", role, l.nick)
		}
		var err error
		if l.role == "owner" {
			err = d.Users().SetTelegramUserID(uid, viewer)
		} else {
			err = d.RouterOperators().Add(uid, viewer, viewer)
		}
		if err != nil {
			return fmt.Errorf("роль %s, %s: %w", role, l.nick, err)
		}
	}
	return nil
}
