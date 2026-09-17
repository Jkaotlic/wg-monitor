package backend

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/Jkaotlic/wg-monitor/pkg/wire"
)

// Вопрос агенту, ответ на который сервер ждёт сам, но коротко.
//
// Удаление VPN-туннеля и предпросмотр импорта решают на сервере по свежему
// ответу роутера (цикл 4, решения 2 и 3): клиенту в этом не доверяют. Держать
// запрос дольше нельзя -- облачный релей KeenDNS рвёт соединение на 15-й
// секунде (handler.go, maxCmdWait). Поэтому сервер ждёт ответ
// miniappAgentAskWait, а не дождавшись, отвечает «ещё спрашиваю», и клиент
// повторяет тот же запрос. Повтор не ставит вторую команду: вопрос, заданный
// не раньше miniappAgentAskReuse назад и ещё не отвеченный, переиспользуется.
const (
	miniappAgentAskWait  = 8 * time.Second
	miniappAgentAskReuse = 2 * time.Minute
)

type miniappAgentQuestion struct {
	cmdID string
	asked time.Time
}

type miniappAgentQuestions struct {
	mu    sync.Mutex
	wait  time.Duration
	reuse time.Duration
	now   func() time.Time
	open  map[string]miniappAgentQuestion
}

func newMiniappAgentQuestions(wait, reuse time.Duration, now func() time.Time) *miniappAgentQuestions {
	return &miniappAgentQuestions{wait: wait, reuse: reuse, now: now, open: map[string]miniappAgentQuestion{}}
}

// ask -- ответ агента на action или answered=false, если он не пришёл за
// wait. key отделяет вопросы одного роутера друг от друга. Полученный ответ
// закрывает вопрос: следующий ask спросит роутер заново. err -- только
// отказ очереди (или генератора id): вопрос тогда не открыт.
func (q *miniappAgentQuestions) ask(ctx context.Context, sink CommandSink, routerID int64, key, action string, args map[string]any) (*wire.CommandResult, bool, error) {
	full := fmt.Sprintf("%d/%s", routerID, key)
	now := q.now()

	q.mu.Lock()
	for k, open := range q.open {
		if now.Sub(open.asked) > q.reuse {
			delete(q.open, k)
		}
	}
	open, ok := q.open[full]
	if !ok {
		id, err := newCmdID()
		if err != nil {
			q.mu.Unlock()
			return nil, false, fmt.Errorf("id gen: %w", err)
		}
		if args == nil {
			args = map[string]any{}
		}
		if err := sink.Enqueue(routerID, wire.Command{ID: id, Action: action, Args: args, IssuedAt: now.UTC()}); err != nil {
			q.mu.Unlock()
			return nil, false, fmt.Errorf("enqueue %s: %w", action, err)
		}
		open = miniappAgentQuestion{cmdID: id, asked: now}
		q.open[full] = open
	}
	q.mu.Unlock()

	res, answered := sink.AwaitResult(ctx, routerID, open.cmdID, q.wait)
	if !answered || res == nil {
		return nil, false, nil
	}
	q.mu.Lock()
	if cur, ok := q.open[full]; ok && cur.cmdID == open.cmdID {
		delete(q.open, full)
	}
	q.mu.Unlock()
	return res, true, nil
}
