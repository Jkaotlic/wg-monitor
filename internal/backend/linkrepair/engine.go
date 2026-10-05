package linkrepair

import (
	"context"
	"crypto/rand"
	"encoding/base64"
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
	// ErrAutoDisabled -- автозапуск не состоялся: автопочинка этого
	// VPN-туннеля выключена или стоит (лимит попыток, провал до человека).
	ErrAutoDisabled = errors.New("автопочинка этого VPN-туннеля выключена или стоит")
	// ErrNotInAnySet -- VPN-туннель не стоит ни в одном общем наборе правил:
	// через него ничего не идёт, и чинить нечего. Отдельной меткой, чтобы
	// отчёт не пугал «заблокированное не открывается» -- для такого туннеля
	// это неправда.
	ErrNotInAnySet = errors.New("VPN-туннель не входит ни в один общий набор правил — чинить нечего")
)

// MinAgentLadder -- агент, который понимает tunnel_import target_id. Старые
// агенты аргумент молча пропускают и, не найдя туннель по имени, заводят
// НОВЫЙ -- ровно тот мусор, от которого лесенка уходит. Поэтому ступени 2-3
// на агенте старше этого (или неизвестной версии) не выполняются вовсе.
const MinAgentLadder = "v0.54.0"

// notStartedTimeout -- сколько ждать Telegram с правкой «не запускалась».
const notStartedTimeout = 30 * time.Second

// maxRelocations -- сколько других локаций пробует ступень «пересоздать».
// Третья смена страны подряд -- уже не починка, а перебор.
const maxRelocations = 2

// Setting -- настройка автопочинки туннеля, как её видит движок.
type Setting struct {
	Enabled       bool
	Provider      string
	Option        string
	AllowRelocate bool
	// TunnelName -- имя VPN-туннеля, для которого настройку включали. Другое
	// имя под тем же id -- VPN-туннель переименовали (или под id уже другой):
	// перезапуск идёт, а выпуск конфига ждёт, пока человек подтвердит
	// автопочинку в приложении (оно и перепишет имя). Пусто -- имя не
	// записано, сверять не с чем.
	TunnelName string
	// RelocateSpent -- страна «Amnezia Premium», которую автопочинка этой
	// настройки уже выпустила при смене локации. Новая страна -- место в
	// подписке, поэтому она одна на настройку навсегда: не пусто -- смены
	// локации больше нет. Повторное включение отметку не снимает (это деньги).
	RelocateSpent string
}

// Reporter -- как движок говорит с людьми по ходу починки. Реализация живёт
// в пакете notify (правит сообщения тревоги); nil в Deps -- молчать.
type Reporter interface {
	Begin(ctx context.Context, routerID int64, checkName string) Thread
}

// Thread -- одна починка глазами людей.
type Thread interface {
	Progress(ctx context.Context, text string)
	// Done -- починил: правка; ответ о восстановлении гасится.
	Done(ctx context.Context, text string)
	// NeedHuman -- правка и ответ со звуком; пустой action -- только правка.
	NeedHuman(ctx context.Context, text, action string)
	// NotStarted -- правка «Автопочинка не запускалась: …».
	NotStarted(ctx context.Context, why string)
}

// nopThread -- нить без людей: Report не задан.
type nopThread struct{}

func (nopThread) Progress(context.Context, string)          {}
func (nopThread) Done(context.Context, string)              {}
func (nopThread) NeedHuman(context.Context, string, string) {}
func (nopThread) NotStarted(context.Context, string)        {}

type Deps struct {
	Store *provision.Store
	// Probe -- проверки мастера замены (WaitHandshake, VerifyExit,
	// AnalyzeConf): те же критерии «получилось», без его задания и шагов.
	Probe  replace.Deps
	Source Source
	// Settings -- настройка автопочинки туннеля; ok=false -- строки нет
	// (выключено).
	Settings func(routerID int64, tunnelID string) (Setting, bool)
	// SaveOption -- удачная смена локации запоминается в настройке туннеля.
	SaveOption func(routerID int64, tunnelID, provider, option string)
	// SpendRelocation записывает, что для настройки выпущена новая страна
	// «Amnezia Premium» (option), -- ДО выпуска: упади бэкенд посреди, отметка
	// уже стоит. Ошибка (или nil) -- страна не выпускается.
	SpendRelocation func(routerID int64, tunnelID, option string) error
	// Attempts гасит цикл «падает -- чиним»; действует только на автозапуск.
	Attempts Attempts
	Commands replace.Commander
	Report   Reporter
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
	// выключатель автопочинки и счётчик попыток: человек, нажавший
	// «Починить», просил явно, и отказывать ему из-за счётчика неверно.
	Auto bool
	// TunnelName -- имя VPN-туннеля, каким его знает запускающий: автозапуск
	// берёт его из самой проверки, приложение -- из последних событий
	// роутера. Нужно, когда снимок не пришёл и имени взять больше неоткуда.
	// Пустое -- не знает.
	TunnelName string
}

// renamed -- настройка записана для VPN-туннеля с другим именем. Не знаем
// одного из имён -- сверять не с чем, настройка считается своей.
func (s Setting) renamed(current string) bool {
	stored, cur := strings.TrimSpace(s.TunnelName), strings.TrimSpace(current)
	return stored != "" && cur != "" && stored != cur
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

// carrierOf -- упал ли резерв, а не первое звено цепочки. Уводить и
// возвращать трафик имеет смысл только для первого звена: оно и несёт трафик,
// когда живо. Упал резерв -- route_policy_promote сделал бы его первым и
// переставил цепочку, которую человек собирал сам.
//
// reserve -- упал резерв. carrier -- звено, через которое трафик идёт сейчас:
// активное и доступное, иначе первое доступное по порядку. Пусто при reserve --
// живого звена нет, и называть лежащее «тем, через что идёт трафик» нельзя.
func carrierOf(pol wire.RoutePolicySummary, brokenTunnelID string) (carrier string, reserve bool) {
	if len(pol.Interfaces) == 0 || pol.Interfaces[0].TunnelID == brokenTunnelID {
		return "", false
	}
	label := func(iface wire.RoutePolicyInterface) string {
		return orID(iface.Name, iface.Bind)
	}
	for _, iface := range pol.Interfaces {
		if iface.Role == "active" && iface.Available && iface.TunnelID != brokenTunnelID {
			return label(iface), true
		}
	}
	for _, iface := range pol.Interfaces {
		if iface.Available && iface.TunnelID != brokenTunnelID {
			return label(iface), true
		}
	}
	return "", true
}

// lineNames -- как VPN-туннели называются для человека. Отчёт о починке
// уходит владельцу в личку, и VPN-туннели в нём обязаны звучать полной формой
// и так, как он их назвал: идентификатор («awg12») он нигде не видел.
type lineNames struct {
	broken string
	// backup -- запасной VPN-туннель, на котором СЕЙЧАС идёт трафик. Пусто --
	// резерва нет или увести не вышло.
	backup string
	// noSnapshot -- снимка от роутера нет (молчит или прислал непонятное):
	// есть ли запасной VPN-туннель и подхватил ли он трафик, неизвестно.
	noSnapshot bool
	// notInSet -- VPN-туннель вне общих наборов правил: через него ничего не
	// идёт.
	notInSet bool
	// failbackFailed -- VPN-туннель починен, но вернуть на него трафик не
	// вышло: трафик остался на запасном.
	failbackFailed bool
	// reserve -- упал не первый VPN-туннель цепочки, а резерв: уводить и
	// возвращать нечего. carrier -- звено, через которое трафик идёт сейчас;
	// пусто при reserve -- живого звена у трафика нет.
	reserve bool
	carrier string
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

// orID -- первое непустое значение.
func orID(vals ...string) string {
	for _, v := range vals {
		if v = strings.TrimSpace(v); v != "" {
			return v
		}
	}
	return ""
}

// agentCanLadder -- понимает ли агент tunnel_import target_id. Неизвестная
// или нечитаемая версия -- «нет»: цена ошибки здесь -- лишний VPN-туннель на
// роутере человека.
func agentCanLadder(version string) bool {
	v := strings.TrimSpace(version)
	if v == "" {
		return false
	}
	if strings.TrimPrefix(strings.TrimPrefix(v, "v"), "V") == strings.TrimPrefix(MinAgentLadder, "v") {
		return true
	}
	return upstream.SoftwareNewerThan(MinAgentLadder, v)
}

func (d Deps) baseCtx() context.Context {
	if d.BaseCtx != nil {
		return d.BaseCtx
	}
	return context.Background()
}

func (d Deps) begin(ctx context.Context, req StartReq) Thread {
	if d.Report == nil {
		return nopThread{}
	}
	if th := d.Report.Begin(ctx, req.RouterID, req.CheckName); th != nil {
		return th
	}
	return nopThread{}
}

// Start заводит починку. Все отказы выдаются ДО замка и до первой команды:
// это единственное место, где отказ ничего не стоит.
func (d Deps) Start(req StartReq) (string, error) {
	sc, ok := ScenarioFor(req.CheckName)
	if !ok {
		return "", ErrNoScenario
	}
	ctx := d.baseCtx()
	var set Setting
	if d.Settings != nil {
		if s, found := d.Settings(req.RouterID, sc.TunnelID); found {
			set = s
		}
	}
	// Настройка записана для VPN-туннеля с другим именем: переименовали (или
	// id достался другому). Настройка остаётся, перезапуск идёт как обычно;
	// выпуск конфига ждёт подтверждения человека (см. run).
	renamedTo := ""
	if set.renamed(req.TunnelName) {
		renamedTo = strings.TrimSpace(req.TunnelName)
	}
	// notStarted -- при автозапуске люди уже получили тревогу; почему
	// починка не пошла, дописывается к ней. Ручному запуску ответ виден на
	// экране сразу.
	//
	// Автозапуск зовётся из обработчика отчёта агента: поход в Telegram там
	// держал бы отчёт. Begin -- без сети и синхронно (закрытие проверки
	// ставится сразу), сама правка -- в своей горутине и со сроком.
	notStarted := func(why string) {
		if !req.Auto {
			return
		}
		th := d.begin(ctx, req)
		go func() {
			c, cancel := context.WithTimeout(ctx, notStartedTimeout)
			defer cancel()
			th.NotStarted(c, why)
		}()
	}
	if req.Auto {
		// Выключено -- молчим: тревога уже ушла, а «не запускалась» про
		// выключенное -- шум.
		if !set.Enabled {
			return "", ErrAutoDisabled
		}
		if allow, why, tooOften := d.Attempts.verdict(req.Nickname, req.CheckName); !allow {
			// Потолок попыток -- не новый провал: громкого ответа нет, но что
			// делать, человек читает в той же правке.
			if tooOften {
				why += ". Что делать: " + ActTooOften
			}
			notStarted(why)
			return "", fmt.Errorf("%w: %s", ErrAutoDisabled, why)
		}
	}
	// Порог агента тот же, что у мастера замены: без route_policy_promote
	// увести и вернуть трафик нечем.
	if upstream.SoftwareNewerThan(req.AgentVersion, replace.MinAgentVersion) {
		notStarted("агент роутера слишком старый — " + ActAgentOld)
		return "", fmt.Errorf("%w: на роутере агент %s, нужен %s или новее",
			replace.ErrAgentTooOld, req.AgentVersion, replace.MinAgentVersion)
	}
	if !d.Store.TryLock(req.Nickname) {
		notStarted("уже идёт починка или замена конфига")
		return "", ErrAlreadyRunning
	}
	job := d.Store.Create(KindLinkRepair, req.Nickname, Steps())
	d.Store.Update(job.ID, func(j *provision.Job) { j.Target = req.CheckName })
	th := d.begin(ctx, req)
	go d.run(job.ID, req, sc, set, renamedTo, th)
	return job.ID, nil
}

// run -- лесенка. renamedTo -- новое имя VPN-туннеля, если запускающий уже
// знает, что его переименовали; иначе сверка по снимку.
func (d Deps) run(jobID string, req StartReq, sc Scenario, set Setting, renamedTo string, th Thread) {
	defer d.Store.Unlock(req.Nickname)
	ctx := d.baseCtx()

	pol, backup, carrier, reserve, brokenName, err := d.findPolicy(ctx, req.RouterID, sc.TunnelID)
	if err != nil {
		// Снимка нет или VPN-туннель не в наборе -- чинить вслепую нельзя.
		// Имя -- из снимка, иначе от запускающего, иначе id.
		names := lineNames{broken: orID(brokenName, req.TunnelName, sc.TunnelID)}
		action := ""
		if errors.Is(err, ErrNotInAnySet) {
			names.notInSet = true
		} else {
			names.noSnapshot = true
			action = ActRouterSilent
		}
		d.finishFail(ctx, jobID, req, th, StepFailover, names, err, action)
		return
	}
	// Сверка по снимку -- когда запускающий имени не знал.
	if renamedTo == "" && set.renamed(brokenName) {
		renamedTo = brokenName
	}
	names := namesFor(pol, sc.TunnelID, backup)
	names.carrier, names.reserve = carrier, reserve
	if names.broken == sc.TunnelID {
		names.broken = orID(brokenName, req.TunnelName, sc.TunnelID)
	}
	var log []string // что сделано -- для правок и итоговой фразы

	// Ступень 0. Увести трафик, пока чиним. Резерва может не быть -- это не
	// провал: чинить всё равно надо, человек просто побудет без обхода.
	// Упал резерв -- трафик и так идёт через первое звено, цепочку не трогаем.
	if reserve {
		d.step(jobID, StepFailover, provision.StepDone, textCarrier(carrier))
	} else if backup != "" {
		d.step(jobID, StepFailover, provision.StepActive, "уводим трафик на запасной VPN-туннель «"+names.backup+"»")
		if _, err := d.command(ctx, req.RouterID, "route_policy_promote", map[string]any{
			"policy_name": pol.Name,
			"tunnel_id":   backup,
		}); err != nil {
			// Отказ увода -- не повод бросать починку: чиним как без резерва,
			// трафик там, где был. Возвращать потом нечего. Молчание -- не
			// отказ: роутер мог команду и не получить.
			what := "роутер не дал увести трафик на запасной VPN-туннель «" + names.backup + "»"
			if errors.Is(err, errNoAnswer) {
				what = "роутер не подтвердил увод трафика на запасной VPN-туннель «" + names.backup + "»"
			}
			d.step(jobID, StepFailover, provision.StepFailed, what+" ("+err.Error()+") — чиним как есть")
			log = append(log, what)
			names.backup = ""
		} else {
			d.step(jobID, StepFailover, provision.StepDone, "трафик идёт через запасной VPN-туннель «"+names.backup+"»")
			log = append(log, textMovedTo(names.backup))
		}
	} else {
		d.step(jobID, StepFailover, provision.StepDone, "запасного VPN-туннеля нет — чиним как есть")
	}

	// Ступень 1. Поднять.
	th.Progress(ctx, progressText(names, log, "перезапускаю VPN-туннель «"+names.broken+"»"))
	if d.tryRestart(ctx, jobID, req, sc, names) {
		d.skip(jobID, "не понадобилось", StepReissue, StepRecreate)
		d.finishOK(ctx, jobID, req, th, pol, sc, names, append(log, textRestarted))
		return
	}
	log = append(log, textRestartNoHelp)
	if d.aborted(ctx, jobID, req, th, names, log) {
		return
	}

	// VPN-туннель переименовали: согласие на выпуск конфига давали другому
	// имени. Перезапуск ничего не тратит и уже прошёл; выпуск -- после
	// подтверждения в приложении.
	if renamedTo != "" {
		d.skip(jobID, "VPN-туннель теперь называется «"+renamedTo+"» — выпуск конфига ждёт подтверждения автопочинки в приложении", StepReissue, StepRecreate)
		d.finishNeedHuman(ctx, jobID, req, th, names, log, ActRenamed(renamedTo))
		return
	}

	// Ступени 2-3 требуют агент с target_id и источник.
	if !agentCanLadder(req.AgentVersion) {
		d.skip(jobID, "агент роутера не умеет заменять конфиг на месте", StepReissue, StepRecreate)
		d.finishNeedHuman(ctx, jobID, req, th, names, log, ActAgentOld)
		return
	}
	if set.Provider == "" || d.Source == nil {
		d.skip(jobID, "источник не выбран", StepReissue, StepRecreate)
		d.finishNeedHuman(ctx, jobID, req, th, names, log, ActNoSource)
		return
	}

	// Ступень 2. Тот же конфиг на месте. У «Amnezia Premium» -- только если
	// страна ещё выпущена: отозванную кабинет выпустил бы заново, и это новое
	// место в подписке.
	if set.Provider == "amnezia" {
		if nh := d.checkIssued(ctx, jobID, req, set); nh != nil {
			if d.aborted(ctx, jobID, req, th, names, log) {
				return
			}
			d.skip(jobID, "не понадобилось: источник ждёт человека", StepRecreate)
			d.finishNeedHuman(ctx, jobID, req, th, names, log, nh.Action)
			return
		}
	}
	th.Progress(ctx, progressText(names, log, "выпускаю конфиг заново из "+sourceLabel(set.Provider)))
	ok, nh := d.tryIssue(ctx, jobID, StepReissue, req, sc, names, func() (replace.Issued, error) {
		return d.Source.Issue(ctx, req.RouterID, set.Provider, set.Option)
	})
	if ok {
		d.skip(jobID, "не понадобилось", StepRecreate)
		d.finishOK(ctx, jobID, req, th, pol, sc, names, append(log, textReissued(set.Provider)))
		return
	}
	if nh != nil {
		d.skip(jobID, "не понадобилось: источник ждёт человека", StepRecreate)
		d.finishNeedHuman(ctx, jobID, req, th, names, log, nh.Action)
		return
	}
	log = append(log, textReissueNoHelp)
	if d.aborted(ctx, jobID, req, th, names, log) {
		return
	}

	// Ступень 3. Пересоздать.
	ok, nh, used := d.tryRecreate(ctx, jobID, req, sc, names, set, th, log)
	if ok {
		if set.Provider != "awg3" && used.ID != "" && used.ID != set.Option && d.SaveOption != nil {
			d.SaveOption(req.RouterID, sc.TunnelID, set.Provider, used.ID)
		}
		d.finishOK(ctx, jobID, req, th, pol, sc, names, append(log, textRecreated(set.Provider, optionLabel(used))))
		return
	}
	if nh != nil {
		d.finishNeedHuman(ctx, jobID, req, th, names, log, nh.Action)
		return
	}
	if d.aborted(ctx, jobID, req, th, names, log) {
		return
	}
	d.finishNeedHuman(ctx, jobID, req, th, names, log, lastAction(set))
}

// lastAction -- что сказать человеку, когда не помогла ни одна ступень. Свой
// сервер -- проверить его: локации у него нет. Смена локации уже разрешена
// (и не помогла) -- советовать её разрешить незачем.
func lastAction(set Setting) string {
	switch {
	case set.Provider == "awg3":
		if panel, _, _ := strings.Cut(set.Option, "/"); strings.TrimSpace(panel) != "" {
			return ActVPSPanel(strings.TrimSpace(panel))
		}
		return ActServerDead
	case set.AllowRelocate:
		return ActRelocateNoHelp
	default:
		return ActServerDead
	}
}

// tryRestart -- ступень 1: перезапуск и доказательство.
func (d Deps) tryRestart(ctx context.Context, jobID string, req StartReq, sc Scenario, names lineNames) bool {
	d.step(jobID, StepRestart, provision.StepActive, "перезапускаю VPN-туннель «"+names.broken+"»")
	if _, err := d.command(ctx, req.RouterID, "tunnel_restart", map[string]any{"tunnel_id": sc.TunnelID}); err != nil {
		d.step(jobID, StepRestart, provision.StepFailed, "перезапуск не прошёл: "+err.Error())
		return false
	}
	verdict, err := d.prove(ctx, req, sc.TunnelID, names.broken)
	if err != nil {
		d.step(jobID, StepRestart, provision.StepFailed, "перезапуск не помог: "+err.Error())
		return false
	}
	d.step(jobID, StepRestart, provision.StepDone, "перезапустил, "+verdict)
	return true
}

// MinAgentExitProbe -- агент, который меряет адрес выхода по конкретному
// VPN-туннелю (exit_ip_probe tunnel_id, v0.47). Старше -- мерить нечем:
// check_via_tunnel идёт через активное звено набора правил, а после увода
// это резерв. Тогда доказательство -- только свежий обмен ключами.
const MinAgentExitProbe = "v0.47.0"

// prove -- доказательство ступени: свежий обмен ключами и выход через
// ИМЕННО этот VPN-туннель, отличный от прямого.
func (d Deps) prove(ctx context.Context, req StartReq, tunnelID, name string) (string, error) {
	if err := d.Probe.WaitHandshake(ctx, req.RouterID, tunnelID, name); err != nil {
		if ctx.Err() != nil {
			return "", errStopped
		}
		return "", err
	}
	// Пустая версия даёт false, как и в пороге мастера замены: агент, не
	// назвавший версию, до ступени перезапуска допущен и меряется наравне.
	if upstream.SoftwareNewerThan(req.AgentVersion, MinAgentExitProbe) {
		return "ключами обменялся (адрес выхода этот агент мерить не умеет)", nil
	}
	verdict, err := d.Probe.VerifyTunnelExit(ctx, req.RouterID, tunnelID)
	if err != nil {
		if ctx.Err() != nil {
			return "", errStopped
		}
		return "", err
	}
	return verdict, nil
}

// errStopped -- бэкенд останавливается: ступень не провалена, её прервали.
var errStopped = errors.New("починка прервана: сервер приложения перезапускается")

// tryIssue -- ступени 2 и 3: выпустить конфиг, проверить, положить в ТОТ ЖЕ
// VPN-туннель (target_id), доказать. nh != nil -- источник ждёт человека.
func (d Deps) tryIssue(ctx context.Context, jobID, step string, req StartReq, sc Scenario, names lineNames, issue func() (replace.Issued, error)) (bool, *NeedHuman) {
	d.step(jobID, step, provision.StepActive, "выпускаю конфиг")
	issued, err := issue()
	if err != nil {
		var nh *NeedHuman
		if errors.As(err, &nh) {
			d.logWarn("linkrepair: источник ждёт человека", "step", step, "err", nh.Cause)
			d.step(jobID, step, provision.StepFailed, "источник не выдал конфиг — "+nh.Action)
			return false, nh
		}
		// Сырые ошибки источника (кабинет, база) -- оператору в лог.
		d.logWarn("linkrepair: источник не выдал конфиг", "step", step, "err", err)
		d.step(jobID, step, provision.StepFailed, "источник не выдал конфиг")
		return false, nil
	}
	if len(issued.Conf) == 0 {
		d.step(jobID, step, provision.StepFailed, "источник вернул пустой конфиг")
		return false, nil
	}
	if detail, _, err := d.Probe.AnalyzeConf(ctx, req.RouterID, issued.Conf); err != nil {
		d.step(jobID, step, provision.StepFailed, detail)
		return false, nil
	}
	if ctx.Err() != nil {
		d.step(jobID, step, provision.StepFailed, errStopped.Error())
		return false, nil
	}
	backend := issued.Backend
	if backend == "" {
		backend = "nativewg"
	}
	d.step(jobID, step, provision.StepActive, "кладу конфиг в VPN-туннель «"+names.broken+"»")
	if _, err := d.command(ctx, req.RouterID, "tunnel_import", map[string]any{
		"conf":      base64.StdEncoding.EncodeToString(issued.Conf),
		"name":      issued.TunnelName,
		"replace":   true,
		"backend":   backend,
		"target_id": sc.TunnelID,
	}); err != nil {
		d.step(jobID, step, provision.StepFailed, "заменить конфиг на роутере не вышло: "+err.Error())
		return false, nil
	}
	verdict, err := d.prove(ctx, req, sc.TunnelID, names.broken)
	if err != nil {
		d.step(jobID, step, provision.StepFailed, "конфиг заменён, но "+err.Error())
		return false, nil
	}
	d.step(jobID, step, provision.StepDone, "конфиг заменён, "+verdict)
	return true, nil
}

// checkIssued -- страна настройки «Amnezia Premium» всё ещё выпущена. Не
// выпущена (отозвали) или список не получен -- ступень 2 не выпускает: без
// проверки «тот же конфиг» мог бы занять новое место в подписке.
func (d Deps) checkIssued(ctx context.Context, jobID string, req StartReq, set Setting) *NeedHuman {
	opts, err := d.Source.Options(ctx, req.RouterID, set.Provider)
	if ctx.Err() != nil {
		// Остановка, а не отказ кабинета: «обновите ключ» здесь неправда.
		d.step(jobID, StepReissue, provision.StepFailed, errStopped.Error())
		return &NeedHuman{Cause: errStopped, Action: ActAborted}
	}
	if err != nil {
		var nh *NeedHuman
		if errors.As(err, &nh) {
			d.step(jobID, StepReissue, provision.StepFailed, "кабинет не дал список стран — "+nh.Action)
			return nh
		}
		d.logWarn("linkrepair: список стран не получен", "err", err)
		d.step(jobID, StepReissue, provision.StepFailed, "кабинет не дал список стран — выпускать вслепую не стал")
		return &NeedHuman{Cause: err, Action: ActAmneziaKey}
	}
	label := set.Option
	for _, o := range opts {
		if o.ID != set.Option {
			continue
		}
		label = optionLabel(o)
		if o.Issued {
			return nil
		}
	}
	d.step(jobID, StepReissue, provision.StepFailed, "страна «"+label+"» больше не выпущена в кабинете — новое место в подписке не беру")
	return &NeedHuman{Cause: errors.New("страна не выпущена"), Action: ActCountryRevoked(label)}
}

// relocateNewOnce -- у кабинета выпущенная страна -- это ключ, который уже
// стоит на другом устройстве или в другом VPN-туннеле («Amnezia Premium»):
// поставить его сюда -- сломать оба места. Смена локации берёт только ещё
// не выпущенную страну, а она -- место в подписке, поэтому одна на настройку
// навсегда. У «HideMy.name» код открывает все серверы разом.
func relocateNewOnce(provider string) bool { return provider == "amnezia" }

// tryRecreate -- ступень 3. Свой сервер: новый пир. Кабинет: другая
// локация -- только если человек разрешил её менять. used -- вариант,
// который помог.
func (d Deps) tryRecreate(ctx context.Context, jobID string, req StartReq, sc Scenario, names lineNames, set Setting, th Thread, log []string) (ok bool, nh *NeedHuman, used Option) {
	if set.Provider == "awg3" {
		th.Progress(ctx, progressText(names, log, "завожу новое подключение на своём сервере"))
		ok, nh := d.tryIssue(ctx, jobID, StepRecreate, req, sc, names, func() (replace.Issued, error) {
			return d.Source.Fresh(ctx, req.RouterID, set.Provider, set.Option)
		})
		return ok, nh, Option{ID: set.Option}
	}
	if !set.AllowRelocate {
		d.skip(jobID, "смена локации не разрешена", StepRecreate)
		return false, nil, Option{}
	}
	opts, err := d.Source.Options(ctx, req.RouterID, set.Provider)
	if err != nil {
		if errors.As(err, &nh) {
			d.step(jobID, StepRecreate, provision.StepFailed, "кабинет не дал список локаций — "+nh.Action)
			return false, nh, Option{}
		}
		d.logWarn("linkrepair: варианты кабинета не получены", "err", err)
		d.step(jobID, StepRecreate, provision.StepFailed, "кабинет не дал список локаций")
		return false, nil, Option{}
	}
	if relocateNewOnce(set.Provider) {
		return d.tryNewCountry(ctx, jobID, req, sc, names, set, th, log, opts)
	}
	var cands []Option
	for _, o := range opts {
		if o.ID == "" || o.ID == set.Option {
			continue
		}
		cands = append(cands, o)
	}
	if len(cands) == 0 {
		d.step(jobID, StepRecreate, provision.StepFailed, "другой локации у кабинета нет")
		return false, nil, Option{}
	}
	// Отказ кабинета по одной локации -- не повод бросать остальные: человек
	// нужен, только если не помогла ни одна. Тогда -- действие последнего
	// отказа.
	var lastNH *NeedHuman
	for i, opt := range cands {
		if i == maxRelocations || ctx.Err() != nil {
			break
		}
		th.Progress(ctx, progressText(names, log, "пробую локацию «"+optionLabel(opt)+"»"))
		ok, nh := d.tryIssue(ctx, jobID, StepRecreate, req, sc, names, func() (replace.Issued, error) {
			return d.Source.Issue(ctx, req.RouterID, set.Provider, opt.ID)
		})
		if ok {
			return true, nil, opt
		}
		if nh != nil {
			lastNH = nh
		}
	}
	return false, lastNH, Option{}
}

// tryNewCountry -- смена локации у «Amnezia Premium»: одна ещё не выпущенная
// страна, один раз на настройку. Отметка пишется до выпуска; не записалась --
// страна не выпускается (иначе следующая починка выпустила бы ещё одну).
func (d Deps) tryNewCountry(ctx context.Context, jobID string, req StartReq, sc Scenario, names lineNames, set Setting, th Thread, log []string, opts []Option) (bool, *NeedHuman, Option) {
	if spent := strings.TrimSpace(set.RelocateSpent); spent != "" {
		label := spent
		for _, o := range opts {
			if o.ID == spent {
				label = optionLabel(o)
			}
		}
		d.step(jobID, StepRecreate, provision.StepFailed,
			"новую страну («"+label+"») автопочинка уже выпускала — вторую не выпускаю: это ещё одно место в подписке")
		return false, &NeedHuman{Cause: errors.New("новая страна уже выпускалась"), Action: ActRelocateSpent}, Option{}
	}
	var opt Option
	for _, o := range opts {
		if o.ID != "" && o.ID != set.Option && !o.Issued {
			opt = o
			break
		}
	}
	if opt.ID == "" {
		d.step(jobID, StepRecreate, provision.StepFailed,
			"все страны уже выпущены — их ключи стоят в других местах, а один ключ в двух местах ломает оба")
		return false, &NeedHuman{Cause: errors.New("невыпущенной страны нет"), Action: ActNoNewCountry}, Option{}
	}
	notIssued := &NeedHuman{Cause: errors.New("отметка о новой стране не записалась"), Action: ActNewCountryNotIssued}
	if d.SpendRelocation == nil {
		d.step(jobID, StepRecreate, provision.StepFailed, "отметку о новой стране записать некуда — новую страну не выпускаю")
		return false, notIssued, Option{}
	}
	if err := d.SpendRelocation(req.RouterID, sc.TunnelID, opt.ID); err != nil {
		d.logWarn("linkrepair: отметка о новой стране не записалась", "err", err)
		d.step(jobID, StepRecreate, provision.StepFailed, "отметка о новой стране не записалась — новую страну не выпускаю")
		return false, notIssued, Option{}
	}
	th.Progress(ctx, progressText(names, log, "выпускаю новую страну «"+optionLabel(opt)+"»"))
	ok, nh := d.tryIssue(ctx, jobID, StepRecreate, req, sc, names, func() (replace.Issued, error) {
		return d.Source.Issue(ctx, req.RouterID, set.Provider, opt.ID)
	})
	if ok {
		return true, nil, opt
	}
	if nh == nil && ctx.Err() == nil {
		// Пробовалась одна страна: «другие локации тоже не помогли» -- неправда.
		nh = &NeedHuman{Cause: errors.New("новая страна не помогла"), Action: ActNewCountryNoHelp(optionLabel(opt))}
	}
	return false, nh, Option{}
}

// optionLabel -- как вариант кабинета называется для человека.
func optionLabel(o Option) string { return orID(o.Label, o.ID) }

// aborted -- бэкенд останавливается: дальше ступени не идут, трафик остаётся
// там, где он сейчас. Это не вердикт автопочинке, поэтому стоп не ставится.
func (d Deps) aborted(ctx context.Context, jobID string, req StartReq, th Thread, names lineNames, log []string) bool {
	if ctx.Err() == nil {
		return false
	}
	d.skip(jobID, errStopped.Error(), StepRestart, StepReissue, StepRecreate)
	d.skipFailback(jobID, names)
	d.Store.Update(jobID, func(j *provision.Job) {
		// Подсказка -- «что делать»: без неё экран починки показал бы голое
		// «Не получилось». Сделать можно одно -- запустить снова.
		j.State = provision.StateFailed
		j.Hint = ActAborted
	})
	th.NeedHuman(context.WithoutCancel(ctx), failText(names, log), "")
	return true
}

// finishOK -- ступень доказана: вернуть трафик (если уводили), снять стоп,
// сказать людям.
func (d Deps) finishOK(ctx context.Context, jobID string, req StartReq, th Thread, pol wire.RoutePolicySummary, sc Scenario, names lineNames, log []string) {
	if names.backup != "" {
		d.step(jobID, StepFailback, provision.StepActive, "возвращаю трафик на VPN-туннель «"+names.broken+"»")
		if _, err := d.command(ctx, req.RouterID, "route_policy_promote", map[string]any{
			"policy_name": pol.Name,
			"tunnel_id":   sc.TunnelID,
		}); err != nil {
			// VPN-туннель починен, трафик на рабочем запасном -- это не
			// провал починки, но человеку сказать надо.
			d.step(jobID, StepFailback, provision.StepFailed, "вернуть трафик не вышло, он идёт через запасной VPN-туннель «"+names.backup+"»: "+err.Error())
			names.failbackFailed = true
		} else {
			d.step(jobID, StepFailback, provision.StepDone, "трафик снова идёт через VPN-туннель «"+names.broken+"»")
		}
	} else if names.reserve {
		d.step(jobID, StepFailback, provision.StepDone, "возвращать нечего — "+textCarrier(names.carrier))
	} else {
		d.step(jobID, StepFailback, provision.StepDone, "возвращать нечего — запасного VPN-туннеля не было")
	}
	if req.Auto {
		_ = d.Attempts.Record(req.Nickname, req.CheckName, true)
	} else {
		_ = d.Attempts.Clear(req.Nickname, req.CheckName)
	}
	d.Store.Update(jobID, func(j *provision.Job) { j.State = provision.StateSuccess })
	th.Done(context.WithoutCancel(ctx), doneText(names, log))
}

// finishNeedHuman -- лесенка кончилась без доказанной ступени: трафик
// остаётся на резерве (если он был), VPN-туннель -- как есть. Ничего не
// откатывается «назад на сломанный» (D2).
func (d Deps) finishNeedHuman(ctx context.Context, jobID string, req StartReq, th Thread, names lineNames, log []string, action string) {
	d.skipFailback(jobID, names)
	if req.Auto {
		_ = d.Attempts.Record(req.Nickname, req.CheckName, false)
	}
	text := failText(names, log)
	d.Store.Update(jobID, func(j *provision.Job) {
		// Подсказка -- «что делать» на экране починки: только действие.
		j.State = provision.StateFailed
		j.Hint = action
	})
	th.NeedHuman(context.WithoutCancel(ctx), text, action)
}

// finishFail -- провал до лесенки (снимок, увод): шаг step провален с
// причиной, остальные ступени не начинались.
func (d Deps) finishFail(ctx context.Context, jobID string, req StartReq, th Thread, step string, names lineNames, cause error, action string) {
	d.step(jobID, step, provision.StepFailed, cause.Error())
	d.skip(jobID, "не начинали", StepRestart, StepReissue, StepRecreate)
	d.skipFailback(jobID, names)
	if req.Auto {
		// Роутер молчит -- это не вердикт автопочинке: попытка считается,
		// но стоп не ставится, следующая тревога снова может её запустить.
		_ = d.Attempts.Record(req.Nickname, req.CheckName, names.noSnapshot)
	}
	text := failText(names, nil)
	d.Store.Update(jobID, func(j *provision.Job) {
		// Подсказка -- только действие; причина -- в шаге step.
		j.State = provision.StateFailed
		j.Hint = action
	})
	th.NeedHuman(context.WithoutCancel(ctx), text, action)
}

func (d Deps) skipFailback(jobID string, names lineNames) {
	if names.reserve {
		d.skip(jobID, "возвращать нечего — "+textCarrier(names.carrier), StepFailback)
		return
	}
	if names.backup != "" {
		d.skip(jobID, "трафик остаётся на запасном VPN-туннеле «"+names.backup+"»", StepFailback)
		return
	}
	d.skip(jobID, "возвращать нечего", StepFailback)
}

// skip помечает пропущенными шаги, которые ещё не начинались.
func (d Deps) skip(jobID, detail string, steps ...string) {
	d.Store.Update(jobID, func(j *provision.Job) {
		for i := range j.Steps {
			for _, name := range steps {
				if j.Steps[i].Name == name && j.Steps[i].Status == provision.StepPending {
					j.Steps[i].Status = provision.StepSkipped
					j.Steps[i].Detail = detail
				}
			}
		}
	})
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
		return nil, errNoAnswer
	}
	if res.Status != "ok" {
		d.logCommandFailure(action, res.Status, res.Output)
		return nil, errors.New(replace.RouterFailure(true, res.Status))
	}
	return res, nil
}

// errNoAnswer -- роутер не ответил на команду: выполнил ли он её, неизвестно.
var errNoAnswer = errors.New(replace.RouterFailure(false, ""))

// logCommandFailure -- то, что владельцу не показывают (какая команда, с
// каким статусом, что ответил агент), остаётся оператору в логе. Причина
// провала уходит в личку, и сырому ответу агента там не место.
func (d Deps) logCommandFailure(action, status, output string) {
	d.logWarn("linkrepair command failed", "action", action, "status", status, "output", strings.TrimSpace(output))
}

func (d Deps) logWarn(msg string, args ...any) {
	if d.Logger != nil {
		d.Logger.Warn(msg, args...)
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

// findPolicy ищет политику, в цепочке которой стоит упавшая линия, и
// подбирает ей замену. Снимок спрашивается у агента: бэкенд состав политик
// не хранит.
//
// name -- имя упавшего VPN-туннеля из того же снимка. Оно нужно и тогда, когда
// набора не нашлось: причина уходит владельцу в личку, и идентификатор там
// читать некому. Не пришёл снимок -- имени взять неоткуда, name пустое.
func (d Deps) findPolicy(ctx context.Context, routerID int64, tunnelID string) (pol wire.RoutePolicySummary, backup, carrier string, reserve bool, name string, err error) {
	res, err := d.command(ctx, routerID, "route_status", map[string]any{})
	if err != nil {
		return pol, "", "", false, "", fmt.Errorf("не узнали у роутера, в каком общем наборе правил этот VPN-туннель: %w", err)
	}
	var snap wire.RouteSnapshot
	if err := json.Unmarshal([]byte(res.Output), &snap); err != nil {
		d.logCommandFailure("route_status", "unparsable", err.Error())
		return pol, "", "", false, "", errors.New("не узнали у роутера, в каком общем наборе правил этот VPN-туннель: " + replace.RouterGarbled)
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
			if c, res := carrierOf(p, tunnelID); res {
				return p, "", c, true, name, nil
			}
			backup, _ := pickBackup(p, tunnelID)
			return p, backup, "", false, name, nil
		}
	}
	// Причина уходит владельцу в личку: ни идентификатора, ни «политики».
	return pol, "", "", false, name, ErrNotInAnySet
}
