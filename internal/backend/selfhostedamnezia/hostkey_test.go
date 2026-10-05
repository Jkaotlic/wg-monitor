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
}

func startTestSSHServer(t *testing.T) *testSSHServer {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	srv := &testSSHServer{fp: ssh.FingerprintSHA256(signer.PublicKey())}
	cfg := &ssh.ServerConfig{PasswordCallback: func(_ ssh.ConnMetadata, pw []byte) (*ssh.Permissions, error) {
		srv.auths.Add(1)
		if string(pw) != "pw" {
			return nil, errors.New("bad password")
		}
		return nil, nil
	}}
	cfg.AddHostKey(signer)
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
			go srv.serve(conn, cfg)
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

func TestTrustNewHostKeyResetsAndNextLoginRemembers(t *testing.T) {
	srv := startTestSSHServer(t)
	s, path := sshService(t, srv, "pw", "SHA256:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA")
	if err := s.TrustNewHostKey("home"); err != nil {
		t.Fatal(err)
	}
	if got := storedHostKey(t, path); got != "" {
		t.Fatalf("доверие новому ключу не сбросило отпечаток: %q", got)
	}
	res, err := s.Check(context.Background(), "home")
	if err != nil || !res.OK {
		t.Fatalf("вход после доверия: %+v %v", res, err)
	}
	if got := storedHostKey(t, path); got != srv.fp {
		t.Fatalf("новый ключ не запомнен: %q", got)
	}
	if err := s.TrustNewHostKey("nope"); !errors.Is(err, ErrInstanceNotFound) {
		t.Fatalf("чужой инстанс: %v", err)
	}
}

func TestUpdateKeepsHostKeyForSameAddressAndDropsForNew(t *testing.T) {
	srv := startTestSSHServer(t)
	s, path := sshService(t, srv, "pw", "SHA256:BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB")
	inst := homeInstance()
	inst.Label = "Дом-2"
	inst.SSHHost, inst.SSHPort, inst.SSHUser = "127.0.0.1", srv.port(t), "root"
	if err := s.Update("home", inst); err != nil {
		t.Fatal(err)
	}
	if got := storedHostKey(t, path); got != "SHA256:BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB" {
		t.Fatalf("правка подписи стёрла отпечаток: %q", got)
	}
	inst.SSHHost, inst.SSHPassword = "203.0.113.5", "pw2"
	if err := s.Update("home", inst); err != nil {
		t.Fatal(err)
	}
	if got := storedHostKey(t, path); got != "" {
		t.Fatalf("новый адрес SSH унаследовал отпечаток старого: %q", got)
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
