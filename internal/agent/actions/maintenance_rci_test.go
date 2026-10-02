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
		if path == "/rci/components/list" {
			// Проверка «есть ли что ставить» перед commit: по умолчанию статус
			// не получен -- установка идёт как раньше. См. withList.
			return nil, errors.New("rci: HTTP 500")
		}
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
	// Ответ СИНТЕТИЧЕСКИЙ: форма записи об ошибке -- по общему виду ответов
	// RCI, текст выдуман; настоящий отказ commit на роутере не снимался.
	answer := `{"status":[{"status":"error","code":"0","ident":"Synthetic::Test","message":"synthetic refusal for the test."}]}`
	exec, rci, calls := fakeFirmware(answer, nil, logBefore, logBefore)
	_, err := InstallFirmware(context.Background(), exec, rci)
	if err == nil || !strings.Contains(err.Error(), "synthetic refusal for the test.") {
		t.Fatalf("err = %v", err)
	}
	if countCalls(*calls, "-c components commit") != 0 {
		t.Fatalf("после отказа RCI commit повторён через ndmc: %v", *calls)
	}
	if countCalls(*calls, "-c show log 40") != 1 {
		t.Fatalf("после отказа RCI журнал смотреть незачем: %v", *calls)
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
	// 200 с ответом не-JSON: запрос ушёл, задача могла стартовать -- решает журнал.
	garbage := "<html>" + strings.Repeat("A", 300_000) + "TAILMARK</html>"
	exec, rci, calls := fakeFirmware(garbage, nil, logBefore, logBefore)
	msg, err := InstallFirmware(context.Background(), exec, rci)
	if err != nil || msg != FirmwareUnconfirmedMsg {
		t.Fatalf("%.80q %v", msg, err)
	}
	if strings.Contains(msg, "TAILMARK") || strings.Contains(msg, "AAAA") {
		t.Fatalf("тело утекло в ответ")
	}
	if countCalls(*calls, "-c components commit") != 0 {
		t.Fatalf("calls = %v", *calls)
	}
}

func TestInstallFirmware_RCIEmptyAnswerWatchesLog(t *testing.T) {
	noWait(t)
	after := logBefore + "I [Oct 02 11:32:34] ndm: Core::System::RebootManager: started a reboot process.\n"
	exec, rci, calls := fakeFirmware("", nil, logBefore, after)
	msg, err := InstallFirmware(context.Background(), exec, rci)
	if err != nil || msg != FirmwareStartedMsg {
		t.Fatalf("%q %v", msg, err)
	}
	if countCalls(*calls, "-c components commit") != 0 {
		t.Fatalf("calls = %v", *calls)
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

// Сбой ПОСЛЕ отправки commit (таймаут, 5xx, оборванное чтение): задача на
// роутере могла стартовать. Ошибкой это не называем и второй раз через ndmc
// не запускаем -- смотрим журнал.
func TestInstallFirmware_RCIOtherErrorIsErrorWithoutNdmc(t *testing.T) {
	noWait(t)
	exec, rci, calls := fakeFirmware("", errors.New("rci: HTTP 500"), logBefore, logBefore)
	msg, err := InstallFirmware(context.Background(), exec, rci)
	if err != nil || msg != FirmwareUnconfirmedMsg {
		t.Fatalf("%q %v", msg, err)
	}
	if countCalls(*calls, "-c components commit") != 0 {
		t.Fatalf("calls = %v", *calls)
	}
}

func TestInstallFirmware_RCITimeoutThenRebootIsStarted(t *testing.T) {
	noWait(t)
	after := logBefore +
		"I [Oct 02 11:31:54] ndm: Components::Manager: update task started.\n" +
		"I [Oct 02 11:32:34] ndm: Core::System::RebootManager: activated reboot.\n"
	exec, rci, calls := fakeFirmware("", errors.New("rci /rci/components/commit: context deadline exceeded"), logBefore, after)
	// Журнал «после» отдаётся и без подтверждённого commit: запрос ушёл.
	committedExec := func(ctx context.Context, name string, args ...string) ([]byte, error) {
		if strings.Join(args, " ") == "-c show log 40" && countCalls(*calls, rciCommitCall) > 0 {
			return []byte(after), nil
		}
		return exec(ctx, name, args...)
	}
	msg, err := InstallFirmware(context.Background(), committedExec, rci)
	if err != nil || msg != FirmwareStartedMsg {
		t.Fatalf("%q %v", msg, err)
	}
	if countCalls(*calls, "-c components commit") != 0 {
		t.Fatalf("calls = %v", *calls)
	}
}

func TestInstallFirmware_RCITimeoutThenFailureIsInterrupted(t *testing.T) {
	noWait(t)
	after := logBefore + "W [Oct 02 11:31:54] ndm: Components::Manager: update interrupted.\n"
	exec, rci, calls := fakeFirmware("", errors.New("rci: timeout"), logBefore, after)
	committedExec := func(ctx context.Context, name string, args ...string) ([]byte, error) {
		if strings.Join(args, " ") == "-c show log 40" && countCalls(*calls, rciCommitCall) > 0 {
			return []byte(after), nil
		}
		return exec(ctx, name, args...)
	}
	_, err := InstallFirmware(context.Background(), committedExec, rci)
	if err == nil || !strings.HasPrefix(err.Error(), FirmwareInterrupted+": ") {
		t.Fatalf("err = %v", err)
	}
}

// 401/403/405: роутер запрос не принял -- задача не стартовала, это отказ.
func TestInstallFirmware_RCIRejectedIsErrorWithoutNdmc(t *testing.T) {
	noWait(t)
	exec, rci, calls := fakeFirmware("", fmt.Errorf("rci: HTTP 401: %w", ErrRCIRejected), logBefore, logBefore)
	_, err := InstallFirmware(context.Background(), exec, rci)
	if err == nil || !strings.Contains(err.Error(), "HTTP 401") {
		t.Fatalf("err = %v", err)
	}
	if countCalls(*calls, "-c components commit") != 0 {
		t.Fatalf("calls = %v", *calls)
	}
}

// --- «ставить нечего» ---

// withList подставляет ответ на components/list перед commit.
func withList(rci RCIFunc, answer string, asked *int) RCIFunc {
	return func(ctx context.Context, method, path string, body []byte) ([]byte, error) {
		if path == "/rci/components/list" {
			*asked++
			return []byte(answer), nil
		}
		return rci(ctx, method, path, body)
	}
}

func TestInstallFirmware_UpToDateDoesNotCommit(t *testing.T) {
	noWait(t)
	noListWait(t)
	exec, rci, calls := fakeFirmware(rciCommitStarted, nil, logBefore, logBefore)
	asked := 0
	_, err := InstallFirmware(context.Background(), exec, withList(rci, rciListNoUpdate, &asked))
	if err == nil || err.Error() != FirmwareUpToDate+": 5.02.B.0.0-0" {
		t.Fatalf("err = %v", err)
	}
	if asked != 1 {
		t.Fatalf("статус спрошен %d раз", asked)
	}
	if countCalls(*calls, rciCommitCall) != 0 || countCalls(*calls, "-c components commit") != 0 {
		t.Fatalf("commit ушёл, хотя ставить нечего: %v", *calls)
	}
}

func TestInstallFirmware_UpdateAvailableCommits(t *testing.T) {
	noWait(t)
	noListWait(t)
	exec, rci, calls := fakeFirmware(rciCommitStarted, nil, logBefore, logBefore)
	asked := 0
	msg, err := InstallFirmware(context.Background(), exec, withList(rci, rciListHasUpdate, &asked))
	if err != nil || msg != FirmwareStartedMsg {
		t.Fatalf("%q %v", msg, err)
	}
	if countCalls(*calls, rciCommitCall) != 1 {
		t.Fatalf("calls = %v", *calls)
	}
}

func TestInstallFirmware_StatusFailureStillCommits(t *testing.T) {
	noWait(t)
	noListWait(t)
	exec, rci, calls := fakeFirmware(rciCommitStarted, nil, logBefore, logBefore)
	asked := 0
	// Сервер обновлений молчит: статус неизвестен -- поведение прежнее.
	msg, err := InstallFirmware(context.Background(), exec, withList(rci, `{"firmware":{"version":"5.02.B.0.0-0"},"sandbox":"stable"}`, &asked))
	if err != nil || msg != FirmwareStartedMsg {
		t.Fatalf("%q %v", msg, err)
	}
	if countCalls(*calls, rciCommitCall) != 1 {
		t.Fatalf("calls = %v", *calls)
	}
}

func TestFirmwareWatchWindows(t *testing.T) {
	// Строки о перезагрузке приходят ~через 40 с после commit (роутер 02.10):
	// окно пути RCI -- 25 взглядов по 2 с; старый путь ndmc -- прежние 10.
	if firmwareWatchRCI.total != 25 || firmwareWatch.total != 10 {
		t.Fatalf("rci=%d ndmc=%d", firmwareWatchRCI.total, firmwareWatch.total)
	}
}

func TestInstallFirmware_RCIWatchesLongerThanNdmc(t *testing.T) {
	oldN, oldR := firmwareWatch, firmwareWatchRCI
	nop := func(context.Context) error { return nil }
	firmwareWatch = firmwareWatchCfg{total: 2, sleep: nop}
	firmwareWatchRCI = firmwareWatchCfg{total: 7, sleep: nop}
	t.Cleanup(func() { firmwareWatch, firmwareWatchRCI = oldN, oldR })
	exec, rci, calls := fakeFirmware(`{}`, nil, logBefore, logBefore)
	if _, err := InstallFirmware(context.Background(), exec, rci); err != nil {
		t.Fatal(err)
	}
	if got := countCalls(*calls, "-c show log 40"); got != 1+7 {
		t.Fatalf("путь RCI: взглядов в журнал %d, ждали 8", got)
	}
	exec, _, calls = fakeFirmware("", nil, logBefore, logBefore)
	if _, err := InstallFirmware(context.Background(), exec, nil); err != nil {
		t.Fatal(err)
	}
	if got := countCalls(*calls, "-c show log 40"); got != 1+2 {
		t.Fatalf("путь ndmc: взглядов в журнал %d, ждали 3", got)
	}
}

func TestInstallFirmware_NdmcFallbackFailureCarriesNote(t *testing.T) {
	noWait(t)
	after := logBefore +
		"I [Oct 02 11:31:54] ndm: Components::Manager: update task started.\n" +
		"E [Oct 02 11:31:54] ndm: Core::Ndss: [7758] cannot connect to the server.\n" +
		"W [Oct 02 11:31:54] ndm: Components::Manager: update interrupted.\n"
	exec, rci, _ := fakeFirmware("", ErrRCIUnreachable, logBefore, after)
	_, err := InstallFirmware(context.Background(), exec, rci)
	want := "firmware update interrupted; rci unavailable, started via ndmc: "
	if err == nil || !strings.HasPrefix(err.Error(), want) || !strings.HasPrefix(err.Error(), FirmwareInterrupted) {
		t.Fatalf("err = %v", err)
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

// Настоящие строки журнала перед перезагрузкой (роутер, KeenOS 5.02, 02.10).
func TestInstallFirmware_RealGoingLinesAreStarted(t *testing.T) {
	for _, line := range []string{
		"I [Oct 02 11:32:33] ndm: Core::System::RebootManager: activated reboot.",
		"I [Oct 02 11:32:33] ndm: Core::System::Update::RunningConfig: firmware update...",
		"I [Oct 02 11:32:34] ndm: Core::System::RebootManager: started a reboot process.",
	} {
		noWait(t)
		exec, rci, _ := fakeFirmware(`{}`, nil, logBefore, logBefore+line+"\n")
		msg, err := InstallFirmware(context.Background(), exec, rci)
		if err != nil || msg != FirmwareStartedMsg {
			t.Fatalf("%s: %q %v", line, msg, err)
		}
	}
}

func TestInstallFirmware_LookalikeLinesAreNotStarted(t *testing.T) {
	for _, line := range []string{
		"I [Oct 02 11:32:30] ndm: Core::System::Firmware: firmware update started.",
		"I [Oct 02 11:32:30] ndm: Core::System::RebootManager: reboot schedule cleared.",
		"W [Oct 02 11:32:30] ndm: Components::Manager: firmware update is not available.",
	} {
		noWait(t)
		exec, rci, _ := fakeFirmware(`{}`, nil, logBefore, logBefore+line+"\n")
		msg, err := InstallFirmware(context.Background(), exec, rci)
		if err != nil || msg != FirmwareUnconfirmedMsg {
			t.Fatalf("%s: %q %v", line, msg, err)
		}
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
		FirmwareUpToDate:       "firmware is up to date",
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
