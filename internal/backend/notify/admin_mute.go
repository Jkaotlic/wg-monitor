// internal/backend/notify/admin_mute.go
package notify

import (
	"strconv"
	"strings"

	"github.com/Jkaotlic/wg-monitor/internal/backend/tg"
)

// AdminMuteButtonText -- кнопка под каждым уведомлением админу.
//
// Решение оператора 15.09: админу по умолчанию приходит всё по всем роутерам,
// а выключить можно по роутеру. Кнопка -- выключение в одно касание прямо из
// лички, не открывая приложение; вернуть -- в приложении, экран «Парк».
const AdminMuteButtonText = "🔕 Не писать мне про этот роутер"

const adminMutePrefix = "nmute:"

// AdminMuteCallbackData -- данные кнопки: "nmute:<router_id>". Два поля, а не
// общий формат "action:uid:check": Parse таких не принимает, и обработчик
// разбирает кнопку до него (callbacks.Router.HandleCallback).
func AdminMuteCallbackData(routerUserID int64) string {
	return adminMutePrefix + strconv.FormatInt(routerUserID, 10)
}

// ParseAdminMuteCallback разбирает "nmute:<router_id>". Всё прочее, включая
// ноль и отрицательные номера, -- не эта кнопка.
func ParseAdminMuteCallback(data string) (int64, bool) {
	rest, ok := strings.CutPrefix(data, adminMutePrefix)
	if !ok || rest == "" {
		return 0, false
	}
	id, err := strconv.ParseInt(rest, 10, 64)
	if err != nil || id <= 0 {
		return 0, false
	}
	return id, true
}

// withAdminMuteRow возвращает НОВУЮ клавиатуру: ряды вызывающего плюс ряд
// выключения. Исходную не трогает -- она общая для всех адресатов рассылки и
// для следующих напоминаний.
func withAdminMuteRow(kb *tg.InlineKeyboardMarkup, routerUserID int64) *tg.InlineKeyboardMarkup {
	var rows [][]tg.InlineKeyboardButton
	if kb != nil {
		rows = make([][]tg.InlineKeyboardButton, 0, len(kb.InlineKeyboard)+1)
		rows = append(rows, kb.InlineKeyboard...)
	}
	rows = append(rows, []tg.InlineKeyboardButton{{
		Text:         AdminMuteButtonText,
		CallbackData: AdminMuteCallbackData(routerUserID),
	}})
	return &tg.InlineKeyboardMarkup{InlineKeyboard: rows}
}
