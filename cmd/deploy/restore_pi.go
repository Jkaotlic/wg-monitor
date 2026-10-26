package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path"
	"slices"
	"strings"
	"time"
)

// Восстановление бэкапа на докер-раскладке (Raspberry Pi) -- те же шаги, что
// раздел DEPLOY.md «Восстановление бэкапа на Pi», но мастером. На Pi живёт
// боевой бэкенд, поэтому порядок осторожный: любой провал до замены -- стоп
// без замены, контейнер запускается обратно при любом исходе.

const (
	piBackendContainer = "wg-monitor-backend"
	piRestoreTmp       = "data/restore-tmp" // от корня раскладки, как в DEPLOY.md
	piContainerDBPath  = "/data/state.db"   // db_path в backend.yaml контейнера

	// Коды выхода скрипта замены: по ним мастер говорит правду о том,
	// где осталась база.
	piScriptFailedBeforeReplace = 10 // провал до замены: база не тронута
	piScriptFailedAfterReplace  = 11 // провал после начала замены, откат удался
	piScriptRollbackFailed      = 12 // откат не удался: контейнер не запускается
)

// piHealthTimeout -- сколько ждать /healthz после запуска контейнера на Pi:
// на Raspberry Pi бэкенд поднимается дольше, чем на VPS. Тесты сокращают.
var piHealthTimeout = 60 * time.Second

// restoreConfirm -- вопрос оператору; тесты подменяют.
var restoreConfirm = Confirm

// restoreStdinIsTerminal -- можно ли спросить оператора; тесты подменяют.
var restoreStdinIsTerminal = func() bool { return isTerminal(os.Stdin) }

func isDockerBackend(state *State) bool {
	return state != nil && strings.TrimSpace(state.Backend.SourceBind) != ""
}

// InspectRestoreBackupForDocker читает архив для восстановления на Pi: в его
// backend.yaml пути контейнера (/data/...), хранилища ложатся в data/ рядом с
// базой. revive.key извлекается только для показа -- на Pi его не кладём.
func InspectRestoreBackupForDocker(archivePath, passphrase string) (*RestoreBackup, func(), error) {
	b, cleanup, err := inspectRestoreBackup(archivePath, passphrase, validateRestoreBackendYAMLForDocker, resolveDockerRestoreExtras)
	if b != nil {
		b.ForDocker = true
	}
	return b, cleanup, err
}

func validateRestoreBackendYAMLForDocker(p string) error {
	cfg, err := readRestoreBackendYAML(p)
	if err != nil {
		return err
	}
	if got := strings.TrimSpace(cfg.DBPath); got != piContainerDBPath {
		return fmt.Errorf("validate backend.yaml: db_path must be %s for the Raspberry Pi restore, got %q", piContainerDBPath, got)
	}
	return nil
}

// resolveDockerRestoreExtras: хранилище из архива ложится в data/<имя>, как в
// DEPLOY.md. Если backend.yaml архива ждёт его в другом месте, мастер не
// угадывает -- отказ до любых действий на Pi.
func resolveDockerRestoreExtras(backendYAMLPath string, extracted map[string]string) ([]RestoreExtraFile, []string, error) {
	if len(extracted) == 0 {
		return nil, nil, nil
	}
	cfg, err := readRestoreBackendYAML(backendYAMLPath)
	if err != nil {
		return nil, nil, err
	}
	var extras []RestoreExtraFile
	var warnings []string
	for _, name := range restoreStoreNames {
		local, ok := extracted[name]
		if !ok {
			continue
		}
		want := path.Join(path.Dir(piContainerDBPath), name)
		if dest := cfg.storeDestination(name); dest != want {
			return nil, nil, fmt.Errorf("unsupported restore destination for %s: backend.yaml expects %q, the Raspberry Pi restore puts stores at %s", name, dest, want)
		}
		extras = append(extras, RestoreExtraFile{Name: name, LocalPath: local, RemotePath: want})
	}
	if local, ok := extracted[restoreReviveKey]; ok {
		extras = append(extras, RestoreExtraFile{Name: restoreReviveKey, LocalPath: local})
		warnings = append(warnings, "в архиве есть revive.key, но на Pi мастер его не трогает: secrets/revive.key остаётся прежним")
	}
	return extras, warnings, nil
}

// piRestoreStores -- хранилища из архива, которые кладутся в data/ (revive.key
// сюда не входит никогда).
func piRestoreStores(extras []RestoreExtraFile) []RestoreExtraFile {
	var out []RestoreExtraFile
	for _, e := range extras {
		if slices.Contains(restoreStoreNames, e.Name) {
			out = append(out, e)
		}
	}
	return out
}

func restoreBackupToPi(state *State, secrets *SecretStore, backup *RestoreBackup, yes bool) error {
	base := backupPathsForState(state).LayoutRoot
	if !restoreDestinationRe.MatchString(base) || path.Clean(base) != base {
		return fmt.Errorf("unexpected Raspberry Pi layout root %q", base)
	}
	if strings.TrimSpace(state.Backend.Host) == "" {
		return fmt.Errorf("backend host is not configured in wizard.toml")
	}
	// Без терминала спросить некого: «нет» от пустого stdin -- не отказ
	// оператора, и выход 0 без восстановления обманул бы cron/скрипт.
	if !yes && !restoreStdinIsTerminal() {
		return errors.New("восстановление на Pi требует подтверждения в терминале; запустите с --yes, если уверены")
	}
	if !yes && !restoreConfirm("Восстановить на Pi? Бэкенд будет остановлен", false) {
		PrintInfo("восстановление на Pi отменено, на сервере ничего не менялось")
		return nil
	}
	stores := piRestoreStores(backup.Extras)
	stamp := time.Now().UTC().Format("20060102T150405Z")

	PrintStep(1, 6, "SSH к Pi")
	s, err := connectRestoreBackend(state, secrets)
	if err != nil {
		return err
	}
	defer s.Close()

	PrintStep(2, 6, "Проверка до остановки")
	running, err := piRestorePreflight(s, base)
	if err != nil {
		return err
	}
	if err := piCheckFreeSpace(s, base, piStagedBytes(backup, stores)); err != nil {
		return err
	}
	if !running {
		PrintWarn("контейнер " + piBackendContainer + " сейчас не запущен")
		if !yes && !restoreConfirm("Контейнер "+piBackendContainer+" сейчас не запущен; в конце восстановления мастер его запустит. Продолжить?", false) {
			PrintInfo("восстановление на Pi отменено, на сервере ничего не менялось")
			return nil
		}
	}

	// Выгрузка и проверка архива -- пока бэкенд работает: битый архив или
	// оборванная связь не стоят ни секунды простоя.
	PrintStep(3, 6, "Выгрузка архива и проверка целостности базы")
	tmp := base + "/" + piRestoreTmp
	if err := piStageArchive(s, tmp, backup, stores); err != nil {
		piRemoveTmp(s, tmp)
		return fmt.Errorf("выгрузка архива на Pi не удалась -- замены не было, бэкенд не останавливался: %w", err)
	}
	out, err := s.MustRun("sqlite3 " + shellSingleQuote(tmp+"/state.db") + " 'PRAGMA integrity_check;'")
	if err != nil || strings.TrimSpace(out) != "ok" {
		piRemoveTmp(s, tmp)
		why := strings.TrimSpace(out)
		if err != nil {
			why = err.Error()
		}
		return fmt.Errorf("архив повреждён: база не прошла integrity_check (%s) -- замены не было, бэкенд не останавливался", why)
	}
	PrintOK("integrity_check: ok")

	PrintStep(4, 6, "Остановка бэкенда")
	if _, err := s.MustRun("docker stop " + piBackendContainer); err != nil {
		piRemoveTmp(s, tmp)
		// Мог остановиться наполовину -- запускаем обратно.
		return piStartBack(s, fmt.Errorf("остановить контейнер не удалось, замены не было: %w", err))
	}

	PrintStep(5, 6, "Замена базы и хранилищ")
	names := make([]string, 0, len(stores))
	for _, e := range stores {
		names = append(names, e.Name)
	}
	if err := piRunRestoreScript(s, base, stamp, names); err != nil {
		return err
	}
	PrintOK("база заменена, прежняя сохранена как data/state.db.bak." + stamp)

	PrintStep(6, 6, "Запуск и проверка /healthz")
	if _, err := s.MustRun("docker start " + piBackendContainer); err != nil {
		return fmt.Errorf("контейнер не запустился: %w\n%s", err, piRollbackHint(base, stamp, names))
	}
	if err := stepVerifyHTTPWithin(s, piHealthURL(state.Backend.Domain), piHealthTimeout); err != nil {
		return fmt.Errorf("бэкенд не ответил на /healthz за %s (%w).\n"+
			"бэкенд ещё не ответил — возможно, он ещё запускается: посмотрите docker logs %s, прежде чем откатывать.\n%s",
			piHealthTimeout, err, piBackendContainer, piRollbackHint(base, stamp, names))
	}
	PrintOK("восстановление на Pi завершено")
	PrintInfo("сохранённые пароли роутеров после восстановления вводятся заново")
	return nil
}

// piRunRestoreScript запускает скрипт замены и по коду выхода говорит, где
// база. В тексте ошибки -- только stderr, не сам скрипт.
func piRunRestoreScript(s remoteExec, base, stamp string, names []string) error {
	_, stderr, rc, err := s.Run(buildPiRestoreScript(base, stamp, names))
	if err != nil {
		// Скрипт на Pi сам откатывает и запускает контейнер, если доработал;
		// исход отсюда не виден. Текст ошибки транспорта несёт сам скрипт --
		// его не показываем, и контейнер отсюда не запускаем.
		what := "обрыв связи"
		if errors.Is(err, context.DeadlineExceeded) {
			what = "таймаут"
		}
		return fmt.Errorf("%s во время замены, исход неизвестен. Сначала проверьте на Pi:\n"+
			"  ls -d %s/%s   # каталога нет — скрипт доработал; проверьте ниже, запущен ли бэкенд\n"+
			"  pgrep -af '[r]estore-tmp'   # скрипт ещё идёт -- дождитесь его конца\n"+
			"  docker ps -a --filter name=%s\n"+
			"Только если каталог остался, а скрипта уже нет -- %s", what, base, piRestoreTmp, piBackendContainer, piRollbackHint(base, stamp, names))
	}
	why := ""
	if e := strings.TrimSpace(stderr); e != "" {
		why = ": " + e
	}
	switch rc {
	case 0:
		return nil
	case piScriptRollbackFailed:
		// Запускать бэкенд на полувозвращённой базе нельзя -- только руками.
		return fmt.Errorf("замена не удалась (код %d%s), откат не удался — вернуть руками:\n%s", rc, why, piRollbackHint(base, stamp, names))
	case piScriptFailedAfterReplace:
		// Скрипт уже откатил и запустил контейнер; ещё один start безопасен.
		return piStartBack(s, fmt.Errorf("замена не удалась (код %d%s): прежняя база возвращена из копии state.db.bak.%s", rc, why, stamp))
	default:
		return piStartBack(s, fmt.Errorf("замена не удалась (код %d%s): замена не начиналась, база не тронута", rc, why))
	}
}

// piHealthURL -- /healthz бэкенда на самом Pi: по умолчанию listen
// 127.0.0.1:8080 (DEPLOY.md), локальный адрес из wizard.toml -- если задан.
func piHealthURL(domain string) string {
	if strings.TrimSpace(domain) == "" {
		return "http://127.0.0.1:8080/healthz"
	}
	return backendHealthURL(domain)
}

// piRestorePreflight -- только чтение: всё нужное есть, иначе контейнер не
// останавливаем вовсе. running -- запущен ли контейнер сейчас.
func piRestorePreflight(s remoteExec, base string) (running bool, err error) {
	checks := []struct{ cmd, why string }{
		{"test -d " + shellSingleQuote(base+"/data"), "нет каталога " + base + "/data"},
		{"command -v sqlite3 >/dev/null 2>&1", "на Pi нет sqlite3 -- поставьте пакет sqlite3, без него базу из архива не проверить"},
		{"command -v docker >/dev/null 2>&1", "на Pi нет docker"},
		{"docker inspect -f '{{.State.Running}}' " + piBackendContainer, "нет контейнера " + piBackendContainer},
	}
	var out string
	for _, c := range checks {
		var rc int
		out, _, rc, err = s.Run(c.cmd)
		if err != nil {
			return false, fmt.Errorf("проверка до остановки: %w", err)
		}
		if rc != 0 {
			return false, errors.New("проверка до остановки не прошла, ничего не менялось: " + c.why)
		}
	}
	PrintOK("sqlite3, docker и каталог data/ на месте")
	// Последняя проверка -- docker inspect: её вывод true/false.
	return strings.TrimSpace(out) == "true", nil
}

// piStagedBytes -- сколько займут на Pi база и хранилища из архива.
func piStagedBytes(backup *RestoreBackup, stores []RestoreExtraFile) int64 {
	var n int64
	for _, p := range append([]string{backup.StateDBPath}, piExtraPaths(stores)...) {
		if st, err := os.Stat(p); err == nil {
			n += st.Size()
		}
	}
	return n
}

func piExtraPaths(stores []RestoreExtraFile) []string {
	out := make([]string, 0, len(stores))
	for _, e := range stores {
		out = append(out, e.LocalPath)
	}
	return out
}

// piNeedBytes: копия прежней базы с -wal (.bak), запас на ещё одну такую же
// (откат копирует .bak во временный файл) и выгруженный архив.
func piNeedBytes(liveDB, staged int64) int64 {
	return 2*liveDB + staged
}

// piCheckFreeSpace -- только чтение: хватит ли места на файловой системе
// data/ под копию прежней базы и распакованный архив. Не хватит -- отказ до
// выгрузки и останова; не удалось узнать -- тоже отказ, а не угадывание.
func piCheckFreeSpace(s remoteExec, base string, staged int64) error {
	cmd := "cd " + shellSingleQuote(base) + ` && n=0; for f in data/state.db data/state.db-wal; do if [ -f "$f" ]; then n=$((n + $(wc -c < "$f"))); fi; done; echo "$n $(df -Pk data | awk 'NR==2{print $4}')"`
	out, _, rc, err := s.Run(cmd)
	if err != nil {
		return fmt.Errorf("проверка до остановки: %w", err)
	}
	var live, availKB int64
	if n, _ := fmt.Sscan(strings.TrimSpace(out), &live, &availKB); rc != 0 || n != 2 || live < 0 || availKB < 0 {
		return errors.New("проверка до остановки не прошла, ничего не менялось: не удалось узнать свободное место на диске Pi (df -Pk data)")
	}
	need, free := piNeedBytes(live, staged), availKB*1024
	if free < need {
		return fmt.Errorf("мало места на диске Pi: нужно %s, свободно %s -- ничего не менялось, бэкенд не останавливался", piMB(need), piMB(free))
	}
	PrintOK("место на диске Pi: нужно " + piMB(need) + ", свободно " + piMB(free))
	return nil
}

func piMB(n int64) string {
	return fmt.Sprintf("%.1f МБ", float64(n)/(1<<20))
}

// piStageArchive -- как `rm -rf data/restore-tmp` + extract в DEPLOY.md:
// архив уже расшифрован и разобран мастером, на Pi уходят база и хранилища.
func piStageArchive(s remoteExec, tmp string, backup *RestoreBackup, stores []RestoreExtraFile) error {
	q := shellSingleQuote(tmp)
	if _, err := s.MustRun("rm -rf " + q + " && install -d -m 700 " + q); err != nil {
		return err
	}
	db, err := os.ReadFile(backup.StateDBPath)
	if err != nil {
		return fmt.Errorf("read extracted state.db: %w", err)
	}
	if err := stepUploadFile(s, tmp+"/state.db", db, "600"); err != nil {
		return err
	}
	for _, e := range stores {
		body, err := os.ReadFile(e.LocalPath)
		if err != nil {
			return fmt.Errorf("read extracted %s: %w", e.Name, err)
		}
		if err := stepUploadFile(s, tmp+"/"+e.Name, body, "600"); err != nil {
			return err
		}
	}
	return nil
}

func piRemoveTmp(s remoteExec, tmp string) {
	if _, err := s.MustRun("rm -rf " + shellSingleQuote(tmp)); err != nil {
		PrintWarn("не удалось убрать " + tmp + ": " + err.Error())
	}
}

// piStartBack запускает контейнер обратно после провала и возвращает cause;
// если и запуск не удался, говорит об этом.
func piStartBack(s remoteExec, cause error) error {
	if _, err := s.MustRun("docker start " + piBackendContainer); err != nil {
		return fmt.Errorf("%w; контейнер обратно не запустился (%v) -- запустите вручную: docker start %s", cause, err, piBackendContainer)
	}
	PrintInfo("контейнер " + piBackendContainer + " запущен обратно")
	return cause
}

// buildPiRestoreScript -- шаги 3-4 раздела DEPLOY.md одним скриптом. Файлы из
// data/restore-tmp сначала получают режим 600 и владельца каталога data/
// (chown --reference=data, как fileown.MatchDir), затем сохраняется текущее
// (база вместе с -wal/-shm под именами state.db.bak.<ts>-wal/-shm, чтобы копия
// открывалась как цельная база), и только потом mv на место. Провал после
// начала замены -- откат из копий; провал до замены -- копии этого прогона
// (метка stamp) убираются, чтобы половинная копия не съела место. Каталог распаковки убирается, контейнер
// запускается при любом исходе. secrets/ (revive.key) не трогается.
func buildPiRestoreScript(base, stamp string, stores []string) string {
	var prep, backupOld, install, rollback, dropBak strings.Builder
	for _, name := range stores {
		live := "data/" + name
		fmt.Fprintf(&dropBak, " %s.bak.%s", live, stamp)
		fmt.Fprintf(&prep, "chmod 600 %s/%s\nchown --reference=data %s/%s\n", piRestoreTmp, name, piRestoreTmp, name)
		fmt.Fprintf(&backupOld, "if [ -f %s ]; then cp -p %s %s.bak.%s; fi\n", live, live, live, stamp)
		fmt.Fprintf(&install, "mv -f %s/%s %s\n", piRestoreTmp, name, live)
		fmt.Fprintf(&rollback, "\tif [ -f %[1]s.bak.%[2]s ]; then { cp -p %[1]s.bak.%[2]s %[1]s.rb.%[2]s && mv -f %[1]s.rb.%[2]s %[1]s; } || rbfail=1; else rm -f %[1]s || rbfail=1; fi\n", live, stamp)
	}
	return fmt.Sprintf(`set -eu
cd %[1]s
replaced=0
rollback() {
	rbfail=0
	rm -f data/state.db-wal data/state.db-shm || rbfail=1
	if [ -f data/state.db.bak.%[2]s ]; then { cp -p data/state.db.bak.%[2]s data/state.db.rb.%[2]s && mv -f data/state.db.rb.%[2]s data/state.db; } || rbfail=1; else rm -f data/state.db || rbfail=1; fi
	if [ -f data/state.db.bak.%[2]s-wal ]; then { cp -p data/state.db.bak.%[2]s-wal data/state.db-wal.rb.%[2]s && mv -f data/state.db-wal.rb.%[2]s data/state.db-wal; } || rbfail=1; fi
	if [ -f data/state.db.bak.%[2]s-shm ]; then { cp -p data/state.db.bak.%[2]s-shm data/state.db-shm.rb.%[2]s && mv -f data/state.db-shm.rb.%[2]s data/state.db-shm; } || rbfail=1; fi
%[6]s	return $rbfail
}
trap 'rc=$?; if [ "$rc" != 0 ]; then if [ "$replaced" = 1 ]; then if rollback; then rc=%[9]d; else rc=%[11]d; fi; else rc=%[10]d; rm -f data/state.db.bak.%[2]s data/state.db.bak.%[2]s-wal data/state.db.bak.%[2]s-shm%[12]s || true; fi; fi; rm -rf %[3]s || true; if [ "$rc" != %[11]d ]; then docker start %[7]s >/dev/null 2>&1 || true; fi; exit $rc' EXIT
test -s %[3]s/state.db
chmod 600 %[3]s/state.db
chown --reference=data %[3]s/state.db
%[4]sif [ -f data/state.db ]; then cp -p data/state.db data/state.db.bak.%[2]s; fi
if [ -f data/state.db-wal ]; then cp -p data/state.db-wal data/state.db.bak.%[2]s-wal; fi
if [ -f data/state.db-shm ]; then cp -p data/state.db-shm data/state.db.bak.%[2]s-shm; fi
%[5]sreplaced=1
rm -f data/state.db-wal data/state.db-shm
mv -f %[3]s/state.db data/state.db
%[8]s`, shellSingleQuote(base), stamp, piRestoreTmp, prep.String(), backupOld.String(), rollback.String(), piBackendContainer, install.String(),
		piScriptFailedAfterReplace, piScriptFailedBeforeReplace, piScriptRollbackFailed, dropBak.String())
}

// piRollbackHint -- как вернуть прежнюю базу руками, если новая не поднялась.
func piRollbackHint(base, stamp string, stores []string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Посмотрите docker logs %s. Вернуть прежнюю базу:\n", piBackendContainer)
	fmt.Fprintf(&b, "  cd %s\n  docker stop %s\n  rm -f data/state.db-wal data/state.db-shm\n", base, piBackendContainer)
	fmt.Fprintf(&b, "  cp -p data/state.db.bak.%[1]s data/state.db\n", stamp)
	fmt.Fprintf(&b, "  [ -f data/state.db.bak.%[1]s-wal ] && cp -p data/state.db.bak.%[1]s-wal data/state.db-wal\n", stamp)
	fmt.Fprintf(&b, "  [ -f data/state.db.bak.%[1]s-shm ] && cp -p data/state.db.bak.%[1]s-shm data/state.db-shm\n", stamp)
	for _, name := range stores {
		fmt.Fprintf(&b, "  [ -f data/%[1]s.bak.%[2]s ] && cp -p data/%[1]s.bak.%[2]s data/%[1]s\n", name, stamp)
	}
	fmt.Fprintf(&b, "  docker start %s", piBackendContainer)
	return b.String()
}
