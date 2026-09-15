package main

import (
	"testing"
	"time"
)

// Остановка бэкенда ждёт фоновые проверки оживления, но не вечно: зависшая
// проба панели не имеет права держать контейнер после SIGTERM.
func TestWaitBounded(t *testing.T) {
	if !waitBounded(func() {}, time.Second) {
		t.Fatal("мгновенное ожидание -- true")
	}
	block := make(chan struct{})
	defer close(block)
	start := time.Now()
	if waitBounded(func() { <-block }, 50*time.Millisecond) {
		t.Fatal("зависшее ожидание -- false")
	}
	if time.Since(start) > time.Second {
		t.Fatalf("ожидание не ограничено: %v", time.Since(start))
	}
}
