package linkrepair

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/Jkaotlic/wg-monitor/internal/backend/provision"
	"github.com/Jkaotlic/wg-monitor/internal/backend/replace"
	"github.com/Jkaotlic/wg-monitor/internal/backend/upstream"
	"github.com/Jkaotlic/wg-monitor/pkg/wire"
)

var (
	// ErrNoScenario -- поломка настоящая, но отсюда не чинится. Это ответ,
	// а не ошибка: у пропавшего интернета и молчащего роутера нет шага,
	// который движок мог бы выполнить.
	ErrNoScenario = errors.New("для этой поломки сценария нет")
	// ErrAlreadyRunning -- замок общий с мастером замены: две операции разом
	// оставили бы маршрутизацию в состоянии, которого не ждал никто.
	ErrAlreadyRunning = errors.New("на этом роутере уже идёт починка или замена конфига")
	ErrAutoDisabled   = errors.New("полуавтомат выключен владельцем")
	ErrUnknownOrigin  = errors.New("не помним, каким конфигом поднят этот VPN-туннель")
	// ErrNotInAnySet -- VPN-туннель не стоит ни в одном общем наборе правил:
	// через него ничего не идёт, и чинить нечего. Отдельной меткой, чтобы
	// отчёт не пугал «заблокированное не открывается» -- для такого туннеля
	// это неправда.
	ErrNotInAnySet = errors.New("VPN-туннель не входит ни в один общий набор правил — чинить нечего")
)

// OriginReader -- чем была поднята линия. Пустой ok означает «система этого
// не помнит»: у линий, заведённых руками или до мастера замены, строки нет,
// и выдумать провайдера за них нечем.
type OriginReader interface {
	Get(routerID int64, tunnelID string) (provider, option string, ok bool)
}

type Deps struct {
	Store   *provision.Store
	Replace replace.Deps
	Origin  OriginReader
	// Attempts гасит цикл «падает -- чиним»; действует только на автозапуск.
	Attempts   Attempts
	AutoRepair func(routerID int64) bool
	Commands   replace.Commander
	Notify     func(ctx context.Context, routerID int64, text string)
	// BaseCtx принадлежит процессу, а не запросу: починка переживает
	// возврат HTTP-хендлера, который её запустил.
	BaseCtx   context.Context
	Now       func() time.Time
	AwaitStep time.Duration
	Logger    *slog.Logger
}

type StartReq struct {
	RouterID     int64
	Nickname     string
	CheckName    string
	AgentVersion string
	// Auto -- запуск сторожем, а не кнопкой. Только для него действуют
	// выключатель полуавтомата и счётчик попыток: человек, нажавший
	// «Починить», просил явно, и отказывать ему из-за счётчика неверно.
	Auto bool
	// TunnelName -- имя VPN-туннеля, каким его знает запускающий: автозапуск
	// берёт его из самой проверки, приложение -- из последних событий
	// роутера. Нужно, когда снимок не пришёл и имени взять больше неоткуда.
	// Пустое -- не знает.
	TunnelName string
}

// pickBackup выбирает линию, которой отдать трафик. Список интерфейсов
// политики уже отсортирован по приоритету, поэтому первый подходящий -- это
// ровно тот, на кого политика уйдёт сама. Интерфейсы без TunnelID (WAN,
// мост, гостевая сеть) не годятся: увести туда значит снять защиту.
func pickBackup(pol wire.RoutePolicySummary, brokenTunnelID string) (string, bool) {
	for _, iface := range pol.Interfaces {
		if iface.TunnelID == "" || iface.TunnelID == brokenTunnelID {
			continue
		}
		if !iface.Available {
			continue
		}
		return iface.TunnelID, true
	}
	return "", false
}

// lineNames -- как VPN-туннели называются для человека. Отчёт о починке
// уходит владельцу в личку, и VPN-туннели в нём обязаны звучать полной формой
// и так, как он их назвал: идентификатор («awg12») он нигде не видел.
type lineNames struct {
	broken string
	backup string
}

// namesFor берёт имена из того же снимка политики, где движок нашёл линии:
// у каждого звена рядом с TunnelID лежит Name. Нет имени -- остаётся
// идентификатор: выдумать его нечем, а промолчать хуже.
func namesFor(pol wire.RoutePolicySummary, brokenID, backupID string) lineNames {
	name := func(id string) string {
		if id == "" {
			return ""
		}
		for _, iface := range pol.Interfaces {
			if iface.TunnelID == id && strings.TrimSpace(iface.Name) != "" {
				return strings.TrimSpace(iface.Name)
			}
		}
		return id
	}
	return lineNames{broken: name(brokenID), backup: name(backupID)}
}

// Start заводит починку. Все отказы выдаются ДО замка и до первой команды:
// это единственное место, где отказ ничего не стоит.
func (d Deps) Start(req StartReq) (string, error) {
	sc, ok := ScenarioFor(req.CheckName)
	if !ok {
		return "", ErrNoScenario
	}
	if req.Auto {
		if d.AutoRepair != nil && !d.AutoRepair(req.RouterID) {
			return "", ErrAutoDisabled
		}
		if allow, why := d.Attempts.Allow(req.Nickname, req.CheckName); !allow {
			return "", fmt.Errorf("%w: %s", ErrAutoDisabled, why)
		}
	}
	// Порог агента тот же, что у мастера замены: без route_policy_promote и
	// tunnel_power сценарий не доживёт до конца, а откат опирается на них же.
	if upstream.SoftwareNewerThan(req.AgentVersion, replace.MinAgentVersion) {
		return "", fmt.Errorf("%w: на роутере агент %s, нужен %s или новее",
			replace.ErrAgentTooOld, req.AgentVersion, replace.MinAgentVersion)
	}
	if !d.Store.TryLock(req.Nickname) {
		return "", ErrAlreadyRunning
	}
	job := d.Store.Create(KindLinkRepair, req.Nickname, Steps())
	go d.run(job.ID, req, sc)
	return job.ID, nil
}

func (d Deps) run(jobID string, req StartReq, sc Scenario) {
	defer d.Store.Unlock(req.Nickname)
	ctx := d.BaseCtx
	if ctx == nil {
		ctx = context.Background()
	}

	pol, backup, brokenName, err := d.findPolicy(ctx, req.RouterID, sc.TunnelID)
	if err != nil {
		// Имя VPN-туннеля findPolicy достаёт из снимка и тогда, когда набора
		// не нашлось. Не пришёл сам снимок -- имя знает запускающий
		// (req.TunnelName); не знает и он -- остаётся id.
		if brokenName == "" {
			brokenName = strings.TrimSpace(req.TunnelName)
		}
		if brokenName == "" {
			brokenName = sc.TunnelID
		}
		d.fail(ctx, jobID, req, StepFailover, lineNames{broken: brokenName}, err)
		return
	}
	names := namesFor(pol, sc.TunnelID, backup)

	// Шаг 1. Увести трафик, пока чиним. Резерва может не быть -- это не
	// провал: чинить всё равно надо, человек просто побудет без обхода.
	if backup != "" {
		d.step(jobID, StepFailover, provision.StepActive, "уводим трафик на резерв")
		if _, err := d.command(ctx, req.RouterID, "route_policy_promote", map[string]any{
			"policy_name": pol.Name,
			"tunnel_id":   backup,
		}); err != nil {
			d.fail(ctx, jobID, req, StepFailover, lineNames{broken: names.broken},
				fmt.Errorf("увести трафик на запасной VPN-туннель «%s» не вышло: %w", names.backup, err))
			return
		}
		d.step(jobID, StepFailover, provision.StepDone, "трафик идёт через «"+names.backup+"»")
	} else {
		d.step(jobID, StepFailover, provision.StepDone, "резерва нет — чиним как есть")
	}

	// Шаг 2. Замена конфига теми же параметрами, что были у упавшей линии.
	provider, option, ok := d.Origin.Get(req.RouterID, sc.TunnelID)
	if !ok {
		d.fail(ctx, jobID, req, replace.StepIssue, names, ErrUnknownOrigin)
		return
	}
	// Мастер замены внутри починки МОЛЧИТ. Его текст говорит про «замену
	// конфига» языком инженера, а движок уже рассказывает ту же историю
	// по-человечески: два сообщения об одном событии -- это не забота, а шум.
	inner := d.Replace
	inner.Notify = nil
	err = inner.RunOnJob(ctx, jobID, replace.StartReq{
		RouterID: req.RouterID, Nickname: req.Nickname,
		Provider: provider, OptionID: option,
		OldTunnelID: sc.TunnelID, PolicyName: pol.Name,
		AgentVersion: req.AgentVersion,
	})
	if req.Auto {
		_ = d.Attempts.Record(req.Nickname, req.CheckName, err == nil)
	}
	if err != nil {
		// Мастер замены уже пометил свой шаг провалившимся и откатился.
		// Здесь остаётся сказать это человеку и закрыть задание.
		d.step(jobID, StepFailback, provision.StepFailed, "VPN-туннель не вернулся")
		d.Store.Update(jobID, func(j *provision.Job) {
			j.State = provision.StateFailed
			j.Hint = err.Error()
		})
		d.notifyResult(ctx, req, false, names, err)
		return
	}

	// Шаг 3. Мастер замены уже поставил новую линию первым звеном политики --
	// возврат состоялся. Шаг закрывается фактом, а не ещё одной командой.
	d.step(jobID, StepFailback, provision.StepDone, "VPN-туннель вернулся на место")
	d.Store.Update(jobID, func(j *provision.Job) { j.State = provision.StateSuccess })
	d.notifyResult(ctx, req, true, names, nil)
}

func (d Deps) now() time.Time {
	if d.Now != nil {
		return d.Now()
	}
	return time.Now()
}

func (d Deps) awaitStep() time.Duration {
	if d.AwaitStep > 0 {
		return d.AwaitStep
	}
	return 3 * time.Minute
}

func newCmdID() (string, error) {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("идентификатор команды не сгенерировался: %w", err)
	}
	return hex.EncodeToString(b[:]), nil
}

// command отправляет агенту команду и ждёт ответа. Механика та же, что у
// мастера замены: очередь и ожидание -- единственный способ поговорить с
// роутером, и заводить второй здесь незачем.
func (d Deps) command(ctx context.Context, routerID int64, action string, args map[string]any) (*wire.CommandResult, error) {
	id, err := newCmdID()
	if err != nil {
		return nil, err
	}
	cmd := wire.Command{ID: id, Action: action, Args: args, IssuedAt: d.now().UTC()}
	if err := d.Commands.Enqueue(routerID, cmd); err != nil {
		d.logCommandFailure(action, "enqueue", err.Error())
		return nil, errors.New(replace.CommandNotSent)
	}
	res, ok := d.Commands.AwaitResult(ctx, routerID, id, d.awaitStep())
	if !ok || res == nil {
		d.logCommandFailure(action, "no answer", "")
		return nil, errors.New(replace.RouterFailure(false, ""))
	}
	if res.Status != "ok" {
		d.logCommandFailure(action, res.Status, res.Output)
		return nil, errors.New(replace.RouterFailure(true, res.Status))
	}
	return res, nil
}

// logCommandFailure -- то, что владельцу не показывают (какая команда, с
// каким статусом, что ответил агент), остаётся оператору в логе. Причина
// провала уходит в личку, и сырому ответу агента там не место.
func (d Deps) logCommandFailure(action, status, output string) {
	if d.Logger != nil {
		d.Logger.Warn("linkrepair command failed", "action", action, "status", status, "output", strings.TrimSpace(output))
	}
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

func (d Deps) fail(ctx context.Context, jobID string, req StartReq, step string, names lineNames, cause error) {
	d.step(jobID, step, provision.StepFailed, cause.Error())
	d.Store.Update(jobID, func(j *provision.Job) {
		j.State = provision.StateFailed
		j.Hint = cause.Error()
	})
	if req.Auto {
		_ = d.Attempts.Record(req.Nickname, req.CheckName, false)
	}
	d.notifyResult(ctx, req, false, names, cause)
}

// findPolicy ищет политику, в цепочке которой стоит упавшая линия, и
// подбирает ей замену. Снимок спрашивается у агента: бэкенд состав политик
// не хранит.
//
// name -- имя упавшего VPN-туннеля из того же снимка. Оно нужно и тогда, когда
// набора не нашлось: причина уходит владельцу в личку, и идентификатор там
// читать некому. Не пришёл снимок -- имени взять неоткуда, name пустое.
func (d Deps) findPolicy(ctx context.Context, routerID int64, tunnelID string) (pol wire.RoutePolicySummary, backup, name string, err error) {
	res, err := d.command(ctx, routerID, "route_status", map[string]any{})
	if err != nil {
		return pol, "", "", fmt.Errorf("не узнали у роутера, в каком общем наборе правил этот VPN-туннель: %w", err)
	}
	var snap wire.RouteSnapshot
	if err := json.Unmarshal([]byte(res.Output), &snap); err != nil {
		d.logCommandFailure("route_status", "unparsable", err.Error())
		return pol, "", "", errors.New("не узнали у роутера, в каком общем наборе правил этот VPN-туннель: " + replace.RouterGarbled)
	}
	for _, t := range snap.Tunnels {
		if t.ID == tunnelID && strings.TrimSpace(t.Name) != "" {
			name = strings.TrimSpace(t.Name)
		}
	}
	for _, p := range snap.Policies {
		for _, iface := range p.Interfaces {
			if iface.TunnelID != tunnelID {
				continue
			}
			backup, _ := pickBackup(p, tunnelID)
			return p, backup, name, nil
		}
	}
	// Причина уходит владельцу в личку: ни идентификатора, ни «политики».
	return pol, "", name, ErrNotInAnySet
}

// notifyResult -- единственное место, где движок говорит с человеком.
// Машинных имён здесь нет и быть не может: это текст в личку.
func (d Deps) notifyResult(ctx context.Context, req StartReq, ok bool, names lineNames, cause error) {
	if d.Notify == nil {
		return
	}
	line := names.broken
	if line == "" {
		line = req.CheckName
	}
	backup := names.backup
	var text string
	switch {
	case errors.Is(cause, ErrNotInAnySet):
		// Через такой VPN-туннель правила не идут: пугать владельца
		// «заблокированное не открывается» -- врать.
		text = fmt.Sprintf("VPN-туннель «%s» упал. Чинить нечего: он не входит ни в один общий набор правил, и через него ничего не идёт.", line)
	case ok && backup != "":
		text = fmt.Sprintf("VPN-туннель «%s» падал. Увёл трафик на запасной VPN-туннель «%s», выпустил новый конфиг и вернул всё обратно — сейчас работает.", line, backup)
	case ok:
		text = fmt.Sprintf("VPN-туннель «%s» падал. Выпустил новый конфиг — сейчас работает.", line)
	case backup != "":
		text = fmt.Sprintf("VPN-туннель «%s» упал. Увёл трафик на запасной VPN-туннель «%s», обход блокировок работает. Поднять VPN-туннель «%s» не смог: %v", line, backup, line, cause)
	default:
		text = fmt.Sprintf("VPN-туннель «%s» упал, и поднять его не вышло: %v. Заблокированное сейчас не открывается.", line, cause)
	}
	d.Notify(ctx, req.RouterID, text)
}
