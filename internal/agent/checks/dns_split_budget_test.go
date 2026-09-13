package checks

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

// У каждой проверки есть жёсткий бюджет времени в отчёте агента. Семь зон по
// три пробы -- 21 обращение подряд; при таймауте пробы в 2 с это до сорока
// секунд, то есть гарантированный вылет за бюджет. Поэтому вердикт считается
// не чаще раза в MinInterval, а между пересчётами отдаётся из кеша.
func TestDNSSplit_VerdictIsCachedBetweenRuns(t *testing.T) {
	var calls atomic.Int64
	now := time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC)
	c := &DNSSplit{
		Zones:         []string{"ru", "su", "tatar"},
		DefaultCanary: "canary.example.com",
		YandexHost:    "common.dot.dns.yandex.net",
		Foreign:       []string{"9.9.9.9"},
		MinInterval:   10 * time.Minute,
		Now:           func() time.Time { return now },
		Resolve: func(context.Context, string, string) ([]string, error) {
			calls.Add(1)
			return []string{"198.51.100.7"}, nil
		},
	}

	first := c.Run(context.Background(), Deps{})
	afterFirst := calls.Load()
	if afterFirst == 0 {
		t.Fatal("первый прогон не спросил ничего")
	}

	second := c.Run(context.Background(), Deps{})
	if calls.Load() != afterFirst {
		t.Errorf("второй прогон полез в сеть снова: было %d, стало %d", afterFirst, calls.Load())
	}
	if second.Details["checked_at"] != first.Details["checked_at"] {
		t.Errorf("кеш отдал другой момент проверки: %v против %v",
			second.Details["checked_at"], first.Details["checked_at"])
	}

	// Срок кеша вышел -- считаем заново, иначе вердикт застынет навсегда.
	now = now.Add(11 * time.Minute)
	c.Run(context.Background(), Deps{})
	if calls.Load() == afterFirst {
		t.Error("после истечения MinInterval вердикт не пересчитан")
	}
}

// Кеш не должен превращать «неизвестно» в вечное «неизвестно»: если первый
// прогон не дал ответа, повтор обязан случиться раньше обычного срока.
func TestDNSSplit_UnknownVerdictIsRetriedSooner(t *testing.T) {
	var calls atomic.Int64
	now := time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC)
	c := &DNSSplit{
		Zones:         []string{"ru"},
		DefaultCanary: "canary.example.com",
		YandexHost:    "common.dot.dns.yandex.net",
		Foreign:       []string{"9.9.9.9"},
		MinInterval:   10 * time.Minute,
		Now:           func() time.Time { return now },
		Resolve: func(context.Context, string, string) ([]string, error) {
			calls.Add(1)
			return nil, context.DeadlineExceeded
		},
	}

	c.Run(context.Background(), Deps{})
	afterFirst := calls.Load()

	now = now.Add(2 * time.Minute)
	c.Run(context.Background(), Deps{})
	if calls.Load() == afterFirst {
		t.Error("неудачный прогон закешировался на полный срок — «неизвестно» застыло бы надолго")
	}
}
