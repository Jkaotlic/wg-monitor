package revive

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/Jkaotlic/wg-monitor/internal/awgmstate"
	"github.com/Jkaotlic/wg-monitor/internal/backend/db"
)

// Tick -- один обход: итоги идущих переустановок, затем ожидающие намерения.
func (s *Service) Tick(ctx context.Context) {
	if !s.Enabled() {
		return
	}
	running, err := s.cfg.DB.Revive().ListByStatus(StatusRunning)
	if err != nil {
		s.logger.Warn("оживление: идущие не прочитаны", "err", err)
	}
	for _, in := range running {
		s.pollOne(ctx, in.RouterID)
	}
	waiting, err := s.cfg.DB.Revive().ListByStatus(StatusWaiting)
	if err != nil {
		s.logger.Warn("оживление: ожидающие не прочитаны", "err", err)
		return
	}
	for _, in := range waiting {
		if ctx.Err() != nil {
			return
		}
		s.checkOne(ctx, in.RouterID)
	}
}

// checkOne -- ожидающее намерение: срок, живой агент, попытки, опрос, запуск.
func (s *Service) checkOne(ctx context.Context, routerID int64) {
	s.work.Lock()
	defer s.work.Unlock()

	in, err := s.cfg.DB.Revive().Get(routerID)
	if err != nil || in == nil || in.Status != StatusWaiting {
		return
	}
	u, err := s.cfg.DB.Users().GetByID(routerID)
	if err != nil {
		return // роутер удалён -- строку намерения уже убрал каскад
	}
	now := s.now()
	waiting := []string{StatusWaiting}

	if !now.Before(in.ExpiresAt) {
		s.finish(ctx, routerID, waiting, StatusExpired, reasonExpired, noticeExpired(u.Nickname, in.ExpiresAt))
		return
	}
	if s.agentFresh(u, now) {
		s.finish(ctx, routerID, waiting, StatusDone, reasonAliveItself, noticeAliveItself(u.Nickname))
		return
	}
	if in.Attempts >= s.cfg.MaxAttempts {
		reason := orText(in.LastError, reasonUnknownFailure)
		s.finish(ctx, routerID, waiting, StatusFailed, reason, noticeGaveUp(u.Nickname, in.Attempts, reason))
		return
	}
	awgmURL := strings.TrimSpace(derefString(u.AWGMURL))
	if awgmURL == "" {
		s.finish(ctx, routerID, waiting, StatusFailed, reasonNoAWGMURL, noticeFailed(u.Nickname, reasonNoAWGMURL))
		return
	}

	state := s.cfg.Probe(ctx, awgmURL)
	if state == ProbeCancelled {
		// Отмена/дедлайн ВЫЗЫВАЮЩЕГО (остановка бэкенда), а не решение
		// панели: ничего не пишем и серию "панель отвечает" не трогаем --
		// иначе выключение процесса гасило бы её так же, как настоящий сон
		// роутера.
		return
	}
	streak, since := 0, time.Time{}
	if state == awgmstate.Reachable {
		streak, since = in.ReachableProbes+1, in.ReachableSince
		if since.IsZero() {
			since = now
		}
	}
	// ProbeInvalidURL (адрес панели не разбирается) идёт тем же путём, что и
	// offline: серия обнуляется, запуск не происходит, но LastProbeText
	// сообщит человеку правильную причину (probeText различает эти состояния).
	if err := s.cfg.DB.Revive().RecordProbe(routerID, now, state, streak, since); err != nil {
		s.logger.Warn("оживление: итог опроса не записан", "router_id", routerID, "err", err)
		return
	}
	if streak < s.cfg.ReachableProbes {
		return
	}
	in.ReachableProbes = streak
	s.launch(ctx, *in, u.Nickname)
}

// launch -- waiting→running, расшифровка секрета в памяти, вызов движка.
// Секреты не попадают ни в журнал, ни в last_error: туда идут только наши
// русские причины и LaunchError.Text.
//
// Секрет читается из базы и расшифровывается ТОЛЬКО после MarkRunning: между
// опросом и этим местом Schedule() мог переставить намерение и заменить
// шифртекст (например, админ ввёл новый пароль заново, пока шли опросы).
// MarkRunning защищён условием WHERE status = 'waiting': если Schedule уже
// переставил намерение (и вернул его в waiting с новым секретом и счётчиком
// попыток 0), эта запись обновит ИМЕННО актуальную строку, и Secret() ниже
// прочитает уже новый шифртекст -- запуск использует текущий пароль, а не
// тот, что был на момент опроса.
func (s *Service) launch(ctx context.Context, in db.ReviveIntent, nick string) {
	if _, busy := s.job(in.RouterID); busy {
		return
	}
	now := s.now()
	ok, err := s.cfg.DB.Revive().MarkRunning(in.RouterID, now)
	if err != nil {
		s.logger.Warn("оживление: запуск не отмечен", "router_id", in.RouterID, "err", err)
		return
	}
	if !ok {
		return
	}
	attempts := in.Attempts + 1
	running := []string{StatusRunning}

	nonce, ct, found, err := s.cfg.DB.Revive().Secret(in.RouterID)
	if err != nil {
		s.logger.Warn("оживление: секрет не прочитан из базы", "router_id", in.RouterID, "err", err)
		s.attemptFailed(ctx, in.RouterID, nick, attempts, reasonLaunchFailed, false)
		return
	}
	if !found {
		s.finish(ctx, in.RouterID, running, StatusFailed, reasonNoSecret, noticeFailed(nick, reasonNoSecret))
		return
	}
	creds, err := s.box.Open(in.RouterID, nonce, ct)
	if err != nil {
		s.logger.Warn("оживление: секрет не расшифрован", "router_id", in.RouterID)
		s.finish(ctx, in.RouterID, running, StatusFailed, reasonSecretUnreadable, noticeFailed(nick, reasonSecretUnreadable))
		return
	}

	jobID, err := s.cfg.Engine.Launch(ctx, in.RouterID, creds, in.TargetVersion)
	creds = Secrets{}
	if err != nil {
		reason, permanent := reasonLaunchFailed, false
		var le *LaunchError
		if errors.As(err, &le) {
			reason, permanent = orText(le.Text, reasonLaunchFailed), le.Permanent
		}
		// err.Error() в журнал не идёт: чужая ошибка движка могла бы нести
		// что угодно. Пишем только нашу причину.
		s.logger.Warn("оживление: переустановка не запустилась", "router_id", in.RouterID, "attempt", attempts, "reason", reason)
		s.attemptFailed(ctx, in.RouterID, nick, attempts, reason, permanent)
		return
	}
	s.setJob(in.RouterID, jobID)
	s.logger.Info("оживление: переустановка запущена", "router_id", in.RouterID, "job_id", jobID, "attempt", attempts)
}

// pollOne -- итог идущей переустановки.
func (s *Service) pollOne(ctx context.Context, routerID int64) {
	s.work.Lock()
	defer s.work.Unlock()

	in, err := s.cfg.DB.Revive().Get(routerID)
	if err != nil || in == nil || in.Status != StatusRunning {
		return
	}
	now := s.now()
	jobID, ok := s.job(routerID)
	if !ok {
		s.backToWaiting(routerID, reasonJobLost, now)
		return
	}
	out, known := s.cfg.Engine.Outcome(jobID)
	if !known {
		s.forgetJob(routerID)
		s.backToWaiting(routerID, reasonJobLost, now)
		return
	}
	if !out.Finished {
		return
	}
	s.forgetJob(routerID)
	nick := s.nickname(routerID)
	running := []string{StatusRunning}
	switch {
	case out.Success:
		s.finish(ctx, routerID, running, StatusDone, reasonRevived, noticeRevived(nick, out.Version))
	case out.AuthFailed:
		s.finish(ctx, routerID, running, StatusFailed, reasonAuthFailed, noticeAuthFailed(nick))
	default:
		s.attemptFailed(ctx, routerID, nick, in.Attempts, orText(out.Text, reasonUnknownFailure), false)
	}
}

// attemptFailed -- неудачная попытка: окончательная -> failed сразу;
// исчерпаны попытки -> failed; иначе обратно в waiting.
func (s *Service) attemptFailed(ctx context.Context, routerID int64, nick string, attempts int, reason string, permanent bool) {
	running := []string{StatusRunning}
	switch {
	case permanent:
		s.finish(ctx, routerID, running, StatusFailed, reason, noticeFailed(nick, reason))
	case attempts >= s.cfg.MaxAttempts:
		s.finish(ctx, routerID, running, StatusFailed, reason, noticeGaveUp(nick, attempts, reason))
	default:
		s.backToWaiting(routerID, reason, s.now())
	}
}

func (s *Service) backToWaiting(routerID int64, reason string, now time.Time) {
	if _, err := s.cfg.DB.Revive().BackToWaiting(routerID, reason, now); err != nil {
		s.logger.Warn("оживление: возврат в ожидание не записан", "router_id", routerID, "err", err)
	}
}

// finish -- условный переход в конечный статус со стиранием секрета. Одно
// уведомление: второй закрывающий получает ok=false и молчит.
func (s *Service) finish(ctx context.Context, routerID int64, from []string, to, reason, notice string) {
	ok, err := s.cfg.DB.Revive().Finish(routerID, from, to, reason, s.now())
	if err != nil {
		s.logger.Warn("оживление: закрытие не записано", "router_id", routerID, "status", to, "err", err)
		return
	}
	if !ok {
		return
	}
	s.forgetJob(routerID)
	s.logger.Info("оживление агента закрыто, секрет стёрт", "router_id", routerID, "status", to)
	if s.cfg.Notifier == nil || notice == "" {
		return
	}
	if _, err := s.cfg.Notifier.Send(ctx, routerID, notice, ""); err != nil {
		s.logger.Warn("оживление: уведомление не доставлено", "router_id", routerID, "err", err)
	}
}

func (s *Service) nickname(routerID int64) string {
	u, err := s.cfg.DB.Users().GetByID(routerID)
	if err != nil {
		return ""
	}
	return u.Nickname
}

func orText(text, fallback string) string {
	if strings.TrimSpace(text) == "" {
		return fallback
	}
	return text
}
