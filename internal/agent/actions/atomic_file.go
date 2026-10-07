package actions

import (
	"os"
	"path/filepath"
)

// writeFileAtomic пишет файл целиком или никак: временный файл в том же
// каталоге (точка в начале имени, права 0600 до самого конца) и
// переименование. Как у ndm-хука (wakehook): каталог init.d и cron
// исполняют то, что видят, и недописанный исполняемый файл туда попасть не
// должен. Имя временного файла не начинается на S -- init.d запустил бы его
// при загрузке.
func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	tmp := filepath.Join(filepath.Dir(path), "."+filepath.Base(path)+".tmp")
	// Остаток прошлой попытки сохранил бы свои права при WriteFile.
	_ = os.Remove(tmp)
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Chmod(tmp, perm); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}
