package callbacks

import (
	"regexp"
	"strings"
	"sync"
)

// Замки хранилищ кабинетов. Запись идёт «прочитать -- изменить -- записать»
// целым файлом, и два параллельных добавления без замка теряли одно: оба
// читали старый файл, второе затирало первое. Замок -- на путь файла, а не
// на роутер: файл один на все роутеры.
var cabinetStoreLocks sync.Map // путь -> *sync.Mutex

func lockCabinetStore(path string) func() {
	v, _ := cabinetStoreLocks.LoadOrStore(path, &sync.Mutex{})
	mu := v.(*sync.Mutex)
	mu.Lock()
	return mu.Unlock
}

// cabinetHiddenValue -- чем секрет заменяется везде, где его могли бы увидеть.
const cabinetHiddenValue = "[скрыто]"

// cabinetSecretMask -- как секрет показывается человеку: четыре последних
// знака. По ним свой ключ узнаётся среди нескольких, а восстановить ключ
// нельзя. У короткого секрета не показывается ни знака.
func cabinetSecretMask(secret string) string {
	r := []rune(strings.TrimSpace(secret))
	if len(r) <= 8 {
		return "••••"
	}
	return "••••" + string(r[len(r)-4:])
}

// redactSecret убирает секрет из текста ошибки перед журналом: ошибки
// кабинета бывают с кусками запроса и ответа.
//
// Кроме самого секрета прячется любой кусок «vpn://…»: ответ кабинета в
// ошибке обрезается (snippet), и обрезанный ключ целиком уже не совпал бы.
func redactSecret(text, secret string) string {
	if secret = strings.TrimSpace(secret); secret != "" {
		text = strings.ReplaceAll(text, secret, cabinetHiddenValue)
	}
	return vpnKeyFragmentRe.ReplaceAllString(text, cabinetHiddenValue)
}

var vpnKeyFragmentRe = regexp.MustCompile(`(?i)vpn://[^\s"'\\]*`)
