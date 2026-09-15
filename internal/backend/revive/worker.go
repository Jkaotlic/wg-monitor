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
	// Fix round 2, Minor #5: счёта в две подряд мало -- между ПЕРВЫМ и
	// ПОСЛЕДНИМ "reachable" серии должно пройти не меньше ConfirmGap. Без
	// этой проверки посторонний Tick, случайно попавший на тот же роутер
	// почти сразу после первого опроса confirmSoon, засчитывал бы второй
	// ответ и запускал переустановку с разбросом в секунду, а не в
	// заявленные спекой тридцать: since -- отметка ПЕРВОГО "reachable" серии
	// (несёт её через RecordProbe независимо от того, кто именно опрашивал).
	if now.Sub(since) < s.cfg.ConfirmGap {
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
	if s.testAfterMarkRunning != nil {
		s.testAfterMarkRunning(in.RouterID)
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
		// Fix round 2, Minor #1: forgetJob здесь раньше стирал jobID из
		// карты ДО backToWaiting, независимо от её итога. Если запись в базу
		// после этого падала, строка оставалась running, а job уже забыт --
		// следующий Tick снова увидел бы "job lost" и по кругу перевёл бы
		// намерение обратно в waiting, СЪЕДАЯ по одной попытке за обход.
		// forgetJob теперь только внутри backToWaiting/finish, и только
		// когда соответствующая запись в базу действительно прошла.
		s.backToWaiting(routerID, reasonJobLost, now)
		return
	}
	if !out.Finished {
		return
	}
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

// backToWaiting -- running→waiting после неудачной, но не окончательной
// попытки (или потерянного задания). forgetJob -- только если запись
// действительно прошла (fix round 2, Minor #1): иначе job остаётся
// отмеченным, и следующий Tick честно повторит ту же попытку записи, вместо
// того чтобы молча забыть задание и перезапустить движок поверх ещё не
// закрытого running.
func (s *Service) backToWaiting(routerID int64, reason string, now time.Time) {
	ok, err := s.cfg.DB.Revive().BackToWaiting(routerID, reason, now)
	if err != nil {
		s.logger.Warn("оживление: возврат в ожидание не записан", "router_id", routerID, "err", err)
		return
	}
	if !ok {
		return
	}
	s.forgetJob(routerID)
}

// notifySendTimeout -- сколько ждём доставки уведомления о закрытии
// намерения; не время самого закрытия (Finish уже записан).
const notifySendTimeout = 5 * time.Second

// finish -- условный переход в конечный статус со стиранием секрета. Одно
// уведомление: второй закрывающий получает ok=false и молчит. forgetJob --
// только после успешной записи в базу (fix round 2, Minor #1): падение
// Finish не должно отдавать роутер под повторный запуск с тем же (возможно
// уже известным плохим) паролем, пока строка ещё running.
func (s *Service) finish(ctx context.Context, routerID int64, from []string, to, reason, notice string) {
	if s.testFinishErr != nil {
		if err := s.testFinishErr(); err != nil {
			s.logger.Warn("оживление: закрытие не записано", "router_id", routerID, "status", to, "err", err)
			return
		}
	}
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
	// Fix round 2, Minor #2: строка уже терминальная и секрет уже стёрт --
	// отправка не имеет права провалиться только потому, что вызывающий ctx
	// (например, Run на остановке бэкенда) уже отменён: дедупликация Finish
	// не даст повторить уведомление никогда, оно было бы потеряно навсегда.
	sendCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), notifySendTimeout)
	defer cancel()
	if _, err := s.cfg.Notifier.Send(sendCtx, routerID, notice, ""); err != nil {
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
