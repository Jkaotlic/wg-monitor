package main

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
	"time"
)

// Ожидания останова идут параллельно под общим потолком: три зависших не
// складываются в 3 x потолок (докер по умолчанию ждёт SIGTERM 10 с, потом
// SIGKILL), и по каждому незавершённому -- своё предупреждение в журнале.
func TestWaitShutdown_SharedDeadline(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))
	block := make(chan struct{})
	defer close(block)
	hung := func() { <-block }
	const ceiling = 200 * time.Millisecond
	start := time.Now()
	left := waitShutdown(logger, time.Now().Add(ceiling), []shutdownWait{
		{name: "оживление", wait: hung},
		{name: "доставки тревог", wait: hung},
		{name: "maintnotify", wait: hung},
		{name: "готовое", wait: func() {}},
	})
	if el := time.Since(start); el > ceiling+150*time.Millisecond {
		t.Fatalf("ожидания заняли %v, общий потолок %v", el, ceiling)
	}
	if len(left) != 3 {
		t.Fatalf("незавершённые %v, ждали три зависших", left)
	}
	out := buf.String()
	if n := strings.Count(out, "level=WARN"); n != 3 {
		t.Fatalf("предупреждений %d, ждали 3:\n%s", n, out)
	}
	for _, name := range []string{"оживление", "доставки тревог", "maintnotify"} {
		if !strings.Contains(out, name) {
			t.Fatalf("нет предупреждения про %q:\n%s", name, out)
		}
	}
	if strings.Contains(out, "готовое") {
		t.Fatalf("завершившееся ожидание попало в предупреждения:\n%s", out)
	}
}

// Ничего не зависло -- возврат сразу, без предупреждений.
func TestWaitShutdown_AllDone(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))
	start := time.Now()
	left := waitShutdown(logger, time.Now().Add(5*time.Second), []shutdownWait{
		{name: "a", wait: func() {}}, {name: "b", wait: func() {}},
	})
	if len(left) != 0 || buf.Len() != 0 || time.Since(start) > time.Second {
		t.Fatalf("left=%v log=%q за %v", left, buf.String(), time.Since(start))
	}
}
