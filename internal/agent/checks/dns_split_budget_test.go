package checks

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Jkaotlic/wg-monitor/internal/agent/keenetic"
)

// У каждой проверки жёсткий бюджет времени в отчёте агента, а чтение
// running-config через ndmc -- не бесплатное. Агент отчитывается куда чаще,
// чем меняются настройки DNS, поэтому вердикт считается не чаще раза в
// MinInterval, а между пересчётами отдаётся из кеша.
func TestDNSSplit_VerdictIsCachedBetweenRuns(t *testing.T) {
	var calls atomic.Int64
	now := time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC)
	c := &DNSSplit{
		Zones:       []string{"ru", "su", "tatar"},
		YandexHost:  testYandexHost,
		Canary:      "ya.ru",
		MinInterval: 10 * time.Minute,
		Now:         func() time.Time { return now },
		Endpoints: func(context.Context) ([]keenetic.DNSEndpoint, error) {
			calls.Add(1)
			return []keenetic.DNSEndpoint{{Type: "dot", Host: testYandexHost, Port: 853, Zone: "ru"}}, nil
		},
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
	// Одно чтение настроек и одна проба на весь прогон, сколько бы ни было зон.
	if afterFirst != 2 {
		t.Errorf("первый прогон сделал %d обращений, хотим 2 (настройки + проба)", afterFirst)
	}

	second := c.Run(context.Background(), Deps{})
	if calls.Load() != afterFirst {
		t.Errorf("второй прогон полез к роутеру снова: было %d, стало %d", afterFirst, calls.Load())
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
		Zones:       []string{"ru"},
		YandexHost:  testYandexHost,
		Canary:      "ya.ru",
		MinInterval: 10 * time.Minute,
		Now:         func() time.Time { return now },
		Endpoints: func(context.Context) ([]keenetic.DNSEndpoint, error) {
			calls.Add(1)
			return nil, errors.New("ndmc не ответил")
		},
		Resolve: resolvesOK,
	}

	c.Run(context.Background(), Deps{})
	afterFirst := calls.Load()

	now = now.Add(2 * time.Minute)
	c.Run(context.Background(), Deps{})
	if calls.Load() == afterFirst {
		t.Error("неудачный прогон закешировался на полный срок — «неизвестно» застыло бы надолго")
	}
}

// После настоящего сброса DNS кеш обязан отпустить вердикт сразу, а не через
// десять минут: иначе экран сброса проверял бы постусловия по старым настройкам.
func TestDNSSplit_InvalidateForcesRecount(t *testing.T) {
	var calls atomic.Int64
	now := time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC)
	c := &DNSSplit{
		Zones:       []string{"ru"},
		YandexHost:  testYandexHost,
		Canary:      "ya.ru",
		MinInterval: 10 * time.Minute,
		Now:         func() time.Time { return now },
		Endpoints: func(context.Context) ([]keenetic.DNSEndpoint, error) {
			calls.Add(1)
			return []keenetic.DNSEndpoint{{Type: "dot", Host: testYandexHost, Port: 853, Zone: "ru"}}, nil
		},
		Resolve: resolvesOK,
	}
	c.Run(context.Background(), Deps{})
	after := calls.Load()
	c.Invalidate()
	c.Run(context.Background(), Deps{})
	if calls.Load() == after {
		t.Error("после Invalidate вердикт отдан из кеша")
	}
}
