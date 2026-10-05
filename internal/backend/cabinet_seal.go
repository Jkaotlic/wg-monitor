package backend

import (
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/Jkaotlic/wg-monitor/internal/backend/revive"
	"github.com/Jkaotlic/wg-monitor/internal/backend/sealedfile"
)

// Предупреждения сводки парка про ключи кабинетов на диске (v0.55, B1).
// Тексты -- по спеке v0.56 (B5); экспортированы, чтобы песочница показывала те же.
const (
	CabinetSealWarnOpen       = "Ключи кабинетов лежат на сервере открытым текстом. Чтобы зашифровать, укажите файл «revive.key» в настройках сервера и перезапустите — инструкция в DEPLOY.md"
	CabinetSealWarnNoKey      = "Ключи кабинетов зашифрованы, а файл «revive.key» не найден. Верните прежний файл — добавлять ключи заново не нужно"
	CabinetSealWarnWrongKey   = "Ключи кабинетов зашифрованы другим файлом «revive.key». Верните прежний, не создавайте новый"
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
		removeStaleSealTemps(st.Path)
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
			return CabinetSealWarnNoKey
		}
		if len(present) == 0 {
			return ""
		}
		log.Warn("ключи кабинетов лежат на диске открытыми: ключ шифрования (revive.key_file) не задан или не прочитан",
			"reason", keyErr, "stores", storeNames(present))
		return CabinetSealWarnOpen
	}

	// Сначала -- что каждое зашифрованное хранилище открывается этим ключом.
	// Хоть одно нет -- ключ не тот (fix round 1): открытые файлы НЕ
	// перешифровываются, иначе после возврата прежнего ключа уже они
	// перестали бы читаться и ни один ключ не читал бы все хранилища.
	var foreign []StoreFile
	for _, st := range sealed {
		if _, err := sealedfile.ReadFile(st.Path, st.Name); err != nil {
			foreign = append(foreign, st)
		}
	}
	if len(foreign) > 0 {
		// Режим «ключ не тот» (fix round 2): и рантайм не шифрует открытые
		// хранилища этим ключом, а запись поверх шифра отказывает.
		sealedfile.SetWrongKey(box)
		log.Error("хранилища кабинетов не расшифровываются этим ключом — открытые не перешифрованы, файлы не тронуты; верните прежний ключ шифрования",
			"stores", storeNames(foreign))
		return CabinetSealWarnWrongKey
	}

	var warns []string
	for _, st := range present {
		changed, err := sealedfile.Reseal(st.Path, st.Name)
		if err != nil {
			log.Warn("хранилище кабинетов не зашифровано — осталось открытым", "store", st.Name, "err", err)
			warns = append(warns, cabinetSealWarnPartlyOpen)
			continue
		}
		if changed {
			log.Info("хранилище кабинетов зашифровано", "store", st.Name)
		}
	}
	return joinSealWarnings(warns)
}

// removeStaleSealTemps убирает .<имя>.tmp-*, оставшиеся рядом с хранилищем
// после падения посреди записи (sealedfile.WriteFile): в них секреты, а
// читать их некому. Только обычные файлы; зовётся на старте, до того как
// хранилища кто-то пишет.
func removeStaleSealTemps(storePath string) {
	pattern := filepath.Join(filepath.Dir(storePath), "."+globEscape(filepath.Base(storePath))+sealedfile.TempInfix+"*")
	matches, err := filepath.Glob(pattern)
	if err != nil {
		return
	}
	for _, m := range matches {
		if info, err := os.Lstat(m); err == nil && info.Mode().IsRegular() {
			_ = os.Remove(m)
		}
	}
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

// joinSealWarnings -- все предупреждения одной строкой, без повторов: одно
// не затирает другое.
func joinSealWarnings(ws []string) string {
	var out []string
	for _, w := range ws {
		if w != "" && !slices.Contains(out, w) {
			out = append(out, w)
		}
	}
	return strings.Join(out, ". ")
}
