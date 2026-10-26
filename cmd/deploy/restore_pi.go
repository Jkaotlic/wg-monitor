package main

import (
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
)

// restoreConfirm -- вопрос оператору; тесты подменяют.
var restoreConfirm = Confirm

func isDockerBackend(state *State) bool {
	return state != nil && strings.TrimSpace(state.Backend.SourceBind) != ""
}

// InspectRestoreBackupForDocker читает архив для восстановления на Pi: в его
// backend.yaml пути контейнера (/data/...), хранилища ложатся в data/ рядом с
// базой. revive.key извлекается только для показа -- на Pi его не кладём.
func InspectRestoreBackupForDocker(archivePath, passphrase string) (*RestoreBackup, func(), error) {
	return inspectRestoreBackup(archivePath, passphrase, validateRestoreBackendYAMLForDocker, resolveDockerRestoreExtras)
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

func restoreBackupToPi(state *State, secrets *SecretStore, backup *RestoreBackup) error {
	base := backupPathsForState(state).LayoutRoot
	if !restoreDestinationRe.MatchString(base) || path.Clean(base) != base {
		return fmt.Errorf("unexpected Raspberry Pi layout root %q", base)
	}
	if strings.TrimSpace(state.Backend.Host) == "" {
		return fmt.Errorf("backend host is not configured in wizard.toml")
	}
	if !restoreConfirm("Восстановить на Pi? Бэкенд будет остановлен", false) {
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
	if err := piRestorePreflight(s, base); err != nil {
		return err
	}

	PrintStep(3, 6, "Остановка бэкенда и выгрузка архива")
	if _, err := s.MustRun("docker stop " + piBackendContainer); err != nil {
		// Мог остановиться наполовину -- запускаем обратно.
		return piStartBack(s, fmt.Errorf("остановить контейнер не удалось, база не тронута: %w", err))
	}
	tmp := base + "/" + piRestoreTmp
	if err := piStageArchive(s, tmp, backup, stores); err != nil {
		piRemoveTmp(s, tmp)
		return piStartBack(s, fmt.Errorf("выгрузка архива на Pi не удалась, база не тронута: %w", err))
	}

	PrintStep(4, 6, "Проверка целостности базы из архива")
	out, err := s.MustRun("sqlite3 " + shellSingleQuote(tmp+"/state.db") + " 'PRAGMA integrity_check;'")
	if err != nil || strings.TrimSpace(out) != "ok" {
		piRemoveTmp(s, tmp)
		why := strings.TrimSpace(out)
		if err != nil {
			why = err.Error()
		}
		return piStartBack(s, fmt.Errorf("база из архива не прошла integrity_check (%s): замены не было, прежняя база на месте", why))
	}
	PrintOK("integrity_check: ok")

	PrintStep(5, 6, "Замена базы и хранилищ")
	names := make([]string, 0, len(stores))
	for _, e := range stores {
		names = append(names, e.Name)
	}
	if _, err := s.MustRun(buildPiRestoreScript(base, stamp, names)); err != nil {
		// Скрипт сам откатывает и запускает контейнер, но связь могла
		// оборваться раньше -- запускаем ещё раз, это безопасно.
		return piStartBack(s, fmt.Errorf("замена не удалась, прежняя база возвращена из копии: %w", err))
	}
	PrintOK("база заменена, прежняя сохранена как data/state.db.bak." + stamp)

	PrintStep(6, 6, "Запуск и проверка /healthz")
	if _, err := s.MustRun("docker start " + piBackendContainer); err != nil {
		return fmt.Errorf("контейнер не запустился: %w\n%s", err, piRollbackHint(base, stamp, names))
	}
	if err := stepVerifyHTTP(s, piHealthURL(state.Backend.Domain)); err != nil {
		return fmt.Errorf("бэкенд не ответил на /healthz: %w\n%s", err, piRollbackHint(base, stamp, names))
	}
	PrintOK("восстановление на Pi завершено")
	PrintInfo("сохранённые пароли роутеров после восстановления вводятся заново")
	return nil
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
// останавливаем вовсе.
func piRestorePreflight(s remoteExec, base string) error {
	checks := []struct{ cmd, why string }{
		{"test -d " + shellSingleQuote(base+"/data"), "нет каталога " + base + "/data"},
		{"command -v sqlite3 >/dev/null 2>&1", "на Pi нет sqlite3 -- поставьте пакет sqlite3, без него базу из архива не проверить"},
		{"command -v docker >/dev/null 2>&1", "на Pi нет docker"},
		{"docker inspect -f '{{.State.Running}}' " + piBackendContainer, "нет контейнера " + piBackendContainer},
	}
	for _, c := range checks {
		_, _, rc, err := s.Run(c.cmd)
		if err != nil {
			return fmt.Errorf("проверка до остановки: %w", err)
		}
		if rc != 0 {
			return errors.New("проверка до остановки не прошла, ничего не менялось: " + c.why)
		}
	}
	PrintOK("sqlite3, docker и каталог data/ на месте")
	return nil
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
// начала замены -- откат из копий. Каталог распаковки убирается, контейнер
// запускается при любом исходе. secrets/ (revive.key) не трогается.
func buildPiRestoreScript(base, stamp string, stores []string) string {
	var prep, backupOld, install, rollback strings.Builder
	for _, name := range stores {
		live := "data/" + name
		fmt.Fprintf(&prep, "chmod 600 %s/%s\nchown --reference=data %s/%s\n", piRestoreTmp, name, piRestoreTmp, name)
		fmt.Fprintf(&backupOld, "if [ -f %s ]; then cp -p %s %s.bak.%s; fi\n", live, live, live, stamp)
		fmt.Fprintf(&install, "mv -f %s/%s %s\n", piRestoreTmp, name, live)
		fmt.Fprintf(&rollback, "\tif [ -f %[1]s.bak.%[2]s ]; then cp -p %[1]s.bak.%[2]s %[1]s || true; else rm -f %[1]s; fi\n", live, stamp)
	}
	return fmt.Sprintf(`set -eu
cd %[1]s
replaced=0
rollback() {
	rm -f data/state.db-wal data/state.db-shm
	if [ -f data/state.db.bak.%[2]s ]; then cp -p data/state.db.bak.%[2]s data/state.db || true; else rm -f data/state.db; fi
	if [ -f data/state.db.bak.%[2]s-wal ]; then cp -p data/state.db.bak.%[2]s-wal data/state.db-wal || true; fi
	if [ -f data/state.db.bak.%[2]s-shm ]; then cp -p data/state.db.bak.%[2]s-shm data/state.db-shm || true; fi
%[6]s}
trap 'rc=$?; if [ "$rc" != 0 ] && [ "$replaced" = 1 ]; then rollback; fi; rm -rf %[3]s; docker start %[7]s >/dev/null 2>&1 || true; exit $rc' EXIT
test -s %[3]s/state.db
chmod 600 %[3]s/state.db
chown --reference=data %[3]s/state.db
%[4]sif [ -f data/state.db ]; then cp -p data/state.db data/state.db.bak.%[2]s; fi
if [ -f data/state.db-wal ]; then cp -p data/state.db-wal data/state.db.bak.%[2]s-wal; fi
if [ -f data/state.db-shm ]; then cp -p data/state.db-shm data/state.db.bak.%[2]s-shm; fi
%[5]sreplaced=1
rm -f data/state.db-wal data/state.db-shm
mv -f %[3]s/state.db data/state.db
%[8]s`, shellSingleQuote(base), stamp, piRestoreTmp, prep.String(), backupOld.String(), rollback.String(), piBackendContainer, install.String())
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
