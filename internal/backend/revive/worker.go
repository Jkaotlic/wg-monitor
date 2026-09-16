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
//
// Fix round 1, Important #1: держит s.work только на быстрые операции этой
// функции (чтение из БД, опрос панели, запись итога опроса) -- НЕ на весь
// launch. Ранние выходы (истёк/жив сам/попытки исчерпаны/нет адреса) отпускают
// s.work ДО вызова finish -- окно без замка. Fix round 2 (мандатное ревью,
// Regression A): именно в это окно Schedule может успеть переставить
// намерение, и finish() с тем же (from='waiting') условием закрыл бы уже
// ЧУЖУЮ, свежую строку. Защита теперь не временем удержания замка, а
// поколением: finish получает in.Generation, прочитанное здесь же, и пишет
// только если оно всё ещё то же самое -- иначе молча отступает.
func (s *Service) checkOne(ctx context.Context, routerID int64) {
	s.work.Lock()

	in, err := s.cfg.DB.Revive().Get(routerID)
	if err != nil || in == nil || in.Status != StatusWaiting {
		s.work.Unlock()
		return
	}
	u, err := s.cfg.DB.Users().GetByID(routerID)
	if err != nil {
		s.work.Unlock()
		return // роутер удалён -- строку намерения уже убрал каскад
	}
	now := s.now()
	waiting := []string{StatusWaiting}
	generation := in.Generation

	if !now.Before(in.ExpiresAt) {
		s.work.Unlock()
		s.finish(ctx, routerID, waiting, StatusExpired, reasonExpired, noticeExpired(u.Nickname, in.ExpiresAt), generation)
		return
	}
	if s.agentFresh(u, now) {
		s.work.Unlock()
		s.finish(ctx, routerID, waiting, StatusDone, reasonAliveItself, noticeAliveItself(u.Nickname), generation)
		return
	}
	if in.Attempts >= s.cfg.MaxAttempts {
		reason := orText(in.LastError, reasonUnknownFailure)
		s.work.Unlock()
		s.finish(ctx, routerID, waiting, StatusFailed, reason, noticeGaveUp(u.Nickname, in.Attempts, reason), generation)
		return
	}
	awgmURL := strings.TrimSpace(derefString(u.AWGMURL))
	if awgmURL == "" {
		s.work.Unlock()
		s.finish(ctx, routerID, waiting, StatusFailed, reasonNoAWGMURL, noticeFailed(u.Nickname, reasonNoAWGMURL), generation)
		return
	}

	state := s.cfg.Probe(ctx, awgmURL)
	if state == ProbeCancelled {
		// Отмена/дедлайн ВЫЗЫВАЮЩЕГО (остановка бэкенда), а не решение
		// панели: ничего не пишем и серию "панель отвечает" не трогаем --
		// иначе выключение процесса гасило бы её так же, как настоящий сон
		// роутера.
		s.work.Unlock()
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
	if err := s.cfg.DB.Revive().RecordProbe(routerID, now, state, streak, since, generation); err != nil {
		s.logger.Warn("оживление: итог опроса не записан", "router_id", routerID, "err", err)
		s.work.Unlock()
		return
	}
	if streak < s.cfg.ReachableProbes {
		s.work.Unlock()
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
		s.work.Unlock()
		return
	}
	in.ReachableProbes = streak
	// s.work НЕ отпускаем здесь -- launch принимает эстафету, держа лок ещё
	// через MarkRunning (Fix round 2, Important #1: без непрерывности отсюда
	// до MarkRunning конкурентный Put() мог бы влезть между этим Get и
	// записью итога и подменить строку под уже прочитанным `in` -- см.
	// комментарий у launch). launch сам отпускает s.work, но не раньше.
	s.launch(ctx, *in, u.Nickname)
}

// jobLaunching -- служебная метка в s.jobs на время асинхронного вызова
// Engine.Launch (Fix round 1, Important #1). Между MarkRunning и возвратом
// Launch у роутера уже нет замка s.work, и конкурентный pollOne того же Tick
// обязан отличить "ещё запускается" от "задание потеряно" -- иначе он вернул
// бы строку в waiting из-под ещё идущего Launch.
const jobLaunching = "\x00launching"

// jobLaunchingTimeout -- сколько jobLaunching имеет право провисеть в
// s.jobs, прежде чем pollOne сочтёт её потерянной (Fix round 2, Important B,
// мандатное ревью). Это верхняя граница ТОЛЬКО на подготовку запуска
// (расшифровка + Engine.Launch -- в текущем адаптере до 30 с на версию и
// чексуммы GitHub), а не на саму установку: реальный jobID, однажды
// полученный, живёт в карте сколько угодно -- его подтверждает
// Engine.Outcome, а не время. Запас (2 минуты против 30 с) -- на случай
// более медленного движка за тем же интерфейсом. Страховка на тот
// маловероятный случай, если forgetJob почему-то не выполнился на пути
// выхода launch (см. комментарий там) -- без нужды почти никогда не
// сработает, но не даёт роутеру зависнуть в running до перезапуска бэкенда.
const jobLaunchingTimeout = 2 * time.Minute

// launch -- waiting→running, расшифровка секрета в памяти, вызов движка.
// Секреты не попадают ни в журнал, ни в last_error: туда идут только наши
// русские причины и LaunchError.Text.
//
// Вызывается ТОЛЬКО из checkOne, которая уже держит s.work и передаёт
// эстафету сюда без промежуточного Unlock/Lock (Fix round 2, Important #1:
// между checkOne's Get и MarkRunning не должно быть ни одного момента без
// замка, иначе конкурентный Put() мог бы влезть и подменить строку под уже
// прочитанным, устаревшим `in` -- серия, попытки и TargetVersion ушли бы в
// движок по чужим, старым числам). launch отпускает s.work РОВНО ОДИН РАЗ,
// на каждом из своих путей выхода, и ни разу не берёт его заново -- после
// MarkRunning дальнейший разбор итога (Secret/decrypt/Engine.Launch) замку
// не нужен: строка уже "running", и put()/Cancel() отказывают конкурентам по
// статусу в БД, а не по этому замку (Fix round 1, Important #1) -- поэтому
// замок отпускается ДО похода Engine.Launch в сеть (до 30 с на версию и
// чексуммы GitHub), а не после него: держать его на весь этот поход означало
// бы упереться в 15-секундный обрыв HTTP на KeenDNS-реле для ЛЮБОГО другого
// Schedule, ждущего того же s.work.
//
// Каждая запись здесь несёт in.Generation (Fix round 2, мандатное ревью) --
// то же поколение, что checkOne прочитала на своём Get. MarkRunning его не
// меняет, так что оно остаётся действительным для BackToWaiting/Finish в
// конце этой же попытки; если бы намерение как-то успело смениться, эти
// записи молча отступили бы вместо порчи чужой строки.
//
// Секрет читается из базы и расшифровывается ТОЛЬКО после MarkRunning: между
// опросом и этим местом Schedule() мог переставить намерение и заменить
// шифртекст (например, админ ввёл новый пароль заново, пока шли опросы).
// MarkRunning защищён условием WHERE status = 'waiting' AND generation = ?:
// если Schedule уже переставил намерение (и вернул его в waiting с новым
// секретом, счётчиком попыток 0 и новым поколением), это условие не
// сработает, MarkRunning вернёт false, и launch тихо отступит -- вместо
// того чтобы засчитать попытку и запустить движок по чужому, устаревшему
// снимку.
func (s *Service) launch(ctx context.Context, in db.ReviveIntent, nick string) {
	if _, busy := s.job(in.RouterID); busy {
		s.work.Unlock()
		return
	}
	now := s.now()
	ok, err := s.cfg.DB.Revive().MarkRunning(in.RouterID, now, in.Generation)
	if err != nil {
		s.work.Unlock()
		s.logger.Warn("оживление: запуск не отмечен", "router_id", in.RouterID, "err", err)
		return
	}
	if !ok {
		s.work.Unlock()
		return
	}
	if s.testAfterMarkRunning != nil {
		s.testAfterMarkRunning(in.RouterID)
	}
	attempts := in.Attempts + 1
	s.setJob(in.RouterID, jobLaunching)
	s.work.Unlock() // отсюда и дальше -- без замка, см. комментарий выше

	running := []string{StatusRunning}

	// verify-done 15.09 (решение координатора): адрес панели проверяется
	// здесь, перед расшифровкой и запуском, на СВЕЖЕЙ строке роутера --
	// постановка проверяет только адрес, введённый в мини-аппе, а записанный
	// раньше дашбордом может быть http или локальным IP. Туда пароль root не
	// уходит: запуска нет, намерение закрывается, секрет стирается.
	u, err := s.cfg.DB.Users().GetByID(in.RouterID)
	if err != nil {
		s.logger.Warn("оживление: роутер не перечитан перед запуском", "router_id", in.RouterID, "err", err)
		s.attemptFailed(ctx, in.RouterID, nick, attempts, reasonLaunchFailed, false, in.Generation)
		return
	}
	if !s.panelURLSafe(strings.TrimSpace(derefString(u.AWGMURL))) {
		s.logger.Warn("оживление: адрес панели небезопасен, запуск отменён", "router_id", in.RouterID)
		s.finish(ctx, in.RouterID, running, StatusFailed, reasonUnsafeAWGMURL, noticeFailed(nick, reasonUnsafeAWGMURL), in.Generation)
		return
	}

	nonce, ct, found, err := s.cfg.DB.Revive().Secret(in.RouterID)
	if err != nil {
		s.logger.Warn("оживление: секрет не прочитан из базы", "router_id", in.RouterID, "err", err)
		s.attemptFailed(ctx, in.RouterID, nick, attempts, reasonLaunchFailed, false, in.Generation)
		return
	}
	if !found {
		s.finish(ctx, in.RouterID, running, StatusFailed, reasonNoStoredEntry, noticeFailed(nick, reasonNoStoredEntry), in.Generation)
		return
	}
	creds, err := s.box.Open(in.RouterID, nonce, ct)
	if err != nil {
		s.logger.Warn("оживление: секрет не расшифрован", "router_id", in.RouterID)
		s.finish(ctx, in.RouterID, running, StatusFailed, reasonEntryUnreadable, noticeFailed(nick, reasonEntryUnreadable), in.Generation)
		return
	}

	jobID, err := s.cfg.Engine.Launch(ctx, in.RouterID, creds, in.TargetVersion)
	creds = Secrets{}
	if err != nil {
		reason, permanent, noAttempt := reasonLaunchFailed, false, false
		var le *LaunchError
		if errors.As(err, &le) {
			reason, permanent, noAttempt = orText(le.Text, reasonLaunchFailed), le.Permanent, le.NoAttempt
		}
		// err.Error() в журнал не идёт: чужая ошибка движка могла бы нести
		// что угодно. Пишем только нашу причину.
		s.logger.Warn("оживление: переустановка не запустилась", "router_id", in.RouterID, "attempt", attempts, "reason", reason, "no_attempt", noAttempt)
		// Fix round 1, Minor #3: движок занят ЧУЖИМ заданием на этом же
		// роутере (дашборд уже чинит/ставит) -- не вина оживления, тратить
		// на это одну из пяти попыток нечестно. Permanent тут никогда не
		// бывает вместе с NoAttempt (см. reviveLaunchError), но проверка
		// !permanent -- на случай, если это когда-нибудь изменится.
		if noAttempt && !permanent {
			s.backToWaitingNoAttempt(in.RouterID, reason, s.now(), in.Generation)
			return
		}
		s.attemptFailed(ctx, in.RouterID, nick, attempts, reason, permanent, in.Generation)
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
		s.backToWaiting(routerID, reasonJobLost, now, in.Generation)
		return
	}
	if jobID == jobLaunching {
		// Launch этого роутера ещё готовится (Fix round 1, Important #1:
		// s.work уже отпущен между MarkRunning и возвратом Engine.Launch) --
		// обычно это не "потеряно", а "ещё не готово": подождать следующего
		// обхода. Но если jobLaunching висит в карте дольше
		// jobLaunchingTimeout (Fix round 2, Important B) -- запись,
		// переводящая строку из running, скорее всего не прошла (SQLite
		// busy и т.п.), и без этой страховки intent завис бы до
		// перезапуска: забываем метку и возвращаем намерение в waiting.
		since, hasSince := s.jobSince(routerID)
		if hasSince && now.Sub(since) > jobLaunchingTimeout {
			s.logger.Warn("оживление: запуск завис дольше таймаута, считаем потерянным", "router_id", routerID)
			s.forgetJob(routerID)
			s.backToWaiting(routerID, reasonJobLost, now, in.Generation)
		}
		return
	}
	out, known := s.cfg.Engine.Outcome(jobID)
	if !known {
		s.backToWaiting(routerID, reasonJobLost, now, in.Generation)
		return
	}
	if !out.Finished {
		return
	}
	nick := s.nickname(routerID)
	running := []string{StatusRunning}
	switch {
	case out.Success:
		s.finish(ctx, routerID, running, StatusDone, reasonRevived, noticeRevived(nick, out.Version), in.Generation)
	case out.AuthFailed:
		s.finish(ctx, routerID, running, StatusFailed, reasonAuthFailed, noticeAuthFailed(nick), in.Generation)
	default:
		s.attemptFailed(ctx, routerID, nick, in.Attempts, orText(out.Text, reasonUnknownFailure), false, in.Generation)
	}
}

// attemptFailed -- неудачная попытка: окончательная -> failed сразу;
// исчерпаны попытки -> failed; иначе обратно в waiting.
func (s *Service) attemptFailed(ctx context.Context, routerID int64, nick string, attempts int, reason string, permanent bool, generation int64) {
	running := []string{StatusRunning}
	switch {
	case permanent:
		s.finish(ctx, routerID, running, StatusFailed, reason, noticeFailed(nick, reason), generation)
	case attempts >= s.cfg.MaxAttempts:
		s.finish(ctx, routerID, running, StatusFailed, reason, noticeGaveUp(nick, attempts, reason), generation)
	default:
		s.backToWaiting(routerID, reason, s.now(), generation)
	}
}

// backToWaiting -- running→waiting после неудачной, но не окончательной
// попытки (или потерянного задания).
//
// Fix round 2, Important B (мандатное ревью): forgetJob -- БЕЗУСЛОВНО, ДО
// записи в БД. Раньше (fix round 2, Minor #1) он был условным на успех
// записи -- защита от повторного "job lost" на каждом обходе при стабильно
// падающей записи. У этой защиты была своя цена: если запись падала (SQLite
// busy, диск), jobLaunching/jobID оставался в карте НАВСЕГДА -- pollOne
// видел бы его и либо молчал (jobLaunching), либо звал Outcome по чужому
// jobID на каждом обходе, а строка оставалась running до перезапуска
// бэкенда. Зависнуть навсегда хуже, чем изредка потратить лишнюю попытку на
// повторной неудачной записи: пять попыток исчерпаются, и намерение
// закроется честно, а не зависнет. jobLaunchingTimeout в pollOne -- вторая,
// независимая страховка на тот же случай.
func (s *Service) backToWaiting(routerID int64, reason string, now time.Time, generation int64) {
	s.forgetJob(routerID)
	if s.testBackToWaitingErr != nil {
		if err := s.testBackToWaitingErr(); err != nil {
			s.logger.Warn("оживление: возврат в ожидание не записан", "router_id", routerID, "err", err)
			return
		}
	}
	ok, err := s.cfg.DB.Revive().BackToWaiting(routerID, reason, now, generation)
	if err != nil {
		s.logger.Warn("оживление: возврат в ожидание не записан", "router_id", routerID, "err", err)
		return
	}
	if !ok {
		s.logger.Debug("оживление: возврат в ожидание пропущен -- поколение сменилось или статус уже не тот", "router_id", routerID)
	}
}

// backToWaitingNoAttempt -- как backToWaiting, но для запуска, который отказал
// не по вине оживления (Fix round 1, Minor #3: движок переустановки занят
// чужим заданием на этом же роутере). BackToWaitingNoAttempt в БД отменяет
// инкремент, который MarkRunning уже внёс. forgetJob -- безусловно, тем же
// доводом, что у backToWaiting (Fix round 2, Important B).
func (s *Service) backToWaitingNoAttempt(routerID int64, reason string, now time.Time, generation int64) {
	s.forgetJob(routerID)
	ok, err := s.cfg.DB.Revive().BackToWaitingNoAttempt(routerID, reason, now, generation)
	if err != nil {
		s.logger.Warn("оживление: возврат в ожидание без траты попытки не записан", "router_id", routerID, "err", err)
		return
	}
	if !ok {
		s.logger.Debug("оживление: возврат в ожидание (без попытки) пропущен -- поколение сменилось или статус уже не тот", "router_id", routerID)
	}
}

// notifySendTimeout -- сколько ждём доставки уведомления о закрытии
// намерения; не время самого закрытия (Finish уже записан).
const notifySendTimeout = 5 * time.Second

// finish -- условный переход в конечный статус со стиранием секрета. Одно
// уведомление: второй закрывающий получает ok=false и молчит.
//
// generation -- поколение, прочитанное вызывающим на своём Get (Fix round 2,
// мандатное ревью): структурная защита вместо защиты временем удержания
// замка. Без неё Schedule, успевший переставить намерение в щель между
// чтением вызывающего и этой записью (checkOne уже отпустила s.work перед
// ранним выходом -- expired/agentFresh/maxAttempts/no-url), создал бы
// СВЕЖУЮ строку с тем же статусом, и Finish закрыл бы её (стерев её НОВЫЙ
// секрет), как будто это была та, старая строка (Regression A).
//
// forgetJob -- безусловно, ДО записи (Fix round 2, Important B; тем же
// доводом, что у backToWaiting).
func (s *Service) finish(ctx context.Context, routerID int64, from []string, to, reason, notice string, generation int64) {
	s.forgetJob(routerID)
	if s.testBeforeFinishWrite != nil {
		s.testBeforeFinishWrite()
	}
	if s.testFinishErr != nil {
		if err := s.testFinishErr(); err != nil {
			s.logger.Warn("оживление: закрытие не записано", "router_id", routerID, "status", to, "err", err)
			return
		}
	}
	ok, err := s.cfg.DB.Revive().Finish(routerID, from, to, reason, s.now(), generation)
	if err != nil {
		s.logger.Warn("оживление: закрытие не записано", "router_id", routerID, "status", to, "err", err)
		return
	}
	if !ok {
		s.logger.Debug("оживление: закрытие пропущено -- поколение сменилось или статус уже не тот", "router_id", routerID, "status", to)
		return
	}
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
