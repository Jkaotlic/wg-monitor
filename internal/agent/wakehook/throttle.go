package wakehook

import "time"

const (
	defaultMinGap  = 30 * time.Second
	defaultPerHour = 20
)

// Throttle -- не чаще раза в MinGap и не больше PerHour за скользящий час.
// Без своего замка: его держит Watcher.
type Throttle struct {
	MinGap  time.Duration
	PerHour int

	fired      []time.Time
	suppressed []time.Time
}

func trimHour(ts []time.Time, now time.Time) []time.Time {
	cut := now.Add(-time.Hour)
	i := 0
	for i < len(ts) && !ts[i].After(cut) {
		i++
	}
	return ts[i:]
}

func (t *Throttle) Allow(now time.Time) bool {
	t.fired, t.suppressed = trimHour(t.fired, now), trimHour(t.suppressed, now)
	gap, per := t.MinGap, t.PerHour
	if gap <= 0 {
		gap = defaultMinGap
	}
	if per <= 0 {
		per = defaultPerHour
	}
	if n := len(t.fired); (n > 0 && now.Sub(t.fired[n-1]) < gap) || n >= per {
		t.suppressed = append(t.suppressed, now)
		return false
	}
	t.fired = append(t.fired, now)
	return true
}

// Stats -- сколько пробуждений пропущено и погашено за последний час.
func (t *Throttle) Stats(now time.Time) (int, int) {
	t.fired, t.suppressed = trimHour(t.fired, now), trimHour(t.suppressed, now)
	return len(t.fired), len(t.suppressed)
}
