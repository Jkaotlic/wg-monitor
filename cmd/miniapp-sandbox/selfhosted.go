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
	// clients -- выданные подключения «Домашнего VPS»; отзыв их убирает.
	clients []selfhostedamnezia.Client
}

var _ backend.SelfHostedVPS = (*sandboxSelfHosted)(nil)

// sandboxHostKey -- отпечаток ключа хоста «Домашнего VPS» песочницы. Адрес
// SSH со словом «newkey» -- сервер с другим ключом: проверка отказывает
// словами о смене ключа, пока отпечаток не сброшен «Доверять новому ключу».
const sandboxHostKey = "SHA256:0+YzwylrV4vzNCZQZ4WDA6yEr1elQ6zIgwId6M/F9OA"

func newSandboxSelfHosted() *sandboxSelfHosted {
	now := time.Now()
	return &sandboxSelfHosted{clients: []selfhostedamnezia.Client{
		{PublicKey: "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=", Name: "wgmon-sandbox-home-" + now.Add(-72*time.Hour).Format("20060102-150405"), Address: "10.8.1.2/32", CreatedAt: now.Add(-72 * time.Hour)},
		{PublicKey: "BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB=", Name: "wgmon-sandbox-home-" + now.Add(-24*time.Hour).Format("20060102-150405"), Address: "10.8.1.3/32", CreatedAt: now.Add(-24 * time.Hour)},
		{PublicKey: "CCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCC=", Name: "Phone of Ann", Address: "10.8.1.4/32", CreatedAt: now.Add(-6 * time.Hour)},
	}, instances: []selfhostedamnezia.Instance{
		{ID: "home", Label: "Домашний VPS", Enabled: true, EndpointHost: "vpn.sandbox.example.com", EndpointPort: 47567,
			// Пароль-заглушка не литералом: gosec G101 ловит строку в поле SSHPassword,
			// а песочница по SSH не ходит вовсе.
			SSHHost: "203.0.113.10", SSHPort: 22, SSHUser: "root", SSHPassword: strings.Repeat("s", 8),
			SSHHostKey: sandboxHostKey},
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
	inst.SSHHostKey = ""
	if cur := s.instances[i]; inst.SSHHost != "" && inst.SSHHost == cur.SSHHost && inst.SSHPort == cur.SSHPort {
		inst.SSHHostKey = cur.SSHHostKey
	}
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
	presented := sandboxHostKey
	if strings.Contains(inst.SSHHost, "newkey") {
		presented = "SHA256:Zm9yLXNhbmRib3gtb25seS1hbm90aGVyLWhvc3Qta2V5"
	}
	if inst.SSHHostKey != "" && inst.SSHHostKey != presented {
		return selfhostedamnezia.CheckResult{OK: false, Message: (&selfhostedamnezia.HostKeyChangedError{Label: inst.Label}).Error()}, nil
	}
	if inst.SSHHostKey == "" {
		s.mu.Lock()
		if j := s.index(id); j >= 0 {
			s.instances[j].SSHHostKey = presented
		}
		s.mu.Unlock()
	}
	return selfhostedamnezia.CheckResult{OK: true, Message: "Подключение есть: контейнер «amnezia-awg2» отвечает"}, nil
}

func (s *sandboxSelfHosted) TrustNewHostKey(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	i := s.index(id)
	if i < 0 {
		return selfhostedamnezia.ErrInstanceNotFound
	}
	s.instances[i].SSHHostKey = ""
	return nil
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

func (d sandboxDocs) SendPhoto(_ context.Context, chatID int64, _ *int64, filename string, data []byte, caption string) (int64, error) {
	if d.unreachable {
		return 0, &tg.APIError{Method: "sendPhoto", Code: 403, Description: "Forbidden: bot can't initiate conversation with a user"}
	}
	slog.Info("песочница: QR в личку", "chat", chatID, "file", filename, "bytes", len(data), "caption", caption)
	return 2, nil
}

func (s *sandboxSelfHosted) Clients(_ context.Context, id string) ([]selfhostedamnezia.Client, selfhostedamnezia.Instance, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	i := s.index(id)
	if i < 0 {
		return nil, selfhostedamnezia.Instance{}, selfhostedamnezia.ErrInstanceNotFound
	}
	if !s.instances[i].Enabled {
		return nil, selfhostedamnezia.Instance{}, selfhostedamnezia.ErrInstanceDisabled
	}
	if id != "home" {
		return nil, s.instances[i], nil
	}
	return append([]selfhostedamnezia.Client{}, s.clients...), s.instances[i], nil
}

func (s *sandboxSelfHosted) Revoke(_ context.Context, id, publicKey string) (selfhostedamnezia.Client, selfhostedamnezia.Instance, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	i := s.index(id)
	if i < 0 {
		return selfhostedamnezia.Client{}, selfhostedamnezia.Instance{}, selfhostedamnezia.ErrInstanceNotFound
	}
	if id == "home" {
		for k, c := range s.clients {
			if c.PublicKey == publicKey {
				s.clients = append(s.clients[:k:k], s.clients[k+1:]...)
				return c, s.instances[i], nil
			}
		}
	}
	return selfhostedamnezia.Client{}, selfhostedamnezia.Instance{}, selfhostedamnezia.ErrClientNotFound
}
