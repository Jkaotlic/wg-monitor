package linkrepair

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Jkaotlic/wg-monitor/pkg/wire"
)

// Увод без автопочинки (Д1, v0.64). У туннеля без работающего ping-check
// автоперехода нет: политика так и ведёт трафик в мёртвое первое звено
// (router4car4, 02.11). Увод обратим и ничего не тратит, поэтому он идёт и при
// выключенной автопочинке, но только он: перезапуск и выпуск конфига остаются за
// настройкой v0.54. Потолок попыток -- общий с лесенкой.
//
// Запись об уводе -- в базе, ДО отправки команды: падение бэкенда между командой
// и записью потеряло бы возврат, а лишняя запись безвредна -- возврат сверяет её со
// снимком. Снимается только при явном отказе роутера: молчание и отмена не говорят,
// выполнена ли команда, это решит следующий снимок.

// moveRetryPause -- повтор увода и возврата после отказа или молчания не чаще:
// отчёт приходит раз в минуту, а каждая попытка -- это восемь чтений на роутере.
const moveRetryPause = 5 * time.Minute

// movedRecord -- куда увели трафик с упавшего VPN-туннеля. Значение KV.
type movedRecord struct {
	At    time.Time    `json:"at"`
	Moves []movedEntry `json:"moves"`
	// HeldSaid -- человеку уже сказано, что возврат упёрся в потолок попыток.
	HeldSaid bool `json:"held_said,omitempty"`
	// HoldBackup survives restart and requires a deliberate operator route choice.
	HoldBackup bool `json:"hold_backup,omitempty"`
}

type movedEntry struct {
	Policy     string `json:"policy"`
	Backup     string `json:"backup"`
	BackupName string `json:"backup_name"`
	// Origin -- порядок цепочки до увода: возврат восстанавливает его целиком.
	Origin []string `json:"origin,omitempty"`
	// Expect -- какой цепочка стала после наших действий. Другой порядок нашими
	// уводами не объяснить: это ручная правка, её не трогаем.
	Expect []string `json:"expect,omitempty"`
	// Unconfirmed -- команда отправлена, подтверждения не было: выполнена ли,
	// решит снимок.
	Unconfirmed bool `json:"unconfirmed,omitempty"`
}

// ReportChains -- цепочки наборов правил: ключ policyChainKey, значение -- id
// звеньев по приоритету, "" -- звено мимо VPN. nil -- не знаем.
type ReportChains map[string][]string

func policyChainKey(name string) string { return strings.ToLower(strings.TrimSpace(name)) }

func snapshotChains(snap wire.RouteSnapshot) ReportChains {
	out := make(ReportChains, len(snap.Policies))
	for _, p := range snap.Policies {
		chain := make([]string, 0, len(p.Interfaces))
		for _, i := range p.Interfaces {
			chain = append(chain, i.TunnelID)
		}
		out[policyChainKey(p.Name)] = chain
	}
	return out
}

// ChainsFromBriefs -- те же цепочки из сводки политик самого отчёта: команда агенту
// за ними не нужна. Сводки нет -- nil.
func ChainsFromBriefs(briefs []wire.PolicyBrief) ReportChains {
	if len(briefs) == 0 {
		return nil
	}
	out := make(ReportChains, len(briefs))
	for _, p := range briefs {
		chain := make([]string, 0, len(p.Links))
		for _, l := range p.Links {
			chain = append(chain, l.TunnelID)
		}
		out[policyChainKey(p.Name)] = chain
	}
	return out
}

// promoted -- цепочка после «сделать главным»: id первым, остальные сдвинулись.
func promoted(chain []string, id string) []string {
	out := make([]string, 0, len(chain))
	out = append(out, id)
	for _, l := range chain {
		if l != id {
			out = append(out, l)
		}
	}
	return out
}

// restorePlan -- какие звенья и в каком порядке сделать главными, чтобы из current
// получить target, минимальным числом команд. Звено, которое не двигаем, остаётся
// в хвосте в прежнем порядке: значит, неподвижными могут быть только суффикс target,
// идущий в current в том же порядке. Двигать звено мимо VPN (WAN) команда не умеет --
// тогда возвращаем только первое звено.
func restorePlan(current, target []string) []string {
	pos := make(map[string]int, len(current))
	for i, l := range current {
		pos[l] = i
	}
	k := len(target)
	last := len(current)
	for k > 0 {
		i, ok := pos[target[k-1]]
		if !ok || i >= last {
			break
		}
		last = i
		k--
	}
	plan := make([]string, 0, k)
	for i := k - 1; i >= 0; i-- {
		plan = append(plan, target[i])
	}
	if slices.Contains(plan, "") && len(target) > 0 {
		return []string{target[0]}
	}
	return plan
}

// moveVerdict -- что снимок говорит про запись одного набора.
type moveVerdict int

const (
	moveUnknown moveVerdict = iota // цепочки нет в этих данных
	moveReady                      // цепочка такая, какой её оставили мы
	moveWaiting                    // трафик ушёл дальше нашим же каскадом
	moveNotDone                    // команда не подтверждена и не выполнена
	moveForeign                    // порядок нашими уводами не объяснить
)

// classify сверяет запись с цепочкой. Цепочку другая запись могла увести дальше
// (A->B, потом B->C): такой порядок объясним, запись ждёт оживления того звена.
func (d Deps) classify(req StartReq, m movedEntry, chains ReportChains) moveVerdict {
	chain, ok := chains[policyChainKey(m.Policy)]
	if !ok {
		return moveUnknown
	}
	if slices.Equal(chain, m.Expect) {
		return moveReady
	}
	if m.Unconfirmed && slices.Equal(chain, m.Origin) {
		return moveNotDone
	}
	expect, cur := m.Expect, m.Backup
	for range 8 {
		rec, ok, err := d.loadMoved(req.Nickname, tunnelCheckPrefix+cur)
		if err != nil || !ok {
			break
		}
		next := ""
		for _, e := range rec.Moves {
			if e.Policy == m.Policy {
				next = e.Backup
			}
		}
		if next == "" {
			break
		}
		expect, cur = promoted(expect, next), next
		if slices.Equal(chain, expect) {
			return moveWaiting
		}
	}
	return moveForeign
}

func movedKey(nickname, checkName string) string {
	return "linkrepair:moved:" + nickname + ":" + checkName
}

func (d Deps) loadMoved(nickname, checkName string) (movedRecord, bool, error) {
	var rec movedRecord
	if d.Attempts.KV == nil {
		return rec, false, nil
	}
	raw, err := d.Attempts.KV.Get(movedKey(nickname, checkName))
	if err != nil {
		return rec, false, fmt.Errorf("запись об уводе не прочиталась: %w", err)
	}
	if raw == "" {
		return rec, false, nil
	}
	if err := json.Unmarshal([]byte(raw), &rec); err != nil {
		return rec, false, fmt.Errorf("запись об уводе битая: %w", err)
	}
	return rec, len(rec.Moves) > 0, nil
}

// saveMoved пишет запись, только если она изменилась; без уводов -- снимает её.
func (d Deps) saveMoved(nickname, checkName string, rec movedRecord) error {
	if d.Attempts.KV == nil {
		return errors.New("записи об уводе негде жить")
	}
	val := ""
	if len(rec.Moves) > 0 {
		raw, err := json.Marshal(rec)
		if err != nil {
			return err
		}
		val = string(raw)
	}
	if cur, err := d.Attempts.KV.Get(movedKey(nickname, checkName)); err == nil && cur == val {
		return nil
	}
	return d.Attempts.KV.Set(movedKey(nickname, checkName), val)
}

func moveTryKey(nickname, checkName string) string {
	return "linkrepair:move-try:" + nickname + ":" + checkName
}

// tryDue -- пауза после прошлой попытки прошла (или попытки не было).
func (d Deps) tryDue(req StartReq) bool {
	raw, err := d.Attempts.KV.Get(moveTryKey(req.Nickname, req.CheckName))
	if err != nil || raw == "" {
		return err == nil
	}
	at, err := time.Parse(time.RFC3339Nano, raw)
	if err != nil {
		return true
	}
	return d.now().Sub(at) >= moveRetryPause
}

// stampTry -- попытка началась; пауза считается от неё.
func (d Deps) stampTry(req StartReq) {
	if d.Attempts.KV == nil {
		return
	}
	if err := d.Attempts.KV.Set(moveTryKey(req.Nickname, req.CheckName), d.now().UTC().Format(time.RFC3339Nano)); err != nil {
		d.logWarn("linkrepair: время попытки не записалось", "nickname", req.Nickname, "check", req.CheckName, "err", err)
	}
}

// clearTry -- перестановка состоялась: следующей незачем ждать.
func (d Deps) clearTry(req StartReq) {
	if err := d.Attempts.KV.Set(moveTryKey(req.Nickname, req.CheckName), ""); err != nil {
		d.logWarn("linkrepair: время попытки не снялось", "nickname", req.Nickname, "check", req.CheckName, "err", err)
	}
}

// lineDead -- звено мертво по своей последней проверке (Д3).
func (d Deps) lineDead(routerID int64, tunnelID string) bool {
	return d.LineDead != nil && d.LineDead(routerID, tunnelID)
}

// moveLockWait -- сколько увод ждёт замок роутера. Замок нужен только на сами
// перестановки; две смерти в одном отчёте или короткая чужая операция -- не повод
// оставить трафик в мёртвом звене. Идущую минутами замену он не переждёт, и тогда
// молчит: тревога уже ушла.
const moveLockWait = 2 * time.Minute

// MoveState -- что идёт в фоне: счётчик для остановки процесса и защита от второго
// увода той же проверки, пока первый ждёт ответа агента.
type MoveState struct {
	pending  atomic.Int64
	inflight sync.Map
}

func NewMoveState() *MoveState { return &MoveState{} }

// Busy -- идёт ли что-нибудь. nil -- не считаем.
func (m *MoveState) Busy() bool { return m != nil && m.pending.Load() != 0 }

func (m *MoveState) begin() {
	if m != nil {
		m.pending.Add(1)
	}
}

func (m *MoveState) end() {
	if m != nil {
		m.pending.Add(-1)
	}
}

// claim -- занять проверку; false -- по ней уже идёт увод.
func (m *MoveState) claim(key string) bool {
	if m == nil {
		return true
	}
	_, busy := m.inflight.LoadOrStore(key, true)
	return !busy
}

// claimed -- идёт ли увод этой проверки: снимок в отчёте мог обогнать его ответ.
func (m *MoveState) claimed(key string) bool {
	if m == nil {
		return false
	}
	_, busy := m.inflight.Load(key)
	return busy
}

func (m *MoveState) release(key string) {
	if m != nil {
		m.inflight.Delete(key)
	}
}

// WaitMoves ждёт конца фоновых уводов и возвратов: они пишут в базу. Вызывающий
// ставит срок сам и отменяет BaseCtx раньше, поэтому ждать тут почти нечего.
func (d Deps) WaitMoves() {
	for d.Moves.Busy() {
		time.Sleep(10 * time.Millisecond)
	}
}

// startMoveOnly -- увод в фоне. Агент не умеет route_policy_promote -- молчим,
// уводить нечем. Увод той же проверки уже идёт -- второй не нужен.
func (d Deps) startMoveOnly(req StartReq, sc Scenario) {
	if d.Commands == nil || d.Attempts.KV == nil || agentTooOldForPromote(req.AgentVersion) {
		return
	}
	key := movedKey(req.Nickname, req.CheckName)
	if !d.Moves.claim(key) {
		return
	}
	d.Moves.begin()
	go func() {
		defer d.Moves.end()
		defer d.Moves.release(key)
		d.moveOnly(req, sc)
	}()
}

// AfterFailedReport -- отчёт про VPN-туннель в тревоге. Сперва слово самого отчёта:
// неподтверждённый увод виден в его цепочке, команда не нужна. Потом повтор, если
// туннель всё ещё лежит первым звеном при живом резерве (stuck) или про
// неподтверждённое отчёт не сказал: нового Hard автомат не даст, а увод на Hard мог
// не состояться -- снимка не было, замок занят, бэкенд перезапустился. В паузу --
// ни команд, ни записей. Замок занят -- там работа посерьёзнее, спросим позже. На
// потолке роутер не спрашиваем: о нём уже сказано.
func (d Deps) AfterFailedReport(req StartReq, chains ReportChains, stuck bool) {
	sc, ok := ScenarioFor(req.CheckName)
	if !ok || d.Store == nil || d.Attempts.KV == nil || d.Moves.claimed(movedKey(req.Nickname, req.CheckName)) {
		return
	}
	rec, found, err := d.loadMoved(req.Nickname, req.CheckName)
	if err != nil {
		d.logWarn("linkrepair: запись об уводе не прочиталась", "nickname", req.Nickname, "check", req.CheckName, "err", err)
		return
	}
	unknown := false
	if found {
		if chains != nil {
			d.confirmMoves(req, rec, chains)
		} else {
			unknown = slices.ContainsFunc(rec.Moves, func(m movedEntry) bool { return m.Unconfirmed })
		}
	}
	if !stuck && !unknown {
		return
	}
	if d.Store.Locked(req.Nickname) || !d.tryDue(req) {
		return
	}
	if over, _, err := d.Attempts.overLimit(req.Nickname, req.CheckName); err == nil && over && d.saidAlready(req, sayLimit) {
		return
	}
	d.stampTry(req)
	d.startMoveOnly(req, sc)
}

// confirmMoves -- цепочка показала: неподтверждённая команда выполнена. Теперь это
// настоящий увод: попытка считается, человеку правда.
func (d Deps) confirmMoves(req StartReq, rec movedRecord, chains ReportChains) {
	var names []string
	for i, m := range rec.Moves {
		if m.Unconfirmed && d.classify(req, m, chains) == moveReady {
			rec.Moves[i].Unconfirmed = false
			names = append(names, m.BackupName)
		}
	}
	if len(names) == 0 {
		return
	}
	if err := d.saveMoved(req.Nickname, req.CheckName, rec); err != nil {
		d.logWarn("linkrepair: запись об уводе не обновилась", "nickname", req.Nickname, "check", req.CheckName, "err", err)
	}
	d.countAttempt(req)
	d.clearTry(req)
	ctx := d.baseCtx()
	d.begin(ctx, req).Moved(context.WithoutCancel(ctx), movedText(names, orID(req.TunnelName, strings.TrimPrefix(req.CheckName, tunnelCheckPrefix)), ""))
}

func (d Deps) countAttempt(req StartReq) {
	if err := d.Attempts.Started(req.Nickname, req.CheckName); err != nil {
		d.logWarn("linkrepair: попытка не записалась", "nickname", req.Nickname, "check", req.CheckName, "err", err)
	}
}

func movedText(backups []string, broken, tail string) string {
	text := "Увёл трафик на запасной VPN-туннель «" + joinBackupNames(backups) + "»: VPN-туннель «" + broken + "» не отвечает. Верну трафик, когда он оживёт."
	if tail != "" {
		text += " Но " + tail + "."
	}
	return text
}

// lockForMove ждёт замок роутера не дольше moveLockWait. waited -- замок был занят:
// за это время снимок мог устареть.
func (d Deps) lockForMove(ctx context.Context, nickname string) (locked, waited bool) {
	t := time.NewTimer(moveLockWait)
	defer t.Stop()
	for !d.Store.TryLock(nickname) {
		waited = true
		select {
		case <-ctx.Done():
			return false, waited
		case <-t.C:
			return false, waited
		case <-time.After(50 * time.Millisecond):
		}
	}
	return true, waited
}

// Группы сообщений об отказе. Отказ и молчание -- один эпизод: повторы идут раз в
// несколько минут, и их чередование не должно писать человеку снова. Отметка живёт
// до Recovery.
const (
	sayMissed = "missed" // роутер отказал или не подтвердил
	sayLimit  = "limit"  // упёрлись в потолок попыток
	sayStore  = "store"  // не смогли прочитать или записать своё
)

func movedSaidKey(nickname, checkName string) string {
	return "linkrepair:moved-said:" + nickname + ":" + checkName
}

func (d Deps) saidGroups(req StartReq) []string {
	raw, err := d.Attempts.KV.Get(movedSaidKey(req.Nickname, req.CheckName))
	if err != nil || raw == "" {
		return nil
	}
	var groups []string
	if json.Unmarshal([]byte(raw), &groups) != nil {
		return nil
	}
	return groups
}

// saidAlready -- про эту группу в этом эпизоде уже сказано.
func (d Deps) saidAlready(req StartReq, group string) bool {
	return slices.Contains(d.saidGroups(req), group)
}

// markSaid отмечает группу; false -- уже была.
func (d Deps) markSaid(req StartReq, group string) bool {
	groups := d.saidGroups(req)
	if slices.Contains(groups, group) {
		return false
	}
	raw, _ := json.Marshal(append(groups, group))
	if err := d.Attempts.KV.Set(movedSaidKey(req.Nickname, req.CheckName), string(raw)); err != nil {
		d.logWarn("linkrepair: отметка о сказанном не записалась", "nickname", req.Nickname, "check", req.CheckName, "err", err)
	}
	return true
}

// ClearMoveSaid -- VPN-туннель ожил, эпизод кончен: следующий отказ -- уже про новую
// поломку, а возврату незачем ждать паузу прошлых попыток.
func (d Deps) ClearMoveSaid(nickname, checkName string) {
	if d.Attempts.KV == nil {
		return
	}
	for _, key := range []string{movedSaidKey(nickname, checkName), moveTryKey(nickname, checkName)} {
		if err := d.Attempts.KV.Set(key, ""); err != nil {
			d.logWarn("linkrepair: отметка эпизода не снялась", "nickname", nickname, "check", checkName, "err", err)
		}
	}
}

// sayRefused -- отказ увода человеку, один раз на группу в эпизоде.
func (d Deps) sayRefused(ctx context.Context, req StartReq, group, text string) {
	if !d.markSaid(req, group) {
		return
	}
	d.begin(ctx, req).NeedHuman(context.WithoutCancel(ctx), text, "")
}

// moveOnly -- снимок, увод в каждом наборе, где VPN-туннель первое звено, и ни
// шага дальше. Упал резерв, живого запасного нет, вне наборов, снимка нет --
// движок молчит: правду о трафике уже сказала сама тревога (alerts.TrafficFacts),
// попытка не считается. Снимок -- чтение, он идёт без замка: иначе падение резерва
// в одном отчёте с несущим отняло бы замок у того, кому он нужен (контур S16). Если
// замок пришлось ждать, снимок читается заново: решение по устаревшему увело бы
// трафик туда, куда уже нельзя.
//
// Попытка считается только за состоявшийся увод: отказ роутера трафик не двигал.
func (d Deps) moveOnly(req StartReq, sc Scenario) {
	ctx := d.baseCtx()
	plan, cands, ok := d.planMove(ctx, req, sc)
	if !ok {
		return
	}
	locked, waited := d.lockForMove(ctx, req.Nickname)
	if !locked {
		d.logWarn("linkrepair: увод не начат, роутер занят", "nickname", req.Nickname, "check", req.CheckName)
		return
	}
	defer d.Store.Unlock(req.Nickname)
	if waited {
		if plan, cands, ok = d.planMove(ctx, req, sc); !ok {
			return
		}
	}
	broken := orID(plan.name, req.TunnelName, sc.TunnelID)
	refuse := func(group, what string, err error) {
		d.logWarn("linkrepair: увод отклонён", "nickname", req.Nickname, "check", req.CheckName, "why", what, "err", err)
		d.sayRefused(ctx, req, group, "Трафик с VPN-туннеля «"+broken+"» не увёл: "+what+". Заблокированное сейчас не открывается.")
	}
	over, n, err := d.Attempts.overLimit(req.Nickname, req.CheckName)
	if err != nil {
		refuse(sayStore, WhyAttemptsUnread, err)
		return
	}
	if over {
		refuse(sayLimit, fmt.Sprintf("это было уже %d раза за 6 часов — %s", n, ActTooOften), nil)
		return
	}
	// Прежняя запись (другие наборы, прерванный увод) не теряется: заменяются только
	// те наборы, что уводятся сейчас.
	rec, _, err := d.loadMoved(req.Nickname, req.CheckName)
	if err != nil {
		refuse(sayStore, "не смог прочитать, куда уводил", err)
		return
	}
	// The hold belongs to the incident, not to one goroutine's request. Both a
	// pending incident and an existing held move require proof for every new backup.
	history, err := d.Attempts.load(req.Nickname, req.CheckName)
	if err != nil {
		refuse(sayStore, WhyAttemptsUnread, err)
		return
	}
	req.holdBackup = req.holdBackup || history.BackupHold || rec.HoldBackup

	if req.holdBackup {
		// A repeat during repair cooldown may move traffic, but only to a backup
		// whose exit is independently proven. It does not restart/reissue the line.
		verified := cands[:0]
		for _, c := range cands {
			if agentCanExitProbe(req.AgentVersion) {
				if _, err := d.Probe.VerifyTunnelExit(ctx, req.RouterID, c.backup); err == nil {
					verified = append(verified, c)
				}
			}
		}
		cands = verified
		if len(cands) == 0 {
			return
		}
	}

	rec.At, rec.HeldSaid = d.now().UTC(), false
	rec.HoldBackup = rec.HoldBackup || req.holdBackup
	for _, c := range cands {
		chain := plan.chains[policyChainKey(c.policy)]
		rec.Moves = slices.DeleteFunc(rec.Moves, func(m movedEntry) bool { return m.Policy == c.policy })
		rec.Moves = append(rec.Moves, movedEntry{Policy: c.policy, Backup: c.backup, BackupName: c.backupNm,
			Origin: chain, Expect: promoted(chain, c.backup), Unconfirmed: true})
	}
	if err := d.saveMoved(req.Nickname, req.CheckName, rec); err != nil {
		// Без записи увод стал бы безвозвратным: не уводим.
		refuse(sayStore, "не смог записать, куда увожу", err)
		return
	}

	if d.LatestCheck != nil {
		trigger, err := d.LatestCheck(req.RouterID, req.CheckName)
		if err == nil {
			err = d.recordEvidence(req, trigger, plan.snapshot)
		}
		if err != nil {
			refuse(sayStore, "не смог сохранить данные сбоя до увода", err)
			return
		}
	}

	var moved, refused, silent []string
	for _, c := range cands {
		_, err := d.command(ctx, req.RouterID, "route_policy_promote", map[string]any{
			"policy_name": c.policy,
			"tunnel_id":   c.backup,
		})
		switch {
		case err == nil:
			for i := range rec.Moves {
				if rec.Moves[i].Policy == c.policy {
					rec.Moves[i].Unconfirmed = false
				}
			}
			moved = append(moved, c.backupNm)
		case ctx.Err() != nil:
			// Бэкенд останавливается: команда могла уйти, запись остаётся.
			d.logWarn("linkrepair: увод прерван остановкой", "nickname", req.Nickname, "check", req.CheckName)
			if serr := d.saveMoved(req.Nickname, req.CheckName, rec); serr != nil {
				d.logWarn("linkrepair: запись об уводе не обновилась", "nickname", req.Nickname, "check", req.CheckName, "err", serr)
			}
			return
		case errors.Is(err, errNoAnswer):
			// Выполнил ли роутер, неизвестно: запись остаётся, решит снимок.
			silent = append(silent, c.backupNm)
		default:
			// Явный отказ: команда не выполнена, возвращать будет нечего.
			refused = append(refused, c.backupNm)
			rec.Moves = slices.DeleteFunc(rec.Moves, func(m movedEntry) bool { return m.Policy == c.policy })
		}
	}
	if err := d.saveMoved(req.Nickname, req.CheckName, rec); err != nil {
		d.logWarn("linkrepair: запись об уводе не обновилась", "nickname", req.Nickname, "check", req.CheckName, "err", err)
	}
	var trouble []string
	if len(refused) > 0 {
		trouble = append(trouble, "роутер не дал увести трафик на запасной VPN-туннель «"+joinBackupNames(refused)+"»")
	}
	if len(silent) > 0 {
		trouble = append(trouble, "роутер не подтвердил увод трафика на запасной VPN-туннель «"+joinBackupNames(silent)+"», проверяю")
	}
	what := strings.Join(trouble, "; ")
	if len(moved) > 0 {
		d.countAttempt(req)
		if what != "" {
			d.markSaid(req, sayMissed)
		} else {
			d.clearTry(req)
		}
		if rec.HoldBackup {
			d.begin(ctx, req).Moved(context.WithoutCancel(ctx), "Повторный сбой VPN-туннеля «"+broken+"»: трафик оставил на проверенном запасном VPN-туннеле «"+joinBackupNames(moved)+"». Автовозврат отключён; выберите основной VPN-туннель вручную.")
		} else {
			d.begin(ctx, req).Moved(context.WithoutCancel(ctx), movedText(moved, broken, what))
		}
		return
	}
	// Часть трафика уже ушла раньше, или выполнена ли команда -- неизвестно: «не увёл,
	// не открывается» было бы неправдой.
	partly := slices.ContainsFunc(rec.Moves, func(m movedEntry) bool { return !m.Unconfirmed })
	switch {
	case partly:
		d.sayRefused(ctx, req, sayMissed, "Часть трафика уже на запасном VPN-туннеле, а остальное не увёл: "+what+".")
	case len(refused) == 0:
		d.sayRefused(ctx, req, sayMissed, "Р"+strings.TrimPrefix(what, "р")+".")
	default:
		d.sayRefused(ctx, req, sayMissed, "Трафик с VPN-туннеля «"+broken+"» не увёл: "+what+". Заблокированное сейчас не открывается.")
	}
}

// planMove -- снимок и кого уводить. ok=false -- уводить нечего или не из чего решать
// (движок молчит, см. moveOnly). Неподтверждённый увод снимок заодно подтверждает.
func (d Deps) planMove(ctx context.Context, req StartReq, sc Scenario) (policyPlan, []backupCand, bool) {
	plan, err := d.findPolicies(ctx, req.RouterID, sc.TunnelID)
	if err != nil {
		d.logWarn("linkrepair: увод не начат, снимка нет", "nickname", req.Nickname, "check", req.CheckName, "err", err)
		return plan, nil, false
	}
	if plan.incomplete {
		d.logWarn("linkrepair: увод не начат, снимок неполный", "nickname", req.Nickname, "check", req.CheckName)
		return plan, nil, false
	}
	if rec, found, err := d.loadMoved(req.Nickname, req.CheckName); err == nil && found {
		d.confirmMoves(req, rec, plan.chains)
	}
	if plan.reserve {
		return plan, nil, false
	}
	cands := d.backups(req.RouterID, plan, sc.TunnelID)
	return plan, cands, len(cands) > 0
}

// HasMove -- есть ли запись об уводе: одно чтение маленькой таблицы на живой VPN-туннель.
func (d Deps) HasMove(nickname, checkName string) bool {
	_, ok, err := d.loadMoved(nickname, checkName)
	if err != nil {
		d.logWarn("linkrepair: запись об уводе не прочиталась", "nickname", nickname, "check", checkName, "err", err)
	}
	return ok
}

// ReturnDue -- пора ли возвращать: есть запись и отчёт не говорит, что она ещё ждёт.
// Сводка политик самого отчёта отсекает опрос без единой команды: запись в каскаде
// могла бы спрашивать роутер часами. Сводки нет (старый агент) -- не чаще паузы, как
// и после отказа.
func (d Deps) ReturnDue(req StartReq, chains ReportChains) bool {
	rec, found, err := d.loadMoved(req.Nickname, req.CheckName)
	if err != nil {
		d.logWarn("linkrepair: запись об уводе не прочиталась", "nickname", req.Nickname, "check", req.CheckName, "err", err)
		return false
	}
	if !found || rec.HoldBackup {
		return false
	}
	if chains != nil && !slices.ContainsFunc(rec.Moves, func(m movedEntry) bool {
		return d.classify(req, m, chains) != moveWaiting
	}) {
		return false
	}
	if over, _, err := d.Attempts.overLimit(req.Nickname, req.CheckName); err == nil && over && rec.HeldSaid {
		return false // сказано; снова спрашивать роутер незачем, пока окно не отпустит
	}
	return d.tryDue(req)
}

// ReturnMoved -- VPN-туннель снова живой (Д2): вернуть цепочку к порядку до увода.
// Зовётся, пока запись есть (ReturnDue): занятый замок, молчание агента, неполный
// снимок и рестарт бэкенда откладывают возврат, а не теряют его. Порядок, который
// нашими уводами не объяснить, -- решение человека: не трогаем, запись снимается.
// Попытка считается только за состоявшийся возврат: отказ роутера окно не сжигает.
// Упёрся в потолок -- трафик остаётся на живом резерве, запись ждёт, пока окно
// отпустит.
func (d Deps) ReturnMoved(req StartReq) {
	sc, ok := ScenarioFor(req.CheckName)
	if !ok || d.Commands == nil {
		return
	}
	// Замок ждём: туннели одного отчёта возвращаются параллельно, и при отказе от
	// ожидания сосед в каскаде мог бы отнимать его отчёт за отчётом (контур S28).
	// Возврат этой проверки уже идёт -- второй не нужен.
	key := movedKey(req.Nickname, req.CheckName) + ":back"
	if !d.Moves.claim(key) {
		return
	}
	d.stampTry(req)
	d.Moves.begin()
	go func() {
		defer d.Moves.end()
		defer d.Moves.release(key)
		locked, _ := d.lockForMove(d.baseCtx(), req.Nickname)
		if !locked {
			return
		}
		defer d.Store.Unlock(req.Nickname)
		d.returnMoved(req, sc)
	}()
}

func (d Deps) returnMoved(req StartReq, sc Scenario) {
	ctx := d.baseCtx()
	warn := func(msg string, err error) {
		d.logWarn("linkrepair: возврат: "+msg, "nickname", req.Nickname, "check", req.CheckName, "err", err)
	}
	rec, found, err := d.loadMoved(req.Nickname, req.CheckName)
	if err != nil {
		warn("запись об уводе", err)
		return
	}
	if !found || rec.HoldBackup {
		return
	}
	over, _, err := d.Attempts.overLimit(req.Nickname, req.CheckName)
	if err != nil {
		warn("история попыток", err)
		return
	}
	if over && rec.HeldSaid {
		return
	}
	res, err := d.command(ctx, req.RouterID, "route_status", map[string]any{})
	if err != nil {
		warn("снимка нет", err)
		return
	}
	var snap wire.RouteSnapshot
	if err := json.Unmarshal([]byte(res.Output), &snap); err != nil {
		warn("снимок не разобрался", err)
		return
	}
	// Неполный снимок ничего не доказывает: агент отвечает ok и при сбое чтения
	// политик или интерфейсов, а сборка без них (404) не отличит «удалили» от «не
	// умею». «Политики нет» здесь значило бы «не прочиталась», и запись снялась бы
	// вместе с возвратом.
	if len(snap.Warnings) > 0 || !snap.PolicyModel {
		warn("снимок неполный, решу позже", errors.New(strings.Join(snap.Warnings, "; ")))
		return
	}
	chains := snapshotChains(snap)
	var pending, waiting []movedEntry
	for _, m := range rec.Moves {
		switch d.classify(req, m, chains) {
		case moveReady:
			m.Unconfirmed = false
			pending = append(pending, m)
		case moveWaiting:
			waiting = append(waiting, m)
		}
		// Прочее: полный снимок говорит, что политику удалили, цепочку переставили
		// руками или неподтверждённая команда так и не выполнена. Запись снимается,
		// цепочку не трогаем.
	}
	rec.Moves = append(slices.Clone(pending), waiting...)
	if len(pending) == 0 {
		if err := d.saveMoved(req.Nickname, req.CheckName, rec); err != nil {
			warn("запись не обновилась", err)
		}
		return
	}
	broken := req.TunnelName
	for _, t := range snap.Tunnels {
		if t.ID == sc.TunnelID && strings.TrimSpace(t.Name) != "" {
			broken = strings.TrimSpace(t.Name)
		}
	}
	broken = orID(broken, sc.TunnelID)
	bg := context.WithoutCancel(ctx)
	if over {
		rec.HeldSaid = true
		if err := d.saveMoved(req.Nickname, req.CheckName, rec); err != nil {
			warn("запись не обновилась", err)
		}
		names := make([]string, 0, len(pending))
		for _, m := range pending {
			names = append(names, m.BackupName)
		}
		d.begin(ctx, req).Moved(bg, "VPN-туннель «"+broken+"» ожил, но падает раз за разом. Трафик оставил на запасном VPN-туннеле «"+joinBackupNames(names)+"» и верну позже, когда VPN-туннель перестанет падать.")
		return
	}
	var stuck []movedEntry
	for _, m := range pending {
		cur := chains[policyChainKey(m.Policy)]
		failed := false
		for _, id := range restorePlan(cur, m.Origin) {
			if _, err := d.command(ctx, req.RouterID, "route_policy_promote", map[string]any{
				"policy_name": m.Policy,
				"tunnel_id":   id,
			}); err != nil {
				warn("вернуть не вышло, повторю позже", err)
				failed = true
				break
			}
			// Цепочка уже изменилась: на повторе сверка идёт с этим порядком.
			cur = promoted(cur, id)
		}
		if failed {
			m.Expect = cur
			stuck = append(stuck, m)
		}
	}
	rec.Moves = append(stuck, waiting...)
	if err := d.saveMoved(req.Nickname, req.CheckName, rec); err != nil {
		warn("запись не обновилась", err)
	}
	if len(stuck) == len(pending) {
		return
	}
	d.countAttempt(req)
	if len(stuck) == 0 {
		d.clearTry(req)
		d.begin(ctx, req).Moved(bg, "Вернул трафик на VPN-туннель «"+broken+"»: он снова отвечает.")
	}
}
