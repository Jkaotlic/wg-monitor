package revive

import (
	"context"
	"io"
	"log/slog"
	"time"

	"github.com/Jkaotlic/wg-monitor/internal/backend/db"
)

// DefaultJanitorEvery -- как часто беспарольный сторож проходит по базе.
// Реже, чем обычный обход Service (ProbeEvery, минуты): здесь нет сети,
// только удаление просроченных строк, часа вполне достаточно.
const DefaultJanitorEvery = time.Hour

// ExpireOverdueSecrets -- беспарольный сторож (Fix round 1, Important #2,
// мандатное ревью). Решение оператора «затем стирается» обязано работать,
// даже когда revive.key_file потерян или негоден и revive.Service вообще не
// собран (Deps.Revive == nil): без этого сторожа просроченные пароли лежали
// бы в revive_secrets вечно, пока кто-нибудь не вернёт ключ. Крипто-свободная
// операция -- не расшифровывает секреты, только переводит просроченные
// намерения в expired и стирает шифртекст (db.ReviveRepo.ExpireOverdue).
func ExpireOverdueSecrets(d *db.DB, now time.Time) (int64, error) {
	if d == nil {
		return 0, nil
	}
	return d.Revive().ExpireOverdue(now, reasonExpired)
}

// RunJanitor -- сторож на старте и затем раз в every, пока ctx жив. Работает
// независимо от Service (ключ не нужен) -- main.go запускает его, КОГДА
// оживление выключено (ключа нет или он негоден). Когда оживление включено,
// просрочку waiting-намерений уже обрабатывает собственный Tick сервиса
// (Fix round 2, мандатное ревью: исправлен неверный комментарий «безусловно»
// -- на самом деле main.go запускает ЛИБО Service.Run, ЛИБО этот сторож,
// никогда оба сразу).
func RunJanitor(ctx context.Context, d *db.DB, now func() time.Time, every time.Duration, logger *slog.Logger) {
	if d == nil {
		return
	}
	if now == nil {
		now = time.Now
	}
	if every <= 0 {
		every = DefaultJanitorEvery
	}
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	sweep := func() {
		n, err := ExpireOverdueSecrets(d, now().UTC())
		if err != nil {
			logger.Warn("оживление: сторож просроченных секретов не прошёл", "err", err)
			return
		}
		if n > 0 {
			logger.Info("оживление: просроченные секреты стёрты", "count", n)
		}
	}

	sweep()
	if ctx.Err() != nil {
		return
	}
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			sweep()
		}
	}
}
