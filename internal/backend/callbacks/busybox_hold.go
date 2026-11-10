package callbacks

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/Jkaotlic/wg-monitor/internal/backend/tg"
	"github.com/Jkaotlic/wg-monitor/pkg/wire"
	"golang.org/x/mod/semver"
)

// BusyboxHoldQueue uses the normal agent command channel; no shell input.
type BusyboxHoldQueue interface {
	Enqueue(userID int64, command wire.Command) error
	CommandByID(userID int64, id string) (wire.Command, bool)
	AwaitResult(ctx context.Context, userID int64, id string, timeout time.Duration) (*wire.CommandResult, bool)
}

func (r *Router) SetBusyboxHoldQueue(q BusyboxHoldQueue) { r.busyboxQueue = q }

type busyboxHoldAttempt struct {
	ID        string    `json:"id"`
	ExpiresAt time.Time `json:"expires_at"`
}

func (r *Router) handleBusyboxHold(ctx context.Context, m *tg.Message, arg string) {
	if r.cfg.AdminUserID == 0 || m.From.ID != r.cfg.AdminUserID {
		return
	}
	send := func(text string) {
		// One reply per router, bounded even if a package manager dumps a long error.
		runes := []rune(text)
		if len(runes) > 1800 {
			text = string(runes[:1800]) + "…"
		}
		_, _ = r.tg.SendMessage(ctx, m.Chat.ID, nil, text, "", nil)
	}
	if arg == "" {
		send("/busybox_hold <роутер|all> — запретить обновление BusyBox.\n/busybox_hold status — результаты последней попытки.\nНужен агент v0.66.3+. Остальные пакеты продолжат обновляться.")
		return
	}
	if r.busyboxQueue == nil {
		send("Канал команд недоступен. Защита не установлена.")
		return
	}
	users, err := r.d.Users().GetAll()
	if err != nil {
		send("Не удалось получить список роутеров.")
		return
	}
	matched := false
	for _, u := range users {
		if arg != "all" && arg != "status" && arg != u.Nickname {
			continue
		}
		matched = true
		key := fmt.Sprintf("busybox_hold:%d", u.ID)
		raw, err := r.d.KV().Get(key)
		if err != nil {
			send(u.Nickname + ": не удалось прочитать состояние.")
			continue
		}
		var prev busyboxHoldAttempt
		if raw != "" && json.Unmarshal([]byte(raw), &prev) != nil {
			send(u.Nickname + ": сохранённое состояние повреждено.")
			continue
		}
		if arg == "status" {
			if prev.ID == "" {
				send(u.Nickname + ": hold через бота ещё не запрашивался.")
				continue
			}
			result, ok := r.busyboxQueue.AwaitResult(ctx, u.ID, prev.ID, 0)
			if ok {
				marker := "❌ "
				if result.Status == "ok" {
					marker = "✅ "
				}
				send(marker + u.Nickname + ": " + result.Status + "\n" + result.Output + "\nРезультат последней команды, не новая проверка.")
			} else if _, known := r.busyboxQueue.CommandByID(u.ID, prev.ID); !known || !time.Now().Before(prev.ExpiresAt) {
				send(u.Nickname + ": результат не получен или очередь перезапущена; защита НЕ подтверждена. Повторите команду.")
			} else {
				send(u.Nickname + ": ответ ожидается; защита ещё НЕ подтверждена.")
			}
			continue
		}
		version := ""
		if u.LastDeployedVersion != nil {
			version = *u.LastDeployedVersion
		}
		version = "v" + strings.TrimPrefix(version, "v")
		if !semver.IsValid(version) || semver.Compare(version, "v0.66.3") < 0 {
			send(u.Nickname + ": нужно обновить агент до v0.66.3+. Защита не установлена.")
			continue
		}
		if prev.ID != "" && time.Now().Before(prev.ExpiresAt) {
			_, done := r.busyboxQueue.AwaitResult(ctx, u.ID, prev.ID, 0)
			if _, known := r.busyboxQueue.CommandByID(u.ID, prev.ID); known && !done {
				send(u.Nickname + ": команда уже отправлена; ответ ожидается. /busybox_hold status")
				continue
			}
		}
		attempt := busyboxHoldAttempt{ID: "busybox-hold-" + rand.Text(), ExpiresAt: time.Now().Add(15 * time.Minute)}
		body, _ := json.Marshal(attempt)
		if err := r.d.KV().Set(key, string(body)); err != nil {
			send(u.Nickname + ": не удалось сохранить запрос; команда не отправлена.")
			continue
		}
		if err := r.busyboxQueue.Enqueue(u.ID, wire.Command{ID: attempt.ID, Action: "opkg_busybox_hold", ExpiresAt: attempt.ExpiresAt}); err != nil {
			send(u.Nickname + ": команда не отправлена. Защита не установлена.")
			continue
		}
		send(u.Nickname + ": команда отправлена (ожидание до 15 минут). Защита ещё НЕ подтверждена.\nПроверить: /busybox_hold status")
	}
	if !matched {
		send("Роутер не найден.")
	}
}
