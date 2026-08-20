// Package replace -- мастер замены конфига туннеля.
//
// Шесть шагов, и до успеха последнего откат доступен на каждом:
//
//  1. выпустить конфиг у провайдера (сервер, конфиг клиенту не показывается);
//  2. импортировать НОВЫМ туннелем рядом (прежний остаётся нетронутым);
//  3. дождаться рукопожатия;
//  4. сделать новый туннель первым звеном политики;
//  5. проверить, что наружу видно другой адрес;
//  6. выключить прежний туннель -- но НЕ удалять.
//
// Почему заданием, а не цепочкой команд из приложения: операция должна
// переживать закрытие приложения, а откат -- оставаться доступным с другого
// устройства. Поэтому состояние живёт в общем Store (том же, что у
// provision), а не в экране.
//
// Прежний туннель не удаляется никогда. Удаление необратимо, а вся эта
// операция построена на том, что откат возможен в любой момент.
package replace

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"time"

	"github.com/Jkaotlic/wg-monitor/internal/backend/provision"
	"github.com/Jkaotlic/wg-monitor/pkg/wire"
)

// KindReplaceConfig -- вид задания в общем Store.
const KindReplaceConfig provision.JobKind = "replace_config"

// Имена шагов. Экран печатает их словами, поэтому здесь -- ключи, а не текст.
const (
	StepIssue     = "issue"
	StepImport    = "import"
	StepHandshake = "handshake"
	StepPromote   = "promote"
	StepVerify    = "verify"
	StepRetire    = "retire"
)

// ErrAlreadyRunning -- на роутере уже идёт замена. Параллельные замены
// запрещены: две операции разом оставили бы маршрутизацию в состоянии,
// которого не ждал никто.
var ErrAlreadyRunning = errors.New("на этом роутере уже идёт замена конфига — дождитесь её завершения")

// Commander -- очередь команд агенту. Ровно два метода: положить и дождаться.
type Commander interface {
	Enqueue(userID int64, cmd wire.Command) error
	AwaitResult(ctx context.Context, userID int64, id string, timeout time.Duration) (*wire.CommandResult, bool)
}

// Cabinet выпускает конфиг у провайдера. Содержимое конфига уходит отсюда
// прямо в команду агенту и клиенту не показывается никогда.
type Cabinet interface {
	IssueConfig(ctx context.Context, routerID int64, provider, optionID string) (Issued, error)
}

type Issued struct {
	TunnelName string
	Conf       []byte
	Backend    string
}

// OriginWriter запоминает, каким конфигом поднят туннель. Без этого система
// не может ответить на вопрос «чем сейчас живёт маршрутизация».
type OriginWriter interface {
	Record(routerID int64, tunnelID, tunnelName, provider, option string, issuedAt time.Time) error
}

type Deps struct {
	Store    *provision.Store
	Commands Commander
	Cabinet  Cabinet
	Origin   OriginWriter
	// Notify пишет в топик роутера, когда всё кончилось. Работает при
	// закрытом приложении -- в этом и смысл.
	Notify func(ctx context.Context, routerID int64, text string)
	// BaseCtx принадлежит процессу, а не запросу: задание переживает
	// возврат HTTP-хендлера, который его запустил.
	BaseCtx context.Context
	Now     func() time.Time
	Logger  *slog.Logger
	// AwaitStep -- сколько ждать ответа агента на один шаг. Перезагрузка
	// роутера посреди операции -- штатное событие, а не провал, поэтому по
	// спеке это минуты, а не секунды.
	AwaitStep time.Duration
	// HandshakeTries/HandshakeWait -- сколько раз и с каким шагом
	// переспрашивать снимок, ожидая рукопожатия нового туннеля.
	HandshakeTries int
	HandshakeWait  time.Duration
	Sleep          func(context.Context, time.Duration)
}

type StartReq struct {
	RouterID    int64
	Nickname    string
	Provider    string
	OptionID    string
	OldTunnelID string
	PolicyName  string
}

// Steps -- чеклист задания в порядке выполнения.
func Steps() []provision.Step {
	return []provision.Step{
		{Name: StepIssue, Status: provision.StepPending},
		{Name: StepImport, Status: provision.StepPending},
		{Name: StepHandshake, Status: provision.StepPending},
		{Name: StepPromote, Status: provision.StepPending},
		{Name: StepVerify, Status: provision.StepPending},
		{Name: StepRetire, Status: provision.StepPending},
	}
}

func (d Deps) now() time.Time {
	if d.Now != nil {
		return d.Now()
	}
	return time.Now()
}

func (d Deps) sleep(ctx context.Context, dur time.Duration) {
	if d.Sleep != nil {
		d.Sleep(ctx, dur)
		return
	}
	select {
	case <-ctx.Done():
	case <-time.After(dur):
	}
}

func (d Deps) awaitStep() time.Duration {
	if d.AwaitStep > 0 {
		return d.AwaitStep
	}
	return 15 * time.Minute
}

func (d Deps) handshakeTries() int {
	if d.HandshakeTries > 0 {
		return d.HandshakeTries
	}
	return 10
}

func (d Deps) handshakeWait() time.Duration {
	if d.HandshakeWait > 0 {
		return d.HandshakeWait
	}
	return 15 * time.Second
}

// Start заводит задание и уходит: сама работа идёт в фоне, потому что она
// длиннее любого http-запроса.
func (d Deps) Start(req StartReq) (string, error) {
	if !d.Store.TryLock(req.Nickname) {
		return "", ErrAlreadyRunning
	}
	job := d.Store.Create(KindReplaceConfig, req.Nickname, Steps())
	go d.run(job.ID, req)
	return job.ID, nil
}

func (d Deps) run(jobID string, req StartReq) {
	ctx := d.BaseCtx
	if ctx == nil {
		ctx = context.Background()
	}
	defer d.Store.Unlock(req.Nickname)

	state := &runState{}
	if err := d.execute(ctx, jobID, req, state); err != nil {
		d.rollback(ctx, jobID, req, state, err)
		return
	}
	d.finish(ctx, jobID, req, state)
}

// runState -- то, что уже сделано и что придётся отменять.
type runState struct {
	NewTunnelID   string
	NewTunnelName string
	Imported      bool
	Promoted      bool
}

func (d Deps) execute(ctx context.Context, jobID string, req StartReq, state *runState) error {
	// 1. Конфиг выпускает сервер: клиент передал только выбор.
	d.step(jobID, StepIssue, provision.StepActive, "спрашиваем кабинет")
	issued, err := d.Cabinet.IssueConfig(ctx, req.RouterID, req.Provider, req.OptionID)
	if err != nil {
		d.step(jobID, StepIssue, provision.StepFailed, err.Error())
		return fmt.Errorf("кабинет не выдал конфиг: %w", err)
	}
	if len(issued.Conf) == 0 {
		d.step(jobID, StepIssue, provision.StepFailed, "кабинет вернул пустой конфиг")
		return errors.New("кабинет вернул пустой конфиг")
	}
	state.NewTunnelName = issued.TunnelName
	d.step(jobID, StepIssue, provision.StepDone, "конфиг получен: "+issued.TunnelName)

	// 2. Импорт НОВЫМ туннелем: replace=false, прежний остаётся на месте.
	d.step(jobID, StepImport, provision.StepActive, "кладём конфиг на роутер")
	backend := issued.Backend
	if backend == "" {
		backend = "nativewg"
	}
	res, err := d.command(ctx, req.RouterID, "tunnel_import", map[string]any{
		"conf":    base64.StdEncoding.EncodeToString(issued.Conf),
		"name":    issued.TunnelName,
		"replace": false,
		"backend": backend,
	})
	if err != nil {
		d.step(jobID, StepImport, provision.StepFailed, err.Error())
		return fmt.Errorf("импорт не прошёл: %w", err)
	}
	state.Imported = true
	state.NewTunnelID = tunnelIDFromImport(res.Output)
	if state.NewTunnelID == "" {
		if id, ok := d.findTunnelByName(ctx, req.RouterID, issued.TunnelName); ok {
			state.NewTunnelID = id
		}
	}
	if state.NewTunnelID == "" {
		d.step(jobID, StepImport, provision.StepFailed, "роутер не назвал идентификатор нового туннеля")
		return errors.New("роутер не назвал идентификатор нового туннеля")
	}
	d.step(jobID, StepImport, provision.StepDone, "новый туннель "+state.NewTunnelID+" создан рядом с прежним")

	// 3. Рукопожатие: без него линия не живая, что бы ни говорил статус.
	d.step(jobID, StepHandshake, provision.StepActive, "ждём рукопожатия")
	if err := d.waitHandshake(ctx, req.RouterID, state.NewTunnelID); err != nil {
		d.step(jobID, StepHandshake, provision.StepFailed, err.Error())
		return err
	}
	d.step(jobID, StepHandshake, provision.StepDone, "канал живой")

	// 4. Главным делаем звено в политике, а не глобальный маршрут по
	// умолчанию: трафик, ради которого меняют конфиг, идёт политикой.
	d.step(jobID, StepPromote, provision.StepActive, "ставим первым звеном политики")
	if _, err := d.command(ctx, req.RouterID, "route_policy_promote", map[string]any{
		"policy_name": req.PolicyName,
		"tunnel_id":   state.NewTunnelID,
	}); err != nil {
		d.step(jobID, StepPromote, provision.StepFailed, err.Error())
		return fmt.Errorf("не удалось сделать главным: %w", err)
	}
	state.Promoted = true
	d.step(jobID, StepPromote, provision.StepDone, "политика «"+req.PolicyName+"» идёт через новый туннель")

	// 5. Рукопожатия мало: оно бывает и при не ходящем трафике. Критерий --
	// адрес выхода сменился и отличается от прямого.
	d.step(jobID, StepVerify, provision.StepActive, "смотрим, каким адресом видно снаружи")
	verdict, err := d.verifyExit(ctx, req.RouterID)
	if err != nil {
		d.step(jobID, StepVerify, provision.StepFailed, err.Error())
		return err
	}
	d.step(jobID, StepVerify, provision.StepDone, verdict)

	// 6. Прежний туннель выключается, но остаётся: откат возможен всегда.
	d.step(jobID, StepRetire, provision.StepActive, "выключаем прежний туннель")
	if _, err := d.command(ctx, req.RouterID, "tunnel_power", map[string]any{
		"tunnel_id": req.OldTunnelID,
		"on":        false,
	}); err != nil {
		// Не провал операции: новый туннель уже несёт трафик. Прежний
		// остался включённым -- это видно на экране туннелей, и выключить
		// его можно там же.
		d.step(jobID, StepRetire, provision.StepFailed, "прежний туннель остался включённым: "+err.Error())
		return nil
	}
	d.step(jobID, StepRetire, provision.StepDone, "прежний туннель выключен и остался на роутере")
	return nil
}

func (d Deps) finish(ctx context.Context, jobID string, req StartReq, state *runState) {
	if d.Origin != nil {
		if err := d.Origin.Record(req.RouterID, state.NewTunnelID, state.NewTunnelName, req.Provider, req.OptionID, d.now()); err != nil && d.Logger != nil {
			d.Logger.Warn("replace: origin not recorded", "err", err)
		}
	}
	d.Store.Update(jobID, func(j *provision.Job) {
		j.State = provision.StateSuccess
		j.Hint = "готово: трафик политики «" + req.PolicyName + "» идёт через " + state.NewTunnelName
	})
	d.notify(ctx, req.RouterID, fmt.Sprintf(
		"Замена конфига завершена.\nНовый туннель: %s (%s).\nПолитика «%s» идёт через него, прежний туннель выключен и остался на роутере.",
		state.NewTunnelName, state.NewTunnelID, req.PolicyName))
}

// rollback возвращает роутер в исходное состояние: политику -- прежнему
// туннелю, новый туннель -- в выключенное. Новый туннель НЕ удаляется:
// удаление необратимо, а разбираться с ним человек будет на своём экране.
func (d Deps) rollback(ctx context.Context, jobID string, req StartReq, state *runState, cause error) {
	notes := []string{}
	if state.Promoted && req.OldTunnelID != "" {
		if _, err := d.command(ctx, req.RouterID, "route_policy_promote", map[string]any{
			"policy_name": req.PolicyName,
			"tunnel_id":   req.OldTunnelID,
		}); err != nil {
			notes = append(notes, "вернуть политику прежнему туннелю не удалось: "+err.Error())
		} else {
			notes = append(notes, "политика возвращена прежнему туннелю")
		}
	}
	if state.Imported && state.NewTunnelID != "" {
		if _, err := d.command(ctx, req.RouterID, "tunnel_power", map[string]any{
			"tunnel_id": state.NewTunnelID,
			"on":        false,
		}); err != nil {
			notes = append(notes, "выключить новый туннель не удалось: "+err.Error())
		} else {
			notes = append(notes, "новый туннель выключен и оставлен на роутере")
		}
	}
	hint := cause.Error()
	if len(notes) > 0 {
		hint += ". Откат: " + strings.Join(notes, "; ")
	}
	d.Store.Update(jobID, func(j *provision.Job) {
		j.State = provision.StateFailed
		j.Hint = hint
	})
	d.notify(ctx, req.RouterID, "Замена конфига не удалась.\n"+hint)
}

func (d Deps) notify(ctx context.Context, routerID int64, text string) {
	if d.Notify == nil {
		return
	}
	d.Notify(ctx, routerID, text)
}

func (d Deps) step(jobID, name string, status provision.StepStatus, detail string) {
	d.Store.Update(jobID, func(j *provision.Job) {
		for i := range j.Steps {
			if j.Steps[i].Name == name {
				j.Steps[i].Status = status
				j.Steps[i].Detail = detail
				return
			}
		}
	})
}

// command кладёт команду агенту и дожидается ответа. Молчание агента --
// не провал сам по себе: роутер мог перезагрузиться, и ждём мы минутами.
func (d Deps) command(ctx context.Context, routerID int64, action string, args map[string]any) (*wire.CommandResult, error) {
	id, err := newCmdID()
	if err != nil {
		return nil, err
	}
	cmd := wire.Command{ID: id, Action: action, Args: args, IssuedAt: d.now().UTC()}
	if err := d.Commands.Enqueue(routerID, cmd); err != nil {
		return nil, fmt.Errorf("%s: очередь не приняла команду: %w", action, err)
	}
	res, ok := d.Commands.AwaitResult(ctx, routerID, id, d.awaitStep())
	if !ok || res == nil {
		return nil, fmt.Errorf("%s: роутер не ответил за отведённое время", action)
	}
	if res.Status != "ok" {
		return nil, fmt.Errorf("%s: %s", action, strings.TrimSpace(firstLine(res.Output)))
	}
	return res, nil
}

func (d Deps) waitHandshake(ctx context.Context, routerID int64, tunnelID string) error {
	last := ""
	for i := 0; i < d.handshakeTries(); i++ {
		res, err := d.command(ctx, routerID, "route_status", map[string]any{})
		if err != nil {
			return err
		}
		var snap wire.RouteSnapshot
		if err := json.Unmarshal([]byte(res.Output), &snap); err != nil {
			return fmt.Errorf("снимок маршрутизации не разобрался: %w", err)
		}
		for _, t := range snap.Tunnels {
			if t.ID != tunnelID {
				continue
			}
			if t.HasHandshake {
				return nil
			}
			last = fmt.Sprintf("туннель %s есть, но рукопожатия ещё не было", tunnelID)
		}
		if last == "" {
			last = fmt.Sprintf("туннель %s не виден в снимке", tunnelID)
		}
		d.sleep(ctx, d.handshakeWait())
	}
	return errors.New("рукопожатия так и не случилось: " + last)
}

// verifyExit -- критерий успеха. Одного рукопожатия недостаточно: оно бывает
// и когда трафик не ходит, поэтому спрашиваем адрес выхода через туннель и
// напрямую и требуем, чтобы они отличались.
func (d Deps) verifyExit(ctx context.Context, routerID int64) (string, error) {
	viaRes, err := d.command(ctx, routerID, "check_via_tunnel", map[string]any{})
	if err != nil {
		return "", err
	}
	directRes, err := d.command(ctx, routerID, "check_direct", map[string]any{})
	if err != nil {
		return "", err
	}
	via := exitIP(viaRes.Output)
	direct := exitIP(directRes.Output)
	if via == "" {
		return "", errors.New("через туннель адрес выхода не определился")
	}
	if direct != "" && via == direct {
		return "", fmt.Errorf("снаружи виден тот же адрес, что и без туннеля (%s): подмены нет", via)
	}
	return fmt.Sprintf("через туннель %s, напрямую %s", via, orUnknown(direct)), nil
}

func (d Deps) findTunnelByName(ctx context.Context, routerID int64, name string) (string, bool) {
	res, err := d.command(ctx, routerID, "route_status", map[string]any{})
	if err != nil {
		return "", false
	}
	var snap wire.RouteSnapshot
	if err := json.Unmarshal([]byte(res.Output), &snap); err != nil {
		return "", false
	}
	for _, t := range snap.Tunnels {
		if strings.EqualFold(strings.TrimSpace(t.Name), strings.TrimSpace(name)) {
			return t.ID, true
		}
	}
	return "", false
}

// importIDRe читает идентификатор из ответа импорта: агент печатает его в
// человеческой строке «Туннель "amnezia_nl" создан (id=awg21)».
var importIDRe = regexp.MustCompile(`id=([A-Za-z0-9_.-]+)`)

func tunnelIDFromImport(output string) string {
	m := importIDRe.FindStringSubmatch(output)
	if len(m) != 2 {
		return ""
	}
	return strings.TrimSpace(strings.Trim(m[1], `")`))
}

var exitIPRe = regexp.MustCompile(`Exit IP:\s*([0-9a-fA-F.:]+)`)

func exitIP(output string) string {
	m := exitIPRe.FindStringSubmatch(output)
	if len(m) != 2 {
		return ""
	}
	return m[1]
}

func orUnknown(v string) string {
	if v == "" {
		return "неизвестно"
	}
	return v
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

func newCmdID() (string, error) {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return "replace-" + hex.EncodeToString(b[:]), nil
}
