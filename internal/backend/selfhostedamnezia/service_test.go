package selfhostedamnezia

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
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

	// Пустой пароль при изменении того же SSH-входа -- «не менять».
	upd := homeInstance()
	upd.Label, upd.SSHHost = "Дача", "203.0.113.7"
	if err := s.Update("home", upd); err != nil {
		t.Fatal(err)
	}
	got, _ = s.List()
	if got[0].Label != "Дача" || got[0].SSHHost != "203.0.113.7" || got[0].SSHPassword != "SECRET-SSH-MUST-NOT-LEAK" {
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

type checkRunner struct {
	err   error
	calls int32
	args  []string
}

func (r *checkRunner) Run(_ context.Context, args []string, _ []byte) ([]byte, error) {
	atomic.AddInt32(&r.calls, 1)
	r.args = append([]string{}, args...)
	return nil, r.err
}

// «Проверить подключение» -- одна попытка и ответ словами. Пароль в ответ не
// попадает никогда.
func TestServiceCheckOneAttemptInWords(t *testing.T) {
	cases := []struct {
		name string
		err  error
		ok   bool
		want string
	}{
		{"всё хорошо", nil, true, "контейнер «amnezia-awg2» отвечает"},
		{"нет SSH", errors.New("ssh dial 203.0.113.7:22: connect: connection refused"), false, "не отвечает по SSH"},
		{"пароль", errors.New("ssh auth 203.0.113.7:22: ssh: unable to authenticate"), false, "пароль"},
		{"контейнер", errors.New("remote docker true: Error: No such container: amnezia-awg2"), false, "контейнер «amnezia-awg2» не отвечает"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := newTestService(t)
			runner := &checkRunner{err: tc.err}
			s.newRunner = func(Config) Runner { return runner }
			inst := homeInstance()
			inst.SSHHost, inst.SSHPassword = "203.0.113.7", "SECRET-SSH-MUST-NOT-LEAK"
			if err := s.Create(inst); err != nil {
				t.Fatal(err)
			}
			res, err := s.Check(context.Background(), "home")
			if err != nil || res.OK != tc.ok || !strings.Contains(res.Message, tc.want) {
				t.Fatalf("res=%+v err=%v", res, err)
			}
			if strings.Contains(res.Message, "SECRET-SSH") {
				t.Fatal("пароль в ответе проверки")
			}
			if atomic.LoadInt32(&runner.calls) != 1 || len(runner.args) != 1 || runner.args[0] != "true" {
				t.Fatalf("ждали одну попытку `true`: calls=%d args=%v", runner.calls, runner.args)
			}
		})
	}
	s := newTestService(t)
	if _, err := s.Check(context.Background(), "nope"); !errors.Is(err, ErrInstanceNotFound) {
		t.Fatalf("нет сервера: %v", err)
	}
}

func TestServiceDefaultsHideAddressesAndPassword(t *testing.T) {
	s := NewService(filepath.Join(t.TempDir(), "s.json"), Config{Container: "my-awg", SSHHost: "203.0.113.7", SSHPassword: "SECRET-YAML", EndpointHost: "vpn.example.com"})
	def := s.Defaults()
	if def.Container != "my-awg" || def.Interface != "awg0" || def.SSHPort != 22 || def.SSHUser != "root" || len(def.DNS) == 0 {
		t.Fatalf("умолчания: %+v", def)
	}
	if def.SSHPassword != "" || def.SSHHost != "" || def.EndpointHost != "" {
		t.Fatalf("в умолчаниях адреса или пароль: %+v", def)
	}
}

func TestMigrateLegacyPassword(t *testing.T) {
	legacy := Config{Enabled: true, EndpointHost: "vpn.example.com", EndpointPort: 47567, SSHHost: "203.0.113.7", SSHPassword: "SECRET-YAML-PASS"}

	path := filepath.Join(t.TempDir(), "s.json")
	if migrated, err := MigrateLegacyPassword(path, Config{}); migrated || err != nil {
		t.Fatalf("без пароля в YAML переносить нечего: %v %v", migrated, err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("без пароля файл не создаётся")
	}

	// Файла нет -- он создаётся из секции YAML, как её показывал LoadStore.
	migrated, err := MigrateLegacyPassword(path, legacy)
	if !migrated || err != nil {
		t.Fatalf("перенос в новый файл: %v %v", migrated, err)
	}
	st, err := LoadStore(path, Config{})
	if err != nil || len(st.Instances) != 1 || st.Instances[0].SSHPassword != "SECRET-YAML-PASS" || st.Instances[0].SSHHost != "203.0.113.7" {
		t.Fatalf("файл после переноса: %+v err=%v", st, err)
	}
	if migrated, err := MigrateLegacyPassword(path, legacy); migrated || err != nil {
		t.Fatalf("второй запуск ничего не меняет: %v %v", migrated, err)
	}

	// Файл есть: пароль дописывается тем, кто ходил по SSH с паролем из YAML.
	path2 := filepath.Join(t.TempDir(), "s2.json")
	if err := SaveStore(path2, Store{Version: 1, Instances: []Instance{
		{ID: "home", Enabled: true, EndpointHost: "vpn.example.com", EndpointPort: 1},
		{ID: "work", Enabled: true, EndpointHost: "vpn2.example.com", EndpointPort: 2, SSHHost: "203.0.113.9", SSHPassword: "own"},
	}}); err != nil {
		t.Fatal(err)
	}
	if migrated, err := MigrateLegacyPassword(path2, legacy); !migrated || err != nil {
		t.Fatalf("перенос в существующий файл: %v %v", migrated, err)
	}
	st, _ = LoadStore(path2, Config{})
	if st.Instances[0].SSHPassword != "SECRET-YAML-PASS" || st.Instances[0].SSHHost != "203.0.113.7" || st.Instances[1].SSHPassword != "own" {
		t.Fatalf("после переноса: %+v", st.Instances)
	}
}

func TestTunnelNameAndValidInstanceID(t *testing.T) {
	if got := TunnelName("home", "router-owned"); got != "home_router-owned" {
		t.Fatalf("TunnelName = %q", got)
	}
	if got := TunnelName("9x", "Дача"); !strings.HasPrefix(got, "selfhosted-") || len(got) > 32 {
		t.Fatalf("TunnelName с неподходящим началом = %q", got)
	}
	if !ValidInstanceID("home") || ValidInstanceID("Bad!") || ValidInstanceID("h") {
		t.Fatal("ValidInstanceID")
	}
}

// Сохранённый пароль уходит только на тот SSH, к которому его вводили: при
// смене адреса, порта или пользователя пустое поле пароля -- отказ, иначе
// «Проверить подключение» отправила бы пароль на чужой сервер (host key не
// проверяется).
func TestServiceUpdateKeepsPasswordOnlyForSameSSHTarget(t *testing.T) {
	base := homeInstance()
	base.SSHHost, base.SSHPort, base.SSHUser, base.SSHPassword = "203.0.113.7", 22, "root", "SECRET-SSH-MUST-NOT-LEAK"
	cases := []struct {
		name string
		mod  func(*Instance)
	}{
		{"адрес", func(i *Instance) { i.SSHHost = "203.0.113.99" }},
		{"порт", func(i *Instance) { i.SSHPort = 2222 }},
		{"пользователь", func(i *Instance) { i.SSHUser = "admin" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := newTestService(t)
			if err := s.Create(base); err != nil {
				t.Fatal(err)
			}
			upd := base
			upd.SSHPassword = ""
			tc.mod(&upd)
			var fe *FieldError
			if err := s.Update("home", upd); !errors.As(err, &fe) || fe.Field != "ssh_password" || !strings.Contains(fe.Reason, "заново") {
				t.Fatalf("смена SSH без пароля: %v", err)
			}
			got, _ := s.List()
			if got[0].SSHHost != "203.0.113.7" {
				t.Fatalf("отказ всё равно сохранил: %+v", got[0])
			}
			upd.SSHPassword = "new-pass"
			if err := s.Update("home", upd); err != nil {
				t.Fatalf("с новым паролем: %v", err)
			}
		})
	}
	s := newTestService(t)
	if err := s.Create(base); err != nil {
		t.Fatal(err)
	}
	same := base
	same.SSHPassword, same.SSHPort, same.SSHUser = "", 0, "" // 0 и "" -- те же умолчания 22/root
	same.Label = "Дача"
	if err := s.Update("home", same); err != nil {
		t.Fatalf("без смены SSH: %v", err)
	}
	got, _ := s.List()
	if got[0].SSHPassword != "SECRET-SSH-MUST-NOT-LEAK" {
		t.Fatalf("пароль не сохранён: %+v", got[0])
	}
}
