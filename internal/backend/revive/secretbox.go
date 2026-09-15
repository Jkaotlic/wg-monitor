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
	"io"
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

// secretsValues -- настоящие поля секрета: и внутреннее хранилище Secrets, и
// форма, которая шифруется в Box.Seal/Open. json-теги нужны только для
// второго -- наружу пакета этот тип не смотрит.
type secretsValues struct {
	RootPassword string `json:"root_password,omitempty"`
	AWGMLogin    string `json:"awgm_login,omitempty"`
	AWGMPassword string `json:"awgm_password,omitempty"`
	AWGMAPIKey   string `json:"awgm_api_key,omitempty"`
}

// Secrets -- учётные данные для переустановки. Открытый текст лежит за
// указателем на СТРОКУ (JSON secretsValues), а не в собственных полях
// Secrets и не за указателем на структуру -- это не стиль, а требование
// маскировки (fix round 1, Important #1). fmt/slog зовут
// String/Format/LogValue/MarshalJSON только когда могут вызвать Interface()
// на значении: для Secrets напрямую и для Secrets в ЭКСПОРТИРУЕМОМ поле
// чужой структуры это так. Но когда Secrets лежит в НЕЭКСПОРТИРУЕМОМ поле
// чужой структуры (так и будут устроены задания воркера, Task 5+),
// Interface() вызвать нельзя, методы не срабатывают, и печать идёт
// структурной reflection-веткой fmt в обход методов. Для большинства
// глаголов (%v, %+v, %#v, %d) это безопасно и тогда: reflection просто
// печатает адрес указателя. Но для глагола, для которого у *T нет
// обработчика (например %s на указателе), fmt.badVerb сбрасывает глубину
// рекурсии до 0 и на этой глубине САМ разыменовывает указатель -- если
// цель указателя (Elem()) имеет вид Struct/Array/Slice/Map. Проверено
// (fix round 1): с указателем на secretsValues это печатало пароли текстом.
// Указатель на string под такое разыменование не попадает (String -- не
// один из четырёх видов), поэтому пароли снова недостижимы. Открытый текст
// достаётся только явными методами ниже.
type Secrets struct {
	blob *string
}

// NewSecrets собирает Secrets из открытых значений.
func NewSecrets(rootPassword, awgmLogin, awgmPassword, awgmAPIKey string) Secrets {
	return secretsFromValues(secretsValues{
		RootPassword: rootPassword,
		AWGMLogin:    awgmLogin,
		AWGMPassword: awgmPassword,
		AWGMAPIKey:   awgmAPIKey,
	})
}

// secretsFromValues упаковывает поля в JSON-строку за указателем. Marshal
// строк без циклов и каналов не отказывает, поэтому ошибку здесь глотать
// безопасно -- в проде до неё дойти неоткуда.
func secretsFromValues(v secretsValues) Secrets {
	b, err := json.Marshal(v)
	if err != nil {
		return Secrets{}
	}
	blob := string(b)
	return Secrets{blob: &blob}
}

func (s Secrets) values() secretsValues {
	if s.blob == nil {
		return secretsValues{}
	}
	var v secretsValues
	_ = json.Unmarshal([]byte(*s.blob), &v)
	return v
}

func (s Secrets) RootPassword() string { return s.values().RootPassword }
func (s Secrets) AWGMLogin() string    { return s.values().AWGMLogin }
func (s Secrets) AWGMPassword() string { return s.values().AWGMPassword }
func (s Secrets) AWGMAPIKey() string   { return s.values().AWGMAPIKey }

// Equal сравнивает секреты по значению. Через == нельзя: поле blob --
// указатель, и два секрета с одинаковыми паролями после раздельных
// NewSecrets/Open никогда не окажутся одним и тем же указателем.
func (s Secrets) Equal(o Secrets) bool { return s.values() == o.values() }

const maskedSecrets = "скрыто"

func (Secrets) String() string     { return "revive.Secrets{" + maskedSecrets + "}" }
func (s Secrets) GoString() string { return s.String() }

// Format перехватывает печать Secrets целиком. Stringer обслуживает только
// %v/%s; без Format, например, %d печатает пароль внутри текста ошибки вида
// "%!d(string=...)" (fix round 1, Important #1) -- Format отвечает за любой
// глагол одинаково, не заглядывая в него.
func (s Secrets) Format(f fmt.State, _ rune) { _, _ = io.WriteString(f, s.String()) }

func (Secrets) LogValue() slog.Value         { return slog.StringValue(maskedSecrets) }
func (Secrets) MarshalJSON() ([]byte, error) { return json.Marshal(maskedSecrets) }

// Usable -- хватает ли данных на переустановку. Пре-флайт координатора
// 15.09: движок переустановки без root-пароля отказывает
// (provision_handler.go:466-471, root_password_required) -- вход в панель
// awg-manager дополняет root, но не заменяет его. Usable требует ровно
// RootPassword. Пароль из одних пробелов -- всё равно что пустой; сам пароль
// при этом хранится необрезанным.
func (s Secrets) Usable() bool { return strings.TrimSpace(s.RootPassword()) != "" }

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
	plain, err := json.Marshal(s.values())
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
	var w secretsValues
	if err := json.Unmarshal(plain, &w); err != nil {
		return Secrets{}, ErrSecretUnreadable
	}
	return secretsFromValues(w), nil
}
