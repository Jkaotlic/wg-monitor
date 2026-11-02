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
// и записью потеряло бы возврат, а лишняя запись безвредна -- возврат сверяет по
// снимку, что первым звеном стоит именно тот резерв, отказ и молчание её снимают.

// movedRecord -- куда увели трафик с упавшего VPN-туннеля. Значение KV.
type movedRecord struct {
	At    time.Time    `json:"at"`
	Moves []movedEntry `json:"moves"`
	// HeldSaid -- человеку уже сказано, что возврат упёрся в потолок попыток.
	HeldSaid bool `json:"held_said,omitempty"`
}

type movedEntry struct {
	Policy     string `json:"policy"`
	Backup     string `json:"backup"`
	BackupName string `json:"backup_name"`
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

// saveMoved пишет запись; без уводов -- снимает её.
func (d Deps) saveMoved(nickname, checkName string, rec movedRecord) error {
	if d.Attempts.KV == nil {
		return errors.New("записи об уводе негде жить")
	}
	if len(rec.Moves) == 0 {
		return d.Attempts.KV.Set(movedKey(nickname, checkName), "")
	}
	raw, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	return d.Attempts.KV.Set(movedKey(nickname, checkName), string(raw))
}

// HasMove -- есть ли что возвращать. Обработчик отчёта спрашивает на каждый живой
// VPN-туннель: одно чтение маленькой таблицы, сеть и замки не трогаются.
func (d Deps) HasMove(nickname, checkName string) bool {
	_, ok, err := d.loadMoved(nickname, checkName)
	if err != nil {
		d.logWarn("linkrepair: запись об уводе не прочиталась", "nickname", nickname, "check", checkName, "err", err)
	}
	return ok
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

// RetryMove -- VPN-туннель всё ещё лежит и всё ещё первое звено, а живой резерв
// есть (это обработчик отчёта уже проверил по самому отчёту, агенту -- ни одной
// команды): значит, увод на Hard не состоялся -- снимка не было, замок занят,
// бэкенд перезапустился. Нового Hard автомат не даст, поэтому пробуем на следующих
// отчётах. Замок занят -- там работа посерьёзнее, следующий отчёт спросит снова. На
// потолке роутер не спрашиваем: о нём уже сказано.
func (d Deps) RetryMove(req StartReq) {
	sc, ok := ScenarioFor(req.CheckName)
	if !ok || d.Store == nil || d.Attempts.KV == nil {
		return
	}
	if !d.Store.TryLock(req.Nickname) {
		return
	}
	d.Store.Unlock(req.Nickname)
	if over, _, err := d.Attempts.overLimit(req.Nickname, req.CheckName); err == nil && over && d.saidRecently(req, overLimitWord) {
		return
	}
	d.startMoveOnly(req, sc)
}

// lockForMove ждёт замок роутера не дольше moveLockWait.
func (d Deps) lockForMove(ctx context.Context, nickname string) bool {
	t := time.NewTimer(moveLockWait)
	defer t.Stop()
	for !d.Store.TryLock(nickname) {
		select {
		case <-ctx.Done():
			return false
		case <-t.C:
			return false
		case <-time.After(50 * time.Millisecond):
		}
	}
	return true
}

// overLimitWord -- слово, по которому узнаётся уже сказанный отказ на потолке.
const overLimitWord = "за 6 часов"

func movedSaidKey(nickname, checkName string) string {
	return "linkrepair:moved-said:" + nickname + ":" + checkName
}

type movedSaid struct {
	At   time.Time `json:"at"`
	Text string    `json:"text"`
}

// saidRecently -- этот отказ (по подстроке) уже сказан человеку в этом окне.
func (d Deps) saidRecently(req StartReq, substr string) bool {
	raw, err := d.Attempts.KV.Get(movedSaidKey(req.Nickname, req.CheckName))
	if err != nil || raw == "" {
		return false
	}
	var said movedSaid
	if json.Unmarshal([]byte(raw), &said) != nil {
		return false
	}
	return strings.Contains(said.Text, substr) && d.now().Sub(said.At) < attemptWindow
}

// ClearMoveSaid -- VPN-туннель ожил: следующий отказ -- уже про новую поломку.
func (d Deps) ClearMoveSaid(nickname, checkName string) {
	if d.Attempts.KV == nil {
		return
	}
	if err := d.Attempts.KV.Set(movedSaidKey(nickname, checkName), ""); err != nil {
		d.logWarn("linkrepair: отметка об отказе не снялась", "nickname", nickname, "check", checkName, "err", err)
	}
}

// sayRefused -- отказ увода человеку, один раз на текст: повтор идёт на каждый
// отчёт, и без этого правка (а без тревоги -- новое сообщение) шла бы каждую минуту.
func (d Deps) sayRefused(ctx context.Context, req StartReq, text string) {
	if d.saidRecently(req, text) {
		return
	}
	raw, _ := json.Marshal(movedSaid{At: d.now().UTC(), Text: text})
	if err := d.Attempts.KV.Set(movedSaidKey(req.Nickname, req.CheckName), string(raw)); err != nil {
		d.logWarn("linkrepair: отметка об отказе не записалась", "nickname", req.Nickname, "check", req.CheckName, "err", err)
	}
	d.begin(ctx, req).NeedHuman(context.WithoutCancel(ctx), text, "")
}

// moveOnly -- снимок, увод в каждом наборе, где VPN-туннель первое звено, и ни
// шага дальше. Упал резерв, живого запасного нет, вне наборов, снимка нет --
// движок молчит: правду о трафике уже сказала сама тревога (alerts.TrafficFacts),
// попытка не считается. Снимок -- чтение, он идёт без замка: иначе падение резерва
// в одном отчёте с несущим отняло бы замок у того, кому он нужен (контур S16).
//
// Попытка считается только за состоявшийся увод: отказ роутера трафик не двигал, а
// повтор идёт каждый отчёт и за три минуты сжёг бы окно на шесть часов.
func (d Deps) moveOnly(req StartReq, sc Scenario) {
	ctx := d.baseCtx()
	plan, err := d.findPolicies(ctx, req.RouterID, sc.TunnelID)
	if err != nil {
		d.logWarn("linkrepair: увод не начат, снимка нет", "nickname", req.Nickname, "check", req.CheckName, "err", err)
		return
	}
	if plan.reserve {
		return
	}
	cands := d.backups(req.RouterID, plan, sc.TunnelID)
	if len(cands) == 0 {
		return
	}
	if !d.lockForMove(ctx, req.Nickname) {
		d.logWarn("linkrepair: увод не начат, роутер занят", "nickname", req.Nickname, "check", req.CheckName)
		return
	}
	defer d.Store.Unlock(req.Nickname)
	broken := orID(plan.name, req.TunnelName, sc.TunnelID)
	refuse := func(what string, err error) {
		d.logWarn("linkrepair: увод отклонён", "nickname", req.Nickname, "check", req.CheckName, "why", what, "err", err)
		d.sayRefused(ctx, req, "Трафик с VPN-туннеля «"+broken+"» не увёл: "+what+". Заблокированное сейчас не открывается.")
	}
	over, n, err := d.Attempts.overLimit(req.Nickname, req.CheckName)
	if err != nil {
		refuse(WhyAttemptsUnread, err)
		return
	}
	if over {
		refuse(fmt.Sprintf("это было уже %d раза %s — %s", n, overLimitWord, ActTooOften), nil)
		return
	}
	// Прежняя запись (другие наборы, прерванный увод) не теряется: заменяются только
	// те наборы, что уводятся сейчас.
	rec, _, err := d.loadMoved(req.Nickname, req.CheckName)
	if err != nil {
		refuse("не смог прочитать, куда уводил", err)
		return
	}
	rec.At, rec.HeldSaid = d.now().UTC(), false
	for _, c := range cands {
		rec.Moves = slices.DeleteFunc(rec.Moves, func(m movedEntry) bool { return m.Policy == c.policy })
		rec.Moves = append(rec.Moves, movedEntry{Policy: c.policy, Backup: c.backup, BackupName: c.backupNm})
	}
	if err := d.saveMoved(req.Nickname, req.CheckName, rec); err != nil {
		// Без записи увод стал бы безвозвратным: не уводим.
		refuse("не смог записать, куда увожу", err)
		return
	}

	var moved []string
	var refused []string
	var lastErr error
	for _, c := range cands {
		if _, err := d.command(ctx, req.RouterID, "route_policy_promote", map[string]any{
			"policy_name": c.policy,
			"tunnel_id":   c.backup,
		}); err != nil {
			// Бэкенд останавливается: команда могла уйти, это не отказ. Запись
			// остаётся -- возврат сверит её со снимком, новый экземпляр доведёт.
			if ctx.Err() != nil {
				d.logWarn("linkrepair: увод прерван остановкой", "nickname", req.Nickname, "check", req.CheckName)
				return
			}
			refused, lastErr = append(refused, c.backupNm), err
			rec.Moves = slices.DeleteFunc(rec.Moves, func(m movedEntry) bool { return m.Policy == c.policy })
			continue
		}
		moved = append(moved, c.backupNm)
	}
	if err := d.saveMoved(req.Nickname, req.CheckName, rec); err != nil {
		d.logWarn("linkrepair: запись об уводе не обновилась", "nickname", req.Nickname, "check", req.CheckName, "err", err)
	}
	what := ""
	if len(refused) > 0 {
		what = "роутер не дал увести трафик на запасной VPN-туннель «" + joinBackupNames(refused) + "»"
		if errors.Is(lastErr, errNoAnswer) {
			what = "роутер не подтвердил увод трафика на запасной VPN-туннель «" + joinBackupNames(refused) + "»"
		}
	}
	if len(moved) == 0 {
		d.sayRefused(ctx, req, "Трафик с VPN-туннеля «"+broken+"» не увёл: "+what+". Заблокированное сейчас не открывается.")
		return
	}
	if err := d.Attempts.Started(req.Nickname, req.CheckName); err != nil {
		d.logWarn("linkrepair: попытка увода не записалась", "nickname", req.Nickname, "check", req.CheckName, "err", err)
	}
	text := "Увёл трафик на запасной VPN-туннель «" + joinBackupNames(moved) + "»: VPN-туннель «" + broken + "» не отвечает. Верну трафик, когда он оживёт."
	if what != "" {
		text += " Но " + what + "."
	}
	d.begin(ctx, req).Moved(context.WithoutCancel(ctx), text)
}

// ReturnMoved -- VPN-туннель снова живой (Д2): вернуть трафик первым звеном туда,
// откуда его увели. Зовётся на каждый отчёт с живым туннелем, пока запись есть:
// занятый замок, молчание агента, неполный снимок и рестарт бэкенда откладывают
// возврат, а не теряют его. Человек сам переставил цепочку -- его решение не
// трогаем, запись снимается. Попытка считается только за состоявшийся возврат:
// отказ роутера окно не сжигает. Упёрся в потолок -- трафик остаётся на живом
// резерве, запись ждёт, пока окно отпустит.
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
	d.Moves.begin()
	go func() {
		defer d.Moves.end()
		defer d.Moves.release(key)
		if !d.lockForMove(d.baseCtx(), req.Nickname) {
			return
		}
		defer d.Store.Unlock(req.Nickname)
		d.returnMoved(req, sc)
	}()
}

// movedVia -- трафик ушёл с m.Backup дальше по цепочке уводов и стоит на first:
// m.Backup сам лежит, и его запись ведёт к first. Тогда первым стоит не чужая
// перестановка, а наш же каскад: ждём, пока вернётся он, порядок восстановится сам.
func (d Deps) movedVia(req StartReq, m movedEntry, first string) bool {
	cur := m.Backup
	for range 8 {
		rec, ok, err := d.loadMoved(req.Nickname, tunnelCheckPrefix+cur)
		if err != nil || !ok {
			return false
		}
		next := ""
		for _, e := range rec.Moves {
			if e.Policy == m.Policy {
				next = e.Backup
			}
		}
		if next == "" {
			return false
		}
		if next == first {
			return true
		}
		cur = next
	}
	return false
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
	if !found {
		return
	}
	over, _, err := d.Attempts.overLimit(req.Nickname, req.CheckName)
	if err != nil {
		warn("история попыток", err)
		return
	}
	if over && rec.HeldSaid {
		return // сказано; снова спрашивать роутер незачем, пока окно не отпустит
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
	// политик или интерфейсов. «Политики нет» здесь значило бы «не прочиталась», а
	// не «удалили», и запись снялась бы вместе с возвратом.
	if len(snap.Warnings) > 0 {
		warn("снимок неполный, решу со следующим отчётом", errors.New(strings.Join(snap.Warnings, "; ")))
		return
	}
	first := map[string]string{}
	for _, p := range snap.Policies {
		if len(p.Interfaces) > 0 {
			first[strings.ToLower(strings.TrimSpace(p.Name))] = p.Interfaces[0].TunnelID
		}
	}
	var pending, waiting []movedEntry
	for _, m := range rec.Moves {
		head := first[strings.ToLower(strings.TrimSpace(m.Policy))]
		switch {
		case head == m.Backup:
			pending = append(pending, m)
		case head != "" && d.movedVia(req, m, head):
			waiting = append(waiting, m)
		}
		// Прочее -- полный снимок говорит: политику удалили или цепочку переставили
		// руками. Запись снимается, цепочку не трогаем.
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
		if _, err := d.command(ctx, req.RouterID, "route_policy_promote", map[string]any{
			"policy_name": m.Policy,
			"tunnel_id":   sc.TunnelID,
		}); err != nil {
			warn("вернуть не вышло, повторю со следующим отчётом", err)
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
	if err := d.Attempts.Started(req.Nickname, req.CheckName); err != nil {
		warn("попытка не записалась", err)
	}
	if len(stuck) == 0 {
		d.begin(ctx, req).Moved(bg, "Вернул трафик на VPN-туннель «"+broken+"»: он снова отвечает.")
	}
}
