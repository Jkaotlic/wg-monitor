package linkrepair

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
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
	ErrUnknownOrigin  = errors.New("не помним, каким конфигом поднята эта линия")
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

	pol, backup, err := d.findPolicy(ctx, req.RouterID, sc.TunnelID)
	if err != nil {
		d.fail(ctx, jobID, req, StepFailover, "", err)
		return
	}

	// Шаг 1. Увести трафик, пока чиним. Резерва может не быть -- это не
	// провал: чинить всё равно надо, человек просто побудет без обхода.
	if backup != "" {
		d.step(jobID, StepFailover, provision.StepActive, "уводим трафик на резерв")
		if _, err := d.command(ctx, req.RouterID, "route_policy_promote", map[string]any{
			"policy_name": pol.Name,
			"tunnel_id":   backup,
		}); err != nil {
			d.fail(ctx, jobID, req, StepFailover, "", err)
			return
		}
		d.step(jobID, StepFailover, provision.StepDone, "трафик идёт через «"+backup+"»")
	} else {
		d.step(jobID, StepFailover, provision.StepDone, "резерва нет — чиним как есть")
	}

	// Шаг 2. Замена конфига теми же параметрами, что были у упавшей линии.
	provider, option, ok := d.Origin.Get(req.RouterID, sc.TunnelID)
	if !ok {
		d.fail(ctx, jobID, req, replace.StepIssue, backup, ErrUnknownOrigin)
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
		d.step(jobID, StepFailback, provision.StepFailed, "линия не вернулась")
		d.Store.Update(jobID, func(j *provision.Job) {
			j.State = provision.StateFailed
			j.Hint = err.Error()
		})
		d.notifyResult(ctx, req, sc, false, backup, err)
		return
	}

	// Шаг 3. Мастер замены уже поставил новую линию первым звеном политики --
	// возврат состоялся. Шаг закрывается фактом, а не ещё одной командой.
	d.step(jobID, StepFailback, provision.StepDone, "линия вернулась на место")
	d.Store.Update(jobID, func(j *provision.Job) { j.State = provision.StateSuccess })
	d.notifyResult(ctx, req, sc, true, backup, nil)
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
		return nil, fmt.Errorf("%s: очередь не приняла команду: %w", action, err)
	}
	res, ok := d.Commands.AwaitResult(ctx, routerID, id, d.awaitStep())
	if !ok || res == nil {
		return nil, fmt.Errorf("роутер не ответил на %s", action)
	}
	if res.Status != "ok" {
		return nil, fmt.Errorf("%s: %s", action, res.Output)
	}
	return res, nil
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

func (d Deps) fail(ctx context.Context, jobID string, req StartReq, step, backup string, cause error) {
	d.step(jobID, step, provision.StepFailed, cause.Error())
	d.Store.Update(jobID, func(j *provision.Job) {
		j.State = provision.StateFailed
		j.Hint = cause.Error()
	})
	if req.Auto {
		_ = d.Attempts.Record(req.Nickname, req.CheckName, false)
	}
	sc, _ := ScenarioFor(req.CheckName)
	d.notifyResult(ctx, req, sc, false, backup, cause)
}

// findPolicy ищет политику, в цепочке которой стоит упавшая линия, и
// подбирает ей замену. Снимок спрашивается у агента: бэкенд состав политик
// не хранит.
func (d Deps) findPolicy(ctx context.Context, routerID int64, tunnelID string) (wire.RoutePolicySummary, string, error) {
	res, err := d.command(ctx, routerID, "route_status", map[string]any{})
	if err != nil {
		return wire.RoutePolicySummary{}, "", err
	}
	var snap wire.RouteSnapshot
	if err := json.Unmarshal([]byte(res.Output), &snap); err != nil {
		return wire.RoutePolicySummary{}, "", fmt.Errorf("снимок маршрутизации не разобрался: %w", err)
	}
	for _, pol := range snap.Policies {
		for _, iface := range pol.Interfaces {
			if iface.TunnelID != tunnelID {
				continue
			}
			backup, _ := pickBackup(pol, tunnelID)
			return pol, backup, nil
		}
	}
	return wire.RoutePolicySummary{}, "", fmt.Errorf("линия %s не состоит ни в одной политике — чинить нечего", tunnelID)
}

// notifyResult -- единственное место, где движок говорит с человеком.
// Машинных имён здесь нет и быть не может: это текст в личку.
func (d Deps) notifyResult(ctx context.Context, req StartReq, sc Scenario, ok bool, backup string, cause error) {
	if d.Notify == nil {
		return
	}
	line := sc.TunnelID
	if line == "" {
		line = req.CheckName
	}
	var text string
	switch {
	case ok && backup != "":
		text = fmt.Sprintf("Линия «%s» падала. Увёл трафик на «%s», выпустил новый конфиг и вернул всё обратно — сейчас работает.", line, backup)
	case ok:
		text = fmt.Sprintf("Линия «%s» падала. Выпустил новый конфиг — сейчас работает.", line)
	case backup != "":
		text = fmt.Sprintf("Линия «%s» упала. Увёл трафик на «%s», обход блокировок работает. Поднять «%s» не смог: %v", line, backup, line, cause)
	default:
		text = fmt.Sprintf("Линия «%s» упала, и поднять её не вышло: %v. Заблокированное сейчас не открывается.", line, cause)
	}
	d.Notify(ctx, req.RouterID, text)
}
