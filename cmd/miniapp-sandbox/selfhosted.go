package main

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/Jkaotlic/wg-monitor/internal/backend"
	"github.com/Jkaotlic/wg-monitor/internal/backend/selfhostedamnezia"
	"github.com/Jkaotlic/wg-monitor/internal/backend/tg"
)

// Свои VPN-серверы песочницы: два инстанса в памяти -- включённый с SSH
// (проверка успешна) и выключенный без SSH (проверка -- неудача). Поля
// проверяются настоящим selfhostedamnezia.ValidateInstance, чтобы экран формы
// видел те же отказы, что в проде.
type sandboxSelfHosted struct {
	mu        sync.Mutex
	instances []selfhostedamnezia.Instance
	issued    int
}

var _ backend.SelfHostedVPS = (*sandboxSelfHosted)(nil)

func newSandboxSelfHosted() *sandboxSelfHosted {
	return &sandboxSelfHosted{instances: []selfhostedamnezia.Instance{
		{ID: "home", Label: "Домашний VPS", Enabled: true, EndpointHost: "vpn.sandbox.example.com", EndpointPort: 47567,
			// Пароль-заглушка не литералом: gosec G101 ловит строку в поле SSHPassword,
			// а песочница по SSH не ходит вовсе.
			SSHHost: "203.0.113.10", SSHPort: 22, SSHUser: "root", SSHPassword: strings.Repeat("s", 8)},
		{ID: "reserve", Label: "Резервный", Enabled: false, EndpointHost: "vpn2.sandbox.example.com", EndpointPort: 51820,
			DNS: []string{"1.1.1.1", "8.8.8.8"}},
	}}
}

func (s *sandboxSelfHosted) index(id string) int {
	for i, inst := range s.instances {
		if inst.ID == id {
			return i
		}
	}
	return -1
}

// sandboxCleanInstance -- то же, что делает прод перед проверкой: пробелы, регистр,
// умолчания SSH; без SSH-адреса пароль не хранится.
func sandboxCleanInstance(inst selfhostedamnezia.Instance) selfhostedamnezia.Instance {
	inst.ID = strings.ToLower(strings.TrimSpace(inst.ID))
	inst.Label = strings.TrimSpace(inst.Label)
	if inst.Label == "" {
		inst.Label = inst.ID
	}
	inst.EndpointHost = strings.TrimSpace(inst.EndpointHost)
	inst.SSHHost = strings.TrimSpace(inst.SSHHost)
	if inst.SSHHost == "" {
		inst.SSHPort, inst.SSHUser, inst.SSHPassword = 0, "", ""
		return inst
	}
	if inst.SSHPort == 0 {
		inst.SSHPort = 22
	}
	if strings.TrimSpace(inst.SSHUser) == "" {
		inst.SSHUser = "root"
	}
	return inst
}

func (s *sandboxSelfHosted) List() ([]selfhostedamnezia.Instance, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]selfhostedamnezia.Instance{}, s.instances...), nil
}

func (s *sandboxSelfHosted) Defaults() selfhostedamnezia.Config {
	return selfhostedamnezia.Config{
		Container: "amnezia-awg2", Interface: "awg0",
		ConfigPath: "/opt/amnezia/awg/awg0.conf", ClientsPath: "/opt/amnezia/awg/clientsTable",
		ServerPubPath: "/opt/amnezia/awg/wireguard_server_public_key.key", PSKPath: "/opt/amnezia/awg/wireguard_psk.key",
		DNS: []string{"1.1.1.1"}, SSHPort: 22, SSHUser: "root",
	}
}

func (s *sandboxSelfHosted) Create(inst selfhostedamnezia.Instance) error {
	inst = sandboxCleanInstance(inst)
	if err := selfhostedamnezia.ValidateInstance(inst); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.index(inst.ID) >= 0 {
		return selfhostedamnezia.ErrInstanceExists
	}
	s.instances = append(s.instances, inst)
	return nil
}

func (s *sandboxSelfHosted) Update(id string, inst selfhostedamnezia.Instance) error {
	inst.ID = id
	inst = sandboxCleanInstance(inst)
	s.mu.Lock()
	defer s.mu.Unlock()
	i := s.index(inst.ID)
	if i < 0 {
		return selfhostedamnezia.ErrInstanceNotFound
	}
	inst.Enabled = s.instances[i].Enabled
	if inst.SSHHost != "" && inst.SSHPassword == "" {
		cur := s.instances[i]
		if inst.SSHHost != cur.SSHHost || inst.SSHPort != cur.SSHPort || inst.SSHUser != cur.SSHUser {
			return &selfhostedamnezia.FieldError{Field: "ssh_password", Reason: "Адрес SSH изменён — введите пароль заново"}
		}
		inst.SSHPassword = cur.SSHPassword
	}
	if err := selfhostedamnezia.ValidateInstance(inst); err != nil {
		return err
	}
	s.instances[i] = inst
	return nil
}

func (s *sandboxSelfHosted) SetEnabled(id string, enabled bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	i := s.index(id)
	if i < 0 {
		return selfhostedamnezia.ErrInstanceNotFound
	}
	s.instances[i].Enabled = enabled
	return nil
}

func (s *sandboxSelfHosted) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	i := s.index(id)
	if i < 0 {
		return selfhostedamnezia.ErrInstanceNotFound
	}
	s.instances = append(s.instances[:i:i], s.instances[i+1:]...)
	return nil
}

func (s *sandboxSelfHosted) Check(_ context.Context, id string) (selfhostedamnezia.CheckResult, error) {
	s.mu.Lock()
	i := s.index(id)
	var inst selfhostedamnezia.Instance
	if i >= 0 {
		inst = s.instances[i]
	}
	s.mu.Unlock()
	if i < 0 {
		return selfhostedamnezia.CheckResult{}, selfhostedamnezia.ErrInstanceNotFound
	}
	time.Sleep(time.Second) // одна попытка SSH -- экран показывает ожидание
	if inst.SSHHost == "" || strings.Contains(inst.SSHHost, "fail") {
		return selfhostedamnezia.CheckResult{OK: false, Message: "SSH не принял пользователя или пароль"}, nil
	}
	return selfhostedamnezia.CheckResult{OK: true, Message: "Подключение есть: контейнер «amnezia-awg2» отвечает"}, nil
}

func (s *sandboxSelfHosted) Issue(_ context.Context, id, clientName string) (selfhostedamnezia.IssuedConfig, selfhostedamnezia.Instance, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	i := s.index(id)
	if i < 0 {
		return selfhostedamnezia.IssuedConfig{}, selfhostedamnezia.Instance{}, selfhostedamnezia.ErrInstanceNotFound
	}
	if !s.instances[i].Enabled {
		return selfhostedamnezia.IssuedConfig{}, selfhostedamnezia.Instance{}, selfhostedamnezia.ErrInstanceDisabled
	}
	s.issued++
	addr := fmt.Sprintf("10.8.1.%d/32", s.issued+1)
	return selfhostedamnezia.IssuedConfig{
		Name:    clientName,
		Address: addr,
		Config:  []byte("[Interface]\nPrivateKey = sandbox\nAddress = " + addr + "\n\n[Peer]\nPublicKey = sandbox\n"),
	}, s.instances[i], nil
}

// sandboxDocs -- «личка» песочницы: документ в журнал вместо Telegram.
// unreachable -- бот «не может написать» (экран «нажмите /start»).
type sandboxDocs struct{ unreachable bool }

func (d sandboxDocs) SendDocument(_ context.Context, chatID int64, _ *int64, filename string, data []byte, caption string) (int64, error) {
	if d.unreachable {
		return 0, &tg.APIError{Method: "sendDocument", Code: 403, Description: "Forbidden: bot can't initiate conversation with a user"}
	}
	slog.Info("песочница: .conf в личку", "chat", chatID, "file", filename, "bytes", len(data), "caption", caption)
	return 1, nil
}
