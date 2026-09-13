package backend

import (
	"testing"

	"github.com/Jkaotlic/wg-monitor/pkg/wire"
)

// Действие проходит в очередь только когда ОБА гейта согласны -- ровно это
// условие стоит в miniapp_commands.go:180 и wizard_handler.go:904.
//
// Пробел, который закрывает этот тест: убрать dns_open из validCommandActions
// можно было бесследно -- всё компилировалось и весь набор оставался зелёным,
// хотя в бою команда после этого не доезжает до агента вовсе. Существующий
// цикл в miniapp_commands_test.go сторожит обратное направление («всё из
// белого списка обязано быть допустимым»), а прямое -- нет.
func TestDNSOpenPassesBothGates(t *testing.T) {
	const action = "dns_open"

	if !wire.IsValidCommandAction(action) {
		t.Errorf("%s нет в validCommandActions — бэкенд не поставит команду в очередь", action)
	}
	if !miniappCommandAllowlist[action] {
		t.Errorf("%s нет в белом списке мини-аппа — кнопка проверки сайта не сработает", action)
	}
	if !dashboardCommandAllowlist[action] {
		t.Errorf("%s нет в белом списке дашборда — аварийный вход без Telegram не сможет проверить сайт", action)
	}
}

// dns_open -- проверка только на чтение: она разрешает имя и стучится на 443,
// ничего не меняя. Значит она НЕ должна попадать в список действий, доступных
// одному владельцу: смотреть вправе и оператор роутера.
func TestDNSOpenIsNotOwnerOnly(t *testing.T) {
	if miniappOwnerOnlyActions["dns_open"] {
		t.Error("dns_open помечено как «только владельцу», хотя это проверка на чтение")
	}
}
