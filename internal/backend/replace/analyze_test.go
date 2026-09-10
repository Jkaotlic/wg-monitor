package replace

import (
	"encoding/base64"
	"strings"
	"testing"

	"github.com/Jkaotlic/wg-monitor/internal/backend/provision"
	"github.com/Jkaotlic/wg-monitor/pkg/wire"
)

// Шаг «проверяем конфиг» между выпуском и импортом: awg-manager 2.18.2 умеет
// сказать, примет ли модуль роутера конфиг, ещё до того, как на роутере
// что-то появится. Ошибки анализа останавливают замену до импорта; замечания
// идут в описание шага; старый агент и старая панель проверку пропускают, но
// замену не срывают.

const (
	analyzeErrorsOut      = `{"supported":true,"version":"awg1.5","errors":[{"code":"h_overlap","message":"H1 и H2 пересекаются (1, 1): модуль отвергает такой конфиг"}],"warnings":[]}`
	analyzeWarningsOut    = `{"supported":true,"version":"awg1.0","errors":[],"warnings":[{"code":"module_old","message":"модуль роутера старше, часть параметров будет проигнорирована"}]}`
	analyzeUnsupportedOut = `{"supported":false}`
)

func analyzeReplies(analyze wire.CommandResult) map[string]wire.CommandResult {
	return map[string]wire.CommandResult{
		"tunnel_analyze":   analyze,
		"tunnel_import":    {Status: "ok", Output: `✅ Туннель "amnezia_nl" создан (id=awg21)`},
		"check_via_tunnel": {Status: "ok", Output: "Exit IP: 203.0.113.19"},
		"check_direct":     {Status: "ok", Output: "Exit IP: 203.0.113.7"},
	}
}

func stepOf(job provision.Job, name string) provision.Step {
	for _, s := range job.Steps {
		if s.Name == name {
			return s
		}
	}
	return provision.Step{}
}

func sent(cmd *fakeCommander, action string) bool {
	for _, a := range cmd.actions() {
		if a == action {
			return true
		}
	}
	return false
}

func TestReplace_AnalyzeErrorsStopBeforeImport(t *testing.T) {
	cmd := &fakeCommander{replies: analyzeReplies(wire.CommandResult{Status: "ok", Output: analyzeErrorsOut})}
	notes := &noteLog{}
	d := deps(t, cmd, fakeCabinet{conf: []byte("[Interface]\n")}, &fakeOrigin{}, notes)

	id, err := d.Start(startReq())
	if err != nil {
		t.Fatal(err)
	}
	job := waitJob(t, d, id)
	if job.State != provision.StateFailed {
		t.Fatalf("state=%s hint=%q", job.State, job.Hint)
	}
	if st := stepOf(job, "analyze"); st.Status != provision.StepFailed || !strings.Contains(st.Detail, "H1 и H2 пересекаются") {
		t.Fatalf("шаг проверки: %+v", st)
	}
	if sent(cmd, "tunnel_import") {
		t.Fatal("конфиг, который модуль отвергнет, не должен доходить до импорта")
	}
	// В анализ ушёл тот же конфиг, что пошёл бы в импорт.
	if got := cmd.argsOf("tunnel_analyze")["conf"]; got != base64.StdEncoding.EncodeToString([]byte("[Interface]\n")) {
		t.Fatalf("в анализ ушёл не тот конфиг: %v", got)
	}
	if !strings.Contains(job.Hint, "H1 и H2 пересекаются") {
		t.Fatalf("подсказка не называет причину: %q", job.Hint)
	}
	assertOwnerVocabulary(t, ownerReads(job, notes.wait(t, 1)))
}

func TestReplace_AnalyzeWarningsGoOn(t *testing.T) {
	cmd := &fakeCommander{replies: analyzeReplies(wire.CommandResult{Status: "ok", Output: analyzeWarningsOut})}
	d := deps(t, cmd, fakeCabinet{conf: []byte("[Interface]\n")}, &fakeOrigin{}, &noteLog{})

	id, _ := d.Start(startReq())
	job := waitJob(t, d, id)
	if job.State != provision.StateSuccess {
		t.Fatalf("замечания не должны срывать замену: state=%s hint=%q", job.State, job.Hint)
	}
	if st := stepOf(job, "analyze"); st.Status != provision.StepDone || !strings.Contains(st.Detail, "часть параметров будет проигнорирована") {
		t.Fatalf("замечание не видно в шаге: %+v", st)
	}
}

// Старый агент не знает команды, старая панель — ручки. Проверить нечем, но
// это не повод срывать замену: шаг помечается пропущенным, импорт идёт.
func TestReplace_AnalyzeSkippedOnOldAgentOrPanel(t *testing.T) {
	for name, reply := range map[string]wire.CommandResult{
		"старый агент":  {Status: "err", Output: "unknown action: tunnel_analyze"},
		"старая панель": {Status: "ok", Output: analyzeUnsupportedOut},
	} {
		t.Run(name, func(t *testing.T) {
			cmd := &fakeCommander{replies: analyzeReplies(reply)}
			d := deps(t, cmd, fakeCabinet{conf: []byte("[Interface]\n")}, &fakeOrigin{}, &noteLog{})
			id, _ := d.Start(startReq())
			job := waitJob(t, d, id)
			if job.State != provision.StateSuccess {
				t.Fatalf("state=%s hint=%q", job.State, job.Hint)
			}
			if !sent(cmd, "tunnel_import") {
				t.Fatal("импорт не состоялся")
			}
			if st := stepOf(job, "analyze"); st.Status != provision.StepDone || !strings.Contains(st.Detail, "пропуска") {
				t.Fatalf("шаг проверки на %s: %+v", name, st)
			}
		})
	}
}
