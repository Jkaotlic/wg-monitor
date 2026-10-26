package backend

import (
	"testing"
	"time"
)

// TestWaitRelayDrain_BlocksWhileDeliveryInFlight: остановка бэкенда ждёт
// фоновые доставки тревог до d.Close() -- иначе они пишут в закрытую базу.
func TestWaitRelayDrain_BlocksWhileDeliveryInFlight(t *testing.T) {
	waitRelayPoolEmpty(t)
	relaySem <- struct{}{} // доставка в полёте
	done := make(chan struct{})
	go func() {
		WaitRelayDrain()
		close(done)
	}()
	select {
	case <-done:
		<-relaySem
		t.Fatal("WaitRelayDrain вернулся, пока доставка в полёте")
	case <-time.After(50 * time.Millisecond):
	}
	<-relaySem // доставка закончилась
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("WaitRelayDrain не вернулся после опустения пула")
	}
}

func TestWaitRelayDrain_EmptyPoolReturnsAtOnce(t *testing.T) {
	waitRelayPoolEmpty(t)
	done := make(chan struct{})
	go func() { WaitRelayDrain(); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("пустой пул: WaitRelayDrain должен вернуться сразу")
	}
}
