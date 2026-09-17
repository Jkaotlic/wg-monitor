package selfhostedamnezia

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// lockedRunner -- контейнер своего сервера в памяти, безопасный для
// параллельных вызовов. Чтение файла нарочно медленное: без замка инстанса
// два выпуска успевают прочитать один и тот же серверный конфиг.
type lockedRunner struct {
	mu    sync.Mutex
	files map[string][]byte
}

func newLockedRunner() *lockedRunner {
	return &lockedRunner{files: map[string][]byte{
		"/opt/amnezia/awg/awg0.conf":                       []byte("[Interface]\nPrivateKey = server-private\nAddress = 10.8.1.0/24\nListenPort = 47567\n"),
		"/opt/amnezia/awg/clientsTable":                    []byte("[]"),
		"/opt/amnezia/awg/wireguard_server_public_key.key": []byte("server-public\n"),
		"/opt/amnezia/awg/wireguard_psk.key":               []byte("psk\n"),
	}}
}

func (r *lockedRunner) Run(_ context.Context, args []string, stdin []byte) ([]byte, error) {
	if len(args) >= 2 && args[0] == "cat" {
		r.mu.Lock()
		body := append([]byte{}, r.files[args[1]]...)
		r.mu.Unlock()
		time.Sleep(5 * time.Millisecond)
		return body, nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	switch {
	case len(args) >= 5 && args[0] == "sh":
		r.files[args[4]] = append([]byte{}, stdin...)
	case len(args) >= 4 && args[0] == "mv": // mv -f <tmp> <path> (writeAtomic)
		r.files[args[3]] = r.files[args[2]]
		delete(r.files, args[2])
	}
	return nil, nil
}

func newTestService(t *testing.T) *Service {
	t.Helper()
	return NewService(filepath.Join(t.TempDir(), "amnezia-selfhosted.json"), Config{})
}

func homeInstance() Instance {
	return Instance{ID: "home", Label: "Дом", Enabled: true, EndpointHost: "vpn.example.com", EndpointPort: 47567}
}

func TestServiceCreateUpdateKeepsPasswordAndValidates(t *testing.T) {
	s := newTestService(t)
	inst := homeInstance()
	inst.SSHHost, inst.SSHPassword = "203.0.113.7", "SECRET-SSH-MUST-NOT-LEAK"
	if err := s.Create(inst); err != nil {
		t.Fatal(err)
	}
	if err := s.Create(inst); !errors.Is(err, ErrInstanceExists) {
		t.Fatalf("повтор: %v", err)
	}
	got, _ := s.List()
	if len(got) != 1 || got[0].SSHPort != 22 || got[0].SSHUser != "root" {
		t.Fatalf("умолчания SSH не проставлены: %+v", got)
	}

	// Пустой пароль при изменении -- «не менять».
	upd := homeInstance()
	upd.Label, upd.SSHHost = "Дача", "203.0.113.8"
	if err := s.Update("home", upd); err != nil {
		t.Fatal(err)
	}
	got, _ = s.List()
	if got[0].Label != "Дача" || got[0].SSHHost != "203.0.113.8" || got[0].SSHPassword != "SECRET-SSH-MUST-NOT-LEAK" {
		t.Fatalf("после изменения: %+v", got[0])
	}

	// Без SSH-адреса пароль не хранится.
	if err := s.Update("home", homeInstance()); err != nil {
		t.Fatal(err)
	}
	got, _ = s.List()
	if got[0].SSHPassword != "" || got[0].SSHUser != "" || got[0].SSHPort != 0 {
		t.Fatalf("SSH без адреса остался: %+v", got[0])
	}

	bad := homeInstance()
	bad.EndpointPort = 70000
	var fe *FieldError
	if err := s.Update("home", bad); !errors.As(err, &fe) || fe.Field != "endpoint_port" {
		t.Fatalf("порт вне диапазона: %v", err)
	}
	noPass := homeInstance()
	noPass.ID, noPass.SSHHost = "work", "203.0.113.9"
	if err := s.Create(noPass); !errors.As(err, &fe) || fe.Field != "ssh_password" {
		t.Fatalf("SSH без пароля: %v", err)
	}
	if err := s.Update("nope", homeInstance()); !errors.Is(err, ErrInstanceNotFound) {
		t.Fatalf("нет такого: %v", err)
	}
}

func TestValidateInstanceFields(t *testing.T) {
	cases := []struct {
		field string
		mod   func(*Instance)
	}{
		{"id", func(i *Instance) { i.ID = "9bad" }},
		{"label", func(i *Instance) { i.Label = "строка\nвторая" }},
		{"endpoint_host", func(i *Instance) { i.EndpointHost = "" }},
		{"endpoint_port", func(i *Instance) { i.EndpointPort = 0 }},
		{"container", func(i *Instance) { i.Container = "bad name;rm" }},
		{"interface", func(i *Instance) { i.Interface = "much-too-long-interface" }},
		{"config_path", func(i *Instance) { i.ConfigPath = "relative/awg0.conf" }},
		{"clients_path", func(i *Instance) { i.ClientsPath = "/opt/$(id)" }},
		{"server_public_key_path", func(i *Instance) { i.ServerPubPath = "/opt/a b" }},
		{"preshared_key_path", func(i *Instance) { i.PSKPath = "psk" }},
		{"dns", func(i *Instance) { i.DNS = []string{"1.1.1.1", "not-an-ip"} }},
		{"ssh_host", func(i *Instance) { i.SSHHost, i.SSHPassword = "bad host", "x" }},
		{"ssh_port", func(i *Instance) { i.SSHHost, i.SSHPort, i.SSHUser, i.SSHPassword = "203.0.113.7", 99999, "root", "x" }},
		{"ssh_user", func(i *Instance) {
			i.SSHHost, i.SSHPort, i.SSHUser, i.SSHPassword = "203.0.113.7", 22, "Root User", "x"
		}},
	}
	for _, tc := range cases {
		t.Run(tc.field, func(t *testing.T) {
			inst := homeInstance()
			tc.mod(&inst)
			var fe *FieldError
			if err := ValidateInstance(inst); !errors.As(err, &fe) || fe.Field != tc.field || fe.Reason == "" {
				t.Fatalf("ждали поле %q, получили %v", tc.field, err)
			}
		})
	}
	if err := ValidateInstance(homeInstance()); err != nil {
		t.Fatalf("правильный инстанс отвергнут: %v", err)
	}
}

func TestServiceParallelCreatesKeepEveryInstance(t *testing.T) {
	s := newTestService(t)
	const n = 12
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			inst := homeInstance()
			inst.ID = fmt.Sprintf("vps%02d", i)
			if err := s.Create(inst); err != nil {
				t.Errorf("create %d: %v", i, err)
			}
		}(i)
	}
	wg.Wait()
	got, err := s.List()
	if err != nil || len(got) != n {
		t.Fatalf("сохранилось %d из %d (err=%v)", len(got), n, err)
	}
}

// Два параллельных выпуска на один сервер без замка выбрали бы один адрес.
func TestServiceParallelIssuesGetDistinctAddresses(t *testing.T) {
	s := newTestService(t)
	runner := newLockedRunner()
	s.newRunner = func(Config) Runner { return runner }
	if err := s.Create(homeInstance()); err != nil {
		t.Fatal(err)
	}
	const n = 4
	addrs := make([]string, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			issued, inst, err := s.Issue(context.Background(), "home", fmt.Sprintf("client-%d", i))
			if err != nil || inst.ID != "home" {
				t.Errorf("issue %d: inst=%+v err=%v", i, inst, err)
				return
			}
			addrs[i] = issued.Address
		}(i)
	}
	wg.Wait()
	seen := map[string]bool{}
	for _, a := range addrs {
		if a == "" || seen[a] {
			t.Fatalf("адреса выпусков совпали или пусты: %v", addrs)
		}
		seen[a] = true
	}
}

func TestServiceIssueRefusesMissingAndDisabled(t *testing.T) {
	s := newTestService(t)
	s.newRunner = func(Config) Runner { return newLockedRunner() }
	if _, _, err := s.Issue(context.Background(), "home", "c"); !errors.Is(err, ErrInstanceNotFound) {
		t.Fatalf("нет сервера: %v", err)
	}
	if err := s.Create(homeInstance()); err != nil {
		t.Fatal(err)
	}
	if err := s.SetEnabled("home", false); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Issue(context.Background(), "home", "c"); !errors.Is(err, ErrInstanceDisabled) {
		t.Fatalf("выключенный: %v", err)
	}
	if err := s.SetEnabled("nope", true); !errors.Is(err, ErrInstanceNotFound) {
		t.Fatalf("toggle несуществующего: %v", err)
	}
	if err := s.Delete("home"); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete("home"); !errors.Is(err, ErrInstanceNotFound) {
		t.Fatalf("повторное удаление: %v", err)
	}
}
