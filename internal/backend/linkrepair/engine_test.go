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

	noBackup bool                                       // в наборе только awg12
	snapshot string                                     // ответ route_status вместо обычного
	silent   map[string]bool                            // действия, на которые роутер молчит
	refuse   map[string]bool                            // действия, которым агент отказывает
	on       map[string]func(c *scriptCommander, n int) // крючок на n-й вызов действия (под замком)
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
			{ID: "awg12", Name: "Дача", HasHandshake: c.hs},
			{ID: "awg10", Name: "Работа", HasHandshake: true},
		},
	}
	ifaces := []wire.RoutePolicyInterface{
		{Bind: "OpkgTun12", Name: "Дача", TunnelID: "awg12", Role: "active", Available: false, Order: 1},
	}
	if !c.noBackup {
		ifaces = append(ifaces, wire.RoutePolicyInterface{Bind: "OpkgTun10", Name: "Работа", TunnelID: "awg10", Role: "fallback", Available: true, Order: 2})
	}
	snap.Policies = []wire.RoutePolicySummary{{Name: "HydraRoute", Interfaces: ifaces}}
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
	options []string
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

func (s *fakeSource) Options(_ context.Context, _ int64, provider string) ([]string, error) {
	if err := s.rec("options:" + provider); err != nil {
		return nil, err
	}
	return s.options, nil
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
}

func newLadder(t *testing.T, set *Setting) *ladderEnv {
	t.Helper()
	e := &ladderEnv{cmd: newScript(), src: &fakeSource{}, rep: &recReporter{}}
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
	if got := e.src.got(); len(got) != 1 || got[0] != "issue:amnezia:nl" {
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
	e.src.options = []string{"nl", "de", "fi"}

	job, final := e.run(t, ladderReq())

	if job.State != provision.StateFailed {
		t.Fatalf("state=%s", job.State)
	}
	for _, c := range e.src.got() {
		if strings.HasPrefix(c, "options:") {
			t.Fatalf("варианты спрошены без разрешения менять локацию: %v", e.src.got())
		}
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
	e := newLadder(t, &Setting{Enabled: true, Provider: "amnezia", Option: "nl", AllowRelocate: true})
	e.src.options = []string{"nl", "de", "fi"}
	fixOn(e.cmd, "tunnel_import", 2, true)

	job, final := e.run(t, ladderReq())

	if job.State != provision.StateSuccess || final.Kind != "done" {
		t.Fatalf("state=%s final=%+v", job.State, final)
	}
	got := e.src.got()
	want := []string{"issue:amnezia:nl", "options:amnezia", "issue:amnezia:de"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("источник: %v, ждали %v", got, want)
	}
	if s := e.savedOptions(); len(s) != 1 || s[0] != (savedOption{1, "awg12", "amnezia", "de"}) {
		t.Fatalf("удачная локация не записана: %+v", s)
	}
	if !strings.Contains(final.Text, "«de»") {
		t.Fatalf("итог не говорит о смене локации: %q", final.Text)
	}
}

// Больше двух других локаций не пробуем: третья смена страны подряд --
// уже не починка, а перебор.
func TestLadder_RelocateAtMostTwo(t *testing.T) {
	e := newLadder(t, &Setting{Enabled: true, Provider: "amnezia", Option: "nl", AllowRelocate: true})
	e.src.options = []string{"de", "nl", "fi", "se"}

	job, final := e.run(t, ladderReq())

	if job.State != provision.StateFailed || final.Action != ActServerDead {
		t.Fatalf("state=%s final=%+v", job.State, final)
	}
	got := e.src.got()
	want := []string{"issue:amnezia:nl", "options:amnezia", "issue:amnezia:de", "issue:amnezia:fi"}
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
	for _, c := range e.src.got() {
		if strings.HasPrefix(c, "options:") {
			t.Fatal("кабинет отказал -- менять локацию через него же бессмысленно")
		}
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

	if job.State != provision.StateFailed || final.Kind != "need" || final.Action != ActServerDead {
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
		name   string
		setup  func(*scriptCommander)
		want   []string
		forbid []string
		action bool
	}{
		{"молчит на снимке", func(c *scriptCommander) {
			c.silent = map[string]bool{"route_status": true}
		}, []string{"не ответил", "запасной VPN-туннель"}, []string{"Заблокированное"}, true},
		{"снимок не разобрался", func(c *scriptCommander) {
			c.snapshot = "<html>502 Bad Gateway</html>"
		}, []string{"непонятн"}, nil, true},
		{"отказал уводу на резерв", func(c *scriptCommander) {
			c.refuse = map[string]bool{"route_policy_promote": true}
		}, []string{"запасной VPN-туннель «Работа»", "ошибкой"}, nil, false},
		{"VPN-туннель вне наборов", func(c *scriptCommander) {
			c.snapshot = `{"tunnels":[{"id":"awg12","name":"Дача"}],"policies":[]}`
		}, []string{"VPN-туннель «Дача»", "чинить нечего", "ничего не идёт"}, []string{"Заблокированное"}, false},
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
			if n := len(e.cmd.actions("tunnel_restart")); n != 0 {
				t.Fatalf("без снимка или увода ушёл перезапуск: %d", n)
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
	_, calls := e.rep.snapshot()
	if len(calls) != 1 || calls[0].Kind != "notstarted" || !strings.Contains(calls[0].Text, "6 часов") {
		t.Fatalf("причина не сказана: %+v", calls)
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
	_, calls := e.rep.snapshot()
	if len(calls) != 1 || calls[0].Kind != "notstarted" || !strings.Contains(calls[0].Text, "уже идёт") {
		t.Fatalf("замок: %+v", calls)
	}

	e2 := newLadder(t, &Setting{Enabled: true})
	req := ladderReq()
	req.AgentVersion = "v0.14.4"
	if _, err := e2.d.Start(req); !errors.Is(err, replace.ErrAgentTooOld) {
		t.Fatalf("ждали ErrAgentTooOld, получили %v", err)
	}
	_, calls = e2.rep.snapshot()
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
