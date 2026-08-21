// internal/backend/retention/schedule.go
package retention

import (
	"time"
)

// Ключ отметки о последнем проходе обслуживания. Живёт в tg_state рядом с
// остальным состоянием бэкенда -- отдельная таблица ради трёх строк была бы
// лишней.
func lastRunKey(name string) string { return "retention.last_run." + name }

// firstDelay -- через сколько запускать проход после старта процесса.
//
// Раньше здесь стояла константа, и «раз в неделю» из конфига на деле означало
// «раз в неделю И через шесть секунд после каждого рестарта». Четыре выкатки
// за час превращались в четыре полных прохода по базе на полтора гигабайта,
// лежащей на SD-карте: пока они идут, Pi отвечает медленнее, чем внешний
// релей готов ждать, и снаружи это выглядит как упавший бэкенд.
//
// Отметка о прошлом проходе переживает рестарт, поэтому расписание считается
// от неё. Задержка после старта остаётся и для просроченного прохода: сразу
// после запуска бэкенд занят собой -- миграции, первые отчёты агентов, -- и
// добавлять к этому тяжёлый проход незачем.
func firstDelay(lastRun time.Time, every, initialDelay time.Duration, now time.Time) time.Duration {
	if lastRun.IsZero() || every <= 0 {
		return initialDelay
	}
	due := lastRun.Add(every)
	if !due.After(now) {
		return initialDelay
	}
	wait := due.Sub(now)
	if wait < initialDelay {
		return initialDelay
	}
	return wait
}

// loadLastRun читает отметку из базы. Нечитаемая или отсутствующая отметка --
// это «не знаем», и проход пойдёт по обычной задержке: лучше лишний раз
// прибраться, чем не прибраться никогда.
func (p *Policy) loadLastRun(name string) time.Time {
	if p.DB == nil {
		return time.Time{}
	}
	raw, err := p.DB.KV().Get(lastRunKey(name))
	if err != nil || raw == "" {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return time.Time{}
	}
	return t
}

func (p *Policy) saveLastRun(name string, at time.Time) {
	if p.DB == nil {
		return
	}
	if err := p.DB.KV().Set(lastRunKey(name), at.UTC().Format(time.RFC3339)); err != nil && p.Logger != nil {
		p.Logger.Warn("retention: сохранить отметку прохода не удалось", "job", name, "err", err)
	}
}
