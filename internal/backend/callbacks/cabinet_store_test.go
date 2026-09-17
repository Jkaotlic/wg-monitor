package callbacks

import (
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"

	"github.com/Jkaotlic/wg-monitor/internal/backend"
)

func TestCabinetSecretMaskAndRedact(t *testing.T) {
	if got := cabinetSecretMask("vpn://abcdefgh1234"); got != "••••1234" {
		t.Fatalf("маска ключа = %q", got)
	}
	if got := cabinetSecretMask("12345678"); got != "••••" {
		t.Fatalf("короткий секрет не показывает ни знака: %q", got)
	}
	if got := redactSecret("amnezia login: bad vpn://SECRET-X", "vpn://SECRET-X"); got != "amnezia login: bad [скрыто]" {
		t.Fatalf("redact = %q", got)
	}
	if got := redactSecret("text", ""); got != "text" {
		t.Fatalf("пустой секрет ничего не заменяет: %q", got)
	}
}

// Запись -- «прочитать, изменить, записать» целым файлом. Без замка два
// параллельных добавления читали один файл, и второе затирало первое.
func TestAmneziaSecretsParallelAddsKeepEveryKey(t *testing.T) {
	r := &Router{cfg: Config{AmneziaSecretsPath: filepath.Join(t.TempDir(), "amnezia-premium.json")}}
	const n = 16
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if _, err := r.addAmneziaKeyLabeled(7, fmt.Sprintf("vpn://parallel-key-%02d", i), ""); err != nil {
				t.Errorf("add %d: %v", i, err)
			}
		}(i)
	}
	wg.Wait()
	keys, err := r.listAmneziaKeys(7)
	if err != nil {
		t.Fatal(err)
	}
	if len(keys.Keys) != n {
		t.Fatalf("сохранилось %d ключей из %d: параллельная запись потеряла ключи", len(keys.Keys), n)
	}
}

func TestHideMySecretsParallelAddsKeepEveryCode(t *testing.T) {
	r := &Router{cfg: Config{HideMySecretsPath: filepath.Join(t.TempDir(), "hidemyname.json")}}
	const n = 16
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if _, err := r.addHideMyCodeLabeled(7, fmt.Sprintf("10000000000%02d", i), ""); err != nil {
				t.Errorf("add %d: %v", i, err)
			}
		}(i)
	}
	wg.Wait()
	codes, err := r.listHideMyCodes(7)
	if err != nil {
		t.Fatal(err)
	}
	if len(codes.Codes) != n {
		t.Fatalf("сохранилось %d кодов из %d", len(codes.Codes), n)
	}
}

func TestAmneziaSecretsLabelActiveAndNotFound(t *testing.T) {
	r := &Router{cfg: Config{AmneziaSecretsPath: filepath.Join(t.TempDir(), "amnezia-premium.json")}}
	first, err := r.addAmneziaKeyLabeled(7, "vpn://first-key-0001", "Дом")
	if err != nil || first.Label != "Дом" {
		t.Fatalf("first = %+v, err=%v", first, err)
	}
	second, err := r.addAmneziaKeyLabeled(7, "vpn://second-key-0002", "")
	if err != nil || second.Label != "Ключ #2" {
		t.Fatalf("second = %+v, err=%v", second, err)
	}
	// Тот же ключ второй раз -- не новый, а переименованный и активный.
	again, err := r.addAmneziaKeyLabeled(7, "vpn://first-key-0001", "Дача")
	if err != nil || again.ID != first.ID || again.Label != "Дача" {
		t.Fatalf("again = %+v, err=%v", again, err)
	}
	if err := r.setActiveAmneziaKey(7, second.ID); err != nil {
		t.Fatal(err)
	}
	keys, _ := r.listAmneziaKeys(7)
	if keys.ActiveID != second.ID || len(keys.Keys) != 2 {
		t.Fatalf("keys = %+v", keys)
	}
	if err := r.setActiveAmneziaKey(7, "000000000000"); !errors.Is(err, backend.ErrCabinetSecretNotFound) {
		t.Fatalf("активный несуществующий: %v", err)
	}
	if err := r.deleteAmneziaKey(7, "000000000000"); !errors.Is(err, backend.ErrCabinetSecretNotFound) {
		t.Fatalf("удаление несуществующего: %v", err)
	}
	if _, err := r.addAmneziaKeyLabeled(7, "not-a-key", ""); !errors.Is(err, backend.ErrCabinetSecretInvalid) {
		t.Fatalf("неверный вид: %v", err)
	}
}

func TestHideMySecretsLabelActiveAndNotFound(t *testing.T) {
	r := &Router{cfg: Config{HideMySecretsPath: filepath.Join(t.TempDir(), "hidemyname.json")}}
	first, err := r.addHideMyCodeLabeled(7, "123456789012345", "Работа")
	if err != nil || first.Label != "Работа" {
		t.Fatalf("first = %+v, err=%v", first, err)
	}
	second, err := r.addHideMyCodeLabeled(7, "987654321098765", "")
	if err != nil || second.Label != "Код #2" {
		t.Fatalf("second = %+v, err=%v", second, err)
	}
	if err := r.setActiveHideMyCode(7, first.ID); err != nil {
		t.Fatal(err)
	}
	codes, _ := r.listHideMyCodes(7)
	if codes.ActiveID != first.ID {
		t.Fatalf("codes = %+v", codes)
	}
	if err := r.setActiveHideMyCode(7, "000000000000"); !errors.Is(err, backend.ErrCabinetSecretNotFound) {
		t.Fatalf("активный несуществующий: %v", err)
	}
	if err := r.deleteHideMyCode(7, "000000000000"); !errors.Is(err, backend.ErrCabinetSecretNotFound) {
		t.Fatalf("удаление несуществующего: %v", err)
	}
	if _, err := r.addHideMyCodeLabeled(7, "vpn://not-a-code", ""); !errors.Is(err, backend.ErrCabinetSecretInvalid) {
		t.Fatalf("неверный вид: %v", err)
	}
}
