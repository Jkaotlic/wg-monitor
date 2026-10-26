package main

import (
	"errors"
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
	t.Cleanup(func() { restoreConfirm = prev })
	return &n
}

func shortHealthWait(t *testing.T) {
	t.Helper()
	prev := verifyHTTPTimeout
	verifyHTTPTimeout = 10 * time.Millisecond
	t.Cleanup(func() { verifyHTTPTimeout = prev })
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
	if err := restoreBackupToCurrentVPS(piState(), nil, b); err != nil {
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

	if err := restoreBackupToCurrentVPS(piState(), nil, b); err != nil {
		t.Fatalf("restore: %v\n%s", err, strings.Join(fake.calls, "\n"))
	}
	order := []string{
		"run: docker stop wg-monitor-backend",
		"rm -rf '/root/wg-monitor/data/restore-tmp'",
		"upload: /root/wg-monitor/data/restore-tmp/state.db mode=600",
		"upload: /root/wg-monitor/data/restore-tmp/awg3-panels.json mode=600",
		"PRAGMA integrity_check",
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
	if !strings.Contains(script, `trap 'rc=$?; if [ "$rc" != 0 ] && [ "$replaced" = 1 ]; then rollback; fi; rm -rf data/restore-tmp; docker start wg-monitor-backend >/dev/null 2>&1 || true; exit $rc' EXIT`) {
		t.Fatalf("нет ловушки выхода:\n%s", script)
	}
	if strings.Index(script, "trap 'rc=") > strings.Index(script, "chmod 600") {
		t.Fatalf("ловушка взводится после первого шага:\n%s", script)
	}
	if strings.Contains(script, "revive.key") || strings.Contains(script, "secrets/") {
		t.Fatalf("скрипт не трогает secrets/:\n%s", script)
	}
}

func TestRestoreToPiIntegrityFailureLeavesDBAndStartsContainer(t *testing.T) {
	stubRestoreConfirm(t, true)
	fake := newFakeRemote()
	fake.reply = piReply("*** in database main ***\nPage 7: btreeInitPage() returns error code 11\n", "200")
	stubRestoreConnect(t, fake)
	b := inspectPiArchive(t, piArchiveMembers())

	err := restoreBackupToCurrentVPS(piState(), nil, b)
	if err == nil || !strings.Contains(err.Error(), "integrity_check") {
		t.Fatalf("err=%v, want отказ по integrity_check", err)
	}
	all := strings.Join(fake.calls, "\n")
	for _, bad := range []string{"mv -f", ".bak.", "rm -f data/state.db-wal"} {
		if strings.Contains(all, bad) {
			t.Fatalf("при битой базе есть замена (%q):\n%s", bad, all)
		}
	}
	integ := fake.indexOfCall("PRAGMA integrity_check")
	start := fake.indexOfCall("run: docker start wg-monitor-backend")
	if start < integ {
		t.Fatalf("контейнер не запущен обратно после провала проверки:\n%s", all)
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

	err := restoreBackupToCurrentVPS(piState(), nil, b)
	if err == nil {
		t.Fatal("провал /healthz -- ошибка")
	}
	for _, want := range []string{"docker logs wg-monitor-backend", "state.db.bak.", "-wal", "docker start wg-monitor-backend"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("подсказка без %q: %v", want, err)
		}
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
	err := restoreBackupToCurrentVPS(piState(), nil, b)
	if err == nil || !strings.Contains(err.Error(), "sqlite3") {
		t.Fatalf("err=%v", err)
	}
	if fake.indexOfCall("docker stop") >= 0 {
		t.Fatalf("контейнер остановлен, хотя проверка до остановки не прошла:\n%s", strings.Join(fake.calls, "\n"))
	}
}

func TestRestoreToPiTransportFailureAfterStopStartsContainer(t *testing.T) {
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
	if err := restoreBackupToCurrentVPS(piState(), nil, b); err == nil {
		t.Fatal("ожидалась ошибка")
	}
	if fake.indexOfCall("run: docker start wg-monitor-backend") < fake.indexOfCall("docker stop") {
		t.Fatalf("нет docker start после сбоя:\n%s", strings.Join(fake.calls, "\n"))
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
