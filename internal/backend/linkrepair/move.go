package linkrepair

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
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

// startMoveOnly -- автозапуск при выключенной автопочинке. Агент не умеет
// route_policy_promote -- молчим, уводить нечем.
func (d Deps) startMoveOnly(req StartReq, sc Scenario) {
	if d.Commands == nil || d.Attempts.KV == nil || agentTooOldForPromote(req.AgentVersion) {
		return
	}
	if d.Pending != nil {
		d.Pending.Add(1)
	}
	go func() {
		if d.Pending != nil {
			defer d.Pending.Add(-1)
		}
		d.moveOnly(req, sc)
	}()
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

// moveOnly -- снимок, увод в каждом наборе, где VPN-туннель первое звено, и ни
// шага дальше. Упал резерв, живого запасного нет, вне наборов, снимка нет --
// движок молчит: правду о трафике уже сказала сама тревога (alerts.TrafficFacts),
// попытка не считается. Снимок -- чтение, он идёт без замка: иначе падение резерва
// в одном отчёте с несущим отняло бы замок у того, кому он нужен (контур S16).
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

	th := d.begin(ctx, req)
	refuse := func(what string, err error) {
		d.logWarn("linkrepair: увод отклонён", "nickname", req.Nickname, "check", req.CheckName, "why", what, "err", err)
		th.NeedHuman(context.WithoutCancel(ctx), "Трафик с VPN-туннеля «"+broken+"» не увёл: "+what+". Заблокированное сейчас не открывается.", "")
	}
	over, n, err := d.Attempts.overLimit(req.Nickname, req.CheckName)
	if err != nil {
		refuse(WhyAttemptsUnread, err)
		return
	}
	if over {
		refuse(fmt.Sprintf("это было уже %d раза за 6 часов — %s", n, ActTooOften), nil)
		return
	}
	if err := d.Attempts.Started(req.Nickname, req.CheckName); err != nil {
		refuse(whyAttemptUnsaved, err)
		return
	}
	rec := movedRecord{At: d.now().UTC()}
	for _, c := range cands {
		rec.Moves = append(rec.Moves, movedEntry{Policy: c.policy, Backup: c.backup, BackupName: c.backupNm})
	}
	if err := d.saveMoved(req.Nickname, req.CheckName, rec); err != nil {
		// Без записи увод стал бы безвозвратным: не уводим.
		refuse("не смог записать, куда увожу", err)
		d.finishAttempt(req, true)
		return
	}

	var moved []movedEntry
	var refused []string
	var lastErr error
	for i, c := range cands {
		if _, err := d.command(ctx, req.RouterID, "route_policy_promote", map[string]any{
			"policy_name": c.policy,
			"tunnel_id":   c.backup,
		}); err != nil {
			refused, lastErr = append(refused, c.backupNm), err
			continue
		}
		moved = append(moved, rec.Moves[i])
	}
	rec.Moves = moved
	if err := d.saveMoved(req.Nickname, req.CheckName, rec); err != nil {
		d.logWarn("linkrepair: запись об уводе не обновилась", "nickname", req.Nickname, "check", req.CheckName, "err", err)
	}
	d.finishAttempt(req, true)

	what := ""
	if len(refused) > 0 {
		what = "роутер не дал увести трафик на запасной VPN-туннель «" + joinBackupNames(refused) + "»"
		if errors.Is(lastErr, errNoAnswer) {
			what = "роутер не подтвердил увод трафика на запасной VPN-туннель «" + joinBackupNames(refused) + "»"
		}
	}
	bg := context.WithoutCancel(ctx)
	if len(moved) == 0 {
		th.NeedHuman(bg, "Трафик с VPN-туннеля «"+broken+"» не увёл: "+what+". Заблокированное сейчас не открывается.", "")
		return
	}
	movedNames := make([]string, 0, len(moved))
	for _, m := range moved {
		movedNames = append(movedNames, m.BackupName)
	}
	text := "Увёл трафик на запасной VPN-туннель «" + joinBackupNames(movedNames) + "»: VPN-туннель «" + broken + "» не отвечает. Верну трафик, когда он оживёт."
	if what != "" {
		text += " Но " + what + "."
	}
	th.Moved(bg, text)
}

// ReturnMoved -- VPN-туннель снова живой (Д2): вернуть трафик первым звеном туда,
// откуда его увели. Зовётся на каждый отчёт с живым туннелем, пока запись есть:
// занятый замок, молчание агента и рестарт бэкенда откладывают возврат, а не
// теряют его. Человек сам переставил цепочку -- первым стоит не наш резерв --
// его решение не трогаем, запись снимается. Возврат -- тоже попытка: упёрся в
// потолок -- трафик остаётся на живом резерве, запись ждёт, пока окно отпустит.
func (d Deps) ReturnMoved(req StartReq) {
	sc, ok := ScenarioFor(req.CheckName)
	if !ok || d.Commands == nil {
		return
	}
	if !d.Store.TryLock(req.Nickname) {
		return
	}
	go func() {
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
	first := map[string]string{}
	for _, p := range snap.Policies {
		if len(p.Interfaces) > 0 {
			first[strings.ToLower(strings.TrimSpace(p.Name))] = p.Interfaces[0].TunnelID
		}
	}
	var pending []movedEntry
	for _, m := range rec.Moves {
		if first[strings.ToLower(strings.TrimSpace(m.Policy))] == m.Backup {
			pending = append(pending, m)
		}
	}
	rec.Moves = pending
	if len(pending) == 0 {
		if err := d.saveMoved(req.Nickname, req.CheckName, rec); err != nil {
			warn("запись не снялась", err)
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
	if err := d.Attempts.Started(req.Nickname, req.CheckName); err != nil {
		warn("попытка не записалась", err)
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
	rec.Moves = stuck
	if err := d.saveMoved(req.Nickname, req.CheckName, rec); err != nil {
		warn("запись не обновилась", err)
	}
	if len(stuck) == 0 {
		d.begin(ctx, req).Moved(bg, "Вернул трафик на VPN-туннель «"+broken+"»: он снова отвечает.")
	}
}
