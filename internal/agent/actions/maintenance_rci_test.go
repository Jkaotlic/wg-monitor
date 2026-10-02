package actions

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/Jkaotlic/wg-monitor/internal/agent/awgmgr"
	"github.com/Jkaotlic/wg-monitor/pkg/wire"
)

// Ответы живого роутера (KeenOS 5.02, 02.10): commit через локальный RCI.
const rciCommitStarted = `{"continued": true, "status":[{"status":"message","code":"24249020","ident":"Components::Manager","message":"update task started."}]}`

const rciListHasUpdate = `{"firmware":{"version":"5.02.B.0.0-0","title":"5.2 Beta 0"},"sandbox":"draft","local":{"sandbox":"draft","version":"5.02.A.9.0-0","title":"5.2 Alpha 9"},"component":{"afp":{"version":"3.1.18-1"},"chilli":{"version":"1.6-1"}}}`

const rciListNoUpdate = `{"firmware":{"version":"5.02.B.0.0-0","title":"5.2 Beta 0"},"sandbox":"stable","local":{"sandbox":"stable","version":"5.02.B.0.0-0","title":"5.2 Beta 0"},"component":{}}`

// fakeFirmware -- exec (журнал, старый commit) и RCI (новый commit) с общим
// состоянием: журнал «после» отдаётся, как только commit прошёл любым путём.
// commitAnswer -- ответ RCI на components/commit; commitErr -- его ошибка.
func fakeFirmware(commitAnswer string, commitErr error, before string, after ...string) (ExecFunc, RCIFunc, *[]string) {
	var calls []string
	n := 0
	committed := false
	exec := func(ctx context.Context, name string, args ...string) ([]byte, error) {
		cmd := strings.Join(args, " ")
		calls = append(calls, cmd)
		switch cmd {
		case "-c components commit":
			committed = true
			return nil, nil
		case "-c show log 40":
			if !committed {
				return []byte(before), nil
			}
			if n < len(after) {
				n++
			}
			return []byte(after[n-1]), nil
		}
		return nil, fmt.Errorf("unexpected %q", cmd)
	}
	rci := func(ctx context.Context, method, path string, body []byte) ([]byte, error) {
		calls = append(calls, "rci "+method+" "+path+" "+string(body))
		if path != "/rci/components/commit" {
			return nil, fmt.Errorf("unexpected rci %q", path)
		}
		if commitErr != nil {
			return nil, commitErr
		}
		committed = true
		return []byte(commitAnswer), nil
	}
	return exec, rci, &calls
}

func countCalls(calls []string, want string) int {
	n := 0
	for _, c := range calls {
		if c == want {
			n++
		}
	}
	return n
}

const rciCommitCall = "rci POST /rci/components/commit {}"

func TestInstallFirmware_RCICommitStarted(t *testing.T) {
	noWait(t)
	exec, rci, calls := fakeFirmware(rciCommitStarted, nil, logBefore, logBefore, logBefore, logBefore)
	msg, err := InstallFirmware(context.Background(), exec, rci)
	if err != nil || msg != FirmwareStartedMsg {
		t.Fatalf("%q %v", msg, err)
	}
	if countCalls(*calls, rciCommitCall) != 1 {
		t.Fatalf("commit через RCI не один раз: %v", *calls)
	}
	if countCalls(*calls, "-c components commit") != 0 {
		t.Fatalf("commit ушёл ещё и через ndmc: %v", *calls)
	}
	if (*calls)[0] != "-c show log 40" {
		t.Fatalf("журнал «до» не прочитан перед commit: %v", *calls)
	}
}

func TestInstallFirmware_RCIContinuedAloneIsStarted(t *testing.T) {
	noWait(t)
	exec, rci, _ := fakeFirmware(`{"continued": true}`, nil, logBefore, logBefore)
	msg, err := InstallFirmware(context.Background(), exec, rci)
	if err != nil || msg != FirmwareStartedMsg {
		t.Fatalf("%q %v", msg, err)
	}
}

func TestInstallFirmware_RCIErrorObjectIsError(t *testing.T) {
	noWait(t)
	answer := `{"status":[{"status":"error","code":"24248321","ident":"Components::Manager","message":"nothing to commit."}]}`
	exec, rci, calls := fakeFirmware(answer, nil, logBefore, logBefore)
	_, err := InstallFirmware(context.Background(), exec, rci)
	if err == nil || !strings.Contains(err.Error(), "nothing to commit.") {
		t.Fatalf("err = %v", err)
	}
	if countCalls(*calls, "-c components commit") != 0 {
		t.Fatalf("после отказа RCI commit повторён через ndmc: %v", *calls)
	}
}

func TestInstallFirmware_RCIErrorMessageIsExcerpt(t *testing.T) {
	noWait(t)
	long := strings.Repeat("очень длинный текст ", 400)
	b, _ := json.Marshal(map[string]any{"status": []map[string]string{{"status": "error", "message": long}}})
	exec, rci, _ := fakeFirmware(string(b), nil, logBefore, logBefore)
	_, err := InstallFirmware(context.Background(), exec, rci)
	if err == nil {
		t.Fatal("ждали ошибку")
	}
	if n := len([]rune(err.Error())); n > 260 {
		t.Fatalf("ошибка в %d знаков -- ответ утёк целиком", n)
	}
}

func TestInstallFirmware_RCIGarbageNeverLeaksIntoError(t *testing.T) {
	noWait(t)
	garbage := "<html>" + strings.Repeat("A", 300_000) + "TAILMARK</html>"
	exec, rci, _ := fakeFirmware(garbage, nil, logBefore, logBefore)
	_, err := InstallFirmware(context.Background(), exec, rci)
	if err == nil {
		t.Fatal("ждали ошибку на ответ не-JSON")
	}
	if strings.Contains(err.Error(), "TAILMARK") || len([]rune(err.Error())) > 300 {
		t.Fatalf("тело утекло: длина %d", len(err.Error()))
	}
}

func TestInstallFirmware_RCIUnreachableFallsBackToNdmc(t *testing.T) {
	noWait(t)
	after := logBefore + "I [Oct 02 11:31:54] ndm: Components::Manager: update task started.\n"
	exec, rci, calls := fakeFirmware("", fmt.Errorf("dial: %w", ErrRCIUnreachable), logBefore, after, after, after)
	msg, err := InstallFirmware(context.Background(), exec, rci)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if countCalls(*calls, "-c components commit") != 1 {
		t.Fatalf("запасной путь ndmc не использован: %v", *calls)
	}
	// Старый путь не доказывает установку: «update task started» в журнале
	// появляется и при оборванной задаче. Отвечаем известной мини-аппу меткой.
	if !strings.HasPrefix(msg, FirmwareUnconfirmedMsg) {
		t.Fatalf("msg = %q", msg)
	}
}

func TestInstallFirmware_NilRCIUsesNdmc(t *testing.T) {
	noWait(t)
	exec, _, calls := fakeFirmware("", nil, logBefore, logBefore)
	msg, err := InstallFirmware(context.Background(), exec, nil)
	if err != nil || !strings.HasPrefix(msg, FirmwareUnconfirmedMsg) {
		t.Fatalf("%q %v", msg, err)
	}
	if countCalls(*calls, "-c components commit") != 1 {
		t.Fatalf("calls = %v", *calls)
	}
}

func TestInstallFirmware_RCIOtherErrorIsErrorWithoutNdmc(t *testing.T) {
	noWait(t)
	exec, rci, calls := fakeFirmware("", errors.New("rci: HTTP 500"), logBefore, logBefore)
	_, err := InstallFirmware(context.Background(), exec, rci)
	if err == nil || !strings.Contains(err.Error(), "HTTP 500") {
		t.Fatalf("err = %v", err)
	}
	if countCalls(*calls, "-c components commit") != 0 {
		t.Fatalf("calls = %v", *calls)
	}
}

func TestInstallFirmware_RebootLineIsStartedAtOnce(t *testing.T) {
	noWait(t)
	after := logBefore + "I [Oct 02 11:32:34] ndm: Core::System::RebootManager: started a reboot process.\n"
	// RCI ответил без подтверждения старта -- решает журнал.
	exec, rci, calls := fakeFirmware(`{}`, nil, logBefore, after, after, after)
	msg, err := InstallFirmware(context.Background(), exec, rci)
	if err != nil || msg != FirmwareStartedMsg {
		t.Fatalf("%q %v", msg, err)
	}
	if got := countCalls(*calls, "-c show log 40"); got != 2 {
		t.Fatalf("после строки о перезагрузке окно не закрыто: взглядов в журнал %d", got)
	}
}

func TestInstallFirmware_FirmwareUpdateLineIsStarted(t *testing.T) {
	noWait(t)
	after := logBefore + "I [Oct 02 11:32:30] ndm: Core::System::Firmware: firmware update started.\n"
	exec, rci, _ := fakeFirmware(`{}`, nil, logBefore, after)
	msg, err := InstallFirmware(context.Background(), exec, rci)
	if err != nil || msg != FirmwareStartedMsg {
		t.Fatalf("%q %v", msg, err)
	}
}

func TestInstallFirmware_NdmcFallbackRebootLineIsStarted(t *testing.T) {
	noWait(t)
	after := logBefore + "I [Oct 02 11:32:34] ndm: Core::System::RebootManager: started a reboot process.\n"
	exec, rci, _ := fakeFirmware("", ErrRCIUnreachable, logBefore, after)
	msg, err := InstallFirmware(context.Background(), exec, rci)
	if err != nil || msg != FirmwareStartedMsg {
		t.Fatalf("%q %v", msg, err)
	}
}

func TestInstallFirmware_OldRebootLineIsNotStarted(t *testing.T) {
	noWait(t)
	old := "I [Oct 01 09:00:00] ndm: Core::System::RebootManager: started a reboot process.\n"
	exec, rci, _ := fakeFirmware(`{}`, nil, old, old, old, old)
	msg, err := InstallFirmware(context.Background(), exec, rci)
	if err != nil || msg != FirmwareUnconfirmedMsg {
		t.Fatalf("%q %v", msg, err)
	}
}

func TestInstallFirmware_RCIStartedThenLogFailureIsInterrupted(t *testing.T) {
	noWait(t)
	after := logBefore +
		"I [Oct 02 11:31:54] ndm: Components::Manager: update task started.\n" +
		"E [Oct 02 11:31:54] ndm: Core::Ndss: [7758] cannot connect to the server.\n" +
		"E [Oct 02 11:31:54] ndm: Components::UpdateTask: request failed (0).\n" +
		"W [Oct 02 11:31:54] ndm: Components::Manager: update interrupted.\n"
	exec, rci, _ := fakeFirmware(rciCommitStarted, nil, logBefore, after)
	_, err := InstallFirmware(context.Background(), exec, rci)
	if err == nil || !strings.HasPrefix(err.Error(), FirmwareInterrupted) || !strings.Contains(err.Error(), "cannot connect to the server") {
		t.Fatalf("err = %v", err)
	}
}

func TestInstallFirmware_RCIPreReadFailureIsUnconfirmed(t *testing.T) {
	noWait(t)
	_, rci, _ := fakeFirmware(rciCommitStarted, nil, logBefore, logBefore)
	exec := func(ctx context.Context, name string, args ...string) ([]byte, error) {
		return nil, errors.New("exit status 1")
	}
	msg, err := InstallFirmware(context.Background(), exec, rci)
	if err != nil || msg != FirmwareUnconfirmedMsg {
		t.Fatalf("%q %v", msg, err)
	}
}

func TestFirmwareResultPrefixesAreStable(t *testing.T) {
	// Мини-апп разбирает эти начала строк дословно (miniapp/src/maintenance.js).
	for got, want := range map[string]string{
		FirmwareStartedMsg:     "firmware download started; router will reboot when done",
		FirmwareUnconfirmedMsg: "firmware install kicked; not confirmed by router log",
		FirmwareInterrupted:    "firmware update interrupted",
		FirmwareServerSilent:   "firmware server did not answer",
	} {
		if got != want {
			t.Fatalf("%q != %q", got, want)
		}
	}
}

// --- firmware_status через RCI ---

func noListWait(t *testing.T) *int {
	t.Helper()
	sleeps := 0
	old := firmwareListRetry
	firmwareListRetry = firmwareWatchCfg{total: 5, sleep: func(context.Context) error { sleeps++; return nil }}
	t.Cleanup(func() { firmwareListRetry = old })
	return &sleeps
}

func listRCI(answers ...string) (RCIFunc, *int) {
	n := 0
	return func(ctx context.Context, method, path string, body []byte) ([]byte, error) {
		if method != "POST" || path != "/rci/components/list" || string(body) != "{}" {
			return nil, fmt.Errorf("unexpected rci %s %s %q", method, path, body)
		}
		i := n
		if i >= len(answers) {
			i = len(answers) - 1
		}
		n++
		return []byte(answers[i]), nil
	}, &n
}

func failExec(t *testing.T) ExecFunc {
	return func(ctx context.Context, name string, args ...string) ([]byte, error) {
		t.Errorf("ndmc не должен вызываться: %s %v", name, args)
		return nil, errors.New("unexpected")
	}
}

func TestGetFirmwareStatus_RCIHasUpdate(t *testing.T) {
	noListWait(t)
	rci, n := listRCI(rciListHasUpdate)
	fs, err := GetFirmwareStatus(context.Background(), failExec(t), rci)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	want := wire.FirmwareStatus{Current: "5.02.A.9.0-0", Available: "5.02.B.0.0-0", Channel: "draft"}
	if fs != want {
		t.Fatalf("fs = %+v, want %+v", fs, want)
	}
	if *n != 1 {
		t.Fatalf("запросов %d", *n)
	}
}

func TestGetFirmwareStatus_RCINoUpdate(t *testing.T) {
	noListWait(t)
	rci, _ := listRCI(rciListNoUpdate)
	fs, err := GetFirmwareStatus(context.Background(), failExec(t), rci)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if fs.Current != "5.02.B.0.0-0" || fs.Available != "" || fs.Channel != "stable" {
		t.Fatalf("fs = %+v", fs)
	}
}

func TestGetFirmwareStatus_RCIContinuedThenFull(t *testing.T) {
	sleeps := noListWait(t)
	rci, n := listRCI(`{"continued": true}`, `{"continued": true}`, rciListHasUpdate)
	fs, err := GetFirmwareStatus(context.Background(), failExec(t), rci)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if fs.Current != "5.02.A.9.0-0" || fs.Available != "5.02.B.0.0-0" {
		t.Fatalf("fs = %+v", fs)
	}
	if *n != 3 || *sleeps != 2 {
		t.Fatalf("запросов %d, пауз %d", *n, *sleeps)
	}
}

func TestGetFirmwareStatus_RCIContinuedForeverGivesUp(t *testing.T) {
	noListWait(t)
	rci, n := listRCI(`{"continued": true}`)
	_, err := GetFirmwareStatus(context.Background(), failExec(t), rci)
	if err == nil {
		t.Fatal("ждали ошибку")
	}
	if *n != 5 {
		t.Fatalf("запросов %d, ждали 5", *n)
	}
	if strings.HasPrefix(err.Error(), FirmwareServerSilent) {
		t.Fatalf("«список ещё грузится» выдан за молчание сервера: %v", err)
	}
}

func TestGetFirmwareStatus_RCIFirmwareWithoutLocalMeansServerSilent(t *testing.T) {
	noListWait(t)
	rci, _ := listRCI(`{"firmware":{"version":"5.02.B.0.0-0"},"sandbox":"stable"}`)
	_, err := GetFirmwareStatus(context.Background(), failExec(t), rci)
	if err == nil || !strings.HasPrefix(err.Error(), FirmwareServerSilent) {
		t.Fatalf("err = %v", err)
	}
}

func TestGetFirmwareStatus_RCIErrorObject(t *testing.T) {
	noListWait(t)
	rci, _ := listRCI(`{"status":[{"status":"error","ident":"Core::Ndss","message":"cannot connect to the server."}]}`)
	_, err := GetFirmwareStatus(context.Background(), failExec(t), rci)
	if err == nil || !strings.Contains(err.Error(), "cannot connect to the server.") {
		t.Fatalf("err = %v", err)
	}
}

func TestGetFirmwareStatus_RCIUnreachableFallsBackToNdmc(t *testing.T) {
	noListWait(t)
	rci := func(ctx context.Context, method, path string, body []byte) ([]byte, error) {
		return nil, fmt.Errorf("dial tcp 127.0.0.1:79: connection refused: %w", ErrRCIUnreachable)
	}
	ndmc := 0
	exec := func(ctx context.Context, name string, args ...string) ([]byte, error) {
		if name == "ndmc" && strings.Join(args, " ") == "-c components list" {
			ndmc++
			return []byte(ndmcComponentsListGolden_HasUpdate), nil
		}
		return nil, fmt.Errorf("unexpected %s %v", name, args)
	}
	fs, err := GetFirmwareStatus(context.Background(), exec, rci)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if ndmc != 1 || fs.Current != "5.00.C.11.0-0" || fs.Available != "5.00.C.12.0-0" {
		t.Fatalf("ndmc=%d fs=%+v", ndmc, fs)
	}
}

func TestGetFirmwareStatus_RCIOtherErrorIsError(t *testing.T) {
	noListWait(t)
	rci := func(ctx context.Context, method, path string, body []byte) ([]byte, error) {
		return nil, errors.New("rci: HTTP 500")
	}
	_, err := GetFirmwareStatus(context.Background(), failExec(t), rci)
	if err == nil || !strings.Contains(err.Error(), "HTTP 500") {
		t.Fatalf("err = %v", err)
	}
}

func TestGetFirmwareStatus_RCIBodyNeverLeaksIntoError(t *testing.T) {
	noListWait(t)
	// 260 КБ компонентов без блока local и без firmware -- в ошибку ни байта лишнего.
	big := `{"sandbox":"stable","component":{"x":{"title":"` + strings.Repeat("B", 260_000) + `TAILMARK"}}}`
	garbage := strings.Repeat("G", 260_000) + "TAILMARK"
	for name, body := range map[string]string{"json": big, "garbage": garbage} {
		rci, _ := listRCI(body)
		_, err := GetFirmwareStatus(context.Background(), failExec(t), rci)
		if err == nil {
			t.Fatalf("%s: ждали ошибку", name)
		}
		if strings.Contains(err.Error(), "TAILMARK") || len([]rune(err.Error())) > 320 {
			t.Fatalf("%s: тело утекло, длина ошибки %d", name, len(err.Error()))
		}
	}
}

func TestVersionAudit_FirmwareFromRCI(t *testing.T) {
	noListWait(t)
	awg := &fakeAwgInfo{sysInfo: awgmgr.SystemInfo{Version: "2.19.9", FirmwareVersion: "5.02.A.9.0-0"}}
	exec := func(ctx context.Context, name string, args ...string) ([]byte, error) {
		if name == "ndmc" {
			t.Errorf("ndmc не должен вызываться: %v", args)
		}
		return nil, errors.New("n/a")
	}
	rci, _ := listRCI(rciListHasUpdate)
	va, err := VersionAudit(context.Background(), awg, exec, rci)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if va.FirmwareCurrent != "5.02.A.9.0-0" || va.FirmwareAvail != "5.02.B.0.0-0" {
		t.Fatalf("va = %+v", va)
	}
}
