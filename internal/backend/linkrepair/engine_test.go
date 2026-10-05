package linkrepair

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Jkaotlic/wg-monitor/internal/backend/provision"
	"github.com/Jkaotlic/wg-monitor/internal/backend/replace"
	"github.com/Jkaotlic/wg-monitor/pkg/wire"
)

// Адреса выхода в тестах: напрямую -- 203.0.113.7; через живой VPN-туннель --
// 203.0.113.19. Выход через туннель, равный прямому, -- «трафик в обход не
// пошёл», ступень не доказана.
const (
	exitDirect = "Exit IP: 203.0.113.7"
	exitTunnel = "Exit IP: 203.0.113.19"
)

type sentCmd struct {
	Action string
	Args   map[string]any
}

// scriptCommander -- поддельный агент. Состояние упавшего VPN-туннеля awg12
// (свежий обмен ключами и выход через туннель) меняется крючками на
// N-й вызов команды: так сценарий говорит «перезапуск помог» или «помог
// только второй импорт».
type scriptCommander struct {
	mu     sync.Mutex
	sent   []sentCmd
	byID   map[string]string
	counts map[string]int

	hs  bool   // у awg12 свежий обмен ключами
	via string // ответ check_via_tunnel

	noBackup    bool                                       // в наборе только awg12
	reserve     bool                                       // awg12 -- резерв: первым в цепочке стоит живой awg10
	carrierDown bool                                       // вместе с reserve: первое звено awg10 тоже лежит
	snapshot    string                                     // ответ route_status вместо обычного
	extra       []wire.RoutePolicySummary                  // наборы правил сверх основного
	silent      map[string]bool                            // действия, на которые роутер молчит
	refuse      map[string]bool                            // действия, которым агент отказывает
	on          map[string]func(c *scriptCommander, n int) // крючок на n-й вызов действия (под замком)
}

func newScript() *scriptCommander {
	return &scriptCommander{via: exitDirect}
}

func (c *scriptCommander) Enqueue(_ int64, cmd wire.Command) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.byID == nil {
		c.byID = map[string]string{}
		c.counts = map[string]int{}
	}
	c.sent = append(c.sent, sentCmd{Action: cmd.Action, Args: cmd.Args})
	c.byID[cmd.ID] = cmd.Action
	c.counts[cmd.Action]++
	if hook := c.on[cmd.Action]; hook != nil {
		hook(c, c.counts[cmd.Action])
	}
	return nil
}

func (c *scriptCommander) AwaitResult(_ context.Context, _ int64, id string, _ time.Duration) (*wire.CommandResult, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	action := c.byID[id]
	if c.silent[action] {
		return nil, false
	}
	if c.refuse[action] {
		return &wire.CommandResult{ID: id, Status: "err", Output: "HTTP_500 something went wrong"}, true
	}
	out := ""
	switch action {
	case "route_status":
		out = c.snapshotLocked()
	case "tunnel_import":
		out = `✅ Туннель "Дача" заменён (id=awg12)`
	case "check_via_tunnel":
		out = c.via
	case "exit_ip_probe":
		// Замер по tunnel_id: выход сменился, только когда awg12 починен.
		if c.via == exitTunnel {
			out = `{"vpn_ip":"203.0.113.19","direct_ip":"203.0.113.7","changed":true,"source":"awgm"}`
		} else {
			out = `{"vpn_ip":"203.0.113.7","direct_ip":"203.0.113.7","changed":false,"source":"awgm"}`
		}
	case "check_direct":
		out = exitDirect
	}
	return &wire.CommandResult{ID: id, Status: "ok", Output: out}, true
}

func (c *scriptCommander) snapshotLocked() string {
	if c.snapshot != "" {
		return c.snapshot
	}
	snap := wire.RouteSnapshot{
		Tunnels: []wire.TunnelMeta{
			{ID: "awg12", Name: "Дача", HasHandshake: c.hs, HandshakeAge: 4},
			{ID: "awg10", Name: "Работа", HasHandshake: true, HandshakeAge: 30},
		},
	}
	ifaces := []wire.RoutePolicyInterface{
		{Bind: "OpkgTun12", Name: "Дача", TunnelID: "awg12", Role: "active", Available: false, Order: 1},
	}
	if !c.noBackup {
		ifaces = append(ifaces, wire.RoutePolicyInterface{Bind: "OpkgTun10", Name: "Работа", TunnelID: "awg10", Role: "fallback", Available: true, Order: 2})
	}
	if c.reserve {
		ifaces = []wire.RoutePolicyInterface{
			{Bind: "OpkgTun10", Name: "Работа", TunnelID: "awg10", Role: "active", Available: true, Order: 1},
			{Bind: "OpkgTun12", Name: "Дача", TunnelID: "awg12", Role: "unavailable", Available: false, Order: 2},
		}
		if c.carrierDown {
			ifaces[0].Role, ifaces[0].Available = "unavailable", false
		}
	}
	snap.Policies = append([]wire.RoutePolicySummary{{Name: "HydraRoute", Interfaces: ifaces}}, c.extra...)
	b, _ := json.Marshal(snap)
	return string(b)
}

func (c *scriptCommander) all() []sentCmd {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]sentCmd(nil), c.sent...)
}

func (c *scriptCommander) count() int { return len(c.all()) }

func (c *scriptCommander) actions(name string) []sentCmd {
	var out []sentCmd
	for _, s := range c.all() {
		if s.Action == name {
			out = append(out, s)
		}
	}
	return out
}

// promotedTo -- куда по порядку уводили общий набор правил.
func (c *scriptCommander) promotedTo() []string {
	var out []string
	for _, s := range c.actions("route_policy_promote") {
		out = append(out, fmt.Sprint(s.Args["tunnel_id"]))
	}
	return out
}

// fixOn -- после n-го вызова action у awg12 появляется свежий обмен ключами
// и (если exit) выход через туннель.
func fixOn(c *scriptCommander, action string, n int, exit bool) {
	if c.on == nil {
		c.on = map[string]func(*scriptCommander, int){}
	}
	c.on[action] = func(c *scriptCommander, got int) {
		if got == n {
			c.hs = true
			if exit {
				c.via = exitTunnel
			}
		}
	}
}

// fakeSource -- источник конфига: пишет вызовы и отдаёт заготовленное.
type fakeSource struct {
	mu      sync.Mutex
	calls   []string
	errs    map[string]error // по «issue:провайдер:вариант» / «fresh:…» / «options:провайдер»
	options []Option
	// onOptions -- крючок на вызов Options (например, остановить бэкенд).
	onOptions func()
	// full -- подписка кабинета заполнена: свободного места нет.
	full bool
}

// issuedOpts -- варианты кабинета, все уже выпущенные; подпись -- id в
// верхнем регистре, чтобы тексты отличали подпись от id.
func issuedOpts(ids ...string) []Option {
	out := make([]Option, 0, len(ids))
	for _, id := range ids {
		out = append(out, Option{ID: id, Label: strings.ToUpper(id), Issued: true})
	}
	return out
}

func (s *fakeSource) rec(key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, key)
	return s.errs[key]
}

func (s *fakeSource) Issue(_ context.Context, _ int64, provider, option string) (replace.Issued, error) {
	if err := s.rec("issue:" + provider + ":" + option); err != nil {
		return replace.Issued{}, err
	}
	return replace.Issued{TunnelName: provider + "_" + option, Conf: []byte("[Interface]\n"), Backend: "nativewg"}, nil
}

func (s *fakeSource) Fresh(_ context.Context, _ int64, provider, option string) (replace.Issued, error) {
	if err := s.rec("fresh:" + provider + ":" + option); err != nil {
		return replace.Issued{}, err
	}
	return replace.Issued{TunnelName: provider + "_" + option, Conf: []byte("[Interface]\n"), Backend: "nativewg"}, nil
}

func (s *fakeSource) Options(_ context.Context, _ int64, provider string) ([]Option, error) {
	if s.onOptions != nil {
		s.onOptions()
	}
	if err := s.rec("options:" + provider); err != nil {
		return nil, err
	}
	return s.options, nil
}

func (s *fakeSource) HasRoom(_ context.Context, _ int64, provider string) (bool, error) {
	if err := s.rec("room:" + provider); err != nil {
		return false, err
	}
	return !s.full, nil
}

func (s *fakeSource) got() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.calls...)
}

// recReporter -- как движок говорил с людьми: каждый вызов нити по порядку.
type recCall struct{ Kind, Text, Action string }

type recReporter struct {
	mu     sync.Mutex
	begins []string
	calls  []recCall
}

func (r *recReporter) Begin(_ context.Context, _ int64, checkName string) Thread {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.begins = append(r.begins, checkName)
	return recThread{r: r}
}

func (r *recReporter) add(c recCall) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, c)
}

func (r *recReporter) snapshot() ([]string, []recCall) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.begins...), append([]recCall(nil), r.calls...)
}

type recThread struct{ r *recReporter }

func (t recThread) Progress(_ context.Context, text string) { t.r.add(recCall{"progress", text, ""}) }
func (t recThread) Done(_ context.Context, text string)     { t.r.add(recCall{"done", text, ""}) }
func (t recThread) NeedHuman(_ context.Context, text, action string) {
	t.r.add(recCall{"need", text, action})
}
func (t recThread) NotStarted(_ context.Context, why string) { t.r.add(recCall{"notstarted", why, ""}) }

// final ждёт итогового вызова нити (done или need): движок закрывает
// задание и говорит с людьми не одним действием.
func (r *recReporter) final(t *testing.T) recCall {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		_, calls := r.snapshot()
		for _, c := range calls {
			if c.Kind == "done" || c.Kind == "need" {
				return c
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("итога людям не сказано")
	return recCall{}
}

// waitCalls ждёт, пока нить получит хотя бы n вызовов: «не запускалась»
// уходит людям в своей горутине, Start его не ждёт.
func (r *recReporter) waitCalls(t *testing.T, n int) []recCall {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, calls := r.snapshot(); len(calls) >= n {
			return calls
		}
		time.Sleep(5 * time.Millisecond)
	}
	_, calls := r.snapshot()
	return calls
}

type savedOption struct {
	RouterID           int64
	TunnelID, Provider string
	Option             string
}

type ladderEnv struct {
	d     Deps
	cmd   *scriptCommander
	src   *fakeSource
	rep   *recReporter
	mu    sync.Mutex
	saved []savedOption
	// spent -- отметки «новая страна выпущена»; spendErr -- отметка не пишется.
	spent    []string
	spendErr error
	// unconfirmed -- что легло на роутер, но проверку не прошло.
	unconfirmed []savedOption
}

func (e *ladderEnv) unconfirmedOptions() []savedOption {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]savedOption(nil), e.unconfirmed...)
}

func (e *ladderEnv) spentMarks() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string(nil), e.spent...)
}

func newLadder(t *testing.T, set *Setting) *ladderEnv {
	t.Helper()
	// По умолчанию страна «nl» у кабинета выпущена: ступень 2 у «Amnezia
	// Premium» сверяется со списком, прежде чем выпускать.
	e := &ladderEnv{cmd: newScript(), src: &fakeSource{options: []Option{{ID: "nl", Label: "NL", Issued: true}}}, rep: &recReporter{}}
	e.d = Deps{
		Store: provision.NewStore(),
		Probe: replace.Deps{
			Commands: e.cmd, AwaitStep: time.Second,
			HandshakeTries: 2, HandshakeWait: time.Millisecond,
			Sleep: func(context.Context, time.Duration) {},
		},
		Source: e.src,
		Settings: func(int64, string) (Setting, bool) {
			if set == nil {
				return Setting{}, false
			}
			return *set, true
		},
		SaveOption: func(routerID int64, tunnelID, provider, option string) {
			e.mu.Lock()
			defer e.mu.Unlock()
			e.saved = append(e.saved, savedOption{routerID, tunnelID, provider, option})
		},
		SaveUnconfirmed: func(routerID int64, tunnelID, provider, option string) {
			e.mu.Lock()
			defer e.mu.Unlock()
			e.unconfirmed = append(e.unconfirmed, savedOption{routerID, tunnelID, provider, option})
		},
		SpendRelocation: func(routerID int64, tunnelID, option string) error {
			e.mu.Lock()
			defer e.mu.Unlock()
			if e.spendErr != nil {
				return e.spendErr
			}
			e.spent = append(e.spent, fmt.Sprint(routerID, "/", tunnelID, "/", option))
			return nil
		},
		Attempts:  Attempts{KV: newSafeKV()},
		Commands:  e.cmd,
		Report:    e.rep,
		BaseCtx:   context.Background(),
		AwaitStep: time.Second,
	}
	return e
}

func (e *ladderEnv) savedOptions() []savedOption {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]savedOption(nil), e.saved...)
}

func ladderReq() StartReq {
	return StartReq{RouterID: 1, Nickname: "роутер", CheckName: "tunnel_awg12", AgentVersion: "v0.54.0", Auto: true}
}

// run запускает починку и ждёт и задания, и итога людям.
func (e *ladderEnv) run(t *testing.T, req StartReq) (provision.Job, recCall) {
	t.Helper()
	id, err := e.d.Start(req)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	job := waitDone(t, e.d, id)
	final := e.rep.final(t)
	ownerTextsClean(t, job, e.rep)
	return job, final
}

func stepOf(job provision.Job, name string) provision.Step {
	for _, s := range job.Steps {
		if s.Name == name {
			return s
		}
	}
	return provision.Step{}
}

func wantSteps(t *testing.T, job provision.Job, want map[string]provision.StepStatus) {
	t.Helper()
	for name, st := range want {
		if got := stepOf(job, name); got.Status != st {
			t.Errorf("шаг %s = %s (%q), ждали %s", name, got.Status, got.Detail, st)
		}
	}
}

// ownerTextsClean -- всё, что читает владелец (шаги, подсказка, нить), под
// сторожами словаря: латиница только в ёлочках, «VPN-туннель» вместо
// «линии», идентификаторов VPN-туннелей нет.
func ownerTextsClean(t *testing.T, job provision.Job, rep *recReporter) {
	t.Helper()
	texts := []string{job.Hint}
	for _, s := range job.Steps {
		texts = append(texts, s.Detail)
	}
	_, calls := rep.snapshot()
	for _, c := range calls {
		texts = append(texts, c.Text, c.Action)
	}
	for _, text := range texts {
		if words := latinOutsideQuotes(text); len(words) > 0 {
			t.Errorf("владелец читает латиницу %v: %q", words, text)
		}
		if strings.Contains(strings.ToLower(text), "лини") {
			t.Errorf("«линия» в тексте владельцу: %q", text)
		}
		if strings.Contains(text, "awg12") || strings.Contains(text, "awg10") {
			t.Errorf("идентификатор VPN-туннеля в тексте владельцу: %q", text)
		}
	}
}

// ---- Лесенка ----

func TestLadder_RestartFixes(t *testing.T) {
	e := newLadder(t, &Setting{Enabled: true, Provider: "amnezia", Option: "nl"})
	fixOn(e.cmd, "tunnel_restart", 1, true)

	job, final := e.run(t, ladderReq())

	if job.State != provision.StateSuccess {
		t.Fatalf("state=%s hint=%q", job.State, job.Hint)
	}
	wantSteps(t, job, map[string]provision.StepStatus{
		StepFailover: provision.StepDone, StepRestart: provision.StepDone,
		StepReissue: provision.StepSkipped, StepRecreate: provision.StepSkipped,
		StepFailback: provision.StepDone,
	})
	if final.Kind != "done" {
		t.Fatalf("итог %+v, ждали done", final)
	}
	if !strings.Contains(final.Text, "перезапустил") || !strings.Contains(final.Text, "VPN-туннель «Дача»") {
		t.Fatalf("итог не говорит, что сделано: %q", final.Text)
	}
	if got := e.src.got(); len(got) != 0 {
		t.Fatalf("источник спрошен, хотя перезапуск помог: %v", got)
	}
	if n := len(e.cmd.actions("tunnel_import")); n != 0 {
		t.Fatalf("tunnel_import ушёл %d раз", n)
	}
	if got := e.cmd.promotedTo(); len(got) != 2 || got[0] != "awg10" || got[1] != "awg12" {
		t.Fatalf("увод и возврат: %v", got)
	}
	restart := e.cmd.actions("tunnel_restart")
	if len(restart) != 1 || restart[0].Args["tunnel_id"] != "awg12" {
		t.Fatalf("перезапуск: %+v", restart)
	}
}

func TestLadder_ReissueInPlace(t *testing.T) {
	e := newLadder(t, &Setting{Enabled: true, Provider: "amnezia", Option: "nl"})
	fixOn(e.cmd, "tunnel_import", 1, true)

	job, final := e.run(t, ladderReq())

	if job.State != provision.StateSuccess || final.Kind != "done" {
		t.Fatalf("state=%s final=%+v", job.State, final)
	}
	imp := e.cmd.actions("tunnel_import")
	if len(imp) != 1 {
		t.Fatalf("импортов %d", len(imp))
	}
	if imp[0].Args["target_id"] != "awg12" || imp[0].Args["replace"] != true {
		t.Fatalf("импорт не в тот же VPN-туннель: %+v", imp[0].Args)
	}
	if got := e.src.got(); strings.Join(got, ",") != "options:amnezia,issue:amnezia:nl" {
		t.Fatalf("источник: %v", got)
	}
	wantSteps(t, job, map[string]provision.StepStatus{
		StepRestart: provision.StepFailed, StepReissue: provision.StepDone,
		StepRecreate: provision.StepSkipped, StepFailback: provision.StepDone,
	})
	if !strings.Contains(final.Text, "«Amnezia Premium»") {
		t.Fatalf("итог не называет источник: %q", final.Text)
	}
}

func TestLadder_RecreateAwg3NewPeer(t *testing.T) {
	e := newLadder(t, &Setting{Enabled: true, Provider: "awg3", Option: "vps1/wg0"})
	fixOn(e.cmd, "tunnel_import", 2, true)

	job, final := e.run(t, ladderReq())

	if job.State != provision.StateSuccess || final.Kind != "done" {
		t.Fatalf("state=%s final=%+v", job.State, final)
	}
	got := e.src.got()
	if len(got) != 2 || got[0] != "issue:awg3:vps1/wg0" || got[1] != "fresh:awg3:vps1/wg0" {
		t.Fatalf("источник: %v", got)
	}
	for _, imp := range e.cmd.actions("tunnel_import") {
		if imp.Args["target_id"] != "awg12" {
			t.Fatalf("импорт мимо awg12: %+v", imp.Args)
		}
	}
	wantSteps(t, job, map[string]provision.StepStatus{
		StepReissue: provision.StepFailed, StepRecreate: provision.StepDone, StepFailback: provision.StepDone,
	})
	if s := e.savedOptions(); len(s) != 0 {
		t.Fatalf("у своего сервера вариант не меняется, а записан: %+v", s)
	}
}

func TestLadder_RelocateOnlyWhenAllowed(t *testing.T) {
	e := newLadder(t, &Setting{Enabled: true, Provider: "amnezia", Option: "nl"})
	e.src.options = issuedOpts("nl", "de", "fi")

	job, final := e.run(t, ladderReq())

	if job.State != provision.StateFailed {
		t.Fatalf("state=%s", job.State)
	}
	// Список спрошен один раз -- сверка страны перед ступенью 2; для смены
	// локации без разрешения -- ни разу.
	if got := e.src.got(); strings.Join(got, ",") != "options:amnezia,issue:amnezia:nl" {
		t.Fatalf("варианты спрошены без разрешения менять локацию: %v", got)
	}
	if final.Kind != "need" || final.Action != ActServerDead {
		t.Fatalf("итог %+v, ждали need с ActServerDead", final)
	}
	if job.Hint != ActServerDead {
		t.Fatalf("подсказка %q", job.Hint)
	}
	wantSteps(t, job, map[string]provision.StepStatus{
		StepReissue: provision.StepFailed, StepRecreate: provision.StepSkipped, StepFailback: provision.StepSkipped,
	})
}

func TestLadder_RelocateTriesNextOption(t *testing.T) {
	e := newLadder(t, &Setting{Enabled: true, Provider: "hidemyname", Option: "nl", AllowRelocate: true})
	e.src.options = issuedOpts("nl", "de", "fi")
	fixOn(e.cmd, "tunnel_import", 2, true)

	job, final := e.run(t, ladderReq())

	if job.State != provision.StateSuccess || final.Kind != "done" {
		t.Fatalf("state=%s final=%+v", job.State, final)
	}
	got := e.src.got()
	want := []string{"issue:hidemyname:nl", "options:hidemyname", "issue:hidemyname:de"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("источник: %v, ждали %v", got, want)
	}
	if s := e.savedOptions(); len(s) != 1 || s[0] != (savedOption{1, "awg12", "hidemyname", "de"}) {
		t.Fatalf("удачная локация не записана: %+v", s)
	}
	if !strings.Contains(final.Text, "«DE»") {
		t.Fatalf("итог не называет локацию подписью кабинета: %q", final.Text)
	}
	if sp := e.spentMarks(); len(sp) != 0 {
		t.Fatalf("у «HideMy.name» отметка о новой стране не ставится: %v", sp)
	}
}

// «Amnezia Premium»: уже выпущенная страна стоит на другом устройстве или в
// другом VPN-туннеле -- один ключ в двух местах ломает оба. Поэтому смена
// локации берёт только НЕ выпущенную страну, и только одну на настройку:
// это место в подписке. Отметка ставится до выпуска.
func TestLadder_RelocateAmneziaNewCountryOnce(t *testing.T) {
	e := newLadder(t, &Setting{Enabled: true, Provider: "amnezia", Option: "nl", AllowRelocate: true})
	e.src.options = []Option{{ID: "nl", Issued: true}, {ID: "fi", Label: "Финляндия", Issued: true}, {ID: "de", Label: "Германия"}, {ID: "se", Label: "Швеция"}}
	fixOn(e.cmd, "tunnel_import", 2, true)

	job, final := e.run(t, ladderReq())

	if job.State != provision.StateSuccess || final.Kind != "done" {
		t.Fatalf("state=%s final=%+v", job.State, final)
	}
	want := []string{"options:amnezia", "issue:amnezia:nl", "options:amnezia", "room:amnezia", "issue:amnezia:de"}
	if got := e.src.got(); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("источник: %v, ждали %v -- выпущенная страна занята в другом месте", got, want)
	}
	if sp := e.spentMarks(); len(sp) != 1 || sp[0] != "1/awg12/de" {
		t.Fatalf("отметка о новой стране: %v", sp)
	}
	if s := e.savedOptions(); len(s) != 1 || s[0] != (savedOption{1, "awg12", "amnezia", "de"}) {
		t.Fatalf("удачная страна не записана: %+v", s)
	}
	if !strings.Contains(final.Text, "«Германия»") {
		t.Fatalf("итог: %q", final.Text)
	}
}

// Новая страна не помогла: вторую не выпускаем -- место уже потрачено.
func TestLadder_RelocateAmneziaNewCountryFailsNoSecond(t *testing.T) {
	e := newLadder(t, &Setting{Enabled: true, Provider: "amnezia", Option: "nl", AllowRelocate: true})
	e.src.options = []Option{{ID: "nl", Issued: true}, {ID: "de", Label: "Германия"}, {ID: "se", Label: "Швеция"}}

	job, final := e.run(t, ladderReq())

	if job.State != provision.StateFailed || final.Kind != "need" || final.Action != ActNewCountryNoHelp("Германия") {
		t.Fatalf("state=%s final=%+v", job.State, final)
	}
	for _, c := range e.src.got() {
		if c == "issue:amnezia:se" {
			t.Fatal("выпущена вторая новая страна -- второе место в подписке")
		}
	}
	if sp := e.spentMarks(); len(sp) != 1 || sp[0] != "1/awg12/de" {
		t.Fatalf("отметка о новой стране: %v", sp)
	}
	if s := e.savedOptions(); len(s) != 0 {
		t.Fatalf("неудачная страна записана: %+v", s)
	}
}

// Новую страну эта настройка уже выпускала: больше никогда, нужен человек.
func TestLadder_RelocateAmneziaSpentNeedsHuman(t *testing.T) {
	e := newLadder(t, &Setting{Enabled: true, Provider: "amnezia", Option: "de", AllowRelocate: true, RelocateSpent: "de"})
	e.src.options = []Option{{ID: "nl", Issued: true}, {ID: "de", Label: "Германия", Issued: true}, {ID: "se", Label: "Швеция"}}

	job, final := e.run(t, ladderReq())

	if final.Kind != "need" || final.Action != ActRelocateSpent {
		t.Fatalf("итог %+v, ждали need с ActRelocateSpent", final)
	}
	if job.Hint != ActRelocateSpent {
		t.Fatalf("подсказка %q", job.Hint)
	}
	for _, c := range e.src.got() {
		if c == "issue:amnezia:se" || c == "issue:amnezia:nl" {
			t.Fatalf("после потраченной отметки выпущена страна: %v", e.src.got())
		}
	}
	if sp := e.spentMarks(); len(sp) != 0 {
		t.Fatalf("отметка поставлена ещё раз: %v", sp)
	}
	st := stepOf(job, StepRecreate)
	if st.Status != provision.StepFailed || !strings.Contains(st.Detail, "«Германия»") {
		t.Fatalf("ступень 3: %+v", st)
	}
}

// Все страны подписки уже выпущены -- брать чужой ключ нельзя, нужен человек.
func TestLadder_RelocateAmneziaAllIssuedNeedsHuman(t *testing.T) {
	e := newLadder(t, &Setting{Enabled: true, Provider: "amnezia", Option: "nl", AllowRelocate: true})
	e.src.options = []Option{{ID: "nl", Issued: true}, {ID: "fi", Label: "Финляндия", Issued: true}}

	job, final := e.run(t, ladderReq())

	if final.Kind != "need" || final.Action != ActNoNewCountry {
		t.Fatalf("итог %+v, ждали need с ActNoNewCountry", final)
	}
	if job.Hint != ActNoNewCountry {
		t.Fatalf("подсказка %q", job.Hint)
	}
	for _, c := range e.src.got() {
		if c == "issue:amnezia:fi" {
			t.Fatal("выпущенная страна взята -- её ключ уже стоит в другом месте")
		}
	}
	if st := stepOf(job, StepRecreate); st.Status != provision.StepFailed {
		t.Fatalf("ступень 3: %+v", st)
	}
}

// Отметка не записалась -- новую страну не выпускаем: иначе следующая
// починка выпустила бы ещё одну.
func TestLadder_RelocateAmneziaSpendNotSavedNoIssue(t *testing.T) {
	e := newLadder(t, &Setting{Enabled: true, Provider: "amnezia", Option: "nl", AllowRelocate: true})
	e.src.options = []Option{{ID: "nl", Issued: true}, {ID: "de", Label: "Германия"}}
	e.spendErr = errors.New("база занята")

	job, final := e.run(t, ladderReq())

	// «Другие локации тоже не помогли» -- неправда: ни одна не пробовалась.
	if final.Action != ActNewCountryNotIssued {
		t.Fatalf("итог %+v, ждали ActNewCountryNotIssued", final)
	}

	for _, c := range e.src.got() {
		if c == "issue:amnezia:de" {
			t.Fatal("страна выпущена без записанной отметки")
		}
	}
	if st := stepOf(job, StepRecreate); st.Status != provision.StepFailed {
		t.Fatalf("ступень 3: %+v", st)
	}
}

// Отказ кабинета по одной локации не конец: пробуем следующую. Человек нужен,
// только если не помогла ни одна, и тогда -- действие последнего отказа.
func TestLadder_RelocateNeedHumanTriesNext(t *testing.T) {
	e := newLadder(t, &Setting{Enabled: true, Provider: "hidemyname", Option: "nl", AllowRelocate: true})
	e.src.options = issuedOpts("de", "fi")
	e.src.errs = map[string]error{"issue:hidemyname:de": &NeedHuman{Cause: errors.New("сервер закрыт"), Action: ActHideMyCode}}
	fixOn(e.cmd, "tunnel_import", 2, true)

	job, final := e.run(t, ladderReq())

	if job.State != provision.StateSuccess || final.Kind != "done" {
		t.Fatalf("state=%s final=%+v", job.State, final)
	}
	if got := e.src.got(); got[len(got)-1] != "issue:hidemyname:fi" {
		t.Fatalf("следующая локация не пробована: %v", got)
	}

	e2 := newLadder(t, &Setting{Enabled: true, Provider: "hidemyname", Option: "nl", AllowRelocate: true})
	e2.src.options = issuedOpts("de", "fi")
	e2.src.errs = map[string]error{
		"issue:hidemyname:de": &NeedHuman{Cause: errors.New("сервер закрыт"), Action: "первое"},
		"issue:hidemyname:fi": &NeedHuman{Cause: errors.New("сервер закрыт"), Action: ActHideMyCode},
	}
	_, final2 := e2.run(t, ladderReq())
	if final2.Kind != "need" || final2.Action != ActHideMyCode {
		t.Fatalf("итог %+v, ждали действие последнего отказа", final2)
	}
}

// У «HideMy.name» выпуск не занимает места: пробуются любые серверы.
func TestLadder_RelocateHideMyAnyServer(t *testing.T) {
	e := newLadder(t, &Setting{Enabled: true, Provider: "hidemyname", Option: "nl1", AllowRelocate: true})
	e.src.options = []Option{{ID: "nl1"}, {ID: "de1", Label: "Германия"}}
	fixOn(e.cmd, "tunnel_import", 2, true)

	job, final := e.run(t, ladderReq())

	if job.State != provision.StateSuccess || final.Kind != "done" {
		t.Fatalf("state=%s final=%+v", job.State, final)
	}
	if got := e.src.got(); got[len(got)-1] != "issue:hidemyname:de1" {
		t.Fatalf("источник: %v", got)
	}
}

// Больше двух других локаций не пробуем: третья смена страны подряд --
// уже не починка, а перебор.
func TestLadder_RelocateAtMostTwo(t *testing.T) {
	e := newLadder(t, &Setting{Enabled: true, Provider: "hidemyname", Option: "nl", AllowRelocate: true})
	e.src.options = issuedOpts("de", "nl", "fi", "se")

	job, final := e.run(t, ladderReq())

	// Смена локации разрешена и не помогла: «разрешите менять локацию» --
	// совет, который уже исполнен.
	if job.State != provision.StateFailed || final.Action != ActRelocateNoHelp {
		t.Fatalf("state=%s final=%+v", job.State, final)
	}
	got := e.src.got()
	want := []string{"issue:hidemyname:nl", "options:hidemyname", "issue:hidemyname:de", "issue:hidemyname:fi"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("источник: %v, ждали %v", got, want)
	}
	if s := e.savedOptions(); len(s) != 0 {
		t.Fatalf("неудачная локация записана: %+v", s)
	}
}

func TestLadder_NoSourceRestartOnly(t *testing.T) {
	e := newLadder(t, &Setting{Enabled: true})

	job, final := e.run(t, ladderReq())

	if final.Kind != "need" || final.Action != ActNoSource {
		t.Fatalf("итог %+v", final)
	}
	for _, name := range []string{StepReissue, StepRecreate} {
		st := stepOf(job, name)
		if st.Status != provision.StepSkipped || !strings.Contains(st.Detail, "источник не выбран") {
			t.Fatalf("шаг %s: %+v", name, st)
		}
	}
	if n := len(e.cmd.actions("tunnel_import")); n != 0 {
		t.Fatalf("без источника ушёл импорт: %d", n)
	}
	// Резерв лучше, чем ничего: увод остаётся.
	if got := e.cmd.promotedTo(); len(got) != 1 || got[0] != "awg10" {
		t.Fatalf("увод: %v", got)
	}
}

// Ручной запуск без настройки -- та же лесенка с пустым источником.
func TestLadder_ManualWithoutSetting(t *testing.T) {
	e := newLadder(t, nil)
	req := ladderReq()
	req.Auto = false

	job, final := e.run(t, req)

	if final.Action != ActNoSource || job.State != provision.StateFailed {
		t.Fatalf("state=%s final=%+v", job.State, final)
	}
	if n := len(e.cmd.actions("tunnel_restart")); n != 1 {
		t.Fatalf("перезапусков %d", n)
	}
}

func TestLadder_SourceGoneNeedsHuman(t *testing.T) {
	e := newLadder(t, &Setting{Enabled: true, Provider: "amnezia", Option: "nl", AllowRelocate: true})
	e.src.errs = map[string]error{"issue:amnezia:nl": &NeedHuman{Cause: errors.New("ключ не принят"), Action: ActAmneziaKey}}

	job, final := e.run(t, ladderReq())

	if final.Kind != "need" || final.Action != ActAmneziaKey {
		t.Fatalf("итог %+v", final)
	}
	if got := e.cmd.promotedTo(); len(got) != 1 || got[0] != "awg10" {
		t.Fatalf("трафик обязан остаться на резерве: %v", got)
	}
	if n := len(e.cmd.actions("tunnel_import")); n != 0 {
		t.Fatalf("импорт без конфига: %d", n)
	}
	wantSteps(t, job, map[string]provision.StepStatus{
		StepReissue: provision.StepFailed, StepRecreate: provision.StepSkipped, StepFailback: provision.StepSkipped,
	})
	if got := e.src.got(); strings.Join(got, ",") != "options:amnezia,issue:amnezia:nl" {
		t.Fatalf("кабинет отказал -- менять локацию через него же бессмысленно: %v", got)
	}
}

func TestLadder_OldAgentStopsAfterRestart(t *testing.T) {
	for _, ver := range []string{"v0.53.0", "", "dev"} {
		t.Run("версия «"+ver+"»", func(t *testing.T) {
			e := newLadder(t, &Setting{Enabled: true, Provider: "amnezia", Option: "nl"})
			req := ladderReq()
			req.AgentVersion = ver

			job, final := e.run(t, req)

			if final.Kind != "need" || final.Action != ActAgentOld {
				t.Fatalf("итог %+v", final)
			}
			wantSteps(t, job, map[string]provision.StepStatus{
				StepRestart: provision.StepFailed, StepReissue: provision.StepSkipped, StepRecreate: provision.StepSkipped,
			})
			if n := len(e.cmd.actions("tunnel_import")); n != 0 {
				t.Fatalf("старому агенту ушёл tunnel_import: %d", n)
			}
			if len(e.src.got()) != 0 {
				t.Fatalf("источник спрошен зря: %v", e.src.got())
			}
		})
	}
}

// D2: провал всех ступеней не возвращает трафик на сломанный VPN-туннель.
func TestLadder_FailureNeverPromotesBroken(t *testing.T) {
	e := newLadder(t, &Setting{Enabled: true, Provider: "awg3", Option: "vps1/wg0"})

	job, final := e.run(t, ladderReq())

	// Свой сервер: советовать «смените сервер или разрешите менять локацию»
	// незачем -- локации у него нет, проверять надо сам сервер.
	if job.State != provision.StateFailed || final.Kind != "need" || final.Action != ActVPSPanel("vps1") {
		t.Fatalf("state=%s final=%+v", job.State, final)
	}
	for _, id := range e.cmd.promotedTo() {
		if id == "awg12" {
			t.Fatalf("сломанный VPN-туннель снова первым: %v", e.cmd.promotedTo())
		}
	}
	if !strings.Contains(final.Text, "запасной VPN-туннель «Работа»") {
		t.Fatalf("итог не говорит, где идёт трафик: %q", final.Text)
	}
	if !strings.Contains(stepOf(job, StepFailback).Detail, "«Работа»") {
		t.Fatalf("шаг возврата: %+v", stepOf(job, StepFailback))
	}
}

// Обмен ключами есть, но выход тот же, что напрямую: ступень не доказана,
// трафик на неё не возвращается, лесенка идёт дальше.
func TestLadder_FailbackOnlyAfterProof(t *testing.T) {
	e := newLadder(t, &Setting{Enabled: true, Provider: "awg3", Option: "vps1/wg0"})
	e.cmd.on = map[string]func(*scriptCommander, int){
		"tunnel_import": func(c *scriptCommander, n int) {
			c.hs = true // обмен есть с первого импорта
			if n == 2 {
				c.via = exitTunnel // выход -- только со второго
			}
		},
	}

	job, final := e.run(t, ladderReq())

	if job.State != provision.StateSuccess || final.Kind != "done" {
		t.Fatalf("state=%s final=%+v", job.State, final)
	}
	if st := stepOf(job, StepReissue); st.Status != provision.StepFailed {
		t.Fatalf("ступень без выхода засчитана: %+v", st)
	}
	if got := e.cmd.promotedTo(); len(got) != 2 || got[1] != "awg12" {
		t.Fatalf("возврат: %v", got)
	}
	// Возврат -- только после доказанной ступени 3: перед ним два импорта.
	imports := 0
	for _, s := range e.cmd.all() {
		if s.Action == "tunnel_import" {
			imports++
		}
		if s.Action == "route_policy_promote" && s.Args["tunnel_id"] == "awg12" && imports < 2 {
			t.Fatal("трафик вернули до доказанной ступени")
		}
	}
}

// Без резерва трафик никуда не уводится и не возвращается; провал честно
// говорит, что заблокированное не открывается.
func TestLadder_NoBackup(t *testing.T) {
	e := newLadder(t, &Setting{Enabled: true})
	e.cmd.noBackup = true

	job, final := e.run(t, ladderReq())

	if n := len(e.cmd.actions("route_policy_promote")); n != 0 {
		t.Fatalf("без резерва ушло %d promote", n)
	}
	if !strings.Contains(final.Text, "Заблокированное сейчас не открывается") {
		t.Fatalf("итог: %q", final.Text)
	}
	if st := stepOf(job, StepFailover); st.Status != provision.StepDone {
		t.Fatalf("увод: %+v", st)
	}
}

func TestLadder_NoBackupRestartFixes(t *testing.T) {
	e := newLadder(t, &Setting{Enabled: true})
	e.cmd.noBackup = true
	fixOn(e.cmd, "tunnel_restart", 1, true)

	job, final := e.run(t, ladderReq())

	if job.State != provision.StateSuccess || final.Kind != "done" {
		t.Fatalf("state=%s final=%+v", job.State, final)
	}
	if n := len(e.cmd.actions("route_policy_promote")); n != 0 {
		t.Fatalf("без резерва ушло %d promote", n)
	}
	if strings.Contains(final.Text, "вернул") {
		t.Fatalf("возвращать было нечего: %q", final.Text)
	}
}

// Упал резерв, а трафик идёт через первое звено цепочки: уводить нечего и
// возвращать некуда. Ни одного route_policy_promote -- иначе починка резерва
// переставила бы цепочку и сделала его первым.
func TestLadder_ReserveBrokenNoPromote(t *testing.T) {
	for _, fixed := range []bool{true, false} {
		t.Run(fmt.Sprint("починен=", fixed), func(t *testing.T) {
			e := newLadder(t, &Setting{Enabled: true})
			e.cmd.reserve = true
			if fixed {
				fixOn(e.cmd, "tunnel_restart", 1, true)
			}

			job, final := e.run(t, ladderReq())

			if n := len(e.cmd.actions("route_policy_promote")); n != 0 {
				t.Fatalf("резерв упал, а набор правил переставлен %d раз: %v", n, e.cmd.promotedTo())
			}
			if st := stepOf(job, StepFailover); st.Status != provision.StepDone || !strings.Contains(st.Detail, "трафик и так идёт через «Работа»") {
				t.Fatalf("ступень 0: %+v", st)
			}
			for _, bad := range []string{"увёл трафик", "вернул на него", "Заблокированное"} {
				if strings.Contains(final.Text, bad) {
					t.Fatalf("итог врёт про трафик (%q): %q", bad, final.Text)
				}
			}
			if !fixed && !strings.Contains(final.Text, "«Работа»") {
				t.Fatalf("провал не говорит, где идёт трафик: %q", final.Text)
			}
			if fixed && (job.State != provision.StateSuccess || final.Kind != "done") {
				t.Fatalf("state=%s final=%+v", job.State, final)
			}
		})
	}
}

// Упал резерв, а первое звено цепочки тоже лежит: называть его тем, через
// что «и так идёт трафик», -- неправда. Говорим, что рабочего VPN-туннеля у
// трафика сейчас нет; набор правил по-прежнему не трогаем.
func TestLadder_ReserveBrokenCarrierDown(t *testing.T) {
	for _, fixed := range []bool{true, false} {
		t.Run(fmt.Sprint("починен=", fixed), func(t *testing.T) {
			e := newLadder(t, &Setting{Enabled: true})
			e.cmd.reserve, e.cmd.carrierDown = true, true
			if fixed {
				fixOn(e.cmd, "tunnel_restart", 1, true)
			}

			job, final := e.run(t, ladderReq())

			if n := len(e.cmd.actions("route_policy_promote")); n != 0 {
				t.Fatalf("набор правил переставлен %d раз", n)
			}
			texts := []string{final.Text}
			for _, st := range job.Steps {
				texts = append(texts, st.Detail)
			}
			for _, text := range texts {
				if strings.Contains(text, "«Работа»") {
					t.Fatalf("назван лежащий VPN-туннель: %q", text)
				}
			}
			if st := stepOf(job, StepFailover); st.Status != provision.StepDone || !strings.Contains(st.Detail, "нет рабочего VPN-туннеля") {
				t.Fatalf("ступень 0: %+v", st)
			}
			if !fixed && !strings.Contains(final.Text, "нет рабочего VPN-туннеля") {
				t.Fatalf("провал не говорит, что трафику некуда идти: %q", final.Text)
			}
		})
	}
}

// Ход починки виден людям по мере дела: перед каждой ступенью -- правка.
func TestLadder_ProgressTellsWhatNow(t *testing.T) {
	e := newLadder(t, &Setting{Enabled: true, Provider: "amnezia", Option: "nl"})
	fixOn(e.cmd, "tunnel_import", 1, true)

	e.run(t, ladderReq())

	_, calls := e.rep.snapshot()
	var progress []string
	for _, c := range calls {
		if c.Kind == "progress" {
			progress = append(progress, c.Text)
		}
	}
	if len(progress) < 2 {
		t.Fatalf("правок хода %d: %v", len(progress), progress)
	}
	last := progress[len(progress)-1]
	for _, w := range []string{"Чиню:", "запасной VPN-туннель «Работа»", "перезапуск не помог", "«Amnezia Premium»"} {
		if !strings.Contains(last, w) {
			t.Errorf("в ходе нет %q: %q", w, last)
		}
	}
}

// Удача снимает стоп после провала (D1), провал при автозапуске его ставит.
func TestLadder_AttemptsFollowOutcome(t *testing.T) {
	e := newLadder(t, &Setting{Enabled: true})
	e.run(t, ladderReq())
	if blocked, _ := e.d.Attempts.Blocked("роутер", "tunnel_awg12"); !blocked {
		t.Fatal("провал автопочинки обязан её остановить")
	}

	e2 := newLadder(t, &Setting{Enabled: true})
	e2.d.Attempts = e.d.Attempts
	fixOn(e2.cmd, "tunnel_restart", 1, true)
	req := ladderReq()
	req.Auto = false
	e2.run(t, req)
	if blocked, why := e.d.Attempts.Blocked("роутер", "tunnel_awg12"); blocked {
		t.Fatalf("удачная ручная починка обязана снять стоп: %s", why)
	}
}

// Снимка нет -- чинить вслепую нельзя. Человеку -- слова, а не инженерия.
func TestLadder_RouterFailuresReachOwnerAsWords(t *testing.T) {
	cases := []struct {
		name    string
		setup   func(*scriptCommander)
		want    []string
		forbid  []string
		action  bool
		restart int // увод не удался -- лесенка идёт дальше без него
	}{
		{"молчит на снимке", func(c *scriptCommander) {
			c.silent = map[string]bool{"route_status": true}
		}, []string{"не ответил", "запасной VPN-туннель"}, []string{"Заблокированное"}, true, 0},
		{"снимок не разобрался", func(c *scriptCommander) {
			c.snapshot = "<html>502 Bad Gateway</html>"
		}, []string{"непонятн"}, nil, true, 0},
		{"отказал уводу на резерв", func(c *scriptCommander) {
			c.refuse = map[string]bool{"route_policy_promote": true}
		}, []string{"запасной VPN-туннель «Работа»", "роутер не дал"}, nil, true, 1},
		{"VPN-туннель вне наборов", func(c *scriptCommander) {
			c.snapshot = `{"tunnels":[{"id":"awg12","name":"Дача"}],"policies":[]}`
		}, []string{"VPN-туннель «Дача»", "чинить нечего", "ничего не идёт"}, []string{"Заблокированное"}, false, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newLadder(t, &Setting{Enabled: true, Provider: "amnezia", Option: "nl"})
			tc.setup(e.cmd)
			// Имя знает запускающий (автозапуск -- из проверки): без снимка
			// его больше взять неоткуда.
			req := ladderReq()
			req.TunnelName = "Дача"
			job, final := e.run(t, req)
			if job.State != provision.StateFailed || final.Kind != "need" {
				t.Fatalf("state=%s final=%+v", job.State, final)
			}
			if (final.Action != "") != tc.action {
				t.Fatalf("действие для человека %q, ждали непустое=%v", final.Action, tc.action)
			}
			texts := []string{job.Hint, final.Text, final.Action}
			for _, st := range job.Steps {
				texts = append(texts, st.Detail)
			}
			all := strings.Join(texts, "\n")
			for _, w := range tc.want {
				if !strings.Contains(all, w) {
					t.Errorf("человек не прочёл %q:\n%s", w, all)
				}
			}
			for _, f := range tc.forbid {
				if strings.Contains(all, f) {
					t.Errorf("человек прочёл %q:\n%s", f, all)
				}
			}
			if n := len(e.cmd.actions("tunnel_restart")); n != tc.restart {
				t.Fatalf("перезапусков %d, ждали %d", n, tc.restart)
			}
		})
	}
}

// Страну отозвали в кабинете: выпуск «того же конфига» занял бы новое место в
// подписке. Ступень 2 не выпускает, нужен человек.
func TestLadder_ReissueAmneziaRevokedNeedsHuman(t *testing.T) {
	for _, opts := range [][]Option{
		{{ID: "nl", Label: "Нидерланды"}, {ID: "de", Label: "Германия", Issued: true}},
		{{ID: "de", Label: "Германия", Issued: true}},
	} {
		e := newLadder(t, &Setting{Enabled: true, Provider: "amnezia", Option: "nl", AllowRelocate: true})
		e.src.options = opts

		job, final := e.run(t, ladderReq())

		label := "nl"
		if len(opts) == 2 {
			label = "Нидерланды"
		}
		if final.Kind != "need" || final.Action != ActCountryRevoked(label) {
			t.Fatalf("итог %+v, ждали ActCountryRevoked(%q)", final, label)
		}
		for _, c := range e.src.got() {
			if strings.HasPrefix(c, "issue:") {
				t.Fatalf("выпуск без выпущенной страны: %v", e.src.got())
			}
		}
		if n := len(e.cmd.actions("tunnel_import")); n != 0 {
			t.Fatalf("импортов %d", n)
		}
		wantSteps(t, job, map[string]provision.StepStatus{StepReissue: provision.StepFailed, StepRecreate: provision.StepSkipped})
	}
}

// Остановка посреди сверки страны: это прерывание, а не «обновите ключ».
func TestLadder_ReissueCheckCancelledIsAborted(t *testing.T) {
	e := newLadder(t, &Setting{Enabled: true, Provider: "amnezia", Option: "nl"})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	e.d.BaseCtx = ctx
	e.src.errs = map[string]error{"options:amnezia": &NeedHuman{Cause: context.Canceled, Action: ActAmneziaKey}}
	e.src.onOptions = cancel

	job, final := e.run(t, ladderReq())

	if final.Action == ActAmneziaKey || job.Hint == ActAmneziaKey {
		t.Fatalf("прерывание выдано за ключ: final=%+v hint=%q", final, job.Hint)
	}
	if job.Hint != ActAborted {
		t.Fatalf("подсказка %q, ждали ActAborted", job.Hint)
	}
	for _, st := range job.Steps {
		if strings.Contains(st.Detail, "бэкенд") {
			t.Fatalf("шаг %s: %q", st.Name, st.Detail)
		}
	}
}

// Роутер не ответил на увод: «не дал» -- неправда, он мог и не получить.
func TestLadder_PromoteSilentSaysNotConfirmed(t *testing.T) {
	e := newLadder(t, &Setting{Enabled: true})
	e.cmd.silent = map[string]bool{"route_policy_promote": true}
	e.d.AwaitStep = 50 * time.Millisecond

	job, _ := e.run(t, ladderReq())

	st := stepOf(job, StepFailover)
	if !strings.Contains(st.Detail, "роутер не подтвердил увод трафика на запасной VPN-туннель «Работа»") || strings.Contains(st.Detail, "не дал") {
		t.Fatalf("ступень 0: %+v", st)
	}
}

// Роутер отказал уводу на резерв: это не повод бросать починку -- перезапуск
// и дальше идут как без резерва, и удача остаётся удачей.
func TestLadder_PromoteRefusedStillRepairs(t *testing.T) {
	e := newLadder(t, &Setting{Enabled: true, Provider: "amnezia", Option: "nl"})
	e.cmd.refuse = map[string]bool{"route_policy_promote": true}
	fixOn(e.cmd, "tunnel_restart", 1, true)

	job, final := e.run(t, ladderReq())

	if job.State != provision.StateSuccess || final.Kind != "done" {
		t.Fatalf("state=%s final=%+v", job.State, final)
	}
	if n := len(e.cmd.actions("route_policy_promote")); n != 1 {
		t.Fatalf("увод пробован %d раз, возврата быть не должно", n)
	}
	if st := stepOf(job, StepFailover); !strings.Contains(st.Detail, "роутер не дал") {
		t.Fatalf("ступень 0: %+v", st)
	}
	if strings.Contains(final.Text, "вернул") || strings.Contains(final.Text, "увёл") {
		t.Fatalf("итог врёт про трафик: %q", final.Text)
	}
	if allow, why := e.d.Attempts.Allow("роутер", "tunnel_awg12"); !allow {
		t.Fatalf("удача поставила стоп: %s", why)
	}
}

// Снимка нет: громко «роутер не ответил», и это не вердикт автопочинке --
// следующая тревога снова может её запустить.
func TestLadder_NoSnapshotNotSticky(t *testing.T) {
	e := newLadder(t, &Setting{Enabled: true, Provider: "amnezia", Option: "nl"})
	e.cmd.silent = map[string]bool{"route_status": true}
	req := ladderReq()
	req.TunnelName = "Дача"

	_, final := e.run(t, req)

	if final.Kind != "need" || final.Action != "роутер не ответил — проверьте, на связи ли он" {
		t.Fatalf("итог %+v", final)
	}
	if allow, why := e.d.Attempts.Allow("роутер", "tunnel_awg12"); !allow {
		t.Fatalf("молчание роутера поставило стоп: %s", why)
	}
}

// Подсказка задания -- это «что делать» на экране починки: только действие
// для человека или пусто. Причина провала живёт в шагах, а не в подсказке.
func TestLadder_HintIsActionOnly(t *testing.T) {
	cases := []struct {
		name  string
		setup func(*scriptCommander)
		want  string
	}{
		{"VPN-туннель вне наборов", func(c *scriptCommander) {
			c.snapshot = `{"tunnels":[{"id":"awg12","name":"Дача"}],"policies":[]}`
		}, ""},
		// Отказ увода лесенку не обрывает: итог -- настоящее действие.
		{"отказал уводу", func(c *scriptCommander) {
			c.refuse = map[string]bool{"route_policy_promote": true}
		}, ActServerDead},
		{"лесенка кончилась", func(*scriptCommander) {}, ActServerDead},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newLadder(t, &Setting{Enabled: true, Provider: "amnezia", Option: "nl"})
			tc.setup(e.cmd)
			job, _ := e.run(t, ladderReq())
			if job.Hint != tc.want {
				t.Fatalf("подсказка %q, ждали %q", job.Hint, tc.want)
			}
		})
	}
}

// Снимок не пришёл -- имя VPN-туннеля знает запускающий.
func TestLadder_NameFromCallerWhenNoSnapshot(t *testing.T) {
	e := newLadder(t, &Setting{Enabled: true})
	e.cmd.silent = map[string]bool{"route_status": true}
	req := ladderReq()
	req.TunnelName = "Дача"
	_, final := e.run(t, req)
	if !strings.Contains(final.Text, "VPN-туннель «Дача»") || strings.Contains(final.Text, "awg12") {
		t.Fatalf("владелец читает не имя VPN-туннеля: %q", final.Text)
	}
}

// Остановка бэкенда посреди починки: лесенка кончается сразу, без новых
// ступеней и без возврата трафика.
func TestLadder_StopsOnCancel(t *testing.T) {
	e := newLadder(t, &Setting{Enabled: true, Provider: "amnezia", Option: "nl"})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	e.d.BaseCtx = ctx
	e.cmd.on = map[string]func(*scriptCommander, int){
		"tunnel_restart": func(*scriptCommander, int) { cancel() },
	}

	job, final := e.run(t, ladderReq())

	if job.State != provision.StateFailed || final.Kind != "need" {
		t.Fatalf("state=%s final=%+v", job.State, final)
	}
	// Экран починки без подсказки показал бы голое «Не получилось».
	if job.Hint != ActAborted {
		t.Fatalf("подсказка прерванной починки %q, ждали %q", job.Hint, ActAborted)
	}
	// «Бэкенд» владельцу -- не слово: ни в подсказке, ни в шагах.
	if strings.Contains(job.Hint, "бэкенд") {
		t.Fatalf("подсказка: %q", job.Hint)
	}
	for _, st := range job.Steps {
		if strings.Contains(st.Detail, "бэкенд") {
			t.Fatalf("шаг %s: %q", st.Name, st.Detail)
		}
	}
	if n := len(e.cmd.actions("tunnel_import")); n != 0 {
		t.Fatalf("после отмены ушёл импорт: %d", n)
	}
	if len(e.src.got()) != 0 {
		t.Fatalf("после отмены спрошен источник: %v", e.src.got())
	}
	if got := e.cmd.promotedTo(); len(got) != 1 {
		t.Fatalf("после отмены трогали набор правил: %v", got)
	}
}

// После увода активен резерв: доказательство обязано мерить выход именно
// через чинимый VPN-туннель (exit_ip_probe по его id), иначе «починил»
// означало бы «резерв работает».
func TestLadder_ProofTargetsBrokenTunnel(t *testing.T) {
	e := newLadder(t, &Setting{Enabled: true, Provider: "amnezia", Option: "nl"})
	fixOn(e.cmd, "tunnel_import", 1, true)

	job, final := e.run(t, ladderReq())

	if job.State != provision.StateSuccess || final.Kind != "done" {
		t.Fatalf("state=%s final=%+v", job.State, final)
	}
	probes := e.cmd.actions("exit_ip_probe")
	// Перезапуск отсеян ещё на обмене ключами; замер -- у ступени «тот же конфиг».
	if len(probes) != 1 {
		t.Fatalf("замеров %d, ждали 1", len(probes))
	}
	for _, p := range probes {
		if p.Args["tunnel_id"] != "awg12" {
			t.Fatalf("замер не по чинимому VPN-туннелю: %+v", p.Args)
		}
	}
	if n := len(e.cmd.actions("check_via_tunnel")); n != 0 {
		t.Fatalf("проверка через активное звено (то есть резерв) ушла %d раз", n)
	}
	// Первый замер -- уже после увода на резерв.
	promoted := false
	for _, s := range e.cmd.all() {
		if s.Action == "route_policy_promote" && s.Args["tunnel_id"] == "awg10" {
			promoted = true
		}
		if s.Action == "exit_ip_probe" && !promoted {
			t.Fatal("замер до увода")
		}
	}
}

// Агент старше v0.47 не умеет мерить выход по туннелю: доказательство --
// только свежий обмен ключами, проверку через активное звено не шлём.
func TestLadder_PreExitProbeAgentHandshakeOnly(t *testing.T) {
	e := newLadder(t, &Setting{Enabled: true, Provider: "amnezia", Option: "nl"})
	fixOn(e.cmd, "tunnel_restart", 1, false) // обмен есть, выход не меряется
	req := ladderReq()
	req.AgentVersion = "v0.46.0"

	job, final := e.run(t, req)

	if job.State != provision.StateSuccess || final.Kind != "done" {
		t.Fatalf("state=%s final=%+v", job.State, final)
	}
	if n := len(e.cmd.actions("exit_ip_probe")) + len(e.cmd.actions("check_via_tunnel")); n != 0 {
		t.Fatalf("старому агенту ушло %d замеров выхода", n)
	}
	if !strings.Contains(stepOf(job, StepRestart).Detail, "ключами обменялся") {
		t.Fatalf("шаг не говорит, чем доказан: %+v", stepOf(job, StepRestart))
	}
}

// ---- Start ----

func TestStart_AutoOffSilent(t *testing.T) {
	for _, set := range []*Setting{nil, {Enabled: false, Provider: "amnezia", Option: "nl"}} {
		e := newLadder(t, set)
		_, err := e.d.Start(ladderReq())
		if !errors.Is(err, ErrAutoDisabled) {
			t.Fatalf("ждали ErrAutoDisabled, получили %v", err)
		}
		if e.cmd.count() != 0 {
			t.Fatalf("роутеру ушло %d команд", e.cmd.count())
		}
		if begins, _ := e.rep.snapshot(); len(begins) != 0 {
			t.Fatalf("выключенная автопочинка заговорила: %v", begins)
		}
	}
}

func TestStart_ThrottledSaysWhy(t *testing.T) {
	e := newLadder(t, &Setting{Enabled: true})
	for i := 0; i < 3; i++ {
		_ = e.d.Attempts.Record("роутер", "tunnel_awg12", true)
	}
	_, err := e.d.Start(ladderReq())
	if !errors.Is(err, ErrAutoDisabled) {
		t.Fatalf("ждали ErrAutoDisabled, получили %v", err)
	}
	if e.cmd.count() != 0 {
		t.Fatalf("роутеру ушло %d команд", e.cmd.count())
	}
	calls := e.rep.waitCalls(t, 1)
	if len(calls) != 1 || calls[0].Kind != "notstarted" || !strings.Contains(calls[0].Text, "6 часов") {
		t.Fatalf("причина не сказана: %+v", calls)
	}
	// Что делать -- в той же правке: это не новый провал, громкого ответа нет.
	if !strings.Contains(calls[0].Text, ActTooOften) {
		t.Fatalf("правка не говорит, что делать: %q", calls[0].Text)
	}
}

func TestStart_NotStartedWhenLockedOrOldAgent(t *testing.T) {
	e := newLadder(t, &Setting{Enabled: true})
	if !e.d.Store.TryLock("роутер") {
		t.Fatal("замок должен браться")
	}
	if _, err := e.d.Start(ladderReq()); !errors.Is(err, ErrAlreadyRunning) {
		t.Fatalf("ждали ErrAlreadyRunning, получили %v", err)
	}
	calls := e.rep.waitCalls(t, 1)
	if len(calls) != 1 || calls[0].Kind != "notstarted" || !strings.Contains(calls[0].Text, "уже идёт") {
		t.Fatalf("замок: %+v", calls)
	}

	e2 := newLadder(t, &Setting{Enabled: true})
	req := ladderReq()
	req.AgentVersion = "v0.14.4"
	if _, err := e2.d.Start(req); !errors.Is(err, replace.ErrAgentTooOld) {
		t.Fatalf("ждали ErrAgentTooOld, получили %v", err)
	}
	calls = e2.rep.waitCalls(t, 1)
	if len(calls) != 1 || calls[0].Kind != "notstarted" {
		t.Fatalf("старый агент: %+v", calls)
	}
	if words := latinOutsideQuotes(calls[0].Text); len(words) > 0 {
		t.Fatalf("причина с латиницей %v: %q", words, calls[0].Text)
	}
	if e2.cmd.count() != 0 {
		t.Fatalf("старому агенту ушло %d команд", e2.cmd.count())
	}
}

// VPN-туннель переименовали (id тот же, имя в настройке другое): настройка
// остаётся, перезапуск идёт сразу, а выпуск и пересоздание ждут, пока человек
// подтвердит автопочинку в приложении. Людям -- громко, что сделать.
func wantRenamedAsks(t *testing.T, e *ladderEnv, job provision.Job, final recCall) {
	t.Helper()
	if job.State != provision.StateFailed || final.Kind != "need" || final.Action != ActRenamed("Дача") {
		t.Fatalf("state=%s final=%+v", job.State, final)
	}
	if job.Hint != ActRenamed("Дача") {
		t.Fatalf("подсказка %q", job.Hint)
	}
	if n := len(e.cmd.actions("tunnel_restart")); n != 1 {
		t.Fatalf("перезапусков %d, ждали 1", n)
	}
	if got := e.src.got(); len(got) != 0 {
		t.Fatalf("источник спрошен до подтверждения: %v", got)
	}
	if n := len(e.cmd.actions("tunnel_import")); n != 0 {
		t.Fatalf("tunnel_import до подтверждения: %d", n)
	}
	for _, st := range []string{StepReissue, StepRecreate} {
		got := stepOf(job, st)
		if got.Status != provision.StepSkipped || !strings.Contains(got.Detail, "«Дача»") {
			t.Fatalf("шаг %s: %+v", st, got)
		}
	}
}

func TestStart_RenamedAutoRestartsThenAsks(t *testing.T) {
	e := newLadder(t, &Setting{Enabled: true, Provider: "amnezia", Option: "nl", AllowRelocate: true, TunnelName: "Старый"})
	req := ladderReq()
	req.TunnelName = "Дача"

	job, final := e.run(t, req)

	wantRenamedAsks(t, e, job, final)
}

// Имя от запускающего не пришло -- сверка по снимку роутера.
func TestLadder_RenamedBySnapshotRestartsThenAsks(t *testing.T) {
	e := newLadder(t, &Setting{Enabled: true, Provider: "amnezia", Option: "nl", TunnelName: "Старый"})

	job, final := e.run(t, ladderReq())

	wantRenamedAsks(t, e, job, final)
}

// Ручной запуск -- то же: согласие на выпуск давали другому имени.
func TestLadder_RenamedManualRestartsThenAsks(t *testing.T) {
	e := newLadder(t, &Setting{Enabled: true, Provider: "amnezia", Option: "nl", TunnelName: "Старый"})
	req := ladderReq()
	req.Auto = false
	req.TunnelName = "Дача"

	job, final := e.run(t, req)

	wantRenamedAsks(t, e, job, final)
}

// Перезапуск помог -- починка удалась, переименование ей не мешает.
func TestLadder_RenamedRestartFixes(t *testing.T) {
	e := newLadder(t, &Setting{Enabled: true, Provider: "amnezia", Option: "nl", TunnelName: "Старый"})
	fixOn(e.cmd, "tunnel_restart", 1, true)

	job, final := e.run(t, ladderReq())

	if job.State != provision.StateSuccess || final.Kind != "done" {
		t.Fatalf("state=%s final=%+v", job.State, final)
	}
}

// Имя совпало -- настройка своя, лесенка как обычно.
func TestLadder_SettingNameMatches(t *testing.T) {
	e := newLadder(t, &Setting{Enabled: true, Provider: "amnezia", Option: "nl", TunnelName: "Дача"})
	fixOn(e.cmd, "tunnel_import", 1, true)
	req := ladderReq()
	req.TunnelName = "Дача"

	job, final := e.run(t, req)

	if job.State != provision.StateSuccess || final.Kind != "done" {
		t.Fatalf("state=%s final=%+v", job.State, final)
	}
}

// blockingReporter -- Telegram, который не отвечает: NotStarted висит, пока
// тест не отпустит.
type blockingReporter struct {
	release  chan struct{}
	deadline chan time.Time
}

func (b *blockingReporter) Begin(context.Context, int64, string) Thread { return b }
func (b *blockingReporter) Progress(context.Context, string)            {}
func (b *blockingReporter) Done(context.Context, string)                {}
func (b *blockingReporter) NeedHuman(context.Context, string, string)   {}
func (b *blockingReporter) NotStarted(ctx context.Context, _ string) {
	dl, _ := ctx.Deadline()
	b.deadline <- dl
	<-b.release
}

// «Не запускалась» уходит из обработчика отчёта агента: медленный Telegram
// не должен держать отчёт. Start возвращается сразу, сообщение -- в своей
// горутине и со сроком.
func TestStart_NotStartedDoesNotBlock(t *testing.T) {
	e := newLadder(t, &Setting{Enabled: true})
	br := &blockingReporter{release: make(chan struct{}), deadline: make(chan time.Time, 1)}
	defer close(br.release)
	e.d.Report = br
	for i := 0; i < 3; i++ {
		_ = e.d.Attempts.Record("роутер", "tunnel_awg12", true)
	}
	done := make(chan error, 1)
	go func() {
		_, err := e.d.Start(ladderReq())
		done <- err
	}()
	select {
	case err := <-done:
		if !errors.Is(err, ErrAutoDisabled) {
			t.Fatalf("ждали ErrAutoDisabled, получили %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Start ждёт Telegram")
	}
	select {
	case dl := <-br.deadline:
		if dl.IsZero() || time.Until(dl) > 31*time.Second {
			t.Fatalf("у «не запускалась» нет срока: %v", dl)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("«не запускалась» так и не ушло")
	}
}

// Report == nil -- починка молчит, но работает.
func TestStart_NilReporterIsSilent(t *testing.T) {
	e := newLadder(t, &Setting{Enabled: true})
	e.d.Report = nil
	fixOn(e.cmd, "tunnel_restart", 1, true)
	id, err := e.d.Start(ladderReq())
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if job := waitDone(t, e.d, id); job.State != provision.StateSuccess {
		t.Fatalf("state=%s", job.State)
	}
}

// Для нечинибельной поломки движок не заводит задание вообще.
func TestStart_NoScenario(t *testing.T) {
	e := newLadder(t, &Setting{Enabled: true})
	req := ladderReq()
	req.CheckName = "external_reach"
	if _, err := e.d.Start(req); !errors.Is(err, ErrNoScenario) {
		t.Fatalf("хотим ErrNoScenario, получили %v", err)
	}
	if e.cmd.count() != 0 {
		t.Fatalf("роутеру ушло %d команд", e.cmd.count())
	}
}

// Ручной запуск при старом агенте -- отказ без «не запускалась»: человек
// видит ответ на экране сразу.
func TestStart_RefusesOldAgentManual(t *testing.T) {
	e := newLadder(t, nil)
	req := ladderReq()
	req.Auto = false
	req.AgentVersion = "v0.14.4"
	if _, err := e.d.Start(req); !errors.Is(err, replace.ErrAgentTooOld) {
		t.Fatalf("хотим ErrAgentTooOld, получили %v", err)
	}
	if begins, _ := e.rep.snapshot(); len(begins) != 0 {
		t.Fatalf("ручной отказ ушёл в личку: %v", begins)
	}
}

// ---- Хелперы, пережившие прежний движок ----

// Резерв -- первый доступный интерфейс политики, который не упавшая линия и
// за которым стоит наш туннель. Список уже отсортирован по приоритету, так
// что «первый подходящий» и есть тот, кого подхватит политика.
func TestPickBackup(t *testing.T) {
	pol := wire.RoutePolicySummary{
		Name: "HydraRoute",
		Interfaces: []wire.RoutePolicyInterface{
			{Bind: "nwg1", TunnelID: "awg12", Role: "active", Available: false, Order: 1},
			{Bind: "nwg3", TunnelID: "awg13", Role: "unavailable", Available: false, Order: 2},
			{Bind: "eth3", TunnelID: "", Role: "fallback", Available: true, Order: 3},
			{Bind: "nwg2", TunnelID: "awg10", Role: "fallback", Available: true, Order: 4},
		},
	}
	got, ok := pickBackup(pol, "awg12")
	if !ok {
		t.Fatal("резерв обязан найтись")
	}
	if got != "awg10" {
		t.Fatalf("резерв = %q, хотим awg10: awg13 недоступен, eth3 -- не туннель", got)
	}
}

func TestPickBackup_NoneWhenAlone(t *testing.T) {
	pol := wire.RoutePolicySummary{
		Interfaces: []wire.RoutePolicyInterface{
			{Bind: "nwg1", TunnelID: "awg12", Role: "active", Available: false},
		},
	}
	if _, ok := pickBackup(pol, "awg12"); ok {
		t.Fatal("одна линия -- резерва нет, и это нормальный случай")
	}
}

// latinOutsideQuotes -- латинские слова вне ёлочек: имена VPN-туннелей
// владелец видит в ёлочках, «VPN» -- часть слова «VPN-туннель», а всё прочее
// латиницей -- инженерия, которую он прочесть не может.
var (
	quotedRe    = regexp.MustCompile(`«[^»]*»`)
	latinWordRe = regexp.MustCompile(`[A-Za-z]{2,}`)
)

func latinOutsideQuotes(text string) []string {
	bare := quotedRe.ReplaceAllString(text, "«»")
	bare = strings.ReplaceAll(bare, "VPN", "")
	return latinWordRe.FindAllString(bare, -1)
}

func waitDone(t *testing.T, d Deps, jobID string) provision.Job {
	t.Helper()
	// Пятнадцать секунд, а не три: при полном прогоне пакеты идут
	// параллельно, машина загружена, и трёх секунд иногда не хватало.
	// Ожидание не удлиняет прогон: цикл выходит, как только задание
	// завершилось.
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		job, ok := d.Store.Get(jobID)
		if ok && job.State != provision.StateRunning {
			return job
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("задание не завершилось за отведённое время")
	return provision.Job{}
}

// Вторая тревога по тому же роутеру, пока идёт первая починка: движок
// отказывает ErrAlreadyRunning, люди получают одно «не запускалась», а первая
// починка доходит до конца как ни в чём не бывало.
func TestAutostart_SecondHardWhileRunning(t *testing.T) {
	e := newLadder(t, &Setting{Enabled: true})
	fixOn(e.cmd, "tunnel_restart", 1, true)
	release := make(chan struct{})
	entered := make(chan struct{})
	restart := e.cmd.on["tunnel_restart"]
	e.cmd.on["route_status"] = func(c *scriptCommander, n int) {
		if n == 1 {
			close(entered)
			<-release
		}
	}
	e.cmd.on["tunnel_restart"] = restart

	id, err := e.d.Start(ladderReq())
	if err != nil {
		t.Fatalf("первый запуск: %v", err)
	}
	<-entered
	second := ladderReq()
	second.CheckName = "tunnel_awg10"
	if _, err := e.d.Start(second); !errors.Is(err, ErrAlreadyRunning) {
		close(release)
		t.Fatalf("ждали ErrAlreadyRunning, получили %v", err)
	}
	close(release)
	if job := waitDone(t, e.d, id); job.State != provision.StateSuccess {
		t.Fatalf("первая починка: %s", job.State)
	}
	e.rep.final(t)
	_, calls := e.rep.snapshot()
	n := 0
	for _, c := range calls {
		if c.Kind == "notstarted" {
			n++
			if !strings.Contains(c.Text, "уже идёт") {
				t.Errorf("причина: %q", c.Text)
			}
		}
	}
	if n != 1 {
		t.Fatalf("«не запускалась» ждали один раз, было %d: %+v", n, calls)
	}
}

// A4.3: новая страна легла на роутер, но проверку не прошла -- настройка и
// происхождение всё равно указывают на неё (на роутере теперь её конфиг), с
// отметкой «не подтверждена»; подтверждённой она не записывается.
func TestLadder_RelocateVerifyFailedRecordsUnconfirmed(t *testing.T) {
	e := newLadder(t, &Setting{Enabled: true, Provider: "amnezia", Option: "nl", AllowRelocate: true})
	e.src.options = []Option{{ID: "nl", Issued: true}, {ID: "de", Label: "Германия"}}

	job, final := e.run(t, ladderReq())

	if job.State != provision.StateFailed || final.Kind != "need" {
		t.Fatalf("state=%s final=%+v", job.State, final)
	}
	if s := e.savedOptions(); len(s) != 0 {
		t.Fatalf("непроверенная страна записана как подтверждённая: %+v", s)
	}
	if u := e.unconfirmedOptions(); len(u) != 1 || u[0] != (savedOption{1, "awg12", "amnezia", "de"}) {
		t.Fatalf("на роутере «de», а запись об источнике отстала: %+v", u)
	}
}

// A4.3: перебор локаций «HideMy.name» -- запись идёт за последней, что легла
// на роутер.
func TestLadder_RelocateVerifyFailedFollowsLastImported(t *testing.T) {
	e := newLadder(t, &Setting{Enabled: true, Provider: "hidemyname", Option: "nl", AllowRelocate: true})
	e.src.options = issuedOpts("de", "fi")

	_, _ = e.run(t, ladderReq())

	u := e.unconfirmedOptions()
	if len(u) == 0 || u[len(u)-1].Option != "fi" {
		t.Fatalf("последняя легла «fi», запись: %+v", u)
	}
}

// A4.3: конфиг на роутер не лёг (агент отказал) -- записывать нечего.
func TestLadder_RelocateImportFailedNoUnconfirmed(t *testing.T) {
	e := newLadder(t, &Setting{Enabled: true, Provider: "amnezia", Option: "nl", AllowRelocate: true})
	e.src.options = []Option{{ID: "nl", Issued: true}, {ID: "de", Label: "Германия"}}
	e.cmd.refuse = map[string]bool{"tunnel_import": true}

	_, _ = e.run(t, ladderReq())

	if u := e.unconfirmedOptions(); len(u) != 0 {
		t.Fatalf("импорт не прошёл, а записано: %+v", u)
	}
}

// A4.6: подписка заполнена -- новую страну не выпускаем и единственную
// попытку не сжигаем: «нужно ваше участие: подписка заполнена».
func TestLadder_RelocateAmneziaFullSubscriptionKeepsAttempt(t *testing.T) {
	e := newLadder(t, &Setting{Enabled: true, Provider: "amnezia", Option: "nl", AllowRelocate: true})
	e.src.options = []Option{{ID: "nl", Issued: true}, {ID: "de", Label: "Германия"}}
	e.src.full = true

	job, final := e.run(t, ladderReq())

	if final.Kind != "need" || final.Action != ActSubscriptionFull {
		t.Fatalf("итог %+v, ждали need с ActSubscriptionFull", final)
	}
	if job.Hint != ActSubscriptionFull {
		t.Fatalf("подсказка %q", job.Hint)
	}
	if sp := e.spentMarks(); len(sp) != 0 {
		t.Fatalf("при полной подписке отметка поставлена: %v", sp)
	}
	for _, c := range e.src.got() {
		if c == "issue:amnezia:de" {
			t.Fatal("при полной подписке выпущена новая страна")
		}
	}
	if st := stepOf(job, StepRecreate); st.Status != provision.StepFailed || !strings.Contains(st.Detail, "заполнена") {
		t.Fatalf("ступень 3: %+v", st)
	}
}

// A4.6: кабинет не ответил про место -- тоже не тратим попытку.
func TestLadder_RelocateAmneziaRoomUnknownKeepsAttempt(t *testing.T) {
	e := newLadder(t, &Setting{Enabled: true, Provider: "amnezia", Option: "nl", AllowRelocate: true})
	e.src.options = []Option{{ID: "nl", Issued: true}, {ID: "de", Label: "Германия"}}
	e.src.errs = map[string]error{"room:amnezia": &NeedHuman{Cause: errors.New("кабинет молчит"), Action: ActAmneziaKey}}

	_, final := e.run(t, ladderReq())

	if final.Kind != "need" || final.Action != ActAmneziaKey {
		t.Fatalf("итог %+v", final)
	}
	if sp := e.spentMarks(); len(sp) != 0 {
		t.Fatalf("без ответа о месте отметка поставлена: %v", sp)
	}
}

// promotes -- увод/возврат по наборам: «набор:туннель» по порядку.
func (c *scriptCommander) promotes() []string {
	var out []string
	for _, s := range c.actions("route_policy_promote") {
		out = append(out, fmt.Sprint(s.Args["policy_name"], ":", s.Args["tunnel_id"]))
	}
	return out
}

// A4.5: VPN-туннель -- активное звено в двух наборах правил: трафик уводится
// и возвращается в обоих. Набор, где он резерв, и набор без него не трогаются.
func TestLadder_FailoverEveryPolicyWhereActive(t *testing.T) {
	e := newLadder(t, &Setting{Enabled: true})
	e.cmd.extra = []wire.RoutePolicySummary{
		{Name: "Policy0", Interfaces: []wire.RoutePolicyInterface{
			{Bind: "OpkgTun12", Name: "Дача", TunnelID: "awg12", Role: "active", Available: false, Order: 1},
			{Bind: "OpkgTun10", Name: "Работа", TunnelID: "awg10", Role: "fallback", Available: true, Order: 2},
			{Bind: "OpkgTun14", Name: "Склад", TunnelID: "awg14", Role: "fallback", Available: true, Order: 3},
		}},
		{Name: "Policy1", Interfaces: []wire.RoutePolicyInterface{
			{Bind: "OpkgTun10", Name: "Работа", TunnelID: "awg10", Role: "active", Available: true, Order: 1},
			{Bind: "OpkgTun12", Name: "Дача", TunnelID: "awg12", Role: "unavailable", Available: false, Order: 2},
		}},
		{Name: "Policy2", Interfaces: []wire.RoutePolicyInterface{
			{Bind: "OpkgTun14", Name: "Склад", TunnelID: "awg14", Role: "active", Available: true, Order: 1},
		}},
	}
	fixOn(e.cmd, "tunnel_restart", 1, true)

	job, final := e.run(t, ladderReq())

	if job.State != provision.StateSuccess || final.Kind != "done" {
		t.Fatalf("state=%s final=%+v", job.State, final)
	}
	want := []string{"HydraRoute:awg10", "Policy0:awg10", "HydraRoute:awg12", "Policy0:awg12"}
	if got := e.cmd.promotes(); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("увод/возврат: %v, ждали %v", got, want)
	}
	if !strings.Contains(final.Text, "«Работа»") {
		t.Fatalf("итог без запасного: %q", final.Text)
	}
}

// A4.5: возврат не удался в одном из наборов -- человеку сказано, что трафик
// остался на запасном.
func TestLadder_FailbackPartialFailure(t *testing.T) {
	e := newLadder(t, &Setting{Enabled: true})
	e.cmd.extra = []wire.RoutePolicySummary{
		{Name: "Policy0", Interfaces: []wire.RoutePolicyInterface{
			{Bind: "OpkgTun12", Name: "Дача", TunnelID: "awg12", Role: "active", Available: false, Order: 1},
			{Bind: "OpkgTun14", Name: "Склад", TunnelID: "awg14", Role: "fallback", Available: true, Order: 2},
		}},
	}
	fixOn(e.cmd, "tunnel_restart", 1, true)
	// Четвёртый promote (возврат во втором наборе) роутер отклоняет: крючок
	// срабатывает при постановке команды, до чтения ответа на неё.
	e.cmd.on["route_policy_promote"] = func(c *scriptCommander, n int) {
		if n == 4 {
			c.refuse = map[string]bool{"route_policy_promote": true}
		}
	}

	job, final := e.run(t, ladderReq())

	if job.State != provision.StateSuccess {
		t.Fatalf("state=%s", job.State)
	}
	if !strings.Contains(final.Text, "вернуть на него трафик не вышло") || !strings.Contains(final.Text, "«Склад»") {
		t.Fatalf("итог: %q", final.Text)
	}
	if st := stepOf(job, StepFailback); st.Status != provision.StepFailed {
		t.Fatalf("шаг возврата: %+v", st)
	}
}
