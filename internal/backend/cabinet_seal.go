package backend

import (
	"errors"
	"log/slog"
	"os"
	"strings"

	"github.com/Jkaotlic/wg-monitor/internal/backend/revive"
	"github.com/Jkaotlic/wg-monitor/internal/backend/sealedfile"
)

// Предупреждения сводки парка про ключи кабинетов на диске (v0.55, B1).
// Словами, без путей и имён файлов.
const (
	cabinetSealWarnOpen       = "Ключи кабинетов лежат на диске открытыми: на сервере не задан ключ шифрования"
	cabinetSealWarnNoKey      = "Ключи кабинетов не прочитать: ключ шифрования не найден — верните на сервер прежний ключ"
	cabinetSealWarnWrongKey   = "Ключи кабинетов не расшифровываются: ключ шифрования не тот, которым они записаны"
	cabinetSealWarnPartlyOpen = "Не все ключи кабинетов зашифрованы — подробности в журнале сервера"
)

// SealCabinetStores ставит ключ шифрования файлов кабинетов (тот же
// revive.key, что у паролей root) и перешифровывает открытые хранилища на
// месте. Зовётся на старте, до того как хранилища кто-то откроет.
//
// Отсутствие или негодность ключа старт не роняет: файлы остаются как есть,
// в журнал -- предупреждение. Возвращает текст для сводки парка ("" --
// всё в порядке или защищать нечего). Повторный вызов безопасен.
func SealCabinetStores(stores []StoreFile, keyFile string, logger *slog.Logger) string {
	log := logger.With("component", "cabinet-seal")
	box, keyErr := loadSealBox(keyFile)
	sealedfile.SetKey(box)

	var present, sealed []StoreFile
	for _, st := range stores {
		if strings.TrimSpace(st.Path) == "" {
			continue
		}
		body, err := os.ReadFile(st.Path) // #nosec G304 -- путь хранилища из конфига
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			log.Warn("хранилище кабинетов не прочитано", "store", st.Name, "err", err)
			continue
		}
		present = append(present, st)
		if sealedfile.IsSealed(body) {
			sealed = append(sealed, st)
		}
		clear(body)
	}

	if box == nil {
		if len(sealed) > 0 {
			log.Error("ключи кабинетов зашифрованы, а ключа шифрования нет — кабинеты не работают; файлы не тронуты",
				"reason", keyErr, "stores", storeNames(sealed))
			return cabinetSealWarnNoKey
		}
		if len(present) == 0 {
			return ""
		}
		log.Warn("ключи кабинетов лежат на диске открытыми: ключ шифрования (revive.key_file) не задан или не прочитан",
			"reason", keyErr, "stores", storeNames(present))
		return cabinetSealWarnOpen
	}

	warn := ""
	for _, st := range present {
		changed, err := sealedfile.Reseal(st.Path, st.Name)
		if err != nil {
			log.Warn("хранилище кабинетов не зашифровано — осталось открытым", "store", st.Name, "err", err)
			warn = cabinetSealWarnPartlyOpen
			continue
		}
		if changed {
			log.Info("хранилище кабинетов зашифровано", "store", st.Name)
		}
	}
	for _, st := range sealed {
		if _, err := sealedfile.ReadFile(st.Path, st.Name); err != nil {
			log.Error("хранилище кабинетов не расшифровывается этим ключом", "store", st.Name)
			warn = cabinetSealWarnWrongKey
		}
	}
	return warn
}

func loadSealBox(keyFile string) (*revive.Box, error) {
	key, err := revive.LoadKey(keyFile)
	if err != nil {
		return nil, err
	}
	defer clear(key)
	return revive.NewBox(key)
}

func storeNames(stores []StoreFile) string {
	names := make([]string, 0, len(stores))
	for _, st := range stores {
		names = append(names, st.Name)
	}
	return strings.Join(names, ",")
}
