package selfhostedamnezia

import (
	"context"
	"errors"
	"net/netip"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"
)

// Service -- свои VPN-серверы для мини-аппа: файл инстансов под замком и
// выпуск конфига под замком на инстанс.
//
// Замок файла нужен потому, что запись -- «прочитать, изменить, записать»
// целым файлом: два параллельных сохранения без него теряли одно. Замок
// инстанса -- потому, что выпуск читает серверный конфиг, выбирает свободный
// адрес и дописывает пира: два параллельных выпуска без замка выбрали бы
// один и тот же адрес.
type Service struct {
	path      string
	legacy    Config
	newRunner func(Config) Runner
	now       func() time.Time

	storeMu   sync.Mutex
	instLocks sync.Map // id -> *sync.Mutex
}

var (
	ErrInstanceNotFound = errors.New("свой сервер не найден")
	ErrInstanceExists   = errors.New("сервер с таким именем уже есть")
	ErrInstanceDisabled = errors.New("свой сервер выключен")
	ErrInstanceNotReady = errors.New("у своего сервера не заполнены адрес и порт для клиентов")
)

// FieldError -- поле формы не прошло проверку. Reason -- для человека.
type FieldError struct {
	Field  string
	Reason string
}

func (e *FieldError) Error() string { return e.Field + ": " + e.Reason }

// CheckResult -- итог «Проверить подключение», словами.
type CheckResult struct {
	OK      bool
	Message string
}

// NewService -- сервис над файлом path; legacy -- секция amnezia_selfhosted
// из backend.yaml (её поля -- умолчания для каждого инстанса, как раньше).
func NewService(path string, legacy Config) *Service {
	if strings.TrimSpace(path) == "" {
		path = legacy.StorePathOrDefault()
	}
	return &Service{path: strings.TrimSpace(path), legacy: legacy, newRunner: runnerForConfig, now: time.Now}
}

func (s *Service) List() ([]Instance, error) {
	s.storeMu.Lock()
	defer s.storeMu.Unlock()
	st, err := LoadStore(s.path, s.legacy)
	if err != nil {
		return nil, err
	}
	return append([]Instance(nil), st.Instances...), nil
}

func (s *Service) update(fn func(*Store) error) error {
	s.storeMu.Lock()
	defer s.storeMu.Unlock()
	st, err := LoadStore(s.path, s.legacy)
	if err != nil {
		return err
	}
	if err := fn(&st); err != nil {
		return err
	}
	return SaveStore(s.path, st)
}

func (s *Service) Create(inst Instance) error {
	inst = cleanInstance(inst)
	if err := ValidateInstance(inst); err != nil {
		return err
	}
	return s.update(func(st *Store) error {
		for _, cur := range st.Instances {
			if cur.ID == inst.ID {
				return ErrInstanceExists
			}
		}
		st.Instances = append(st.Instances, inst)
		return nil
	})
}

// Update заменяет поля инстанса целиком, кроме включённости. Пустой пароль
// SSH -- «не менять»; инстанс без SSH-адреса пароля не хранит.
func (s *Service) Update(id string, inst Instance) error {
	inst.ID = id
	inst = cleanInstance(inst)
	return s.update(func(st *Store) error {
		for i, cur := range st.Instances {
			if cur.ID != inst.ID {
				continue
			}
			inst.Enabled = cur.Enabled
			if inst.SSHHost != "" && inst.SSHPassword == "" {
				inst.SSHPassword = cur.SSHPassword
			}
			if err := ValidateInstance(inst); err != nil {
				return err
			}
			st.Instances[i] = inst
			return nil
		}
		return ErrInstanceNotFound
	})
}

func (s *Service) SetEnabled(id string, enabled bool) error {
	return s.update(func(st *Store) error {
		if !st.SetEnabled(id, enabled) {
			return ErrInstanceNotFound
		}
		return nil
	})
}

func (s *Service) Delete(id string) error {
	return s.update(func(st *Store) error {
		if !st.Delete(id) {
			return ErrInstanceNotFound
		}
		return nil
	})
}

// Issue выпускает нового клиента на инстансе под замком инстанса.
func (s *Service) Issue(ctx context.Context, id, clientName string) (IssuedConfig, Instance, error) {
	id = strings.ToLower(strings.TrimSpace(id))
	unlock := s.lockInstance(id)
	defer unlock()
	inst, cfg, err := s.readyInstance(id)
	if err != nil {
		return IssuedConfig{}, Instance{}, err
	}
	issued, err := IssueWithRunner(ctx, cfg, s.newRunner(cfg), clientName, s.now())
	if err != nil {
		return IssuedConfig{}, Instance{}, err
	}
	return issued, inst, nil
}

func (s *Service) lockInstance(id string) func() {
	v, _ := s.instLocks.LoadOrStore(id, &sync.Mutex{})
	mu := v.(*sync.Mutex)
	mu.Lock()
	return mu.Unlock
}

func (s *Service) find(id string) (Instance, error) {
	id = strings.ToLower(strings.TrimSpace(id))
	insts, err := s.List()
	if err != nil {
		return Instance{}, err
	}
	for _, inst := range insts {
		if inst.ID == id {
			return inst, nil
		}
	}
	return Instance{}, ErrInstanceNotFound
}

func (s *Service) readyInstance(id string) (Instance, Config, error) {
	inst, err := s.find(id)
	if err != nil {
		return Instance{}, Config{}, err
	}
	if !inst.Enabled {
		return Instance{}, Config{}, ErrInstanceDisabled
	}
	cfg := s.legacy.ProviderConfig(inst)
	if !cfg.Ready() {
		return Instance{}, Config{}, ErrInstanceNotReady
	}
	return inst, cfg, nil
}

// cleanInstance -- пробелы, регистр имени и умолчания SSH. Без SSH-адреса
// пользователь, порт и пароль не хранятся: пароль без адреса -- лишний секрет
// на диске.
func cleanInstance(inst Instance) Instance {
	inst.ID = strings.ToLower(strings.TrimSpace(inst.ID))
	inst.Label = strings.TrimSpace(inst.Label)
	if inst.Label == "" {
		inst.Label = inst.ID
	}
	inst.EndpointHost = strings.TrimSpace(inst.EndpointHost)
	inst.Container = strings.TrimSpace(inst.Container)
	inst.Interface = strings.TrimSpace(inst.Interface)
	inst.ConfigPath = strings.TrimSpace(inst.ConfigPath)
	inst.ClientsPath = strings.TrimSpace(inst.ClientsPath)
	inst.ServerPubPath = strings.TrimSpace(inst.ServerPubPath)
	inst.PSKPath = strings.TrimSpace(inst.PSKPath)
	var dns []string
	for _, v := range inst.DNS {
		if v = strings.TrimSpace(v); v != "" {
			dns = append(dns, v)
		}
	}
	inst.DNS = dns
	inst.SSHHost = strings.TrimSpace(inst.SSHHost)
	inst.SSHUser = strings.TrimSpace(inst.SSHUser)
	if inst.SSHHost == "" {
		inst.SSHPort, inst.SSHUser, inst.SSHPassword = 0, "", ""
		return inst
	}
	if inst.SSHPort == 0 {
		inst.SSHPort = 22
	}
	if inst.SSHUser == "" {
		inst.SSHUser = "root"
	}
	return inst
}

var (
	containerNameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,62}$`)
	ifaceNameRe     = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,15}$`)
	remotePathRe    = regexp.MustCompile(`^/[A-Za-z0-9._/-]{1,254}$`)
	sshUserRe       = regexp.MustCompile(`^[a-z_][a-z0-9_-]{0,31}$`)
)

// ValidateInstance проверяет поля формы. Порядок проверок -- порядок полей на
// экране: человек видит первую ошибку сверху.
func ValidateInstance(inst Instance) error {
	if !instanceIDRe.MatchString(inst.ID) {
		return &FieldError{Field: "id", Reason: "Имя: латиница в нижнем регистре, цифры, «-» и «_», от 2 до 16 знаков, первая — буква"}
	}
	if utf8.RuneCountInString(inst.Label) > 40 || hasControl(inst.Label) {
		return &FieldError{Field: "label", Reason: "Подпись — до 40 знаков, без переводов строки"}
	}
	if !hostLooksValid(inst.EndpointHost) {
		return &FieldError{Field: "endpoint_host", Reason: "Адрес для клиентов: имя или IP сервера, без пробелов и «/»"}
	}
	if inst.EndpointPort < 1 || inst.EndpointPort > 65535 {
		return &FieldError{Field: "endpoint_port", Reason: "Порт для клиентов — число от 1 до 65535"}
	}
	if inst.Container != "" && !containerNameRe.MatchString(inst.Container) {
		return &FieldError{Field: "container", Reason: "Имя контейнера: латиница, цифры, «_», «.», «-»"}
	}
	if inst.Interface != "" && !ifaceNameRe.MatchString(inst.Interface) {
		return &FieldError{Field: "interface", Reason: "Интерфейс: до 15 знаков — латиница, цифры, «_», «.», «-»"}
	}
	for _, p := range []struct{ field, value string }{
		{"config_path", inst.ConfigPath},
		{"clients_path", inst.ClientsPath},
		{"server_public_key_path", inst.ServerPubPath},
		{"preshared_key_path", inst.PSKPath},
	} {
		if p.value != "" && !remotePathRe.MatchString(p.value) {
			return &FieldError{Field: p.field, Reason: "Путь — абсолютный: «/», латиница, цифры, «.», «_», «-»"}
		}
	}
	for _, d := range inst.DNS {
		if _, err := netip.ParseAddr(d); err != nil {
			return &FieldError{Field: "dns", Reason: "DNS — IP-адреса, например 1.1.1.1"}
		}
	}
	if inst.SSHHost == "" {
		return nil
	}
	if !hostLooksValid(inst.SSHHost) {
		return &FieldError{Field: "ssh_host", Reason: "Адрес SSH: имя или IP сервера, без пробелов и «/»"}
	}
	if inst.SSHPort < 1 || inst.SSHPort > 65535 {
		return &FieldError{Field: "ssh_port", Reason: "Порт SSH — число от 1 до 65535"}
	}
	if !sshUserRe.MatchString(inst.SSHUser) {
		return &FieldError{Field: "ssh_user", Reason: "Пользователь SSH: латиница в нижнем регистре, цифры, «_», «-»"}
	}
	if inst.SSHPassword == "" {
		return &FieldError{Field: "ssh_password", Reason: "Нужен пароль SSH"}
	}
	return nil
}

func hostLooksValid(host string) bool {
	return host != "" && len(host) <= 253 && !strings.ContainsAny(host, " \t\r\n/")
}

func hasControl(s string) bool {
	for _, r := range s {
		if unicode.IsControl(r) {
			return true
		}
	}
	return false
}
