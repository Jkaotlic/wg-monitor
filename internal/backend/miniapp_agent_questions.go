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

// commandResultTimer -- когда очередь записала результат. Узкий интерфейс по
// типу, а не метод CommandSink (как activeCommandChecker, deploy_wake.go):
// расширение CommandSink сломало бы десятки тестовых очередей.
type commandResultTimer interface {
	ResultRecordedAt(userID int64, id string) (time.Time, bool)
}

// ask -- ответ агента на action или answered=false, если он не пришёл за
// wait. key отделяет вопросы одного роутера друг от друга. Полученный ответ
// закрывает вопрос: следующий ask спросит роутер заново. err -- только
// отказ очереди (или генератора id): вопрос тогда не открыт.
func (q *miniappAgentQuestions) ask(ctx context.Context, sink CommandSink, routerID int64, key, action string, args map[string]any) (*wire.CommandResult, bool, error) {
	return q.askFresh(ctx, sink, routerID, key, action, args, 0)
}

// askFresh -- ask, которому годится только ответ, записанный очередью не
// раньше maxAge назад (0 -- любой). Пролежавший ответ закрывает вопрос, роутеру
// уходит новый, и возвращается answered=false: решать по снимку минутной
// давности нельзя (ревью цикла 4). Очередь, не умеющая сказать время записи
// (тестовые фейки), считается свежей; очередь, умеющая, но не нашедшая записи
// (результат выметен), -- нет.
func (q *miniappAgentQuestions) askFresh(ctx context.Context, sink CommandSink, routerID int64, key, action string, args map[string]any, maxAge time.Duration) (*wire.CommandResult, bool, error) {
	full := fmt.Sprintf("%d/%s", routerID, key)
	if args == nil {
		args = map[string]any{}
	}
	for attempt := 0; ; attempt++ {
		open, err := q.openQuestion(sink, routerID, full, action, args)
		if err != nil {
			return nil, false, err
		}
		res, answered := sink.AwaitResult(ctx, routerID, open.cmdID, q.wait)
		if !answered || res == nil {
			return nil, false, nil
		}
		q.mu.Lock()
		if cur, ok := q.open[full]; ok && cur.cmdID == open.cmdID {
			delete(q.open, full)
		}
		q.mu.Unlock()
		if maxAge <= 0 || q.answerFresh(sink, routerID, open.cmdID, maxAge) {
			return res, true, nil
		}
		if attempt > 0 {
			// Только что заданный вопрос отвечен устаревшим ответом -- так не
			// бывает у настоящей очереди; не крутиться.
			return nil, false, nil
		}
	}
}

func (q *miniappAgentQuestions) answerFresh(sink CommandSink, routerID int64, cmdID string, maxAge time.Duration) bool {
	timer, ok := sink.(commandResultTimer)
	if !ok {
		return true
	}
	at, known := timer.ResultRecordedAt(routerID, cmdID)
	return known && q.now().Sub(at) <= maxAge
}

// openQuestion -- открытый вопрос по ключу или новый, поставленный в очередь.
func (q *miniappAgentQuestions) openQuestion(sink CommandSink, routerID int64, full, action string, args map[string]any) (miniappAgentQuestion, error) {
	now := q.now()
	q.mu.Lock()
	defer q.mu.Unlock()
	for k, open := range q.open {
		if now.Sub(open.asked) > q.reuse {
			delete(q.open, k)
		}
	}
	if open, ok := q.open[full]; ok {
		return open, nil
	}
	id, err := newCmdID()
	if err != nil {
		return miniappAgentQuestion{}, fmt.Errorf("id gen: %w", err)
	}
	if err := sink.Enqueue(routerID, wire.Command{ID: id, Action: action, Args: args, IssuedAt: now.UTC()}); err != nil {
		return miniappAgentQuestion{}, fmt.Errorf("enqueue %s: %w", action, err)
	}
	open := miniappAgentQuestion{cmdID: id, asked: now}
	q.open[full] = open
	return open, nil
}
