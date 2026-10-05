// Package sealedfile -- файлы кабинетов на диске, зашифрованные тем же
// ключом, что пароли root (revive.key, revive.Box), v0.55 (B1).
//
// Формат: первая строка -- заголовок версии Magic, дальше одна строка
// base64(nonce || шифр AES-256-GCM). Открытый JSON начинается с «{» или
// пробела и с заголовком не спутается. AAD -- домен файла (его постоянное
// имя): шифр одного хранилища, подложенный вместо другого, не откроется.
//
// Ключ один на процесс и ставится на старте (SetKey). Нет ключа -- файлы
// читаются и пишутся открытыми, как до v0.55; зашифрованный файл без ключа
// не читается (ErrKeyMissing) и не перезаписывается.
package sealedfile

import (
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync/atomic"

	"github.com/Jkaotlic/wg-monitor/internal/backend/revive"
)

// Домены -- постоянные имена хранилищ; под ними же файлы едут в архив.
const (
	DomainAmnezia    = "amnezia-premium.json" // #nosec G101 -- имя файла хранилища, не секрет
	DomainHideMy     = "hidemyname.json"
	DomainSelfHosted = "amnezia-selfhosted.json"
	DomainAwg3       = "awg3-panels.json"
)

// Magic -- заголовок версии зашифрованного файла.
const Magic = "wg-monitor-sealed v1\n"

// magicFamily -- общая приставка всех версий: файл будущей версии -- тоже
// шифр, а не повреждённый JSON.
const magicFamily = "wg-monitor-sealed "

// ErrKeyMissing -- файл зашифрован, а ключа у процесса нет. Текст -- для
// человека: он доходит до экрана кабинета.
var ErrKeyMissing = errors.New("ключ шифрования не найден — ключи кабинетов не прочитать")

// ErrUnreadable -- файл зашифрован, но не этим ключом (или байты битые).
var ErrUnreadable = errors.New("ключи кабинетов не расшифровываются: ключ шифрования не тот, которым они записаны")

var box atomic.Pointer[revive.Box]

// SetKey ставит ключ процесса; nil -- ключа нет.
func SetKey(b *revive.Box) { box.Store(b) }

func currentBox() *revive.Box { return box.Load() }

// Enabled -- есть ли у процесса ключ: новые записи шифруются.
func Enabled() bool { return currentBox() != nil }

// IsSealed -- байты файла начинаются с заголовка шифра (любой версии).
func IsSealed(body []byte) bool { return bytes.HasPrefix(body, []byte(magicFamily)) }

// Decode возвращает открытые байты: открытый файл -- как есть, шифр --
// расшифрованным.
func Decode(domain string, body []byte) ([]byte, error) {
	return DecodeWith(currentBox(), domain, body)
}

// DecodeWith -- Decode явным ключом, мимо ключа процесса: проверка
// восстановления бэкапа берёт ключ из конфига сама. b == nil -- ключа нет.
func DecodeWith(b *revive.Box, domain string, body []byte) ([]byte, error) {
	if !IsSealed(body) {
		return body, nil
	}
	if b == nil {
		return nil, ErrKeyMissing
	}
	if !bytes.HasPrefix(body, []byte(Magic)) {
		return nil, fmt.Errorf("%w (неизвестная версия формата)", ErrUnreadable)
	}
	raw, err := base64.StdEncoding.DecodeString(string(bytes.TrimSpace(body[len(Magic):])))
	if err != nil {
		return nil, ErrUnreadable
	}
	defer clear(raw)
	const nonceSize = 12 // GCM
	if len(raw) < nonceSize {
		return nil, ErrUnreadable
	}
	plain, err := b.OpenBlob(domain, raw[:nonceSize], raw[nonceSize:])
	if err != nil {
		return nil, ErrUnreadable
	}
	return plain, nil
}

// Encode -- байты для диска: с ключом -- шифр с заголовком, без ключа --
// открытые как есть.
func Encode(domain string, plain []byte) ([]byte, error) {
	b := currentBox()
	if b == nil {
		return plain, nil
	}
	nonce, ct, err := b.SealBlob(domain, plain)
	if err != nil {
		return nil, err
	}
	raw := append(nonce, ct...)
	out := make([]byte, 0, len(Magic)+base64.StdEncoding.EncodedLen(len(raw))+1)
	out = append(out, Magic...)
	out = base64.StdEncoding.AppendEncode(out, raw)
	out = append(out, '\n')
	return out, nil
}

// ReadFile читает файл и возвращает открытые байты. Нет файла -- ошибка с
// os.ErrNotExist, как у os.ReadFile.
func ReadFile(path, domain string) ([]byte, error) {
	body, err := os.ReadFile(path) // #nosec G304 -- путь хранилища из конфига
	if err != nil {
		return nil, err
	}
	return Decode(domain, body)
}

// WriteFile пишет файл атомарно: временный файл 0600 рядом, fsync, rename,
// fsync каталога; каталог создаётся 0700. С ключом -- шифр. Без ключа
// существующий зашифрованный файл не перезаписывается (ErrKeyMissing):
// открытая запись поверх него стёрла бы остальные секреты.
func WriteFile(path, domain string, plain []byte) error {
	if !Enabled() {
		sealed, err := fileIsSealed(path)
		if err != nil {
			return err
		}
		if sealed {
			return ErrKeyMissing
		}
	}
	body, err := Encode(domain, plain)
	if err != nil {
		return err
	}
	return writeAtomic(path, body)
}

// Reseal перешифровывает открытый файл на месте. Ключа нет, файла нет или
// он уже зашифрован -- ничего не делает (повторный старт безопасен).
// changed -- файл переписан.
func Reseal(path, domain string) (changed bool, err error) {
	if !Enabled() {
		return false, nil
	}
	body, err := os.ReadFile(path) // #nosec G304 -- путь хранилища из конфига
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	defer clear(body)
	if IsSealed(body) {
		return false, nil
	}
	if err := WriteFile(path, domain, body); err != nil {
		return false, err
	}
	return true, nil
}

func fileIsSealed(path string) (bool, error) {
	f, err := os.Open(path) // #nosec G304 -- путь хранилища из конфига
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	defer f.Close()
	head := make([]byte, len(magicFamily))
	n, err := io.ReadFull(f, head)
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) && !errors.Is(err, io.EOF) {
		return false, err
	}
	return IsSealed(head[:n]), nil
}

func writeAtomic(path string, body []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("каталог хранилища: %w", err)
	}
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("временный файл хранилища: %w", err)
	}
	tmpPath := tmp.Name()
	defer func() { _ = os.Remove(tmpPath) }() // после rename имени уже нет
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("права временного файла хранилища: %w", err)
	}
	if _, err := tmp.Write(body); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("запись временного файла хранилища: %w", err)
	}
	// Sync до rename: при отвале питания на диске не окажется пустого файла
	// под именем хранилища.
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("синхронизация временного файла хранилища: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("закрытие временного файла хранилища: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("замена файла хранилища: %w", err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return fmt.Errorf("права файла хранилища: %w", err)
	}
	if d, err := os.Open(dir); err == nil { // #nosec G304 -- каталог хранилища из конфига
		_ = d.Sync()
		_ = d.Close()
	}
	return nil
}
