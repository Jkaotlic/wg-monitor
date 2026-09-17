package backend

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Jkaotlic/wg-monitor/pkg/wire"
)

// Роутер не ответил за отведённые секунды -- это не ошибка, а «ещё
// спрашиваю». Повтор того же вопроса не ставит вторую команду: иначе каждый
// повтор клиента добавлял бы роутеру по route_status.
func TestMiniappAgentQuestionsReuseOpenQuestion(t *testing.T) {
	now := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	q := newMiniappAgentQuestions(time.Second, 2*time.Minute, func() time.Time { return now })
	sink := &agentScriptSink{}

	res, answered, err := q.ask(context.Background(), sink, 7, "route_status", "route_status", nil)
	if err != nil || answered || res != nil {
		t.Fatalf("первый вопрос без ответа: res=%v answered=%v err=%v", res, answered, err)
	}
	now = now.Add(90 * time.Second)
	if _, answered, _ := q.ask(context.Background(), sink, 7, "route_status", "route_status", nil); answered {
		t.Fatal("ответа всё ещё нет")
	}
	if got := sink.actions(); len(got) != 1 {
		t.Fatalf("повтор внутри окна поставил вторую команду: %v", got)
	}
	if sink.enqueuedUsers[0] != 7 {
		t.Fatalf("команда ушла не тому роутеру: %v", sink.enqueuedUsers)
	}

	// Ответ пришёл на ПЕРВУЮ команду -- его и получает повтор.
	sink.results = map[string]wire.CommandResult{sink.enqueued[0].ID: {ID: sink.enqueued[0].ID, Status: "ok", Output: "{}"}}
	res, answered, err = q.ask(context.Background(), sink, 7, "route_status", "route_status", nil)
	if err != nil || !answered || res.Output != "{}" {
		t.Fatalf("ответ на первую команду: res=%v answered=%v err=%v", res, answered, err)
	}

	// Полученный ответ закрывает вопрос: следующий спрашивает заново --
	// решение по вчерашнему снимку сервер не принимает.
	if _, _, err := q.ask(context.Background(), sink, 7, "route_status", "route_status", nil); err != nil {
		t.Fatal(err)
	}
	if got := sink.actions(); len(got) != 2 {
		t.Fatalf("после ответа ждали новый вопрос: %v", got)
	}
}

func TestMiniappAgentQuestionsExpireAndSeparateKeys(t *testing.T) {
	now := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	q := newMiniappAgentQuestions(time.Second, 2*time.Minute, func() time.Time { return now })
	sink := &agentScriptSink{}

	_, _, _ = q.ask(context.Background(), sink, 7, "route_status", "route_status", nil)
	// Другой роутер и другой ключ -- другие вопросы.
	_, _, _ = q.ask(context.Background(), sink, 8, "route_status", "route_status", nil)
	_, _, _ = q.ask(context.Background(), sink, 7, "analyze/tok", "tunnel_analyze", map[string]any{"conf": "eA=="})
	if got := sink.actions(); len(got) != 3 {
		t.Fatalf("ждали три разных вопроса: %v", got)
	}
	if sink.enqueued[2].Args["conf"] != "eA==" {
		t.Fatalf("аргументы не дошли: %+v", sink.enqueued[2].Args)
	}
	// Вопрос старше окна не переиспользуется: роутер, молчавший две минуты,
	// спрашивается заново.
	now = now.Add(2*time.Minute + time.Second)
	_, _, _ = q.ask(context.Background(), sink, 7, "route_status", "route_status", nil)
	if got := sink.actions(); len(got) != 4 {
		t.Fatalf("старый вопрос переиспользован: %v", got)
	}
}

// failingEnqueueSink -- уже есть в wizard_handler_test.go:1378.
func TestMiniappAgentQuestionsEnqueueError(t *testing.T) {
	q := newMiniappAgentQuestions(time.Second, time.Minute, time.Now)
	if _, answered, err := q.ask(context.Background(), failingEnqueueSink{err: errors.New("queue full")}, 7, "k", "route_status", nil); err == nil || answered {
		t.Fatalf("ошибка очереди обязана дойти: answered=%v err=%v", answered, err)
	}
	// Неудачная постановка не оставляет открытого вопроса.
	sink := &agentScriptSink{}
	_, _, _ = q.ask(context.Background(), sink, 7, "k", "route_status", nil)
	if got := sink.actions(); len(got) != 1 {
		t.Fatalf("после ошибки очереди вопрос не задан заново: %v", got)
	}
}
