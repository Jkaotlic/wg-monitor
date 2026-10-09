package unstick

import "time"

// Config -- настройки сторожа. Нулевые поля -- умолчания (withDefaults).
type Config struct {
	Enabled          bool
	BrokenAfter      time.Duration // broken
	NeedsAfter       time.Duration // needs_start / needs_stop
	TransitionAfter  time.Duration // starting / stopping
	Poll             time.Duration // как часто читать /api/tunnels/all
	Verify1          time.Duration // ожидание после ступени 1
	Verify2          time.Duration // ожидание после перезапуска службы
	ServiceEvery     time.Duration // ступень 2 не чаще, на роутер
	RetryAfterGiveUp time.Duration // «сдался» без смены статуса держится столько
	GuardWindow      time.Duration // тишина после команды бэкенда
	StatePath        string        // "" -- без файла состояния
}

func (c Config) withDefaults() Config {
	def := func(v *time.Duration, d time.Duration) {
		if *v <= 0 {
			*v = d
		}
	}
	def(&c.BrokenAfter, 2*time.Minute)
	def(&c.NeedsAfter, 2*time.Minute)
	def(&c.TransitionAfter, 5*time.Minute)
	def(&c.Poll, 30*time.Second)
	def(&c.Verify1, time.Minute)
	def(&c.Verify2, 90*time.Second)
	def(&c.ServiceEvery, time.Hour)
	def(&c.RetryAfterGiveUp, 6*time.Hour)
	def(&c.GuardWindow, 10*time.Minute)
	return c
}

func (c Config) threshold(k Kind) time.Duration {
	switch k {
	case KindBroken:
		return c.BrokenAfter
	case KindNeeds:
		return c.NeedsAfter
	default:
		return c.TransitionAfter
	}
}
