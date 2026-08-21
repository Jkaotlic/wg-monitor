package replace

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Jkaotlic/wg-monitor/internal/backend/provision"
	"github.com/Jkaotlic/wg-monitor/pkg/wire"
)

// fakeCommander -- очередь команд в тесте: запоминает, что уехало агенту, и
// отвечает по сценарию.
type fakeCommander struct {
	mu             sync.Mutex
	sent           []wire.Command
	replies        map[string]wire.CommandResult // по действию
	handshakeAfter int                           // сколько раз route_status ответит «рукопожатия нет»
	statusCalls    int
}

func (f *fakeCommander) Enqueue(_ int64, cmd wire.Command) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sent = append(f.sent, cmd)
	return nil
}

func (f *fakeCommander) AwaitResult(_ context.Context, _ int64, id string, _ time.Duration) (*wire.CommandResult, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var action string
	for _, c := range f.sent {
		if c.ID == id {
			action = c.Action
		}
	}
	if action == "route_status" {
		f.statusCalls++
		snap := wire.RouteSnapshot{Tunnels: []wire.TunnelMeta{
			{ID: "awg21", Name: "amnezia_nl", HasHandshake: f.statusCalls > f.handshakeAfter},
			{ID: "awg11", Name: "old"},
		}}
		b, _ := json.Marshal(snap)
		return &wire.CommandResult{ID: id, Status: "ok", Output: string(b)}, true
	}
	if res, ok := f.replies[action]; ok {
		res.ID = id
		return &res, true
	}
	return &wire.CommandResult{ID: id, Status: "ok", Output: "готово"}, true
}

func (f *fakeCommander) actions() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, 0, len(f.sent))
	for _, c := range f.sent {
		out = append(out, c.Action)
	}
	return out
}

func (f *fakeCommander) argsOf(action string) map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.sent {
		if c.Action == action {
			return c.Args
		}
	}
	return nil
}

type fakeCabinet struct {
	conf []byte
	err  error
}

func (f fakeCabinet) IssueConfig(context.Context, int64, string, string) (Issued, error) {
	if f.err != nil {
		return Issued{}, f.err
	}
	return Issued{TunnelName: "amnezia_nl", Conf: f.conf, Backend: "nativewg"}, nil
}

type fakeOrigin struct {
	mu   sync.Mutex
	rows []string
}

func (f *fakeOrigin) Record(routerID int64, tunnelID, tunnelName, provider, option string, _ time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rows = append(f.rows, strings.Join([]string{tunnelID, tunnelName, provider, option}, "/"))
	return nil
}

func deps(t *testing.T, cmd *fakeCommander, cab Cabinet, origin *fakeOrigin, notes *[]string) Deps {
	t.Helper()
	var mu sync.Mutex
	return Deps{
		Store:    provision.NewStore(),
		Commands: cmd,
		Cabinet:  cab,
		Origin:   origin,
		Notify: func(_ context.Context, _ int64, text string) {
			mu.Lock()
			defer mu.Unlock()
			*notes = append(*notes, text)
		},
		BaseCtx:        context.Background(),
		AwaitStep:      time.Second,
		HandshakeTries: 3,
		HandshakeWait:  time.Millisecond,
		Sleep:          func(context.Context, time.Duration) {},
	}
}

func waitJob(t *testing.T, d Deps, id string) provision.Job {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		job, ok := d.Store.Get(id)
		if ok && job.State != provision.StateRunning {
			return job
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("задание не завершилось за отведённое время")
	return provision.Job{}
}

func startReq() StartReq {
	return StartReq{
		RouterID: 1, Nickname: "testkeen",
		Provider: "amnezia", OptionID: "nl",
		OldTunnelID: "awg11", PolicyName: "HydraRoute",
	}
}

// Счастливый путь: шесть шагов, прежний туннель выключен и НЕ удалён,
// происхождение записано, в топик ушло уведомление.
func TestReplace_HappyPath(t *testing.T) {
	cmd := &fakeCommander{replies: map[string]wire.CommandResult{
		"tunnel_import":    {Status: "ok", Output: `✅ Туннель "amnezia_nl" создан (id=awg21)`},
		"check_via_tunnel": {Status: "ok", Output: "Exit IP: 203.0.113.19"},
		"check_direct":     {Status: "ok", Output: "Exit IP: 203.0.113.7"},
	}}
	origin := &fakeOrigin{}
	var notes []string
	d := deps(t, cmd, fakeCabinet{conf: []byte("[Interface]\n")}, origin, &notes)

	id, err := d.Start(startReq())
	if err != nil {
		t.Fatal(err)
	}
	job := waitJob(t, d, id)
	if job.State != provision.StateSuccess {
		t.Fatalf("state=%s hint=%s steps=%+v", job.State, job.Hint, job.Steps)
	}
	for _, s := range job.Steps {
		if s.Status != provision.StepDone {
			t.Errorf("шаг %s = %s (%s)", s.Name, s.Status, s.Detail)
		}
	}
	// Конфиг уехал агенту -- и только агенту.
	importArgs := cmd.argsOf("tunnel_import")
	if importArgs["conf"] != base64.StdEncoding.EncodeToString([]byte("[Interface]\n")) {
		t.Fatalf("агенту не тот конфиг: %+v", importArgs)
	}
	// Прежний туннель НЕ удаляется, а выключается.
	if got := cmd.argsOf("tunnel_power"); got["tunnel_id"] != "awg11" || got["on"] != false {
		t.Fatalf("прежний туннель обязан быть выключен, а не удалён: %+v", got)
	}
	for _, a := range cmd.actions() {
		if a == "tunnel_delete" {
			t.Fatal("удаление туннеля недопустимо ни при каком исходе")
		}
	}
	if len(origin.rows) != 1 || !strings.HasPrefix(origin.rows[0], "awg21/amnezia_nl/amnezia/nl") {
		t.Fatalf("происхождение конфига не записано: %+v", origin.rows)
	}
	if len(notes) != 1 || !strings.Contains(notes[0], "amnezia_nl") {
		t.Fatalf("в топик не ушло внятное уведомление: %+v", notes)
	}
}

// Рукопожатия нет: до политики дело не доходит, новый туннель выключается,
// прежний не трогается вовсе.
func TestReplace_NoHandshakeRollsBack(t *testing.T) {
	cmd := &fakeCommander{
		replies:        map[string]wire.CommandResult{"tunnel_import": {Status: "ok", Output: "создан (id=awg21)"}},
		handshakeAfter: 99,
	}
	var notes []string
	d := deps(t, cmd, fakeCabinet{conf: []byte("[Interface]\n")}, &fakeOrigin{}, &notes)

	id, _ := d.Start(startReq())
	job := waitJob(t, d, id)
	if job.State != provision.StateFailed {
		t.Fatalf("state=%s", job.State)
	}
	if !strings.Contains(job.Hint, "рукопожат") {
		t.Fatalf("подсказка не называет причину: %q", job.Hint)
	}
	for _, a := range cmd.actions() {
		if a == "route_policy_promote" {
			t.Fatal("до политики дело доходить не должно: линия не поднялась")
		}
	}
	power := cmd.argsOf("tunnel_power")
	if power["tunnel_id"] != "awg21" || power["on"] != false {
		t.Fatalf("новый туннель обязан быть выключен откатом: %+v", power)
	}
}

// Адрес выхода не сменился -- значит трафик через новый туннель не идёт.
// Откат обязан вернуть политику прежнему туннелю.
func TestReplace_SameExitIPRollsBackPolicy(t *testing.T) {
	cmd := &fakeCommander{replies: map[string]wire.CommandResult{
		"tunnel_import":    {Status: "ok", Output: "создан (id=awg21)"},
		"check_via_tunnel": {Status: "ok", Output: "Exit IP: 203.0.113.7"},
		"check_direct":     {Status: "ok", Output: "Exit IP: 203.0.113.7"},
	}}
	var notes []string
	d := deps(t, cmd, fakeCabinet{conf: []byte("[Interface]\n")}, &fakeOrigin{}, &notes)

	id, _ := d.Start(startReq())
	job := waitJob(t, d, id)
	if job.State != provision.StateFailed {
		t.Fatalf("state=%s hint=%s", job.State, job.Hint)
	}
	if !strings.Contains(job.Hint, "тот же адрес") {
		t.Fatalf("подсказка не объясняет провал: %q", job.Hint)
	}
	// Политику вернули прежнему туннелю: promote ушёл дважды, второй -- на старый.
	promotes := 0
	var lastPromote map[string]any
	cmd.mu.Lock()
	for _, c := range cmd.sent {
		if c.Action == "route_policy_promote" {
			promotes++
			lastPromote = c.Args
		}
	}
	cmd.mu.Unlock()
	if promotes != 2 || lastPromote["tunnel_id"] != "awg11" {
		t.Fatalf("откат политики не сделан: promotes=%d last=%+v", promotes, lastPromote)
	}
	if !strings.Contains(strings.Join(notes, " "), "не удалась") {
		t.Fatalf("в топик не ушло сообщение о провале: %+v", notes)
	}
}

// Одна замена на роутер: вторая не заводится, а честно говорит, что идёт первая.
func TestReplace_OneAtATime(t *testing.T) {
	cmd := &fakeCommander{
		replies:        map[string]wire.CommandResult{"tunnel_import": {Status: "ok", Output: "создан (id=awg21)"}},
		handshakeAfter: 99,
	}
	var notes []string
	d := deps(t, cmd, fakeCabinet{conf: []byte("[Interface]\n")}, &fakeOrigin{}, &notes)
	d.HandshakeWait = 50 * time.Millisecond
	d.Sleep = func(ctx context.Context, dur time.Duration) { time.Sleep(dur) }

	if _, err := d.Start(startReq()); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Start(startReq()); err == nil {
		t.Fatal("вторая замена не должна запускаться")
	} else if !strings.Contains(err.Error(), "уже идёт") {
		t.Fatalf("err = %v", err)
	}
}

// Кабинет отказал -- на роутер не уходит ничего вовсе.
func TestReplace_CabinetFailureTouchesNothing(t *testing.T) {
	cmd := &fakeCommander{}
	var notes []string
	d := deps(t, cmd, fakeCabinet{err: context.DeadlineExceeded}, &fakeOrigin{}, &notes)

	id, _ := d.Start(startReq())
	job := waitJob(t, d, id)
	if job.State != provision.StateFailed {
		t.Fatalf("state=%s", job.State)
	}
	if len(cmd.actions()) != 0 {
		t.Fatalf("роутер не должен был получить ни одной команды: %v", cmd.actions())
	}
}

// Мастер не спрашивал, умеет ли агент то, что ему предстоит. На роутере с
// v0.14.4 это кончалось хуже, чем отказом: шаг 1 успевал ВЫПУСТИТЬ конфиг в
// платном кабинете, шаг 2 -- завести на роутере новый туннель, и только на
// promote агент отвечал «не знаю такой команды». Откат при этом опирался на
// tunnel_power -- команду, которой у старого агента тоже нет, -- поэтому
// лишний туннель оставался на роутере, а конфиг был потрачен.
//
// Отказ обязан случиться ДО первого необратимого шага.
func TestReplace_RefusesOldAgent(t *testing.T) {
	cmd := &fakeCommander{}
	cab := &countingCabinet{}
	var notes []string
	d := deps(t, cmd, cab, &fakeOrigin{}, &notes)

	req := startReq()
	req.AgentVersion = "v0.14.4"
	id, err := d.Start(req)

	if !errors.Is(err, ErrAgentTooOld) {
		t.Fatalf("ожидали ErrAgentTooOld, получили %v (job=%q)", err, id)
	}
	if id != "" {
		t.Fatalf("задание не должно заводиться: %q", id)
	}
	if cab.calls != 0 {
		t.Fatalf("кабинет не должен был выпускать конфиг: вызовов %d", cab.calls)
	}
	// Замок обязан остаться свободным: иначе один отказ запирал бы роутер на
	// весь TTL задания.
	if !d.Store.TryLock(req.Nickname) {
		t.Fatal("замок роутера остался занят после отказа")
	}
	if !strings.Contains(err.Error(), "0.14.4") {
		t.Errorf("в отказе нет текущей версии: %v", err)
	}
}

func TestReplace_AllowsCurrentAndUnknownAgent(t *testing.T) {
	for _, v := range []string{"v0.18.0", "v0.19.3", "", "неизвестно"} {
		cmd := &fakeCommander{replies: map[string]wire.CommandResult{
			"tunnel_import":    {Status: "ok", Output: `✅ Туннель "amnezia_nl" создан (id=awg21)`},
			"check_via_tunnel": {Status: "ok", Output: "Exit IP: 203.0.113.19"},
			"check_direct":     {Status: "ok", Output: "Exit IP: 203.0.113.7"},
		}}
		var notes []string
		d := deps(t, cmd, fakeCabinet{conf: []byte("[Interface]\n")}, &fakeOrigin{}, &notes)
		req := startReq()
		req.AgentVersion = v
		// Неизвестную версию не запрещаем: агент мог не сообщить её вовсе, а
		// отказ по незнанию перекрыл бы работу исправным роутерам.
		id, err := d.Start(req)
		if err != nil {
			t.Fatalf("версия %q: %v", v, err)
		}
		waitJob(t, d, id)
	}
}

type countingCabinet struct{ calls int }

func (c *countingCabinet) IssueConfig(_ context.Context, _ int64, _, _ string) (Issued, error) {
	c.calls++
	return Issued{Conf: []byte("[Interface]\n"), TunnelName: "amnezia_nl"}, nil
}
