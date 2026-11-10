package callbacks

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"github.com/Jkaotlic/wg-monitor/internal/awgmrelay"
	"github.com/Jkaotlic/wg-monitor/internal/backend/provision"
	"github.com/Jkaotlic/wg-monitor/internal/backend/tg"
	"strings"
	"time"
)

type EntwareRecoveryOps interface {
	Start(int64) (string, error)
	Latest(int64) (provision.Job, bool)
}

func (r *Router) SetEntwareRecovery(ops EntwareRecoveryOps) { r.entwareRecovery = ops }

type recoveryConfirm struct {
	RouterID int64     `json:"router_id"`
	Expires  time.Time `json:"expires"`
}

const recoveryExplanation = "Сначала проверим BusyBox и блокировки, затем запретим обновление BusyBox и восстановим только отсутствующие ссылки на команды. Существующие файлы и ссылки сохранятся. Пакеты и VPN перезапускаться не будут. Если проверка не пройдёт, итог может быть частичным."

func (r *Router) handleEntwareRecover(ctx context.Context, m *tg.Message, arg string) {
	if r.cfg.AdminUserID == 0 || m.From.ID != r.cfg.AdminUserID {
		return
	}
	send := func(s string) { _, _ = r.tg.SendMessage(ctx, m.Chat.ID, nil, s, "", nil) }
	if arg == "script" {
		if files, ok := r.tg.(interface {
			SendDocument(context.Context, int64, *int64, string, []byte, string) (int64, error)
		}); ok {
			_, err := files.SendDocument(ctx, m.Chat.ID, nil, "entware-recover.run", awgmrelay.EntwareRecoveryScript, "В awg-manager: Expert → Файлы → загрузить в /tmp → разрешить выполнение (0700) → запустить. Или SSH: /opt/bin/busybox sh /tmp/entware-recover.run. Успех подтверждает RECOVERY_CONFIRMED. При ошибке hold или часть ссылок могли сохраниться.")
			if err != nil {
				send("Не удалось отправить скрипт. Повторите позже.")
			}
			return
		}
		send("Отправка файла недоступна. Скрипт можно скачать в приложении.")
		return
	}
	words := strings.Fields(arg)
	status := len(words) == 2 && words[0] == "status"
	if len(words) != 1 && !status {
		send("/entware_recover <роутер> — восстановить команды Entware.\n/entware_recover status <роутер> — последний результат.\n/entware_recover script — готовый скрипт для ручного запуска.\nНужен сохранённый вход в awg-manager с Expert; рабочий агент не требуется.")
		return
	}
	nick := words[0]
	if status {
		nick = words[1]
	}
	u, err := r.d.Users().GetByNickname(nick)
	if err != nil {
		send("Роутер не найден.")
		return
	}
	if r.entwareRecovery == nil {
		send("Восстановление не настроено.")
		return
	}
	if status {
		job, ok := r.entwareRecovery.Latest(u.ID)
		if !ok {
			send(nick + ": сохранённого результата пока нет или он устарел.")
			return
		}
		state := "Идёт восстановление. Защита пока не подтверждена."
		if job.State == provision.StateSuccess {
			state = "Команды восстановлены и BusyBox защищён."
		}
		if job.State == provision.StateFailed {
			state = "Восстановление не подтверждено; изменения могут быть частичными."
		}
		for _, step := range job.Steps {
			if step.Detail != "" {
				state += "\n" + step.Detail
			}
		}
		if job.Hint != "" {
			state += "\n" + job.Hint
		}
		send(nick + ": " + state + "\nЭто результат последнего запуска, не новая проверка.")
		return
	}
	token := rand.Text()
	body, _ := json.Marshal(recoveryConfirm{RouterID: u.ID, Expires: time.Now().Add(10 * time.Minute)})
	if err := r.d.KV().Set("entware-confirm:"+token, string(body)); err != nil {
		send("Не удалось подготовить подтверждение.")
		return
	}
	kb := &tg.InlineKeyboardMarkup{InlineKeyboard: [][]tg.InlineKeyboardButton{{{Text: "Восстановить и защитить", CallbackData: "ewrec:" + token}}}}
	_, _ = r.tg.SendMessageWithReplyKeyboard(ctx, m.Chat.ID, nil, "Восстановить команды на «"+nick+"»?\n\n"+recoveryExplanation, "", nil, kb)
}

func (r *Router) handleEntwareRecoverCallback(ctx context.Context, q *tg.CallbackQuery) {
	if r.cfg.AdminUserID == 0 || q.From.ID != r.cfg.AdminUserID || q.Message.Chat.ID != q.From.ID {
		return
	}
	_ = r.tg.AnswerCallbackQuery(ctx, q.ID, "")
	token := strings.TrimPrefix(q.Data, "ewrec:")
	if len(token) > 64 {
		return
	}
	key := "entware-confirm:" + token
	r.entwareConfirmMu.Lock()
	raw, err := r.d.KV().Get(key)
	var in recoveryConfirm
	valid := err == nil && json.Unmarshal([]byte(raw), &in) == nil && in.RouterID > 0 && time.Now().Before(in.Expires)
	if valid {
		valid = r.d.KV().Set(key, "") == nil
	}
	r.entwareConfirmMu.Unlock()
	text := "Подтверждение истекло. Повторите /entware_recover <роутер>."
	if valid && r.entwareRecovery != nil {
		_, err := r.entwareRecovery.Start(in.RouterID)
		if err != nil {
			text = err.Error() + "\nРучной скрипт: /entware_recover script"
		} else {
			u, _ := r.d.Users().GetByID(in.RouterID)
			nick := "роутер"
			if u != nil {
				nick = u.Nickname
			}
			text = fmt.Sprintf("%s: восстановление запущено. Защита ещё не подтверждена.\nПроверить: /entware_recover status %s", nick, nick)
		}
	}
	_, _ = r.tg.SendMessage(ctx, q.Message.Chat.ID, nil, text, "", nil)
}
