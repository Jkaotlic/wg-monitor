// Package cmd is the in-memory command channel between backend and agents.
//
// Жизнь команды: мини-апп (или дашборд) ставит wire.Command человеку → агент
// забирает её долгим опросом GET /v1/cmd → выполняет → POST /v1/cmd/result
// возвращает итог сюда, и его забирает опрос того, кто команду поставил.
// Рестарт бэкенда очередь теряет: человек повторяет действие (ничего денежного
// и внешнего команда не меняет, пока агент её не выполнил).
package cmd

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/Jkaotlic/wg-monitor/pkg/wire"
)

// resultEntry пара́ет полезную нагрузку с временем создания, чтобы уборщик
// Sweep вымётывал протухшее (LOGIC-02). Без этого карта росла бы всё время
// жизни процесса.
var ErrDuplicateResult = errors.New("duplicate command result")
var ErrUnissuedResult = errors.New("command result does not match an issued command")

type resultEntry struct {
	result     wire.CommandResult
	recordedAt time.Time
	// action -- действие команды, продублированное рядом с её результатом.
	//
	// Дубль не от лени: граница по роли на опросе результата
	// (miniappCommandResultHandler) обязана знать действие, а запись о
	// выдаче живёт МЕНЬШЕ результата. Sweep чистит issued по issuedAt, а
	// results по recordedAt одним cutoff, и issuedAt всегда раньше -- значит
	// issued вымётывается первым, и без этого поля действие становилось бы
	// «неизвестным» при живом результате. Здесь оно живёт ровно столько же,
	// сколько сам результат.
	action string
}

type commandEntry struct {
	cmd      wire.Command
	issuedAt time.Time
}

// ExpiredCommandHandler observes commands dropped before issue, either because
// their TTL expired at dequeue time or because a newer command superseded them.
type ExpiredCommandHandler func(userID int64, cmd wire.Command)

// Queue is per-user FIFO queues plus a per-(user,id) result map.
// Concurrent-safe. Single mutex is fine — operations are short and the fleet
// is ~10 users, not 10k.
type Queue struct {
	mu      sync.Mutex
	pending map[int64][]wire.Command // userID → FIFO
	results map[int64]map[string]resultEntry
	issued  map[int64]map[string]commandEntry
	signal  *sync.Cond // signals on Enqueue and RecordResult
	onDrop  ExpiredCommandHandler
	logger  *slog.Logger // optional; nil → slog.Default()
}

// SetLogger overrides the queue's structured logger. Used by main to inject
// the JSON-handler with `component=cmd_queue` (OBS-08). Tests skip this.
func (q *Queue) SetLogger(l *slog.Logger) {
	q.logger = l
}

func (q *Queue) SetExpiredCommandHandler(h ExpiredCommandHandler) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.onDrop = h
}

func (q *Queue) log() *slog.Logger {
	if q.logger != nil {
		return q.logger
	}
	return slog.Default()
}

func New() *Queue {
	q := &Queue{
		pending: make(map[int64][]wire.Command),
		results: make(map[int64]map[string]resultEntry),
		issued:  make(map[int64]map[string]commandEntry),
	}
	q.signal = sync.NewCond(&q.mu)
	return q
}

func defaultCommandTTL(action string) time.Duration {
	switch action {
	case "self_update":
		return 30 * time.Minute
	case "firmware_install", "service_restart", "awgm_update", "hrneo_update":
		return 10 * time.Minute
	default:
		return 15 * time.Minute
	}
}

// CommandTTL -- сколько команда этого действия ждёт агента в очереди. Экспорт
// нужен мини-аппу: ответ на постановку спящему роутеру называет это число
// («выполнится, если он проснётся в течение N минут»).
func CommandTTL(action string) time.Duration {
	return defaultCommandTTL(action)
}

func commandExpired(cmd wire.Command, now time.Time) bool {
	return !cmd.ExpiresAt.IsZero() && !now.Before(cmd.ExpiresAt)
}

func supersedesPending(action string) bool {
	return action == "self_update"
}

func (q *Queue) prepareCommand(userID int64, cmd wire.Command) (wire.Command, error) {
	if cmd.ID == "" {
		q.log().Warn("queue enqueue rejected", "reason", "id-empty", "user_id", userID, "action", cmd.Action)
		return wire.Command{}, errors.New("command id is required")
	}
	if !wire.IsValidCommandAction(cmd.Action) {
		q.log().Warn("queue enqueue rejected", "reason", "invalid-action", "user_id", userID, "action", cmd.Action)
		return wire.Command{}, errors.New("invalid command action: " + cmd.Action)
	}
	if cmd.IssuedAt.IsZero() {
		cmd.IssuedAt = time.Now().UTC()
	}
	if cmd.ExpiresAt.IsZero() {
		cmd.ExpiresAt = cmd.IssuedAt.Add(defaultCommandTTL(cmd.Action))
	}
	return cmd, nil
}

func (q *Queue) enqueueLocked(userID int64, cmd wire.Command) (int, []wire.Command) {
	var dropped []wire.Command
	if supersedesPending(cmd.Action) {
		filtered := q.pending[userID][:0]
		for _, existing := range q.pending[userID] {
			if existing.Action == cmd.Action {
				dropped = append(dropped, existing)
				continue
			}
			filtered = append(filtered, existing)
		}
		if len(filtered) == 0 {
			delete(q.pending, userID)
		} else {
			q.pending[userID] = filtered
		}
	}
	q.pending[userID] = append(q.pending[userID], cmd)
	return len(q.pending[userID]), dropped
}

func notifyDroppedCommands(userID int64, h ExpiredCommandHandler, dropped []wire.Command) {
	if h == nil {
		return
	}
	for _, cmd := range dropped {
		h(userID, cmd)
	}
}

// Enqueue appends cmd to userID's queue and wakes any waiter.
// Validates ID non-empty and Action whitelist.
func (q *Queue) Enqueue(userID int64, cmd wire.Command) error {
	cmd, err := q.prepareCommand(userID, cmd)
	if err != nil {
		return err
	}
	q.mu.Lock()
	pendingLen, dropped := q.enqueueLocked(userID, cmd)
	onDrop := q.onDrop
	q.mu.Unlock()
	q.signal.Broadcast()
	notifyDroppedCommands(userID, onDrop, dropped)
	q.log().Debug("queue enqueue", "user_id", userID, "cmd_id", cmd.ID, "action", cmd.Action, "pending", pendingLen)
	return nil
}

// DropPending removes all queued (not-yet-issued) commands of the given action
// for a user and returns them. Backs the operator's "cancel pending deploy"
// action: dropping a queued self_update means it never fires when a sleeping
// agent later wakes. The expired-command handler is NOT invoked — the caller
// owns the pending-state cleanup (so it can clear unconditionally).
func (q *Queue) DropPending(userID int64, action string) []wire.Command {
	q.mu.Lock()
	defer q.mu.Unlock()
	queue := q.pending[userID]
	if len(queue) == 0 {
		return nil
	}
	var dropped []wire.Command
	filtered := queue[:0]
	for _, existing := range queue {
		if existing.Action == action {
			dropped = append(dropped, existing)
			continue
		}
		filtered = append(filtered, existing)
	}
	if len(filtered) == 0 {
		delete(q.pending, userID)
	} else {
		q.pending[userID] = filtered
	}
	if len(dropped) > 0 {
		q.log().Debug("queue drop pending", "user_id", userID, "action", action, "dropped", len(dropped))
	}
	return dropped
}

// Dequeue returns the head command for userID. If empty, waits up to
// holdTimeout for an Enqueue or until ctx is done. Returns (nil, false) if
// timed out or ctx cancelled. Otherwise (cmd, true).
//
// Implemented via cond.Broadcast + tick-driven re-check rather than per-user
// channels: simpler, and the broadcast cost is negligible at fleet scale.
func (q *Queue) Dequeue(ctx context.Context, userID int64, holdTimeout time.Duration) (*wire.Command, bool) {
	deadline := time.Now().Add(holdTimeout)

	// Spawn a goroutine that wakes the cond when ctx is done or deadline hits,
	// so the cond.Wait below can unblock without a busy-loop. Use NewTimer +
	// Stop instead of time.After so the underlying timer is GC'd promptly when
	// the early-exit `stop` channel fires (PERF-05/BUG-21).
	stop := make(chan struct{})
	defer close(stop)
	timer := time.NewTimer(holdTimeout)
	defer timer.Stop()
	go func() {
		select {
		case <-ctx.Done():
		case <-timer.C:
		case <-stop:
			return
		}
		q.signal.Broadcast()
	}()

	q.mu.Lock()
	defer q.mu.Unlock()
	for {
		if cmds := q.pending[userID]; len(cmds) > 0 {
			head := cmds[0]
			// Realloc when slack >= 4× live entries to release the underlying
			// array (BUG-22). Stays an O(1) amortised pop in the steady-state
			// small-queue case.
			tail := cmds[1:]
			if len(tail) > 16 && cap(tail) > 4*len(tail) {
				fresh := make([]wire.Command, len(tail))
				copy(fresh, tail)
				tail = fresh
			}
			if len(tail) == 0 {
				delete(q.pending, userID)
			} else {
				q.pending[userID] = tail
			}
			if commandExpired(head, time.Now()) {
				q.log().Warn("queue drop expired command", "user_id", userID, "cmd_id", head.ID, "action", head.Action)
				if h := q.onDrop; h != nil {
					q.mu.Unlock()
					h(userID, head)
					q.mu.Lock()
				}
				continue
			}
			bucket, ok := q.issued[userID]
			if !ok {
				bucket = make(map[string]commandEntry)
				q.issued[userID] = bucket
			}
			bucket[head.ID] = commandEntry{cmd: head, issuedAt: time.Now()}
			return &head, true
		}
		if ctx.Err() != nil || !time.Now().Before(deadline) {
			return nil, false
		}
		q.signal.Wait()
	}
}

// RecordResult stores result under (userID, result.ID) and wakes any AwaitResult waiter.
//
// Status validation: пустой статус — отказ (явный bug в агенте). Любой
// non-empty статус принимаем — log-and-accept policy (см. handler.go).
// Whitelist enforcement (validCommandResultStatuses) был причиной потери
// данных при rolling-upgrade флота с новым агентом, эмитящим неизвестный
// backend'у статус.
func (q *Queue) RecordResult(userID int64, result wire.CommandResult) error {
	if result.ID == "" {
		q.log().Warn("queue record result rejected", "reason", "id-empty", "user_id", userID)
		return errors.New("result id is required")
	}
	if result.Status == "" {
		q.log().Warn("queue record result rejected", "reason", "status-empty", "user_id", userID, "cmd_id", result.ID)
		return errors.New("result status is required")
	}
	q.mu.Lock()
	bucket := q.results[userID]
	if _, exists := bucket[result.ID]; exists {
		q.mu.Unlock()
		q.log().Debug("queue duplicate result ignored", "user_id", userID, "cmd_id", result.ID, "status", result.Status)
		return ErrDuplicateResult
	}
	issuedBucket, ok := q.issued[userID]
	if !ok || issuedBucket[result.ID].cmd.ID == "" {
		q.mu.Unlock()
		q.log().Warn("queue record result rejected", "reason", "cmd-not-issued", "user_id", userID, "cmd_id", result.ID)
		return ErrUnissuedResult
	}
	// Действие берём здесь, где запись о выдаче ещё точно есть, и кладём
	// рядом с результатом: переживать issued должно оно, а не наоборот.
	action := issuedBucket[result.ID].cmd.Action
	if bucket == nil {
		bucket = make(map[string]resultEntry)
		q.results[userID] = bucket
	}
	bucket[result.ID] = resultEntry{result: result, recordedAt: time.Now(), action: action}
	q.mu.Unlock()
	q.signal.Broadcast()
	q.log().Debug("queue record result", "user_id", userID, "cmd_id", result.ID, "status", result.Status)
	return nil
}

// ResultRecordedAt -- когда очередь записала результат команды (userID, id).
// false -- результата нет (ещё не пришёл или уже выметен Sweep). Мини-апп по
// этому времени отличает свежий ответ роутера от пролежавшего: решение об
// удалении VPN-туннеля принимается только по свежему снимку.
func (q *Queue) ResultRecordedAt(userID int64, id string) (time.Time, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	r, ok := q.results[userID][id]
	if !ok {
		return time.Time{}, false
	}
	return r.recordedAt, true
}

// CommandByID returns the command last dequeued for (userID, id). It lets the
// result handler recover action/args for commands enqueued without a Telegram
// origin ref, such as VPS deferred self_update jobs.
//
// Пока жив результат, действие узнаётся и по нему -- даже когда запись о
// выдаче уже вымело Sweep (он чистит issued по issuedAt раньше, чем results
// по recordedAt). Тогда возвращается команда с идентификатором и действием,
// но без аргументов: аргументы живут только в issued, а звонящим за границей
// по роли нужно именно действие. Без этого возврата граница на опросе
// результата открывалась бы ровно в окно между выдачей и ответом агента.
func (q *Queue) CommandByID(userID int64, cmdID string) (wire.Command, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if bucket, ok := q.issued[userID]; ok {
		if entry, ok := bucket[cmdID]; ok {
			return entry.cmd, true
		}
	}
	if bucket, ok := q.results[userID]; ok {
		if entry, ok := bucket[cmdID]; ok && entry.action != "" {
			return wire.Command{ID: cmdID, Action: entry.action}, true
		}
	}
	return wire.Command{}, false
}

// HasActiveCommand сообщает, есть ли у пользователя команда этого действия
// в работе: либо непротухшая в очереди, либо уже выданная агенту и ещё не
// отжившая свой TTL.
//
// Нужен постановке обновления при пробуждении (backend/deploy_wake.go):
// агент шлёт отчёт раз в минуту, и без этой проверки каждый следующий отчёт
// клал бы роутеру ещё один self_update, пока тот качает бинарь.
//
// Выданная команда считается активной не бессрочно: агент может уйти в
// перезагрузку, не вернув результата, и тогда запись в issued провисит до
// ближайшего Sweep. Порог -- собственный TTL действия: после него попытка
// считается потерянной, и следующий отчёт вправе начать заново.
func (q *Queue) HasActiveCommand(userID int64, action string) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.hasActiveCommandLocked(userID, action, time.Now())
}

// hasActiveCommandLocked -- тело HasActiveCommand, вызываемое уже под q.mu.
// Отдельная функция нужна EnqueueIfNoActive: проверка и постановка обязаны
// разделить одну блокировку, иначе между ними остаётся окно (см. его
// комментарий).
func (q *Queue) hasActiveCommandLocked(userID int64, action string, now time.Time) bool {
	for _, c := range q.pending[userID] {
		if c.Action == action && !commandExpired(c, now) {
			return true
		}
	}
	ttl := defaultCommandTTL(action)
	for _, e := range q.issued[userID] {
		if e.cmd.Action == action && now.Sub(e.issuedAt) < ttl {
			return true
		}
	}
	return false
}

// EnqueueIfNoActive кладёт cmd, только если у userID сейчас нет активной
// команды того же действия (см. HasActiveCommand). Атомарная замена паре
// раздельных вызовов HasActiveCommand+Enqueue (deploy_wake.go): у раздельных
// вызовов было окно между проверкой и постановкой, где второй параллельный
// контакт (например опрос и отчёт одного роутера почти одновременно) тоже
// успевал увидеть «не занято» и тоже поставить команду -- к тому моменту
// первая уже могла уйти из pending в issued (её выдал Dequeue), и supersede
// в enqueueLocked её не находил, так что в очереди оказывались две self_update
// разом (B1, минус backend, 2026-09-15).
//
// Возвращает (true, nil), когда постановка прошла, и (false, nil), когда
// действие уже занято -- это не ошибка, а нормальный исход досылки.
func (q *Queue) EnqueueIfNoActive(userID int64, cmd wire.Command) (bool, error) {
	cmd, err := q.prepareCommand(userID, cmd)
	if err != nil {
		return false, err
	}
	q.mu.Lock()
	if q.hasActiveCommandLocked(userID, cmd.Action, time.Now()) {
		q.mu.Unlock()
		return false, nil
	}
	pendingLen, dropped := q.enqueueLocked(userID, cmd)
	onDrop := q.onDrop
	q.mu.Unlock()
	q.signal.Broadcast()
	notifyDroppedCommands(userID, onDrop, dropped)
	q.log().Debug("queue enqueue if no active", "user_id", userID, "cmd_id", cmd.ID, "action", cmd.Action, "pending", pendingLen)
	return true, nil
}

// AwaitResult blocks until RecordResult lands a matching (userID,id) entry,
// or the timeout/ctx-cancel hits. Useful for the TG callback handler that
// wants to display the action's outcome inline.
func (q *Queue) AwaitResult(ctx context.Context, userID int64, id string, timeout time.Duration) (*wire.CommandResult, bool) {
	deadline := time.Now().Add(timeout)
	stop := make(chan struct{})
	defer close(stop)
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	go func() {
		select {
		case <-ctx.Done():
		case <-timer.C:
		case <-stop:
			return
		}
		q.signal.Broadcast()
	}()

	q.mu.Lock()
	defer q.mu.Unlock()
	for {
		if bucket, ok := q.results[userID]; ok {
			if r, found := bucket[id]; found {
				out := r.result
				return &out, true
			}
		}
		if ctx.Err() != nil || !time.Now().Before(deadline) {
			slog.Debug("AwaitResult timeout", "user_id", userID, "cmd_id", id, "waited", timeout)
			return nil, false
		}
		q.signal.Wait()
	}
}

// Sweep evicts result and issued entries older than ttl. Caller drives the
// schedule (see cmd/backend/main.go); not auto-spawned to keep the package
// dependency-free of context plumbing. Returns the number of results evicted.
func (q *Queue) Sweep(ttl time.Duration) (results int) {
	if ttl <= 0 {
		return 0
	}
	cutoff := time.Now().Add(-ttl)
	q.mu.Lock()
	defer q.mu.Unlock()
	for uid, bucket := range q.results {
		for id, e := range bucket {
			if e.recordedAt.Before(cutoff) {
				delete(bucket, id)
				results++
			}
		}
		if len(bucket) == 0 {
			delete(q.results, uid)
		}
	}
	for uid, bucket := range q.issued {
		for id, e := range bucket {
			if e.issuedAt.Before(cutoff) {
				delete(bucket, id)
			}
		}
		if len(bucket) == 0 {
			delete(q.issued, uid)
		}
	}
	return results
}
