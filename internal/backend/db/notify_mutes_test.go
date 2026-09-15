package db

import "testing"

func TestNotifyMutes_DefaultIsReceiving(t *testing.T) {
	d, routerA, _ := newTestDBForOps(t)

	// Строки нет -- человек получает уведомления. Это дефолт, на котором
	// держится «все получают, пока сами не выключили».
	muted, err := d.NotifyMutes().IsMuted(1001, routerA)
	if err != nil {
		t.Fatal(err)
	}
	if muted {
		t.Fatal("без строки человек обязан получать уведомления")
	}
}

func TestNotifyMutes_SetAndUnset(t *testing.T) {
	d, routerA, routerB := newTestDBForOps(t)

	if err := d.NotifyMutes().SetMuted(1001, routerA, true); err != nil {
		t.Fatal(err)
	}
	muted, err := d.NotifyMutes().IsMuted(1001, routerA)
	if err != nil {
		t.Fatal(err)
	}
	if !muted {
		t.Fatal("заглушено -- значит заглушено")
	}

	// Заглушение одного роутера не трогает другой.
	muted, err = d.NotifyMutes().IsMuted(1001, routerB)
	if err != nil {
		t.Fatal(err)
	}
	if muted {
		t.Fatal("заглушение точечное: соседний роутер не должен молчать")
	}

	// И не трогает другого человека на том же роутере.
	muted, err = d.NotifyMutes().IsMuted(1002, routerA)
	if err != nil {
		t.Fatal(err)
	}
	if muted {
		t.Fatal("заглушение личное: другой человек продолжает получать")
	}

	if err := d.NotifyMutes().SetMuted(1001, routerA, false); err != nil {
		t.Fatal(err)
	}
	muted, err = d.NotifyMutes().IsMuted(1001, routerA)
	if err != nil {
		t.Fatal(err)
	}
	if muted {
		t.Fatal("включил обратно -- должен получать")
	}
}

func TestNotifyMutes_MutedBy(t *testing.T) {
	d, routerA, _ := newTestDBForOps(t)
	if err := d.NotifyMutes().SetMuted(1001, routerA, true); err != nil {
		t.Fatal(err)
	}
	if err := d.NotifyMutes().SetMuted(1003, routerA, true); err != nil {
		t.Fatal(err)
	}

	got, err := d.NotifyMutes().MutedBy(routerA)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || !got[1001] || !got[1003] {
		t.Fatalf("MutedBy=%v, ждали 1001 и 1003", got)
	}
}

// Экрану парка нужен ответ «что выключил этот человек» одним запросом, а не
// MutedBy на каждый роутер.
func TestNotifyMutes_MutedRoutersOf(t *testing.T) {
	d, routerA, routerB := newTestDBForOps(t)
	if err := d.NotifyMutes().SetMuted(9000, routerA, true); err != nil {
		t.Fatal(err)
	}
	// Чужое выключение того же роутера -- не выбор 9000.
	if err := d.NotifyMutes().SetMuted(1001, routerB, true); err != nil {
		t.Fatal(err)
	}

	got, err := d.NotifyMutes().MutedRoutersOf(9000)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || !got[routerA] || got[routerB] {
		t.Fatalf("MutedRoutersOf(9000)=%v, ждали только %d", got, routerA)
	}
	empty, err := d.NotifyMutes().MutedRoutersOf(4242)
	if err != nil {
		t.Fatal(err)
	}
	if empty == nil || len(empty) != 0 {
		t.Fatalf("у ничего не выключавшего ждали пустую карту, got %v", empty)
	}
}
