package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

const piDockerYAML = `db_path: /data/state.db
telegram:
  bot_token_file: /secrets/bot-token.txt
  admin_user_id: 42
wizard:
  token_file: /secrets/wizard-token.txt
revive:
  key_file: /secrets/revive.key
`

func piState() *State {
	st := &State{}
	st.Backend.Host = "198.51.100.10"
	st.Backend.User = "root"
	st.Backend.SourceBind = "198.51.100.2"
	return st
}

func inspectPiArchive(t *testing.T, members map[string]string) *RestoreBackup {
	t.Helper()
	archive := writeRestoreBackupArchive(t, members)
	b, cleanup, err := InspectRestoreBackupForDocker(archive, "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(cleanup)
	return b
}

func piArchiveMembers() map[string]string {
	return map[string]string{
		"state.db":             "sqlite bytes",
		"backend.yaml":         piDockerYAML,
		"manifest.txt":         "name=wg-monitor-full-backup\ncreated_utc=20261026T010203Z\n",
		"awg3-panels.json":     `{"panels":[]}`,
		"hidemyname.json":      `{}`,
		"revive.key":           "old key bytes",
		"amnezia-premium.json": `{}`,
	}
}

func stubRestoreConfirm(t *testing.T, answer bool) *int {
	t.Helper()
	n := 0
	prev := restoreConfirm
	restoreConfirm = func(string, bool) bool {
		n++
		return answer
	}
	stubStdinTerminal(t, true)
	t.Cleanup(func() { restoreConfirm = prev })
	return &n
}

func stubStdinTerminal(t *testing.T, tty bool) {
	t.Helper()
	prev := restoreStdinIsTerminal
	restoreStdinIsTerminal = func() bool { return tty }
	t.Cleanup(func() { restoreStdinIsTerminal = prev })
}

func shortHealthWait(t *testing.T) {
	t.Helper()
	prev, prevPi := verifyHTTPTimeout, piHealthTimeout
	verifyHTTPTimeout, piHealthTimeout = 10*time.Millisecond, 10*time.Millisecond
	t.Cleanup(func() { verifyHTTPTimeout, piHealthTimeout = prev, prevPi })
}

// piReply -- ответы «здорового» Pi: контейнер есть, база цела, /healthz 200.
func piReply(integrity, http string) func(string) (string, string, int, error) {
	return func(cmd string) (string, string, int, error) {
		switch {
		case strings.Contains(cmd, "docker inspect"):
			return "true\n", "", 0, nil
		case strings.Contains(cmd, "PRAGMA integrity_check"):
			return integrity, "", 0, nil
		case strings.Contains(cmd, "http_code"):
			return http, "", 0, nil
		case strings.Contains(cmd, "df -Pk"):
			return "4096 10485760\n", "", 0, nil // база 4 КБ, свободно 10 ГБ
		case strings.HasPrefix(cmd, "test -e "):
			return "", "", 1, nil
		}
		return "", "", 0, nil
	}
}

func TestRestoreToPiDeclinedMakesNoSSH(t *testing.T) {
	asks := stubRestoreConfirm(t, false)
	connects := stubRestoreConnect(t, newFakeRemote())
	b := inspectPiArchive(t, piArchiveMembers())
	if err := restoreBackupToCurrentVPS(piState(), nil, b, false); err != nil {
		t.Fatalf("отказ оператора -- не ошибка: %v", err)
	}
	if *asks != 1 {
		t.Fatalf("подтверждение спрошено %d раз", *asks)
	}
	if *connects != 0 {
		t.Fatalf("SSH до подтверждения: %d подключений", *connects)
	}
}

func TestRestoreToPiRunsManualProcedureInOrder(t *testing.T) {
	stubRestoreConfirm(t, true)
	fake := newFakeRemote()
	fake.reply = piReply("ok\n", "200")
	stubRestoreConnect(t, fake)
	b := inspectPiArchive(t, piArchiveMembers())

	if err := restoreBackupToCurrentVPS(piState(), nil, b, false); err != nil {
		t.Fatalf("restore: %v\n%s", err, strings.Join(fake.calls, "\n"))
	}
	order := []string{
		"docker inspect",
		"rm -rf '/root/wg-monitor/data/restore-tmp'",
		"upload: /root/wg-monitor/data/restore-tmp/state.db mode=600",
		"upload: /root/wg-monitor/data/restore-tmp/awg3-panels.json mode=600",
		"PRAGMA integrity_check",
		"run: docker stop wg-monitor-backend",           // останов -- только на копирование и замену
		"mv -f data/restore-tmp/state.db data/state.db", // скрипт замены; его порядок -- отдельный тест
		"run: docker start wg-monitor-backend",
		"'http://127.0.0.1:8080/healthz'",
	}
	last := -1
	for _, want := range order {
		i := -1
		for j := last + 1; j < len(fake.calls); j++ {
			if strings.Contains(fake.calls[j], want) {
				i = j
				break
			}
		}
		if i < 0 {
			t.Fatalf("нет %q после шага %d:\n%s", want, last, strings.Join(fake.calls, "\n"))
		}
		last = i
	}
	all := strings.Join(fake.calls, "\n")
	if strings.Contains(all, "revive.key") {
		t.Fatalf("revive.key трогать нельзя:\n%s", all)
	}
	if !fake.closed {
		t.Fatal("SSH не закрыт")
	}
}

func TestPiRestoreScriptKeepsOldDBWithWalBeforeReplacing(t *testing.T) {
	script := buildPiRestoreScript("/root/wg-monitor", "20261026T010203Z", []string{"awg3-panels.json"})
	steps := []string{
		"cd '/root/wg-monitor'",
		"chmod 600 data/restore-tmp/state.db",
		"chown --reference=data data/restore-tmp/state.db",
		"chmod 600 data/restore-tmp/awg3-panels.json",
		"chown --reference=data data/restore-tmp/awg3-panels.json",
		"cp -p data/state.db data/state.db.bak.20261026T010203Z",
		"cp -p data/state.db-wal data/state.db.bak.20261026T010203Z-wal",
		"cp -p data/state.db-shm data/state.db.bak.20261026T010203Z-shm",
		"cp -p data/awg3-panels.json data/awg3-panels.json.bak.20261026T010203Z",
		"replaced=1",
		"rm -f data/state.db-wal data/state.db-shm",
		"mv -f data/restore-tmp/state.db data/state.db",
		"mv -f data/restore-tmp/awg3-panels.json data/awg3-panels.json",
	}
	last := -1
	for _, s := range steps {
		i := strings.Index(script[last+1:], s)
		if i < 0 {
			t.Fatalf("нет %q (после позиции %d):\n%s", s, last, script)
		}
		last += 1 + i
	}
	// Ловушка выхода: откат только после начала замены, контейнер -- всегда.
	if !strings.Contains(script, "docker start wg-monitor-backend >/dev/null 2>&1 || true; fi; exit $rc' EXIT") {
		t.Fatalf("нет ловушки выхода:\n%s", script)
	}
	if strings.Index(script, "trap 'rc=") > strings.Index(script, "chmod 600") {
		t.Fatalf("ловушка взводится после первого шага:\n%s", script)
	}
	if strings.Contains(script, "revive.key") || strings.Contains(script, "secrets/") {
		t.Fatalf("скрипт не трогает secrets/:\n%s", script)
	}
}

func TestRestoreToPiIntegrityFailureNeverStopsBackend(t *testing.T) {
	stubRestoreConfirm(t, true)
	fake := newFakeRemote()
	fake.reply = piReply("*** in database main ***\nPage 7: btreeInitPage() returns error code 11\n", "200")
	stubRestoreConnect(t, fake)
	b := inspectPiArchive(t, piArchiveMembers())

	err := restoreBackupToCurrentVPS(piState(), nil, b, false)
	if err == nil || !strings.Contains(err.Error(), "integrity_check") {
		t.Fatalf("err=%v, want отказ по integrity_check", err)
	}
	all := strings.Join(fake.calls, "\n")
	for _, bad := range []string{"mv -f", ".bak.", "rm -f data/state.db-wal"} {
		if strings.Contains(all, bad) {
			t.Fatalf("при битой базе есть замена (%q):\n%s", bad, all)
		}
	}
	if !strings.Contains(err.Error(), "бэкенд не останавливался") {
		t.Fatalf("текст: %v", err)
	}
	if fake.indexOfCall("docker stop") >= 0 || fake.indexOfCall("docker start") >= 0 {
		t.Fatalf("при битом архиве бэкенд не трогается вовсе:\n%s", all)
	}
	if fake.indexOfCall("rm -rf '/root/wg-monitor/data/restore-tmp'") < 0 {
		t.Fatalf("распакованное не убрано:\n%s", all)
	}
}

func TestRestoreToPiHealthFailureTellsHowToReturnBak(t *testing.T) {
	stubRestoreConfirm(t, true)
	shortHealthWait(t)
	fake := newFakeRemote()
	fake.reply = piReply("ok\n", "000")
	stubRestoreConnect(t, fake)
	b := inspectPiArchive(t, piArchiveMembers())

	err := restoreBackupToCurrentVPS(piState(), nil, b, false)
	if err == nil {
		t.Fatal("провал /healthz -- ошибка")
	}
	for _, want := range []string{"docker logs wg-monitor-backend", "state.db.bak.", "-wal", "docker start wg-monitor-backend"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("подсказка без %q: %v", want, err)
		}
	}
	msg := err.Error()
	first := "бэкенд ещё не ответил — возможно, он ещё запускается: посмотрите docker logs wg-monitor-backend, прежде чем откатывать"
	if i := strings.Index(msg, first); i < 0 || i > strings.Index(msg, "cp -p data/state.db.bak.") {
		t.Fatalf("сначала «ещё запускается», потом рецепт отката: %v", msg)
	}
}

func TestRestoreToPiWaitsForSlowBackend(t *testing.T) {
	if piHealthTimeout != 60*time.Second {
		t.Fatalf("ожидание /healthz на Pi %v, want 60s", piHealthTimeout)
	}
	stubRestoreConfirm(t, true)
	shortHealthWait(t)
	prev := piHealthTimeout
	piHealthTimeout = 5 * time.Second
	t.Cleanup(func() { piHealthTimeout = prev })
	fake := newFakeRemote()
	polls := 0
	fake.reply = func(cmd string) (string, string, int, error) {
		if strings.Contains(cmd, "http_code") {
			polls++
			if polls < 3 {
				return "000", "", 0, nil // контейнер ещё поднимается
			}
		}
		return piReply("ok\n", "200")(cmd)
	}
	stubRestoreConnect(t, fake)
	b := inspectPiArchive(t, piArchiveMembers())
	if err := restoreBackupToCurrentVPS(piState(), nil, b, false); err != nil {
		t.Fatalf("медленный старт -- не провал: %v", err)
	}
}

func TestRestoreToPiPreflightFailureStopsNothing(t *testing.T) {
	stubRestoreConfirm(t, true)
	fake := newFakeRemote()
	fake.reply = func(cmd string) (string, string, int, error) {
		if strings.Contains(cmd, "command -v sqlite3") {
			return "", "", 1, nil
		}
		return piReply("ok\n", "200")(cmd)
	}
	stubRestoreConnect(t, fake)
	b := inspectPiArchive(t, piArchiveMembers())
	err := restoreBackupToCurrentVPS(piState(), nil, b, false)
	if err == nil || !strings.Contains(err.Error(), "sqlite3") {
		t.Fatalf("err=%v", err)
	}
	if fake.indexOfCall("docker stop") >= 0 {
		t.Fatalf("контейнер остановлен, хотя проверка до остановки не прошла:\n%s", strings.Join(fake.calls, "\n"))
	}
}

func TestRestoreToPiStageFailureNeverStopsBackend(t *testing.T) {
	stubRestoreConfirm(t, true)
	fake := newFakeRemote()
	fake.reply = func(cmd string) (string, string, int, error) {
		if strings.Contains(cmd, "install -d -m 700") {
			return "", "", -1, errors.New("connection reset")
		}
		return piReply("ok\n", "200")(cmd)
	}
	stubRestoreConnect(t, fake)
	b := inspectPiArchive(t, piArchiveMembers())
	if err := restoreBackupToCurrentVPS(piState(), nil, b, false); err == nil || !strings.Contains(err.Error(), "бэкенд не останавливался") {
		t.Fatalf("err=%v", err)
	}
	if fake.indexOfCall("docker stop") >= 0 || fake.indexOfCall("docker start") >= 0 {
		t.Fatalf("выгрузка до останова: бэкенд не трогается:\n%s", strings.Join(fake.calls, "\n"))
	}
}

func TestRestoreToPiStopFailureStartsBackAndCleans(t *testing.T) {
	stubRestoreConfirm(t, true)
	fake := newFakeRemote()
	fake.reply = func(cmd string) (string, string, int, error) {
		if strings.HasPrefix(cmd, "docker stop") {
			return "", "timeout", 1, nil
		}
		return piReply("ok\n", "200")(cmd)
	}
	stubRestoreConnect(t, fake)
	b := inspectPiArchive(t, piArchiveMembers())
	if err := restoreBackupToCurrentVPS(piState(), nil, b, false); err == nil {
		t.Fatal("ожидалась ошибка")
	}
	all := strings.Join(fake.calls, "\n")
	stop := fake.indexOfCall("docker stop")
	if fake.indexOfCall("run: docker start wg-monitor-backend") < stop || strings.Contains(all, "mv -f") {
		t.Fatalf("после провала stop -- start и без замены:\n%s", all)
	}
	if strings.LastIndex(all, "rm -rf '/root/wg-monitor/data/restore-tmp'") < strings.Index(all, "docker stop") {
		t.Fatalf("распакованное не убрано после провала stop:\n%s", all)
	}
}

func TestRestoreToPiScriptFailureCodes(t *testing.T) {
	cases := []struct {
		rc          int
		want, wrong string
	}{
		{piScriptFailedBeforeReplace, "замена не начиналась, база не тронута", "возвращена"},
		{piScriptFailedAfterReplace, "прежняя база возвращена из копии state.db.bak.", "не тронута"},
	}
	for _, tc := range cases {
		stubRestoreConfirm(t, true)
		fake := newFakeRemote()
		fake.reply = func(cmd string) (string, string, int, error) {
			if strings.HasPrefix(cmd, "set -eu") {
				return "", "mv: cannot move 'x'\n", tc.rc, nil
			}
			return piReply("ok\n", "200")(cmd)
		}
		stubRestoreConnect(t, fake)
		b := inspectPiArchive(t, piArchiveMembers())
		err := restoreBackupToCurrentVPS(piState(), nil, b, false)
		if err == nil || !strings.Contains(err.Error(), tc.want) || strings.Contains(err.Error(), tc.wrong) {
			t.Fatalf("rc=%d: err=%v", tc.rc, err)
		}
		if !strings.Contains(err.Error(), "mv: cannot move 'x'") || strings.Contains(err.Error(), "set -eu") || strings.Contains(err.Error(), "rollback()") {
			t.Fatalf("rc=%d: в тексте нужен stderr, а не скрипт: %v", tc.rc, err)
		}
		if fake.indexOfCall("run: docker start wg-monitor-backend") < fake.indexOfCall("set -eu") {
			t.Fatalf("rc=%d: после провала скрипта -- docker start:\n%s", tc.rc, strings.Join(fake.calls, "\n"))
		}
	}
}

func TestPiRestoreScriptExitCodes(t *testing.T) {
	script := buildPiRestoreScript("/root/wg-monitor", "20261026T010203Z", nil)
	for _, want := range []string{"rc=10", "rc=11"} {
		if !strings.Contains(script, want) {
			t.Fatalf("нет %q:\n%s", want, script)
		}
	}
}
func TestInspectRestoreBackupForDockerPaths(t *testing.T) {
	b := inspectPiArchive(t, piArchiveMembers())
	names := map[string]string{}
	for _, e := range b.Extras {
		names[e.Name] = e.RemotePath
	}
	if names["awg3-panels.json"] != "/data/awg3-panels.json" || names["hidemyname.json"] != "/data/hidemyname.json" {
		t.Fatalf("extras=%#v", b.Extras)
	}

	vps := piArchiveMembers()
	vps["backend.yaml"] = validRestoreBackendYAML()
	archive := writeRestoreBackupArchive(t, vps)
	if _, _, err := InspectRestoreBackupForDocker(archive, ""); err == nil || !strings.Contains(err.Error(), "/data/state.db") {
		t.Fatalf("архив VPS на Pi: err=%v", err)
	}

	odd := piArchiveMembers()
	odd["backend.yaml"] = piDockerYAML + "hidemyname:\n  secrets_path: /data/stores/hidemyname.json\n"
	archive = writeRestoreBackupArchive(t, odd)
	if _, _, err := InspectRestoreBackupForDocker(archive, ""); err == nil || !strings.Contains(err.Error(), "hidemyname.json") {
		t.Fatalf("хранилище вне /data: err=%v", err)
	}
}

func TestActionRestoreBackupOnPiUsesDockerInspection(t *testing.T) {
	stubRestoreConfirm(t, true)
	fake := newFakeRemote()
	fake.reply = piReply("ok\n", "200")
	stubRestoreConnect(t, fake)
	archive := writeRestoreBackupArchive(t, piArchiveMembers())
	if err := actionRestoreBackup(piState(), nil, nil, RestoreBackupOptions{ArchivePath: archive, Mode: "current"}); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if fake.indexOfCall("mv -f data/restore-tmp/state.db data/state.db") < 0 {
		t.Fatalf("замена не дошла:\n%s", strings.Join(fake.calls, "\n"))
	}
}

func TestRestoreToNewVPSRefusesDockerStateBeforeAnything(t *testing.T) {
	connects := stubRestoreConnect(t, newFakeRemote())
	// dl = nil и secrets = nil: любой шаг дальше отказа упал бы паникой.
	err := restoreBackupToNewVPS(piState(), nil, nil, &RestoreBackup{})
	if !errors.Is(err, errRestoreNewVPSFromDocker) {
		t.Fatalf("err=%v, want errRestoreNewVPSFromDocker", err)
	}
	if *connects != 0 {
		t.Fatalf("подключений: %d", *connects)
	}
	for _, want := range []string{"Raspberry Pi", "/data", "Restore to current VPS"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("в тексте нет %q: %s", want, err)
		}
	}
}

func TestPiRestoreScriptRollbackIsAtomicAndReportsFailure(t *testing.T) {
	script := buildPiRestoreScript("/root/wg-monitor", "20261026T010203Z", []string{"awg3-panels.json"})
	for _, want := range []string{
		"{ cp -p data/state.db.bak.20261026T010203Z data/state.db.rb.20261026T010203Z && mv -f data/state.db.rb.20261026T010203Z data/state.db; } || rbfail=1",
		"{ cp -p data/awg3-panels.json.bak.20261026T010203Z data/awg3-panels.json.rb.20261026T010203Z && mv -f data/awg3-panels.json.rb.20261026T010203Z data/awg3-panels.json; } || rbfail=1",
		"if rollback; then rc=11; else rc=12; fi",
		`if [ "$rc" != 12 ]; then docker start wg-monitor-backend`,
	} {
		if !strings.Contains(script, want) {
			t.Fatalf("нет %q:\n%s", want, script)
		}
	}
}

func TestRestoreToPiRollbackFailureDoesNotStart(t *testing.T) {
	stubRestoreConfirm(t, true)
	fake := newFakeRemote()
	fake.reply = func(cmd string) (string, string, int, error) {
		if strings.HasPrefix(cmd, "set -eu") {
			return "", "cp: No space left on device\n", piScriptRollbackFailed, nil
		}
		return piReply("ok\n", "200")(cmd)
	}
	stubRestoreConnect(t, fake)
	b := inspectPiArchive(t, piArchiveMembers())
	err := restoreBackupToCurrentVPS(piState(), nil, b, false)
	if err == nil || !strings.Contains(err.Error(), "откат не удался — вернуть руками:") || !strings.Contains(err.Error(), "cp -p data/state.db.bak.") {
		t.Fatalf("err=%v", err)
	}
	if fake.indexOfCall("run: docker start") >= 0 {
		t.Fatalf("при неудачном откате контейнер не запускается:\n%s", strings.Join(fake.calls, "\n"))
	}
}

func TestRestoreToPiStoppedContainerIsAnnouncedAndAsked(t *testing.T) {
	var prompts []string
	prev := restoreConfirm
	restoreConfirm = func(p string, _ bool) bool {
		prompts = append(prompts, p)
		return len(prompts) == 1 // первое «да», второе «нет»
	}
	t.Cleanup(func() { restoreConfirm = prev })
	stubStdinTerminal(t, true)
	fake := newFakeRemote()
	fake.reply = func(cmd string) (string, string, int, error) {
		if strings.Contains(cmd, "docker inspect") {
			return "false\n", "", 0, nil
		}
		return piReply("ok\n", "200")(cmd)
	}
	stubRestoreConnect(t, fake)
	b := inspectPiArchive(t, piArchiveMembers())
	if err := restoreBackupToCurrentVPS(piState(), nil, b, false); err != nil {
		t.Fatal(err)
	}
	if len(prompts) != 2 || !strings.Contains(prompts[1], "не запущен") || !strings.Contains(prompts[1], "запустит") {
		t.Fatalf("prompts=%q", prompts)
	}
	all := strings.Join(fake.calls, "\n")
	if strings.Contains(all, "upload:") || strings.Contains(all, "docker stop") || strings.Contains(all, "docker start") {
		t.Fatalf("после отказа ничего не делается:\n%s", all)
	}
}

func TestRestoreToPiScriptTransportErrorOutcomeUnknown(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want string
	}{
		{fmt.Errorf("ssh command %q timed out after 10m0s: %w", "set -eu\ncd '/root/wg-monitor'", context.DeadlineExceeded), "таймаут"},
		{errors.New("ssh command \"set -eu\" broken pipe"), "обрыв связи"},
	} {
		stubRestoreConfirm(t, true)
		fake := newFakeRemote()
		fake.reply = func(cmd string) (string, string, int, error) {
			if strings.HasPrefix(cmd, "set -eu") {
				return "", "", -1, tc.err
			}
			return piReply("ok\n", "200")(cmd)
		}
		stubRestoreConnect(t, fake)
		b := inspectPiArchive(t, piArchiveMembers())
		err := restoreBackupToCurrentVPS(piState(), nil, b, false)
		if err == nil {
			t.Fatal("ожидалась ошибка")
		}
		msg := err.Error()
		for _, want := range []string{"исход неизвестен", tc.want, "ls -d /root/wg-monitor/data/restore-tmp", "pgrep -af '[r]estore-tmp'", "проверьте ниже, запущен ли бэкенд"} {
			if !strings.Contains(msg, want) {
				t.Fatalf("нет %q: %s", want, msg)
			}
		}
		if strings.Contains(msg, "set -eu") || strings.Contains(msg, "cd '/root") {
			t.Fatalf("текст скрипта в сообщении: %s", msg)
		}
		if strings.Index(msg, "pgrep") > strings.Index(msg, "cp -p data/state.db.bak.") {
			t.Fatalf("сначала проверка, потом рецепт отката: %s", msg)
		}
		if fake.indexOfCall("run: docker start") >= 0 {
			t.Fatalf("при неизвестном исходе контейнер не запускается:\n%s", strings.Join(fake.calls, "\n"))
		}
	}
}

func TestRestoreToPiScriptEmptyStderrCode(t *testing.T) {
	stubRestoreConfirm(t, true)
	fake := newFakeRemote()
	fake.reply = func(cmd string) (string, string, int, error) {
		if strings.HasPrefix(cmd, "set -eu") {
			return "", "  \n", piScriptFailedBeforeReplace, nil
		}
		return piReply("ok\n", "200")(cmd)
	}
	stubRestoreConnect(t, fake)
	b := inspectPiArchive(t, piArchiveMembers())
	err := restoreBackupToCurrentVPS(piState(), nil, b, false)
	if err == nil || !strings.Contains(err.Error(), "(код 10)") || strings.Contains(err.Error(), "код 10:") {
		t.Fatalf("err=%v", err)
	}
}

func TestPiRestoreScriptTrapCleanupCannotSkipStart(t *testing.T) {
	script := buildPiRestoreScript("/root/wg-monitor", "20261026T010203Z", nil)
	if !strings.Contains(script, "rm -rf data/restore-tmp || true; if [ \"$rc\" != 12 ]") {
		t.Fatalf("уборка в ловушке не защищена:\n%s", script)
	}
}

func TestRestoreToPiRefusesWhenDiskTooSmall(t *testing.T) {
	stubRestoreConfirm(t, true)
	fake := newFakeRemote()
	fake.reply = func(cmd string) (string, string, int, error) {
		if strings.Contains(cmd, "df -Pk") {
			// база с -wal 3 МБ, свободно 4 МБ: нужно 2*3 МБ + архив.
			return "3145728 4096\n", "", 0, nil
		}
		return piReply("ok\n", "200")(cmd)
	}
	stubRestoreConnect(t, fake)
	b := inspectPiArchive(t, piArchiveMembers())
	err := restoreBackupToCurrentVPS(piState(), nil, b, false)
	if err == nil || !strings.Contains(err.Error(), "мало места на диске Pi: нужно ") || !strings.Contains(err.Error(), ", свободно 4.0 МБ") {
		t.Fatalf("err=%v", err)
	}
	all := strings.Join(fake.calls, "\n")
	if strings.Contains(all, "upload:") || strings.Contains(all, "install -d") || fake.indexOfCall("docker stop") >= 0 || fake.indexOfCall("docker start") >= 0 {
		t.Fatalf("при нехватке места ничего не меняется:\n%s", all)
	}
}

func TestRestoreToPiSpaceCheckUnreadableRefuses(t *testing.T) {
	stubRestoreConfirm(t, true)
	fake := newFakeRemote()
	fake.reply = func(cmd string) (string, string, int, error) {
		if strings.Contains(cmd, "df -Pk") {
			return "garbage\n", "", 0, nil
		}
		return piReply("ok\n", "200")(cmd)
	}
	stubRestoreConnect(t, fake)
	b := inspectPiArchive(t, piArchiveMembers())
	err := restoreBackupToCurrentVPS(piState(), nil, b, false)
	if err == nil || !strings.Contains(err.Error(), "свободное место") {
		t.Fatalf("err=%v", err)
	}
	if strings.Contains(strings.Join(fake.calls, "\n"), "upload:") {
		t.Fatal("выгрузка без проверки места")
	}
}

func TestRestoreToPiWithoutTerminalRefusesUnlessYes(t *testing.T) {
	asks := stubRestoreConfirm(t, false) // EOF на stdin -- «нет»
	stubStdinTerminal(t, false)
	connects := stubRestoreConnect(t, newFakeRemote())
	b := inspectPiArchive(t, piArchiveMembers())
	err := restoreBackupToCurrentVPS(piState(), nil, b, false)
	if err == nil || !strings.Contains(err.Error(), "восстановление на Pi требует подтверждения в терминале; запустите с --yes, если уверены") {
		t.Fatalf("без терминала -- ошибка, а не тихий выход 0: err=%v", err)
	}
	if *asks != 0 || *connects != 0 {
		t.Fatalf("вопросов %d, подключений %d", *asks, *connects)
	}
}

func TestRestoreToPiYesSkipsBothQuestions(t *testing.T) {
	asks := stubRestoreConfirm(t, false)
	stubStdinTerminal(t, false)
	fake := newFakeRemote()
	fake.reply = func(cmd string) (string, string, int, error) {
		if strings.Contains(cmd, "docker inspect") {
			return "false\n", "", 0, nil // контейнер не запущен -- второй вопрос тоже снят
		}
		return piReply("ok\n", "200")(cmd)
	}
	stubRestoreConnect(t, fake)
	b := inspectPiArchive(t, piArchiveMembers())
	if err := restoreBackupToCurrentVPS(piState(), nil, b, true); err != nil {
		t.Fatalf("restore --yes: %v", err)
	}
	if *asks != 0 {
		t.Fatalf("с --yes спрошено %d раз", *asks)
	}
	if fake.indexOfCall("mv -f data/restore-tmp/state.db data/state.db") < 0 {
		t.Fatalf("замена не дошла:\n%s", strings.Join(fake.calls, "\n"))
	}
}

func TestActionRestoreBackupPassesYes(t *testing.T) {
	stubRestoreConfirm(t, false)
	stubStdinTerminal(t, false)
	fake := newFakeRemote()
	fake.reply = piReply("ok\n", "200")
	stubRestoreConnect(t, fake)
	archive := writeRestoreBackupArchive(t, piArchiveMembers())
	if err := actionRestoreBackup(piState(), nil, nil, RestoreBackupOptions{ArchivePath: archive, Mode: "current", Yes: true}); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if fake.indexOfCall("mv -f data/restore-tmp/state.db data/state.db") < 0 {
		t.Fatalf("замена не дошла:\n%s", strings.Join(fake.calls, "\n"))
	}
}

func TestRestorePreviewOnPiWarnsSealedStoresEvenWithKeyInArchive(t *testing.T) {
	// На Pi мастер revive.key не ставит никогда: ключ в архиве хранилищам
	// не поможет, решает ключ, который уже лежит на Pi.
	members := piArchiveMembers()
	members["awg3-panels.json"] = "wg-monitor-sealed v1\nnot-really-ciphertext"
	b := inspectPiArchive(t, members)
	p := RenderRestoreBackupPreview(b)
	want := "sealed stores: awg3-panels.json (open only if secrets/revive.key on the Pi is still the old one: the restore never installs revive.key on the Pi)"
	if !strings.Contains(p, want) {
		t.Fatalf("в превью нет %q:\n%s", want, p)
	}
	if strings.ContainsAny(p, "абвгдежзийклмнопрстуфхцчшщъыьэюя") {
		t.Fatalf("кириллица в превью:\n%s", p)
	}
	delete(members, "revive.key")
	if p := RenderRestoreBackupPreview(inspectPiArchive(t, members)); !strings.Contains(p, want) {
		t.Fatalf("без ключа в архиве то же:\n%s", p)
	}
	members["awg3-panels.json"] = `{"panels":[]}`
	if p := RenderRestoreBackupPreview(inspectPiArchive(t, members)); strings.Contains(p, "sealed stores") {
		t.Fatalf("открытые хранилища -- строки нет:\n%s", p)
	}
}

func TestRestoreToPiPreflightDockerPermissionVsMissingContainer(t *testing.T) {
	for _, tc := range []struct{ stderr, want, wrong string }{
		{"permission denied while trying to connect to the Docker daemon socket at unix:///var/run/docker.sock: Get \"http://%2Fvar%2Frun%2Fdocker.sock/v1.24/containers/wg-monitor-backend/json\": dial unix /var/run/docker.sock: connect: permission denied\n",
			"нет прав на docker (нужен root или группа docker)", "нет контейнера"},
		{"Error: No such object: wg-monitor-backend\n", "нет контейнера wg-monitor-backend", "нет прав"},
	} {
		stubRestoreConfirm(t, true)
		fake := newFakeRemote()
		fake.reply = func(cmd string) (string, string, int, error) {
			if strings.Contains(cmd, "docker inspect") {
				return "", tc.stderr, 1, nil
			}
			return piReply("ok\n", "200")(cmd)
		}
		stubRestoreConnect(t, fake)
		b := inspectPiArchive(t, piArchiveMembers())
		err := restoreBackupToCurrentVPS(piState(), nil, b, false)
		if err == nil || !strings.Contains(err.Error(), tc.want) || strings.Contains(err.Error(), tc.wrong) {
			t.Fatalf("stderr %q: err=%v", tc.stderr, err)
		}
		if fake.indexOfCall("docker stop") >= 0 || strings.Contains(strings.Join(fake.calls, "\n"), "upload:") {
			t.Fatalf("после отказа проверки ничего не делается:\n%s", strings.Join(fake.calls, "\n"))
		}
	}
}
