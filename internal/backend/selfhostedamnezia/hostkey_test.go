package selfhostedamnezia

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

// testSSHServer -- SSH-сервер в процессе: пароль "pw", любая exec-команда
// завершается кодом 0. auths -- сколько раз клиент дошёл до пароля: при
// чужом ключе хоста пароль не должен уходить вовсе.
type testSSHServer struct {
	addr  string
	fp    string
	auths atomic.Int32
	execs atomic.Int32
	// useAlt -- новые соединения получают другой ключ хоста (altFP):
	// подмена сервера между входами одного выпуска.
	useAlt atomic.Bool
	altFP  string
}

func newTestSigner(t *testing.T) ssh.Signer {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	return signer
}

func startTestSSHServer(t *testing.T) *testSSHServer {
	t.Helper()
	signer, altSigner := newTestSigner(t), newTestSigner(t)
	srv := &testSSHServer{fp: ssh.FingerprintSHA256(signer.PublicKey()), altFP: ssh.FingerprintSHA256(altSigner.PublicKey())}
	password := func(_ ssh.ConnMetadata, pw []byte) (*ssh.Permissions, error) {
		srv.auths.Add(1)
		if string(pw) != "pw" {
			return nil, errors.New("bad password")
		}
		return nil, nil
	}
	cfg := &ssh.ServerConfig{PasswordCallback: password}
	cfg.AddHostKey(signer)
	altCfg := &ssh.ServerConfig{PasswordCallback: password}
	altCfg.AddHostKey(altSigner)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	srv.addr = ln.Addr().String()
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			c := cfg
			if srv.useAlt.Load() {
				c = altCfg
			}
			go srv.serve(conn, c)
		}
	}()
	return srv
}

func (srv *testSSHServer) serve(conn net.Conn, cfg *ssh.ServerConfig) {
	sc, chans, reqs, err := ssh.NewServerConn(conn, cfg)
	if err != nil {
		_ = conn.Close()
		return
	}
	defer sc.Close()
	go ssh.DiscardRequests(reqs)
	for ch := range chans {
		channel, requests, err := ch.Accept()
		if err != nil {
			continue
		}
		go func() {
			defer channel.Close()
			for req := range requests {
				if req.Type != "exec" {
					_ = req.Reply(false, nil)
					continue
				}
				srv.execs.Add(1)
				_ = req.Reply(true, nil)
				status := make([]byte, 4)
				binary.BigEndian.PutUint32(status, 0)
				_, _ = channel.SendRequest("exit-status", false, status)
				return
			}
		}()
	}
}

func (srv *testSSHServer) port(t *testing.T) int {
	_, p, _ := net.SplitHostPort(srv.addr)
	n, err := strconv.Atoi(p)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

// sshService -- сервис с одним инстансом «Дом», который ходит по SSH на srv.
func sshService(t *testing.T, srv *testSSHServer, password, knownKey string) (*Service, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "amnezia-selfhosted.json")
	inst := homeInstance()
	inst.SSHHost, inst.SSHPort, inst.SSHUser, inst.SSHPassword = "127.0.0.1", srv.port(t), "root", password
	inst.SSHHostKey = knownKey
	if err := SaveStore(path, Store{Version: 1, Instances: []Instance{inst}}); err != nil {
		t.Fatal(err)
	}
	return NewService(path, Config{}), path
}

func storedHostKey(t *testing.T, path string) string {
	t.Helper()
	st, err := LoadStore(path, Config{})
	if err != nil {
		t.Fatal(err)
	}
	inst, ok := st.Get("home")
	if !ok {
		t.Fatal("инстанс home пропал")
	}
	return inst.SSHHostKey
}

const homeKeyChangedText = "Ключ сервера «Дом» изменился — если вы переустанавливали сервер, подтвердите новый ключ в карточке"

func TestHostKeyRememberedOnFirstSuccessfulLogin(t *testing.T) {
	srv := startTestSSHServer(t)
	s, path := sshService(t, srv, "pw", "")
	res, err := s.Check(context.Background(), "home")
	if err != nil || !res.OK {
		t.Fatalf("первый вход: %+v %v", res, err)
	}
	if got := storedHostKey(t, path); got != srv.fp {
		t.Fatalf("отпечаток не запомнен: %q, ждали %q", got, srv.fp)
	}
	if !strings.HasPrefix(srv.fp, "SHA256:") {
		t.Fatalf("формат отпечатка: %q", srv.fp)
	}
	// Совпал -- вход идёт, отпечаток тот же.
	res, err = s.Check(context.Background(), "home")
	if err != nil || !res.OK {
		t.Fatalf("второй вход: %+v %v", res, err)
	}
	if got := storedHostKey(t, path); got != srv.fp {
		t.Fatalf("отпечаток сменился: %q", got)
	}
}

func TestHostKeyNotRememberedOnFailedLogin(t *testing.T) {
	srv := startTestSSHServer(t)
	s, path := sshService(t, srv, "wrong", "")
	res, err := s.Check(context.Background(), "home")
	if err != nil || res.OK {
		t.Fatalf("неверный пароль должен провалить проверку: %+v %v", res, err)
	}
	if got := storedHostKey(t, path); got != "" {
		t.Fatalf("неудачный вход не должен запоминать ключ: %q", got)
	}
}

func TestHostKeyChangedRefusesWithoutLogin(t *testing.T) {
	srv := startTestSSHServer(t)
	const old = "SHA256:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	s, path := sshService(t, srv, "pw", old)
	res, err := s.Check(context.Background(), "home")
	if err != nil {
		t.Fatal(err)
	}
	if res.OK || res.Message != homeKeyChangedText {
		t.Fatalf("смена ключа: %+v", res)
	}
	if n := srv.auths.Load(); n != 0 {
		t.Fatalf("пароль ушёл на сервер с чужим ключом: %d попыток", n)
	}
	if n := srv.execs.Load(); n != 0 {
		t.Fatalf("команда выполнена на сервере с чужим ключом: %d", n)
	}
	if got := storedHostKey(t, path); got != old {
		t.Fatalf("сохранённый отпечаток тронут: %q", got)
	}

	_, _, err = s.Issue(context.Background(), "home", "router")
	var hk *HostKeyChangedError
	if !errors.Is(err, ErrHostKeyChanged) || !errors.As(err, &hk) || err.Error() != homeKeyChangedText {
		t.Fatalf("выпуск при смене ключа: %v", err)
	}
	if srv.auths.Load() != 0 || srv.execs.Load() != 0 {
		t.Fatal("выпуск дошёл до входа на сервер с чужим ключом")
	}
}

func storedInstance(t *testing.T, path string) Instance {
	t.Helper()
	st, err := LoadStore(path, Config{})
	if err != nil {
		t.Fatal(err)
	}
	inst, ok := st.Get("home")
	if !ok {
		t.Fatal("инстанс home пропал")
	}
	return inst
}

// v0.56, C1: отказанный ключ запоминается как ожидающий с временем, повторный
// отказ того же ключа файл не переписывает.
func TestHostKeyMismatchStoresPendingOnce(t *testing.T) {
	srv := startTestSSHServer(t)
	const old = "SHA256:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	s, path := sshService(t, srv, "pw", old)
	t0 := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return t0 }
	if res, err := s.Check(context.Background(), "home"); err != nil || res.OK {
		t.Fatalf("смена ключа: %+v %v", res, err)
	}
	inst := storedInstance(t, path)
	if inst.SSHHostKey != old || inst.SSHHostKeyPending != srv.fp || !inst.SSHHostKeyPendingAt.Equal(t0) {
		t.Fatalf("ожидающий ключ: доверенный=%q ожидающий=%q время=%v", inst.SSHHostKey, inst.SSHHostKeyPending, inst.SSHHostKeyPendingAt)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	s.now = func() time.Time { return t0.Add(time.Hour) }
	for i := 0; i < 3; i++ {
		if _, _, err := s.Issue(context.Background(), "home", "router"); !errors.Is(err, ErrHostKeyChanged) {
			t.Fatalf("выпуск до подтверждения: %v", err)
		}
		if res, _ := s.Check(context.Background(), "home"); res.OK {
			t.Fatal("проверка до подтверждения прошла")
		}
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("тот же отказанный ключ переписал файл")
	}
	if srv.auths.Load() != 0 || srv.execs.Load() != 0 {
		t.Fatal("до подтверждения пароль или команда ушли на сервер")
	}
	// Сервер предъявил третий ключ -- ожидающий меняется.
	srv.useAlt.Store(true)
	_, _ = s.Check(context.Background(), "home")
	if got := storedInstance(t, path); got.SSHHostKeyPending != srv.altFP || !got.SSHHostKeyPendingAt.Equal(t0.Add(time.Hour)) {
		t.Fatalf("новый отказанный ключ: %q %v", got.SSHHostKeyPending, got.SSHHostKeyPendingAt)
	}
}

func TestRememberPendingHostKeyOnlyForSameAddress(t *testing.T) {
	srv := startTestSSHServer(t)
	const old = "SHA256:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	s, path := sshService(t, srv, "pw", old)
	inst, _ := s.find("home")
	if err := s.rememberPendingHostKey("home", "203.0.113.99", inst.SSHPort, "SHA256:other"); err != nil {
		t.Fatal(err)
	}
	if err := s.rememberPendingHostKey("home", inst.SSHHost, inst.SSHPort+1, "SHA256:other"); err != nil {
		t.Fatal(err)
	}
	if err := s.rememberPendingHostKey("gone", inst.SSHHost, inst.SSHPort, "SHA256:other"); err != nil {
		t.Fatal(err)
	}
	// Отказанный совпал с доверенным (подтвердили, пока шёл вход) -- не ожидающий.
	if err := s.rememberPendingHostKey("home", inst.SSHHost, inst.SSHPort, old); err != nil {
		t.Fatal(err)
	}
	if got := storedInstance(t, path); got.SSHHostKeyPending != "" {
		t.Fatalf("ожидающий записан не для того адреса: %q", got.SSHHostKeyPending)
	}
}

// Подтверждение принимает ровно ожидающий отпечаток; после него вход идёт.
func TestConfirmHostKeyAcceptsExactlyPending(t *testing.T) {
	srv := startTestSSHServer(t)
	const old = "SHA256:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	s, path := sshService(t, srv, "pw", old)
	// Ожидающего нет -- подтверждать нечего.
	if err := s.ConfirmHostKey("home", srv.fp); !errors.Is(err, ErrHostKeyNotPending) {
		t.Fatalf("подтверждение без ожидающего: %v", err)
	}
	_, _ = s.Check(context.Background(), "home")
	if err := s.ConfirmHostKey("home", "SHA256:BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB"); !errors.Is(err, ErrHostKeyNotPending) {
		t.Fatalf("чужой отпечаток: %v", err)
	}
	if err := s.ConfirmHostKey("home", ""); !errors.Is(err, ErrHostKeyNotPending) {
		t.Fatalf("пустой отпечаток: %v", err)
	}
	if got := storedInstance(t, path); got.SSHHostKey != old || got.SSHHostKeyPending != srv.fp {
		t.Fatalf("отказанное подтверждение тронуло ключи: %+v", got)
	}
	if err := s.ConfirmHostKey("nope", srv.fp); !errors.Is(err, ErrInstanceNotFound) {
		t.Fatalf("чужой инстанс: %v", err)
	}
	if err := s.ConfirmHostKey("home", " "+srv.fp+" "); err != nil {
		t.Fatal(err)
	}
	got := storedInstance(t, path)
	if got.SSHHostKey != srv.fp || got.SSHHostKeyPending != "" || !got.SSHHostKeyPendingAt.IsZero() {
		t.Fatalf("после подтверждения: %+v", got)
	}
	if res, err := s.Check(context.Background(), "home"); err != nil || !res.OK {
		t.Fatalf("вход после подтверждения: %+v %v", res, err)
	}
}

// «Сбросить» больше нет: сервис не умеет обнулять доверенный ключ.
func TestNoTrustNewHostKeyReset(t *testing.T) {
	if _, ok := any(&Service{}).(interface{ TrustNewHostKey(string) error }); ok {
		t.Fatal("сброс доверенного ключа остался")
	}
}

func TestUpdateKeepsHostKeyForSameAddressAndDropsForNew(t *testing.T) {
	srv := startTestSSHServer(t)
	s, path := sshService(t, srv, "pw", "SHA256:BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB")
	if err := s.rememberPendingHostKey("home", "127.0.0.1", srv.port(t), srv.fp); err != nil {
		t.Fatal(err)
	}
	inst := homeInstance()
	inst.Label = "Дом-2"
	inst.SSHHost, inst.SSHPort, inst.SSHUser = "127.0.0.1", srv.port(t), "root"
	if err := s.Update("home", inst); err != nil {
		t.Fatal(err)
	}
	if got := storedInstance(t, path); got.SSHHostKey != "SHA256:BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB" || got.SSHHostKeyPending != srv.fp {
		t.Fatalf("правка подписи стёрла отпечатки: %+v", got)
	}
	inst.SSHHost, inst.SSHPassword = "203.0.113.5", "pw2"
	if err := s.Update("home", inst); err != nil {
		t.Fatal(err)
	}
	if got := storedInstance(t, path); got.SSHHostKey != "" || got.SSHHostKeyPending != "" || !got.SSHHostKeyPendingAt.IsZero() {
		t.Fatalf("новый адрес SSH унаследовал отпечатки старого: %+v", got)
	}
}

func TestCreateIgnoresHostKeyFromForm(t *testing.T) {
	s := newTestService(t)
	inst := homeInstance()
	inst.SSHHost, inst.SSHPassword = "203.0.113.5", "pw"
	inst.SSHHostKey = "SHA256:подложенный"
	if err := s.Create(inst); err != nil {
		t.Fatal(err)
	}
	insts, _ := s.List()
	if insts[0].SSHHostKey != "" {
		t.Fatalf("отпечаток пришёл из формы: %q", insts[0].SSHHostKey)
	}
}

func TestRemoteRunnerWithoutPolicyRefuses(t *testing.T) {
	srv := startTestSSHServer(t)
	r := RemoteDockerRunner{Host: "127.0.0.1", Port: srv.port(t), User: "root", Password: "pw", Container: "c"}
	if _, err := r.Run(context.Background(), []string{"true"}, nil); err == nil {
		t.Fatal("без сохранённого отпечатка и без способа его запомнить вход должен быть отказан")
	}
	if srv.auths.Load() != 0 {
		t.Fatal("пароль ушёл без проверки ключа хоста")
	}
}

// Ключ хоста своего сервера не игнорируется нигде в коде пакета.
func TestNoInsecureIgnoreHostKeyInPackage(t *testing.T) {
	files, _ := filepath.Glob("*.go")
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		body, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(body), "InsecureIgnoreHostKey") {
			t.Errorf("%s: InsecureIgnoreHostKey в коде", f)
		}
	}
}

// Fix round 1: отпечаток, запомненный первым входом выпуска, сверяют и
// остальные входы того же выпуска -- до пароля, а не после.
func TestHostKeyPinnedForLaterRunsOfSameRunner(t *testing.T) {
	srv := startTestSSHServer(t)
	s, path := sshService(t, srv, "pw", "")
	inst, err := s.find("home")
	if err != nil {
		t.Fatal(err)
	}
	r := s.newRunner(s.legacy.ProviderConfig(inst), s.hostKeyPolicy(inst))
	if _, err := r.Run(context.Background(), []string{"true"}, nil); err != nil {
		t.Fatalf("первый вход: %v", err)
	}
	srv.useAlt.Store(true)
	_, err = r.Run(context.Background(), []string{"true"}, nil)
	if !errors.Is(err, ErrHostKeyChanged) {
		t.Fatalf("второй вход с другим ключом: %v", err)
	}
	if n := srv.auths.Load(); n != 1 {
		t.Fatalf("пароль ушёл на подменённый сервер: попыток входа %d, ждали 1", n)
	}
	if n := srv.execs.Load(); n != 1 {
		t.Fatalf("команд выполнено %d, ждали 1", n)
	}
	if got := storedHostKey(t, path); got != srv.fp {
		t.Fatalf("отпечаток сменился: %q", got)
	}
}

func TestRememberHostKeyBranches(t *testing.T) {
	srv := startTestSSHServer(t)
	const first = "SHA256:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	s, path := sshService(t, srv, "pw", first)
	inst, _ := s.find("home")

	// Другой вход успел запомнить другой ключ -- это смена ключа.
	if err := s.rememberHostKey("home", inst.SSHHost, inst.SSHPort, "SHA256:other"); !errors.Is(err, ErrHostKeyChanged) {
		t.Fatalf("другой ключ поверх запомненного: %v", err)
	}
	if got := storedHostKey(t, path); got != first {
		t.Fatalf("отпечаток перезаписан: %q", got)
	}
	// Тот же ключ -- ничего не меняется, ошибки нет.
	if err := s.rememberHostKey("home", inst.SSHHost, inst.SSHPort, first); err != nil {
		t.Fatalf("тот же ключ: %v", err)
	}
	// Адрес SSH сменили, пока шёл вход: ключ относится к старому адресу.
	if err := s.rememberHostKey("home", "203.0.113.99", inst.SSHPort, "SHA256:other"); err != nil {
		t.Fatalf("сменённый адрес: %v", err)
	}
	if err := s.rememberHostKey("home", inst.SSHHost, inst.SSHPort+1, "SHA256:other"); err != nil {
		t.Fatalf("сменённый порт: %v", err)
	}
	if err := s.rememberHostKey("gone", inst.SSHHost, inst.SSHPort, "SHA256:other"); err != nil {
		t.Fatalf("исчезнувший сервер: %v", err)
	}
	if got := storedHostKey(t, path); got != first {
		t.Fatalf("отпечаток тронут: %q", got)
	}
	// Без отпечатка при сменённом адресе -- тоже не пишется.
	if err := s.update(func(st *Store) error { st.Instances[0].SSHHostKey = ""; return nil }); err != nil {
		t.Fatal(err)
	}
	if err := s.rememberHostKey("home", "203.0.113.99", inst.SSHPort, "SHA256:other"); err != nil {
		t.Fatal(err)
	}
	if got := storedHostKey(t, path); got != "" {
		t.Fatalf("ключ чужого адреса записан: %q", got)
	}
}

// Гонка двух входов: пока шёл вход по TOFU, другой вход запомнил другой
// ключ -- этот вход отказан, Check говорит о смене ключа, отпечаток прежний.
func TestCheckReportsChangeWhenConcurrentLoginRememberedOtherKey(t *testing.T) {
	srv := startTestSSHServer(t)
	s, path := sshService(t, srv, "pw", "")
	const other = "SHA256:CCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCC"
	inner := s.newRunner
	s.newRunner = func(cfg Config, p HostKeyPolicy) Runner {
		remember := p.Remember
		p.Remember = func(fp string) error {
			// «Другой вход» записал свой ключ первым.
			if err := s.rememberHostKey("home", cfg.SSHHost, cfg.SSHPort, other); err != nil {
				return err
			}
			return remember(fp)
		}
		return inner(cfg, p)
	}
	res, err := s.Check(context.Background(), "home")
	if err != nil || res.OK || res.Message != homeKeyChangedText {
		t.Fatalf("гонка: %+v %v", res, err)
	}
	if n := srv.execs.Load(); n != 0 {
		t.Fatalf("команда выполнена: %d", n)
	}
	if got := storedHostKey(t, path); got != other {
		t.Fatalf("отпечаток: %q, ждали прежний %q", got, other)
	}
}

// Отказ «нет политики ключа» не выдаётся за «вход прошёл».
func TestNoPolicyRefusalIsNotLoginFailureText(t *testing.T) {
	srv := startTestSSHServer(t)
	r := RemoteDockerRunner{Host: "127.0.0.1", Port: srv.port(t), User: "root", Password: "pw", Container: "c"}
	_, err := r.Run(context.Background(), []string{"true"}, nil)
	if !errors.Is(err, errNoHostKeyPolicy) {
		t.Fatalf("ошибка: %v", err)
	}
	if txt := checkFailureText(Config{SSHHost: "127.0.0.1", Container: "c"}, err); strings.Contains(txt, "прошёл") {
		t.Fatalf("отказ без проверки ключа назван «вход прошёл»: %q", txt)
	}
}
