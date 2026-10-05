package linkrepair

import (
	"encoding/json"
	"fmt"
	"time"
)

// Окно и потолок автопочинок одной проверки. Флапающая линия без этого
// получала бы починку каждые несколько минут навсегда.
const (
	attemptWindow = 6 * time.Hour
	attemptLimit  = 3
)

// KVStore -- ключ-значение бэкенда (tg_state). Интерфейс здесь, а не в db,
// чтобы счётчик тестировался без базы.
type KVStore interface {
	Get(string) (string, error)
	Set(string, string) error
}

// Attempts -- счётчик автопочинок. Ключ на роутер и проверку: разные линии
// одного роутера чинятся независимо, и неудача на одной не запирает другую.
type Attempts struct {
	KV  KVStore
	Now func() time.Time
}

type attemptLog struct {
	At     []time.Time `json:"at"`
	Failed bool        `json:"failed"`
}

func (a Attempts) now() time.Time {
	if a.Now != nil {
		return a.Now()
	}
	return time.Now()
}

func attemptKey(nickname, checkName string) string {
	return "linkrepair:attempts:" + nickname + ":" + checkName
}

func (a Attempts) load(nickname, checkName string) attemptLog {
	var log attemptLog
	raw, err := a.KV.Get(attemptKey(nickname, checkName))
	if err != nil || raw == "" {
		return log
	}
	_ = json.Unmarshal([]byte(raw), &log)
	return log
}

// Allow отвечает, можно ли начинать автопочинку. Причина отказа -- текст
// для человека, а не код: её печатает экран.
func (a Attempts) Allow(nickname, checkName string) (bool, string) {
	ok, why, _ := a.verdict(nickname, checkName)
	return ok, why
}

// verdict -- Allow и отдельно: отказ из-за потолка попыток (а не из-за
// провала). Тогда человеку ещё говорится, что делать.
func (a Attempts) verdict(nickname, checkName string) (bool, string, bool) {
	log := a.load(nickname, checkName)
	if log.Failed {
		return false, "прошлая попытка починить не помогла — нужен человек", false
	}
	cutoff := a.now().Add(-attemptWindow)
	fresh := 0
	for _, t := range log.At {
		if t.After(cutoff) {
			fresh++
		}
	}
	if fresh >= attemptLimit {
		return false, fmt.Sprintf("VPN-туннель уже чинили %d раза за 6 часов — дело не в нём", fresh), true
	}
	return true, "", false
}

// Record запоминает попытку. ok=false запрещает следующую автопочинку до
// вмешательства человека: раз автоматика не справилась, повторять её на том
// же месте бессмысленно.
func (a Attempts) Record(nickname, checkName string, ok bool) error {
	log := a.load(nickname, checkName)
	cutoff := a.now().Add(-attemptWindow)
	kept := log.At[:0]
	for _, t := range log.At {
		if t.After(cutoff) {
			kept = append(kept, t)
		}
	}
	log.At = append(kept, a.now())
	log.Failed = !ok
	raw, err := json.Marshal(log)
	if err != nil {
		return err
	}
	return a.KV.Set(attemptKey(nickname, checkName), string(raw))
}

// Blocked -- то же, что Allow, глазами экрана: стоит ли автопочинка и почему.
func (a Attempts) Blocked(nickname, checkName string) (bool, string) {
	ok, why := a.Allow(nickname, checkName)
	return !ok, why
}

// Clear снимает стоп после провала (D1): удачная ручная починка, возврат
// VPN-туннеля в норму, повторное включение тумблера. Окно попыток остаётся:
// флапающий VPN-туннель, который каждый раз чинится, всё равно упирается в
// потолок за 6 часов.
func (a Attempts) Clear(nickname, checkName string) error {
	log := a.load(nickname, checkName)
	if !log.Failed {
		return nil
	}
	log.Failed = false
	raw, err := json.Marshal(log)
	if err != nil {
		return err
	}
	return a.KV.Set(attemptKey(nickname, checkName), string(raw))
}
