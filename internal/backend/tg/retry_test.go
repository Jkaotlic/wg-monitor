package tg

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// recordPauses -- подмена ожидания между попытками: тест не спит, а
// запоминает, сколько клиент собирался ждать.
func recordPauses(c *Client) *[]time.Duration {
	var got []time.Duration
	c.pause = func(_ context.Context, d time.Duration) error {
		got = append(got, d)
		return nil
	}
	return &got
}

// BOT-01: временный 502 от Telegram не должен терять тревогу -- вторая
// попытка доставляет её, и сообщение уходит ровно одно.
func TestSendRetries5xxThenDelivers(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if atomic.AddInt32(&hits, 1) == 1 {
			w.WriteHeader(http.StatusBadGateway)
			w.Write([]byte(`{"ok":false,"error_code":502,"description":"Bad Gateway"}`))
			return
		}
		w.Write([]byte(`{"ok":true,"result":{"message_id":77}}`))
	}))
	defer srv.Close()
	c := &Client{BaseURL: srv.URL + "/bot", Token: "tok", HTTP: srv.Client()}
	pauses := recordPauses(c)

	mid, err := c.SendMessage(context.Background(), 5, nil, "x", "", nil)
	if err != nil || mid != 77 {
		t.Fatalf("SendMessage = %d, %v; want 77, nil", mid, err)
	}
	if n := atomic.LoadInt32(&hits); n != 2 {
		t.Fatalf("запросов %d, want 2 (502, затем доставка)", n)
	}
	if len(*pauses) != 1 || (*pauses)[0] != time.Second {
		t.Fatalf("паузы %v, want [1s]", *pauses)
	}
}

// 5xx подряд -- не больше трёх попыток, паузы 1 с и 3 с, итог -- ошибка.
func TestSendGivesUpAfterThree5xx(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(`{"ok":false,"error_code":500,"description":"Internal Server Error"}`))
	}))
	defer srv.Close()
	c := &Client{BaseURL: srv.URL + "/bot", Token: "tok", HTTP: srv.Client()}
	pauses := recordPauses(c)

	if _, err := c.SendMessageWithKeyboard(context.Background(), 5, nil, "x", "", nil, &InlineKeyboardMarkup{}); err == nil {
		t.Fatal("ждали ошибку после трёх 500")
	}
	if n := atomic.LoadInt32(&hits); n != 3 {
		t.Fatalf("запросов %d, want 3", n)
	}
	if len(*pauses) != 2 || (*pauses)[0] != time.Second || (*pauses)[1] != 3*time.Second {
		t.Fatalf("паузы %v, want [1s 3s]", *pauses)
	}
}

// 429 с retry_after=1 -- подождать ровно столько и повторить.
func TestSendRetries429ByRetryAfter(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if atomic.AddInt32(&hits, 1) == 1 {
			w.WriteHeader(http.StatusTooManyRequests)
			w.Write([]byte(`{"ok":false,"error_code":429,"description":"Too Many Requests: retry after 1","parameters":{"retry_after":1}}`))
			return
		}
		w.Write([]byte(`{"ok":true,"result":{"message_id":5}}`))
	}))
	defer srv.Close()
	c := &Client{BaseURL: srv.URL + "/bot", Token: "tok", HTTP: srv.Client()}
	pauses := recordPauses(c)

	if _, err := c.SendSilentMessageWithKeyboard(context.Background(), 5, nil, "x", "", nil, &InlineKeyboardMarkup{}); err != nil {
		t.Fatalf("SendSilentMessageWithKeyboard: %v", err)
	}
	if n := atomic.LoadInt32(&hits); n != 2 {
		t.Fatalf("запросов %d, want 2", n)
	}
	if len(*pauses) != 1 || (*pauses)[0] != time.Second {
		t.Fatalf("паузы %v, want [1s]", *pauses)
	}
}

// 429 дольше потолка (30 с) -- не ждать внутри клиента: ошибка с RetryAfter
// уходит вызывающему сразу (realert по ней останавливает обход).
func TestSend429BeyondCapIsNotRetried(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.WriteHeader(http.StatusTooManyRequests)
		w.Write([]byte(`{"ok":false,"error_code":429,"description":"Too Many Requests: retry after 60","parameters":{"retry_after":60}}`))
	}))
	defer srv.Close()
	c := &Client{BaseURL: srv.URL + "/bot", Token: "tok", HTTP: srv.Client()}
	pauses := recordPauses(c)

	_, err := c.SendMessage(context.Background(), 5, nil, "x", "", nil)
	if d, ok := RateLimitDelay(err); !ok || d != time.Minute {
		t.Fatalf("err = %v; want 429 c retry_after 60s", err)
	}
	if n := atomic.LoadInt32(&hits); n != 1 || len(*pauses) != 0 {
		t.Fatalf("запросов %d, пауз %v; want 1 и ни одной", n, *pauses)
	}
}

// Таймаут чтения ответа после отправки: Telegram мог уже доставить
// сообщение, повтор дал бы дубль тревоги. Повтора нет.
func TestSendTimeoutAfterSendIsNotRetried(t *testing.T) {
	var hits int32
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&hits, 1)
		<-release
		w.Write([]byte(`{"ok":true,"result":{"message_id":1}}`))
	}))
	defer srv.Close()
	defer close(release)
	httpc := srv.Client()
	httpc.Timeout = 100 * time.Millisecond
	c := &Client{BaseURL: srv.URL + "/bot", Token: "tok", HTTP: httpc}
	pauses := recordPauses(c)

	if _, err := c.SendMessage(context.Background(), 5, nil, "x", "", nil); err == nil {
		t.Fatal("ждали ошибку таймаута")
	}
	if n := atomic.LoadInt32(&hits); n != 1 || len(*pauses) != 0 {
		t.Fatalf("запросов %d, пауз %v; want 1 и ни одной", n, *pauses)
	}
}

// failingDial -- транспорт, который не может даже соединиться: запрос
// заведомо не ушёл, повтор безопасен.
type failingDial struct{ calls int32 }

func (f *failingDial) RoundTrip(*http.Request) (*http.Response, error) {
	atomic.AddInt32(&f.calls, 1)
	return nil, &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("connection refused")}
}

func TestSendRetriesDialErrors(t *testing.T) {
	rt := &failingDial{}
	c := &Client{BaseURL: "http://198.51.100.1/bot", Token: "tok", HTTP: &http.Client{Transport: rt}}
	pauses := recordPauses(c)

	if _, err := c.SendMessage(context.Background(), 5, nil, "x", "", nil); err == nil {
		t.Fatal("ждали ошибку соединения")
	}
	if n := atomic.LoadInt32(&rt.calls); n != 3 || len(*pauses) != 2 {
		t.Fatalf("попыток %d, пауз %v; want 3 и 2", n, *pauses)
	}
}
