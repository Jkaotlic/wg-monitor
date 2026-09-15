// Package revive -- оживление агента на выключенном роутере: намерение,
// зашифрованный секрет, опрос панели и запуск переустановки, когда роутер
// появился.
//
// Решение оператора: «Полный автомат: пароль root или вход в панель
// awg-manager вводится один раз при постановке, хранится на Pi зашифрованным
// до успеха, отмены или срока, затем стирается. Переустановка запускается без
// участия человека.»
package revive

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
)

// KeySize -- длина ключа AES-256.
const KeySize = 32

// ErrKeyNotConfigured -- revive.key_file не задан: функция выключена.
var ErrKeyNotConfigured = errors.New("revive.key_file не задан")

// ErrSecretUnreadable -- секрет не расшифровывается: другой ключ, чужой
// роутер или битые байты. Причину наружу не раскрываем.
var ErrSecretUnreadable = errors.New("секрет оживления не расшифровывается")

// LoadKey читает ключ из файла: 32 байта в стандартном base64, пробелы и
// перевод строки по краям допустимы. Тексты ошибок никогда не содержат
// содержимого файла.
func LoadKey(path string) ([]byte, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, ErrKeyNotConfigured
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("файл ключа оживления не прочитан: %w", err)
	}
	key, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(raw)))
	clear(raw)
	if err != nil {
		return nil, errors.New("файл ключа оживления не в base64")
	}
	if len(key) != KeySize {
		n := len(key)
		clear(key)
		return nil, fmt.Errorf("ключ оживления длиной %d байт, нужно %d", n, KeySize)
	}
	return key, nil
}

// Secrets -- учётные данные для переустановки. Существует открытым текстом
// только в памяти, между Open и вызовом движка. Все способы напечатать
// значение маскируют его.
type Secrets struct {
	RootPassword string
	AWGMLogin    string
	AWGMPassword string
	AWGMAPIKey   string
}

const maskedSecrets = "скрыто"

func (Secrets) String() string               { return "revive.Secrets{" + maskedSecrets + "}" }
func (s Secrets) GoString() string           { return s.String() }
func (Secrets) LogValue() slog.Value         { return slog.StringValue(maskedSecrets) }
func (Secrets) MarshalJSON() ([]byte, error) { return json.Marshal(maskedSecrets) }

// Usable -- хватает ли данных хоть на один способ входа: root-пароль
// терминала, API-ключ панели или пара логин+пароль панели.
// Usable: root-пароль обязателен. Пре-флайт 15.09: движок переустановки без него отказывает
// (provision_handler.go:466-471 root_password_required), а вход в панель -- дополнительный.
func (s Secrets) Usable() bool {
	return s.RootPassword != ""
}

// secretsWire -- форма, которая шифруется. Отдельная структура, потому что
// у Secrets MarshalJSON намеренно маскирующий.
type secretsWire struct {
	RootPassword string `json:"root_password,omitempty"`
	AWGMLogin    string `json:"awgm_login,omitempty"`
	AWGMPassword string `json:"awgm_password,omitempty"`
	AWGMAPIKey   string `json:"awgm_api_key,omitempty"`
}

// Box -- AES-256-GCM с AAD = десятичный user_id: шифртекст одного роутера,
// переложенный в строку другого, не расшифруется.
type Box struct{ aead cipher.AEAD }

func NewBox(key []byte) (*Box, error) {
	if len(key) != KeySize {
		return nil, fmt.Errorf("ключ оживления длиной %d байт, нужно %d", len(key), KeySize)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &Box{aead: aead}, nil
}

func aad(routerID int64) []byte { return []byte(strconv.FormatInt(routerID, 10)) }

func (b *Box) Seal(routerID int64, s Secrets) (nonce, ciphertext []byte, err error) {
	plain, err := json.Marshal(secretsWire(s))
	if err != nil {
		return nil, nil, errors.New("секрет оживления не упакован")
	}
	defer clear(plain)
	nonce = make([]byte, b.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, nil, errors.New("не получен случайный nonce")
	}
	return nonce, b.aead.Seal(nil, nonce, plain, aad(routerID)), nil
}

func (b *Box) Open(routerID int64, nonce, ciphertext []byte) (Secrets, error) {
	if len(nonce) != b.aead.NonceSize() {
		return Secrets{}, ErrSecretUnreadable
	}
	plain, err := b.aead.Open(nil, nonce, ciphertext, aad(routerID))
	if err != nil {
		return Secrets{}, ErrSecretUnreadable
	}
	defer clear(plain)
	var w secretsWire
	if err := json.Unmarshal(plain, &w); err != nil {
		return Secrets{}, ErrSecretUnreadable
	}
	return Secrets(w), nil
}
