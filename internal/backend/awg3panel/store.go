package awg3panel

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/Jkaotlic/wg-monitor/internal/backend/sealedfile"
)

// DefaultStoreName -- файл панелей; кладётся рядом с amnezia-selfhosted.json.
const DefaultStoreName = "awg3-panels.json"

// Lock -- предохранитель, взведённый отказом панели. Снимается только
// пересохранением учётных данных админом.
type Lock string

const (
	LockNone        Lock = ""
	LockBadPassword Lock = "bad_password"
	LockCert        Lock = "cert_rejected"
	// LockServerCert -- наш клиент не смог проверить сертификат панели
	// (отдельно от LockCert -- панель отвергла наш сертификат).
	LockServerCert Lock = "server_cert_rejected"
)

// Issuer -- человек, которому админ разрешил выпускать конфиги с этой панели
// на свои роутеры (v0.51). Секретом не является, но уходит наружу только
// админу -- в View.
type Issuer struct {
	TelegramUserID int64     `json:"tg"`
	GrantedBy      int64     `json:"by"`
	GrantedAt      time.Time `json:"at"`
}

// Instance -- одна панель. Password, CertPEM, KeyPEM -- секреты: из пакета
// наружу уходит только View, печать структуры -- «[скрыто]». Состояние
// предохранителя (Lock, PausedUntil) и Readonly живут на диске: перезапуск
// бэкенда не должен дарить панели ещё одну неудачу.
type Instance struct {
	ID           string    `json:"id"`
	Label        string    `json:"label"`
	BaseURL      string    `json:"base_url"`
	User         string    `json:"user"`
	Password     string    `json:"password"`
	CertPEM      string    `json:"cert_pem"`
	KeyPEM       string    `json:"key_pem"`
	CertSubject  string    `json:"cert_subject,omitempty"`
	CertNotAfter time.Time `json:"cert_not_after,omitzero"`
	Enabled      bool      `json:"enabled"`
	Lock         Lock      `json:"lock,omitempty"`
	PausedUntil  time.Time `json:"paused_until,omitzero"`
	Readonly     bool      `json:"readonly,omitempty"`
	Issuers      []Issuer  `json:"issuers,omitempty"`
}

func (Instance) String() string       { return hiddenValue }
func (Instance) GoString() string     { return hiddenValue }
func (Instance) LogValue() slog.Value { return slog.StringValue(hiddenValue) }

type Store struct {
	Version   int        `json:"version"`
	Instances []Instance `json:"instances"`
}

func LoadStore(path string) (Store, error) {
	body, err := sealedfile.ReadFile(path, sealedfile.DomainAwg3)
	if errors.Is(err, os.ErrNotExist) {
		return Store{Version: 1}, nil
	}
	if err != nil {
		return Store{}, fmt.Errorf("чтение %s: %w", filepath.Base(path), err)
	}
	var st Store
	if err := json.Unmarshal(body, &st); err != nil {
		// Текст ошибки json не печатается: файл хранит пароли.
		return Store{}, fmt.Errorf("файл панелей %s повреждён", filepath.Base(path))
	}
	if st.Version == 0 {
		st.Version = 1
	}
	return st, nil
}

// SaveStore -- целиком, атомарно (временный файл 0600 → fsync → rename),
// каталог 0700.
func SaveStore(path string, st Store) error {
	if st.Version == 0 {
		st.Version = 1
	}
	if st.Instances == nil {
		st.Instances = []Instance{}
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("каталог панелей: %w", err)
	}
	body, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return fmt.Errorf("запись панелей: %w", err)
	}
	// Атомарно (временный файл 0600, fsync, rename): без fsync при отвале
	// питания на диске мог оказаться пустой файл, и секреты панелей терялись
	// молча. С ключом шифрования -- шифр (v0.55, B1).
	if err := sealedfile.WriteFile(path, sealedfile.DomainAwg3, append(body, '\n')); err != nil {
		return fmt.Errorf("запись файла панелей: %w", err)
	}
	return nil
}

// FieldError -- поле формы не прошло проверку. Reason -- для человека и
// никогда не содержит введённого секрета.
type FieldError struct {
	Field  string
	Reason string
}

func (e *FieldError) Error() string { return e.Field + ": " + e.Reason }

var instanceIDRe = regexp.MustCompile(`^[a-z][a-z0-9_-]{1,15}$`)

func ValidInstanceID(id string) bool { return instanceIDRe.MatchString(id) }

// NormalizeBaseURL -- https://хост[:порт][/путь] без логина в адресе, запроса
// и якоря; хвостовой «/» срезается.
func NormalizeBaseURL(raw string) (string, error) {
	s := strings.TrimRight(strings.TrimSpace(raw), "/")
	if strings.ContainsAny(s, " \t\r\n") {
		return "", &FieldError{Field: "base_url", Reason: "Адрес панели — без пробелов"}
	}
	u, err := url.Parse(s)
	if err != nil || u.Hostname() == "" {
		return "", &FieldError{Field: "base_url", Reason: "Адрес панели: https://имя-или-IP[:порт]"}
	}
	if u.Scheme != "https" {
		return "", &FieldError{Field: "base_url", Reason: "Адрес панели — только https://: пароль и сертификат не ходят открытым текстом"}
	}
	if u.User != nil {
		return "", &FieldError{Field: "base_url", Reason: "Логин и пароль — в своих полях, не в адресе"}
	}
	if u.RawQuery != "" || u.Fragment != "" || u.ForceQuery {
		return "", &FieldError{Field: "base_url", Reason: "Адрес панели — без «?» и «#»"}
	}
	return u.String(), nil
}

func hasControl(s string) bool {
	for _, r := range s {
		if unicode.IsControl(r) {
			return true
		}
	}
	return false
}

// validateInstance -- порядок проверок = порядок полей формы.
func validateInstance(inst Instance) error {
	if !instanceIDRe.MatchString(inst.ID) {
		return &FieldError{Field: "id", Reason: "Короткое имя: латиница в нижнем регистре, цифры, «-» и «_», от 2 до 16 знаков, первая — буква"}
	}
	if utf8.RuneCountInString(inst.Label) > 40 || hasControl(inst.Label) {
		return &FieldError{Field: "label", Reason: "Название — до 40 знаков, без переводов строки"}
	}
	if _, err := NormalizeBaseURL(inst.BaseURL); err != nil {
		return err
	}
	if inst.User == "" || utf8.RuneCountInString(inst.User) > 64 || strings.Contains(inst.User, ":") || hasControl(inst.User) {
		return &FieldError{Field: "user", Reason: "Логин панели: до 64 знаков, без «:»"}
	}
	if inst.Password == "" || utf8.RuneCountInString(inst.Password) > 256 || hasControl(inst.Password) {
		return &FieldError{Field: "password", Reason: "Нужен пароль панели (до 256 знаков)"}
	}
	if inst.CertPEM == "" || inst.KeyPEM == "" {
		return &FieldError{Field: "p12", Reason: "Нужен файл .p12 с клиентским сертификатом"}
	}
	return nil
}
