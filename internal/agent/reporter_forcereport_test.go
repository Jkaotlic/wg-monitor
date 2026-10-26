package agent

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Jkaotlic/wg-monitor/internal/agent/awgmgr"
	"github.com/Jkaotlic/wg-monitor/pkg/wire"
)

// v0.62 (задача 13): force_recheck («Проверить ещё раз» в мини-аппе) -- не
// «вернулся после отвала». Отчёт уходит без принудительного Resumed (иначе у
// мобильного роутера бэкенд выбросил бы упавшие проверки, как в AG-05), а
// недоставленный отчёт -- ошибка: экран не должен перечитать старое как свежее.
func TestReporterForceReportFailsWhenBackendDown(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()
	r := NewReporter(ReporterConfig{
		Sender:   NewClient(srv.URL, "t", "test", 2*time.Second),
		Version:  "test",
		Interval: time.Hour,
	})
	if err := r.ForceReport(context.Background()); err == nil {
		t.Fatal("отчёт не дошёл (500), а ForceReport ответил успехом")
	}
}

func TestReporterForceReportDeliveredWithoutResumed(t *testing.T) {
	var got wire.Report
	var posts atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &got)
		posts.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	var kicks atomic.Int32
	awg := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/pingcheck/check-now" {
			kicks.Add(1)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer awg.Close()
	r := NewReporter(ReporterConfig{
		Sender:    NewClient(srv.URL, "t", "test", 2*time.Second),
		Version:   "test",
		Interval:  time.Hour,
		AwgClient: awgmgr.New(awg.URL),
	})
	r.lastReportAt = time.Now()
	if err := r.ForceReport(context.Background()); err != nil {
		t.Fatalf("отчёт доставлен, а ForceReport вернул ошибку: %v", err)
	}
	if posts.Load() != 1 {
		t.Fatalf("POST отчёта %d, want 1", posts.Load())
	}
	if got.Resumed {
		t.Fatal("force_recheck ушёл с Resumed=true: у мобильного упавшие проверки выпадут из тревог")
	}
	if got.Trigger != "" {
		t.Fatalf("trigger = %q, want плановый", got.Trigger)
	}
	// «Проверить ещё раз» по-прежнему просит awg-manager о свежем пингчеке.
	if kicks.Load() != 1 {
		t.Fatalf("пингчек awg-manager пнут %d раз, want 1", kicks.Load())
	}
}

// Ревью задачи 13: «Проверить ещё раз» нажата, пока плановый отчёт в пути.
// Его проверки собраны ДО нажатия -- это не свежий результат, даже если POST
// закончился после: ForceReport дожидается замка и шлёт свой отчёт.
func TestReporterForceReportIgnoresReportStartedBeforePress(t *testing.T) {
	s := &recSender{block: make(chan struct{})}
	r := NewReporter(ReporterConfig{Sender: s, Version: "test", Interval: time.Hour})
	done := make(chan struct{})
	go func() { r.sendOnce(context.Background()); close(done) }() // плановый, висит на POST
	time.Sleep(30 * time.Millisecond)
	pressed := time.Now()
	errc := make(chan error, 1)
	go func() { errc <- r.ForceReport(context.Background()) }()
	time.Sleep(30 * time.Millisecond)
	close(s.block)
	<-done
	select {
	case err := <-errc:
		if err != nil {
			t.Fatalf("ForceReport: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("ForceReport не вернулся")
	}
	reps := s.snapshot()
	if len(reps) != 2 {
		t.Fatalf("POST отчёта %d, want 2 (плановый собран до нажатия)", len(reps))
	}
	if !reps[1].Timestamp.After(pressed) {
		t.Fatalf("второй отчёт собран %v, не после нажатия %v", reps[1].Timestamp, pressed)
	}
}

// Плановый отчёт, НАЧАТЫЙ после нажатия, дошёл, пока ForceReport ждал замка:
// это свежая проверка -- успех без лишнего POST.
func TestReporterForceReportCountsReportStartedAfterPress(t *testing.T) {
	s := &recSender{block: make(chan struct{})}
	r := NewReporter(ReporterConfig{Sender: s, Version: "test", Interval: time.Hour})
	r.forceLockPoll = 300 * time.Millisecond // второй плановый гарантированно успевает к замку первым
	first := make(chan struct{})
	go func() { r.sendOnce(context.Background()); close(first) }() // A: до нажатия, висит
	time.Sleep(30 * time.Millisecond)
	errc := make(chan error, 1)
	go func() { errc <- r.ForceReport(context.Background()) }() // нажатие
	time.Sleep(30 * time.Millisecond)
	second := make(chan struct{})
	go func() { r.sendOnce(context.Background()); close(second) }() // B: после нажатия, ждёт замка
	time.Sleep(30 * time.Millisecond)
	close(s.block)
	<-first
	<-second
	select {
	case err := <-errc:
		if err != nil {
			t.Fatalf("ForceReport: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("ForceReport не вернулся")
	}
	if n := len(s.snapshot()); n != 2 {
		t.Fatalf("POST отчёта %d, want 2 (A и B; свой не нужен)", n)
	}
}

// Срок команды кончился за замком, пока плановый отчёт висит: ошибка, а не
// вечное ожидание.
func TestReporterForceReportGivesUpWithContextWhileWaiting(t *testing.T) {
	s := &recSender{block: make(chan struct{})}
	defer close(s.block)
	r := NewReporter(ReporterConfig{Sender: s, Version: "test", Interval: time.Hour})
	go r.sendOnce(context.Background())
	time.Sleep(30 * time.Millisecond)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	errc := make(chan error, 1)
	go func() { errc <- r.ForceReport(ctx) }()
	select {
	case err := <-errc:
		if err == nil {
			t.Fatal("отчёт не дошёл, а ForceReport ответил успехом")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("ForceReport висит за замком дольше срока команды")
	}
}

// Срок команды кончился в паузе после пинка пингчека (500 мс) -- отчёт не
// отправлен, ForceReport обязан вернуть ошибку, а не успех.
func TestReporterForceReportCancelledDuringSettleIsError(t *testing.T) {
	var posts atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		posts.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	awg := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer awg.Close()
	r := NewReporter(ReporterConfig{
		Sender:    NewClient(srv.URL, "t", "test", 2*time.Second),
		Version:   "test",
		Interval:  time.Hour,
		AwgClient: awgmgr.New(awg.URL),
	})
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	if err := r.ForceReport(ctx); err == nil {
		t.Fatal("срок кончился в паузе после пингчека, а ForceReport ответил успехом")
	}
	if posts.Load() != 0 {
		t.Fatalf("POST отчёта %d, want 0", posts.Load())
	}
}
