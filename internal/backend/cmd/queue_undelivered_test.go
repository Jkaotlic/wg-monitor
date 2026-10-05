package cmd

import (
	"context"
	"testing"
	"time"

	"github.com/Jkaotlic/wg-monitor/pkg/wire"
)

// Пока оборванная self_update ждала возврата, легла новая: возвращать
// старую незачем, место в раздаче освобождается всё равно.
func TestReturnUndeliveredSkipsSupersededSelfUpdate(t *testing.T) {
	q := New()
	q.SetDispatchLimit("self_update", 1, 12*time.Minute)
	if err := q.Enqueue(1, wire.Command{ID: "old", Action: "self_update"}); err != nil {
		t.Fatal(err)
	}
	c, ok := q.Dequeue(context.Background(), 1, time.Millisecond)
	if !ok || c.ID != "old" {
		t.Fatalf("got %+v ok=%v", c, ok)
	}
	if err := q.Enqueue(1, wire.Command{ID: "new", Action: "self_update"}); err != nil {
		t.Fatal(err)
	}
	q.ReturnUndelivered(1, *c)
	c, ok = q.Dequeue(context.Background(), 1, time.Millisecond)
	if !ok || c.ID != "new" {
		t.Fatalf("после возврата выдана %+v ok=%v, хотим новую", c, ok)
	}
	if _, ok := q.Dequeue(context.Background(), 1, time.Millisecond); ok {
		t.Fatal("старая self_update вернулась рядом с новой")
	}
}

// Результат уже пришёл -- команда дошла, возвращать нечего.
func TestReturnUndeliveredIgnoresAnswered(t *testing.T) {
	q := New()
	if err := q.Enqueue(1, wire.Command{ID: "x", Action: "diag_now"}); err != nil {
		t.Fatal(err)
	}
	c, _ := q.Dequeue(context.Background(), 1, time.Millisecond)
	if err := q.RecordResult(1, wire.CommandResult{ID: c.ID, Status: "ok"}); err != nil {
		t.Fatal(err)
	}
	q.ReturnUndelivered(1, *c)
	if _, ok := q.Dequeue(context.Background(), 1, time.Millisecond); ok {
		t.Fatal("отвеченная команда выдана повторно")
	}
}
