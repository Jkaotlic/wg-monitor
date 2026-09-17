package callbacks

import (
	"errors"
	"regexp"
	"strings"
	"sync"

	"github.com/Jkaotlic/wg-monitor/internal/backend"
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

// redactSecret убирает секрет из текста ошибки перед журналом и ответом:
// ошибки кабинета бывают с кусками запроса и ответа.
//
// Простой замены мало: ответ в ошибке обрезается (snippet) и бывает в
// JSON-экранировании. Поэтому прячется ещё:
//   - любой «vpn://…», в том числе «vpn:\/\/…»;
//   - тело ключа без префикса и любой его кусок от 16 знаков;
//   - любая серия из 10 и более цифр (код HideMy.name или его часть).
func redactSecret(text, secret string) string {
	if secret = strings.TrimSpace(secret); secret != "" {
		text = strings.ReplaceAll(text, secret, cabinetHiddenValue)
		body := secret
		if len(body) >= 6 && strings.EqualFold(body[:6], "vpn://") {
			body = body[6:]
		}
		if len(body) > secretFragmentMin {
			text = redactFragments(text, body)
		}
	}
	text = vpnKeyFragmentRe.ReplaceAllString(text, cabinetHiddenValue)
	return longDigitsRe.ReplaceAllString(text, cabinetHiddenValue)
}

// secretFragmentMin -- с какой длины кусок секрета в тексте считается утечкой.
const secretFragmentMin = 16

// redactFragments прячет в text все места, где совпало не меньше
// secretFragmentMin подряд идущих байт секрета.
func redactFragments(text, secret string) string {
	if len(text) < secretFragmentMin {
		return text
	}
	windows := make(map[string]struct{}, len(secret))
	for i := 0; i+secretFragmentMin <= len(secret); i++ {
		windows[secret[i:i+secretFragmentMin]] = struct{}{}
	}
	hidden := make([]bool, len(text))
	found := false
	for i := 0; i+secretFragmentMin <= len(text); i++ {
		if _, ok := windows[text[i:i+secretFragmentMin]]; ok {
			found = true
			for j := i; j < i+secretFragmentMin; j++ {
				hidden[j] = true
			}
		}
	}
	if !found {
		return text
	}
	var b strings.Builder
	for i := 0; i < len(text); {
		if !hidden[i] {
			b.WriteByte(text[i])
			i++
			continue
		}
		b.WriteString(cabinetHiddenValue)
		for i < len(text) && hidden[i] {
			i++
		}
	}
	return b.String()
}

var (
	vpnKeyFragmentRe = regexp.MustCompile(`(?i)vpn:(\\?/){2}[^\s"']*`)
	longDigitsRe     = regexp.MustCompile(`\d{10,}`)
)

// redactCabinetError -- ошибка кабинета без секрета: её текст уходит в ответ
// выпуска и в журналы мастера замены. Отказ по слотам сохраняется как есть --
// по нему обработчики выбирают код ответа.
func redactCabinetError(err error, secret string) error {
	if err == nil || errors.Is(err, backend.ErrVPNSlotBusy) {
		return err
	}
	return errors.New(redactSecret(err.Error(), secret))
}
