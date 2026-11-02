// Package e2e -- сквозной контур: настоящие агент и бэкенд в одном процессе,
// поверх настоящего HTTP. Подделки только на краю системы: awg-manager и
// Telegram (HTTP-серверы) и вызовы ОС (ndmc, привязка сокета к интерфейсу).
// Юниты с обеих сторон стыка зелёные и при дыре на самом стыке (разбор 02.11):
// подмена стыка уничтожила бы смысл контура.
package e2e

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Jkaotlic/wg-monitor/internal/agent"
	"github.com/Jkaotlic/wg-monitor/internal/agent/actions"
	"github.com/Jkaotlic/wg-monitor/internal/agent/awgmgr"
	"github.com/Jkaotlic/wg-monitor/internal/agent/checks"
	"github.com/Jkaotlic/wg-monitor/internal/agent/cmdloop"
	"github.com/Jkaotlic/wg-monitor/internal/agent/exitprobe"
	"github.com/Jkaotlic/wg-monitor/internal/agent/facts"
	"github.com/Jkaotlic/wg-monitor/internal/agent/pingruns"
	"github.com/Jkaotlic/wg-monitor/internal/agent/wanfacts"
	"github.com/Jkaotlic/wg-monitor/internal/backend"
	"github.com/Jkaotlic/wg-monitor/internal/backend/alerts"
	"github.com/Jkaotlic/wg-monitor/internal/backend/awg3panel"
	"github.com/Jkaotlic/wg-monitor/internal/backend/db"
	"github.com/Jkaotlic/wg-monitor/internal/backend/provision"
	"github.com/Jkaotlic/wg-monitor/internal/backend/tg"
	"github.com/Jkaotlic/wg-monitor/internal/backend/wiring"
	"github.com/Jkaotlic/wg-monitor/pkg/wire"
)

const (
	e2eAdminTG   = int64(424242)
	e2eDashToken = "dash-token-for-e2e-0123456789abcdef"
	e2eRouter    = "router-e2e"
	e2eToken     = "3131313131313131313131313131313131313131313131313131313131313131"
	// e2eAgentVersion -- свежий агент: лесенка и замер выхода ему доступны.
	e2eAgentVersion = "v0.64.0"
	// e2eSettleTimeout -- потолок ожидания условия. Условия выполняются за
	// миллисекунды; срок нужен только, чтобы зависание было падением, а не
	// вечным тестом.
	e2eSettleTimeout = 10 * time.Second
)

// Форма живого роутера: политика из nativewg-первого звена и двух
// opkg-резервов, у всех ping-check выключен, весь прочий трафик -- напрямую.
const (
	carrierID = "awg50"
	backup1ID = "awg10"
	backup2ID = "awg11"
	policyHR  = "HydraRoute"
	freshAge  = 17 * time.Second
	// idleAge -- резерв простаивает: WireGuard не делает рукопожатий без трафика.
	idleAge = 40 * time.Minute
)

func standardTunnels() []fakeTunnel {
	return []fakeTunnel{
		{ID: carrierID, Name: "vpn-main", Backend: "nativewg", NDMS: "Wireguard2", PolicyIface: "Wireguard2", Iface: "nwg2",
			Status: "running", Enabled: true, HandshakeAge: freshAge, PingStatus: "disabled"},
		{ID: backup1ID, Name: "vpn-backup-one", Backend: "kernel", PolicyIface: "OpkgTun10", Iface: "opkgtun10",
			Status: "running", Enabled: true, HandshakeAge: idleAge, PingStatus: "disabled"},
		{ID: backup2ID, Name: "vpn-backup-two", Backend: "kernel", PolicyIface: "OpkgTun11", Iface: "opkgtun11",
			Status: "running", Enabled: true, HandshakeAge: idleAge, PingStatus: "disabled"},
	}
}

// rigOpts -- настройки контура. Нулевое значение -- стандартная форма роутера.
type rigOpts struct {
	// AgentVersion -- версия, которую называет агент; пусто -- e2eAgentVersion.
	AgentVersion string
	// BareRouter -- не заводить туннели и политику: тест настроит сам.
	BareRouter bool
	// AwaitStep -- сколько движок ждёт ответа агента; 0 -- боевое умолчание.
	// Для сценария «роутер не ответил»: ждать боевые минуты незачем.
	AwaitStep time.Duration
}

// exchange считает обмены агента с бэкендом на самом HTTP-стыке: отвечает на
// вопрос «отчёт принят», не подменяя ни клиента, ни мукс.
type exchange struct {
	reports    atomic.Int64
	lastReport atomic.Int64 // код ответа на последний отчёт
	dispatched atomic.Int64 // /v1/cmd с командой
	results    atomic.Int64 // /v1/cmd/result с ответом 2xx
}

type statusRecorder struct {
	http.ResponseWriter
	code int
}

func (s *statusRecorder) WriteHeader(code int) {
	s.code = code
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusRecorder) Flush() {
	if f, ok := s.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// rig -- собранный контур на один роутер.
type rig struct {
	t    *testing.T
	dir  string
	cfg  *backend.Config
	awgm *fakeAWGM
	tg   *fakeTG

	// Backend. handler подменяется при перезапуске бэкенда: адрес у агента
	// один, как у настоящего за KeenDNS.
	backendURL string
	backendSrv *httptest.Server
	handler    atomic.Pointer[http.Handler]
	exch       exchange
	db         *db.DB
	core       *wiring.Core
	store      *provision.Store
	stopCtx    context.CancelFunc
	routerID   int64

	// Agent.
	client     *agent.Client
	rep        *agent.Reporter
	agentCtx   context.Context
	stopAgent  context.CancelFunc
	loopDone   chan struct{}
	agentAlive bool

	awaitStep time.Duration

	cookieMu sync.Mutex
	cookie   *http.Cookie
}

// newRig поднимает Telegram, awg-manager, бэкенд из настоящей связки и агента.
func newRig(t *testing.T, opts rigOpts) *rig {
	t.Helper()
	r := &rig{t: t, dir: t.TempDir(), tg: newFakeTG(t), awgm: newFakeAWGM(t), awaitStep: opts.AwaitStep}
	r.backendSrv = httptest.NewServer(http.HandlerFunc(r.serveBackend))
	r.backendURL = r.backendSrv.URL
	r.cfg = r.loadBackendConfig()
	if !opts.BareRouter {
		for _, tu := range standardTunnels() {
			r.awgm.SetTunnel(tu)
			r.awgm.SetMatrix(tu.ID, true)
		}
		r.awgm.SetPolicy(policyHR, "Wireguard2", "OpkgTun10", "OpkgTun11")
	}
	r.startBackend()
	uid, err := r.db.Users().Insert(e2eRouter, e2eToken, "198.51.100.5", "nwg2")
	if err != nil {
		t.Fatalf("роутер в базе: %v", err)
	}
	r.routerID = uid
	r.startAgent(opts.AgentVersion)
	t.Cleanup(r.shutdown)
	return r
}

// loadBackendConfig читает YAML настоящим LoadConfig: пороги автомата и прочие
// умолчания -- боевые, а не придуманные тестом. Единственная правка -- лимит
// частоты отчётов: контур сжимает минуты в миллисекунды.
func (r *rig) loadBackendConfig() *backend.Config {
	r.t.Helper()
	write := func(name, body string) string {
		p := filepath.Join(r.dir, name)
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			r.t.Fatal(err)
		}
		return p
	}
	yaml := fmt.Sprintf(`listen: 127.0.0.1:0
db_path: %s
public_base_url: https://backend.example.com
telegram:
  bot_token_file: %s
  admin_user_id: %d
dashboard:
  enabled: true
  token_file: %s
amnezia_selfhosted:
  store_path: %s
`, filepath.Join(r.dir, "backend.db"), write("bot.token", "123456:e2e-bot-token"), e2eAdminTG,
		write("dash.token", e2eDashToken), filepath.Join(r.dir, "selfhosted.json"))
	cfg, err := backend.LoadConfig(write("backend.yaml", yaml))
	if err != nil {
		r.t.Fatalf("конфиг бэкенда: %v", err)
	}
	cfg.RateLimit.ReportPerSec = 10000
	cfg.RateLimit.ReportBurst = 10000
	return cfg
}

func quietLogger() *slog.Logger {
	if os.Getenv("E2E_VERBOSE") != "" {
		return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))
	}
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// startBackend собирает бэкенд боевой связкой (wiring.NewCore) на файле базы.
// Повторный вызов -- перезапуск: память процесса пропадает, база остаётся.
func (r *rig) startBackend() {
	r.t.Helper()
	d, err := db.Open(r.cfg.DBPath)
	if err != nil {
		r.t.Fatalf("база: %v", err)
	}
	r.db = d
	ctx, cancel := context.WithCancel(context.Background())
	r.stopCtx = cancel
	logger := quietLogger()
	r.store = provision.NewPersistentStore(filepath.Join(filepath.Dir(r.cfg.DBPath), "provision-jobs.json"))
	r.core = wiring.NewCore(wiring.Inputs{
		Ctx: ctx, Cfg: r.cfg, DB: d,
		TG: &tg.Client{BaseURL: r.tg.srv.URL + "/bot", Token: r.cfg.Telegram.BotToken,
			HTTP: &http.Client{Timeout: 5 * time.Second}, LongPollHTTP: &http.Client{Timeout: 5 * time.Second}, Logger: logger},
		Logger: logger,
		Provision: provision.Deps{Store: r.store, BaseCtx: ctx, LastSeen: func(string) (time.Time, bool) { return time.Time{}, false },
			Now: time.Now, Logger: logger},
		Awg3Panels: awg3panel.NewService(filepath.Join(r.dir, "awg3-panels.json"), awg3panel.Options{Logger: logger}),
	})
	// Сжатие времени, а не подмена логики: между опросами обмена ключами живой
	// движок ждёт 15 с. StartLinkRepair -- значение метода, копия движка, поэтому
	// привязывается заново.
	r.core.LinkRepair.Probe.HandshakeWait = 5 * time.Millisecond
	r.core.LinkRepair.AwaitStep = r.awaitStep
	r.core.Mux.StartLinkRepair = r.core.LinkRepair.Start
	h := backend.NewMux(r.core.Mux)
	r.handler.Store(&h)
	r.login()
}

// serveBackend -- единая точка входа на все перезапуски; считает обмены.
func (r *rig) serveBackend(w http.ResponseWriter, req *http.Request) {
	rec := &statusRecorder{ResponseWriter: w, code: http.StatusOK}
	(*r.handler.Load()).ServeHTTP(rec, req)
	switch {
	case req.Method == http.MethodPost && req.URL.Path == "/v1/report":
		r.exch.lastReport.Store(int64(rec.code))
		r.exch.reports.Add(1)
	case req.Method == http.MethodGet && req.URL.Path == "/v1/cmd" && rec.code == http.StatusOK:
		r.exch.dispatched.Add(1)
	case req.Method == http.MethodPost && req.URL.Path == "/v1/cmd/result" && rec.code/100 == 2:
		r.exch.results.Add(1)
	}
}

// restartBackend -- новый экземпляр бэкенда на той же базе: очередь команд и
// память тревог пропали, запись в базе и настройки остались. Агент не трогается.
func (r *rig) restartBackend() {
	r.t.Helper()
	// Как настоящий SIGTERM: сперва отмена, движок не дожидается ответов агента.
	r.stopCtx()
	r.waitEngineIdle()
	r.backendSrv.CloseClientConnections()
	backend.WaitRelayDrain()
	if err := r.db.Close(); err != nil {
		r.t.Fatalf("закрытие базы: %v", err)
	}
	r.startBackend()
}

// startAgent собирает агента так же, как cmd/agent: настоящие проверки,
// репортёр, цикл команд и исполнитель. Вне контура -- то, что требует ОС или
// живёт по времени: см. список подмен в отчёте по циклу (DNS-проверки читают
// ndmc, хук KeenOS и сторож зависаний -- своими петлями).
func (r *rig) startAgent(version string) {
	r.t.Helper()
	if version == "" {
		version = e2eAgentVersion
	}
	interval := 60 * time.Second
	awg := awgmgr.New(r.awgm.srv.URL)
	r.client = agent.NewClient(r.backendURL, e2eToken, version, 10*time.Second)
	tracker := &pingruns.Tracker{Src: awg}
	prober := &exitprobe.Prober{Client: awg,
		// Запасной замер привязывает сокет к интерфейсу -- край ОС. Боевой путь
		// замера идёт через awg-manager (/api/test/ip), его контур и проверяет.
		Own: func(context.Context, string) (string, string, error) {
			return "", "", errors.New("e2e: привязки к интерфейсу в контуре нет")
		}}
	wan := &wanfacts.Collector{Client: awg}
	r.rep = agent.NewReporter(agent.ReporterConfig{
		Sender:   r.client,
		Version:  version,
		Interval: interval,
		Checks: []checks.Check{
			checks.AwgManagerCheck{Client: awg},
			checks.HydraRouteCheck{Client: awg, Policies: func(ctx context.Context, dns []awgmgr.DNSRoute) ([]wire.PolicyBrief, error) {
				return actions.PolicyBriefs(ctx, awg, dns)
			}},
		},
		MultiChecks: []checks.MultiCheck{
			checks.TunnelsCheck{
				Client:          awg,
				HandshakeMaxAge: agent.AWGCheckConfig{}.HandshakeMaxAge(),
				AwgmDownSince:   tracker.DownSince,
				Grace:           &checks.RunGrace{Window: 2 * interval},
			},
		},
		Deps:      checks.Deps{Runner: refusingRunner{}},
		AwgClient: awg,
		StatePath: filepath.Join(r.dir, "reporter-state.json"),
		Facts:     &facts.Hub{Exit: prober.Snapshot, Ping: tracker, WAN: wan.Fact},
	})
	runner := &actions.Runner{
		AwgClient:    awg,
		ForceRecheck: r.rep.ForceReport,
		ReportNow:    r.rep.SendNow,
		ExitProbeNow: prober.ProbeNow,
		Exec: func(context.Context, string, ...string) ([]byte, error) {
			return nil, errors.New("e2e: ndmc в контуре нет")
		},
		BackendURL: r.backendURL,
		Version:    version,
	}
	loop := cmdloop.New(r.client, runner, 1)
	// Паузы цикла сжаты: перезапуск бэкенда рвёт длинный опрос, а ждать
	// секунды незачем.
	loop.BackoffBase, loop.BackoffMax = 10*time.Millisecond, 100*time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	r.agentCtx, r.stopAgent = ctx, cancel
	r.loopDone = make(chan struct{})
	r.agentAlive = true
	go func(done chan struct{}) {
		defer close(done)
		loop.Run(ctx)
	}(r.loopDone)
}

// refusingRunner -- запуск команд ОС: в контуре их нет.
type refusingRunner struct{}

func (refusingRunner) Run(context.Context, string, ...string) (string, error) {
	return "", errors.New("e2e: команд ОС в контуре нет")
}

// killAgent -- агент замолчал: ни отчётов, ни опроса команд.
func (r *rig) killAgent() {
	r.t.Helper()
	if !r.agentAlive {
		return
	}
	r.agentAlive = false
	r.stopAgent()
	<-r.loopDone
}

// reviveAgent -- агент вернулся (новый процесс, состояние репортёра на диске).
func (r *rig) reviveAgent(version string) {
	r.t.Helper()
	if r.agentAlive {
		r.t.Fatal("агент и так работает")
	}
	r.startAgent(version)
}

func (r *rig) shutdown() {
	if os.Getenv("E2E_VERBOSE") != "" {
		for _, line := range r.tg.Log() {
			r.t.Logf("telegram: %s", line)
		}
		s := r.Screen()
		r.t.Logf("экран: %s\n%s", s.RouterJSON, s.EventsJSON)
	}
	r.killAgent()
	// Сперва отмена, потом ожидание: движок мог ждать ответа уже замолчавшего агента.
	r.stopCtx()
	r.waitEngineIdle()
	r.backendSrv.CloseClientConnections()
	backend.WaitRelayDrain()
	r.backendSrv.Close()
	_ = r.db.Close()
	if u := r.awgm.Unknown(); len(u) > 0 {
		r.t.Errorf("агент ходил в ручки, которых подделка не знает: %v", u)
	}
}

// waitFor ждёт условие. Срок -- только страховка от зависания.
func (r *rig) waitFor(what string, cond func() bool) {
	r.t.Helper()
	deadline := time.Now().Add(e2eSettleTimeout)
	for !cond() {
		if time.Now().After(deadline) {
			r.t.Fatalf("не дождались: %s", what)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// settle ждёт доставку тревог: отчёт отвечает агенту раньше, чем Telegram
// получил тревогу (FRESH-04). Очередь доставки ставится синхронно внутри
// обработчика отчёта, так что после ответа на отчёт ей можно верить.
func (r *rig) settle() {
	r.t.Helper()
	r.waitFor("доставка тревог", func() bool { return alerts.DeliveryQueueLen() == 0 })
}

// Tick -- один обход агента: настоящие проверки, отчёт, принятый бэкендом, и
// доставка всего, что из него вышло.
func (r *rig) Tick() {
	r.t.Helper()
	if !r.agentAlive {
		r.t.Fatal("Tick при замолчавшем агенте")
	}
	before := r.exch.reports.Load()
	r.rep.SendNow(r.agentCtx)
	if r.exch.reports.Load() == before {
		r.t.Fatal("отчёт не дошёл до бэкенда")
	}
	if code := r.exch.lastReport.Load(); code != http.StatusOK {
		r.t.Fatalf("бэкенд ответил на отчёт %d", code)
	}
	r.settle()
}

// Ticks -- n обходов подряд.
func (r *rig) Ticks(n int) {
	r.t.Helper()
	for range n {
		r.Tick()
	}
}

// PostReport отправляет отчёт настоящим HTTP-клиентом агента: для отчётов,
// которых нынешний агент не соберёт сам (старая версия).
func (r *rig) PostReport(rep wire.Report) {
	r.t.Helper()
	if _, err := r.client.SendReport(r.agentCtx, rep); err != nil {
		r.t.Fatalf("отчёт: %v", err)
	}
	r.settle()
}

func (r *rig) engineBusy() bool {
	if r.core.LinkRepair.Moves.Busy() {
		return true
	}
	if !r.store.TryLock(e2eRouter) {
		return true
	}
	r.store.Unlock(e2eRouter)
	return false
}

func (r *rig) waitEngineIdle() {
	r.t.Helper()
	r.waitFor("конец починки", func() bool { return !r.engineBusy() })
}

// DrainCommands ждёт, пока движок починки закончит, а агент ответит на всё,
// что ему выдано. Команды выполняет настоящий цикл агента в своей горутине.
func (r *rig) DrainCommands() {
	r.t.Helper()
	r.settle()
	r.waitFor("конец починки и ответы агента", func() bool {
		return !r.engineBusy() && alerts.DeliveryQueueLen() == 0 &&
			r.exch.dispatched.Load() == r.exch.results.Load()
	})
}

// Alerts -- текущие тексты сообщений, ушедших людям (правки применены).
func (r *rig) Alerts() []string { return r.tg.Texts() }

// WaitAlert ждёт сообщение с подстрокой: правка «не запускалась» уходит в
// своей горутине, её не видно в очереди доставки.
func (r *rig) WaitAlert(substr string) {
	r.t.Helper()
	r.waitFor("сообщение со словами «"+substr+"»", func() bool {
		for _, a := range r.Alerts() {
			if strings.Contains(a, substr) {
				return true
			}
		}
		return false
	})
}

func (r *rig) login() {
	r.t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/v1/dashboard/login", strings.NewReader(`{"token":"`+e2eDashToken+`"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	(*r.handler.Load()).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || len(rec.Result().Cookies()) == 0 {
		r.t.Fatalf("вход в веб-управление: код %d (%s)", rec.Code, rec.Body.String())
	}
	r.cookieMu.Lock()
	r.cookie = rec.Result().Cookies()[0]
	r.cookieMu.Unlock()
}

// api -- запрос к мини-аппу по настоящей куке входа, через настоящий сервер.
func (r *rig) api(method, path, body string) (int, []byte) {
	r.t.Helper()
	req, err := http.NewRequest(method, r.backendURL+path, strings.NewReader(body))
	if err != nil {
		r.t.Fatal(err)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	r.cookieMu.Lock()
	req.AddCookie(r.cookie)
	r.cookieMu.Unlock()
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		r.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		r.t.Fatal(err)
	}
	return resp.StatusCode, raw
}

// Screen -- то, что мини-апп получает для экрана роутера. RouterJSON/EventsJSON/
// FleetJSON -- ответы как есть, для прогона через настоящие функции экрана.
type Screen struct {
	Router struct {
		Status           string `json:"status"`
		Stale            bool   `json:"stale"`
		Reach            string `json:"reach"`
		ReserveOnlyAlert bool   `json:"reserve_only_alert"`
		AgentVersion     string `json:"agent_version"`
	} `json:"router"`
	Incidents []struct {
		CheckName string `json:"check_name"`
	} `json:"incidents"`
	Checks []struct {
		CheckName string `json:"check_name"`
		Status    string `json:"status"`
	} `json:"checks"`
	Tunnels []ScreenTunnel `json:"tunnels"`
	Traffic ScreenTraffic  `json:"traffic"`

	RouterJSON json.RawMessage `json:"-"`
	EventsJSON json.RawMessage `json:"-"`
	FleetJSON  json.RawMessage `json:"-"`
}

// ScreenTunnel -- проекция туннеля (internal/backend/miniapp_tunnels.go).
type ScreenTunnel struct {
	TunnelID string `json:"tunnel_id"`
	Name     string `json:"name"`
	Status   string `json:"status"`
	RunState string `json:"run_state"`
	Note     string `json:"note"`
}

// ScreenTraffic -- главный ответ экрана: куда идёт обход и жив ли он.
type ScreenTraffic struct {
	Mode             string   `json:"mode"`
	EgressTunnelID   string   `json:"egress_tunnel_id"`
	ReserveTunnelIDs []string `json:"reserve_tunnel_ids"`
	CarrierTunnelID  string   `json:"carrier_tunnel_id"`
	CarrierBasis     string   `json:"carrier_basis"`
	CarrierAlive     bool     `json:"carrier_alive"`
}

// Tunnel -- строка туннеля на экране; нет строки -- провал теста.
func (s Screen) Tunnel(t *testing.T, id string) ScreenTunnel {
	t.Helper()
	for _, tu := range s.Tunnels {
		if tu.TunnelID == id {
			return tu
		}
	}
	t.Fatalf("на экране нет туннеля %s: %+v", id, s.Tunnels)
	return ScreenTunnel{}
}

// Check -- статус проверки на экране; "" -- строки нет.
func (s Screen) Check(name string) string {
	for _, c := range s.Checks {
		if c.CheckName == name {
			return c.Status
		}
	}
	return ""
}

// Screen читает экран роутера настоящими запросами мини-аппа.
func (r *rig) Screen() Screen {
	r.t.Helper()
	var s Screen
	code, raw := r.api(http.MethodGet, fmt.Sprintf("/v1/miniapp/routers/%d", r.routerID), "")
	if code != http.StatusOK {
		r.t.Fatalf("экран роутера: код %d (%s)", code, raw)
	}
	if err := json.Unmarshal(raw, &s); err != nil {
		r.t.Fatalf("экран роутера: %v (%s)", err, raw)
	}
	s.RouterJSON = raw
	code, raw = r.api(http.MethodGet, fmt.Sprintf("/v1/miniapp/routers/%d/events", r.routerID), "")
	if code != http.StatusOK {
		r.t.Fatalf("события роутера: код %d (%s)", code, raw)
	}
	if err := json.Unmarshal(raw, &s); err != nil {
		r.t.Fatalf("события роутера: %v (%s)", err, raw)
	}
	s.EventsJSON = raw
	// reserve_only_alert приходит только в строке списка роутеров: экран берёт
	// его оттуда (RouterDetail, reserveOnlyAlert).
	code, raw = r.api(http.MethodGet, "/v1/miniapp/routers", "")
	if code != http.StatusOK {
		r.t.Fatalf("список роутеров: код %d (%s)", code, raw)
	}
	var fleet struct {
		Routers []struct {
			ID               int64 `json:"id"`
			ReserveOnlyAlert bool  `json:"reserve_only_alert"`
		} `json:"routers"`
	}
	if err := json.Unmarshal(raw, &fleet); err != nil {
		r.t.Fatalf("список роутеров: %v (%s)", err, raw)
	}
	for _, row := range fleet.Routers {
		if row.ID == r.routerID {
			s.Router.ReserveOnlyAlert = row.ReserveOnlyAlert
		}
	}
	s.FleetJSON = raw
	return s
}

// EnableAutorepair включает автопочинку туннеля настоящей ручкой мини-аппа, без
// источника конфигов: лесенка ограничится перезапуском. Туннель должен быть уже
// известен бэкенду -- нужен хотя бы один Tick до вызова.
func (r *rig) EnableAutorepair(tunnelID string) {
	r.t.Helper()
	code, raw := r.api(http.MethodPut, fmt.Sprintf("/v1/miniapp/routers/%d/tunnels/%s/autorepair", r.routerID, tunnelID), `{"enabled":true,"provider":""}`)
	if code != http.StatusOK {
		r.t.Fatalf("включение автопочинки: код %d (%s)", code, raw)
	}
}

// ScanHeartbeat -- один обход сторожа молчания при часах, сдвинутых на after.
func (r *rig) ScanHeartbeat(after time.Duration) {
	r.t.Helper()
	r.core.Watcher.SetNow(func() time.Time { return time.Now().Add(after) })
	r.core.Watcher.ScanForTest(context.Background())
	r.settle()
}

// AgeLastSeen сдвигает время последнего отчёта роутера назад: экран считает
// «давно не на связи» по настоящим часам, и подвинуть можно только запись.
func (r *rig) AgeLastSeen(d time.Duration) {
	r.t.Helper()
	if _, err := r.db.SQL().Exec(`UPDATE users SET last_seen_at = ? WHERE id = ?`, time.Now().UTC().Add(-d), r.routerID); err != nil {
		r.t.Fatal(err)
	}
}

// MoveRecord -- запись об уводе в настоящей базе ("" -- записи нет).
func (r *rig) MoveRecord(tunnelID string) string {
	r.t.Helper()
	raw, err := r.db.KV().Get("linkrepair:moved:" + e2eRouter + ":tunnel_" + tunnelID)
	if err != nil {
		r.t.Fatal(err)
	}
	return raw
}
