package main

import (
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"golang.org/x/crypto/ssh"
)

// rotSrv -- SSH-сервер в процессе: считает попытки входа по паролю и
// отвечает на команды проверки (имя, MAC, конфиг агента).
type rotSrv struct {
	port      int
	pub       ssh.PublicKey
	passTries int32
	authTries int32
	mac       string
}

func startRotSrv(t *testing.T, mac string) *rotSrv {
	t.Helper()
	signer, err := genTestSigner()
	if err != nil {
		t.Skip("genTestSigner unavailable")
	}
	rs := &rotSrv{pub: signer.PublicKey(), mac: mac}
	cfg := &ssh.ServerConfig{
		AuthLogCallback: func(ssh.ConnMetadata, string, error) { atomic.AddInt32(&rs.authTries, 1) },
		PasswordCallback: func(ssh.ConnMetadata, []byte) (*ssh.Permissions, error) {
			atomic.AddInt32(&rs.passTries, 1)
			return nil, nil
		},
	}
	cfg.AddHostKey(signer)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	rs.port = ln.Addr().(*net.TCPAddr).Port
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				sc, chans, reqs, err := ssh.NewServerConn(conn, cfg)
				if err != nil {
					return
				}
				defer sc.Close()
				go ssh.DiscardRequests(reqs)
				for ch := range chans {
					c, rq, err := ch.Accept()
					if err != nil {
						continue
					}
					go func() {
						defer c.Close()
						for r := range rq {
							if r.Type != "exec" {
								_ = r.Reply(false, nil)
								continue
							}
							_ = r.Reply(true, nil)
							cmd := string(r.Payload[4:])
							out := ""
							switch {
							case strings.Contains(cmd, "kernel/hostname"):
								out = "rt\n"
							case strings.Contains(cmd, "/sys/class/net"):
								out = "eth0=" + rs.mac + "\n"
							}
							_, _ = c.Write([]byte(out))
							_, _ = c.SendRequest("exit-status", false, []byte{0, 0, 0, 0})
							return
						}
					}()
				}
			}()
		}
	}()
	t.Cleanup(func() { ln.Close() })
	return rs
}

// rotFixture: known_hosts хранит ключ старого сервера под «r1»; новый
// сервер (другой ключ) слушает свой порт, агент указывает на него.
func rotFixture(t *testing.T, newMAC string) (*KnownHosts, *AgentState, *rotSrv, string) {
	t.Helper()
	old := startRotSrv(t, "aa:bb:cc:dd:ee:ff")
	kh, err := NewKnownHosts(filepath.Join(t.TempDir(), "known_hosts"))
	if err != nil {
		t.Fatal(err)
	}
	s, err := ConnectSSH("127.0.0.1", old.port, "root", "pw", kh, "r1")
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
	nw := startRotSrv(t, newMAC)
	ag := &AgentState{Nickname: "r1", Host: "127.0.0.1", Port: nw.port, ExpectedMAC: "aabbccddeeff"}
	return kh, ag, nw, ssh.FingerprintSHA256(nw.pub)
}

func noAsk(t *testing.T) func(string, string) string {
	return func(p, _ string) string { t.Fatalf("неожиданный вопрос: %s", p); return "" }
}

func TestCaptureHostKey_NoPasswordSent(t *testing.T) {
	srv := startRotSrv(t, "aa:bb:cc:dd:ee:ff")
	pub, err := CaptureHostKey("127.0.0.1", srv.port)
	if err != nil {
		t.Fatal(err)
	}
	if ssh.FingerprintSHA256(pub) != ssh.FingerprintSHA256(srv.pub) {
		t.Fatal("снят не тот ключ")
	}
	if n := atomic.LoadInt32(&srv.passTries); n != 0 {
		t.Fatalf("пароль отправлен %d раз", n)
	}
	if n := atomic.LoadInt32(&srv.authTries); n != 0 {
		t.Fatalf("попыток аутентификации любым методом: %d", n)
	}
}

func TestHostKeyRotation_RefusedWithoutConfirmation_NoPassword(t *testing.T) {
	kh, ag, nw, _ := rotFixture(t, "aa:bb:cc:dd:ee:ff")
	before, _ := os.ReadFile(kh.path)
	_, err := connectAgentSSHManagedAsk(&State{}, ag, "pw", kh, "t", func(string, string) string { return "wrong" })
	if err == nil {
		t.Fatal("ожидался отказ")
	}
	if n := atomic.LoadInt32(&nw.passTries); n != 0 {
		t.Fatalf("пароль ушёл на неподтверждённый ключ: %d", n)
	}
	after, _ := os.ReadFile(kh.path)
	if string(before) != string(after) {
		t.Fatal("known_hosts изменён без подтверждения")
	}
}

func TestHostKeyRotation_YesToAllDoesNotConfirm(t *testing.T) {
	t.Setenv("WG_YES_TO_ALL", "1")
	kh, ag, nw, _ := rotFixture(t, "aa:bb:cc:dd:ee:ff")
	_, err := connectAgentSSHManagedAsk(&State{}, ag, "pw", kh, "t", func(string, string) string { return "" })
	if err == nil || atomic.LoadInt32(&nw.passTries) != 0 {
		t.Fatalf("WG_YES_TO_ALL не должен подтверждать ключ: err=%v tries=%d", err, nw.passTries)
	}
}

func TestHostKeyRotation_ConfirmByFullAndTail(t *testing.T) {
	for _, mode := range []string{"full", "tail"} {
		kh, ag, nw, fp := rotFixture(t, "aa:bb:cc:dd:ee:ff")
		ans := fp
		if mode == "tail" {
			ans = fp[len(fp)-8:]
		}
		s, err := connectAgentSSHManagedAsk(&State{}, ag, "pw", kh, "t", func(string, string) string { return ans })
		if err != nil {
			t.Fatalf("%s: %v", mode, err)
		}
		s.Close()
		if atomic.LoadInt32(&nw.passTries) == 0 {
			t.Fatalf("%s: вход после подтверждения не состоялся", mode)
		}
		if got := kh.SavedHostKey("r1"); got == nil || ssh.FingerprintSHA256(got) != fp {
			t.Fatalf("%s: known_hosts не обновлён", mode)
		}
	}
}

func TestHostKeyRotation_ShortTailRejected(t *testing.T) {
	kh, ag, nw, fp := rotFixture(t, "aa:bb:cc:dd:ee:ff")
	_, err := connectAgentSSHManagedAsk(&State{}, ag, "pw", kh, "t", func(string, string) string { return fp[len(fp)-7:] })
	if err == nil || atomic.LoadInt32(&nw.passTries) != 0 {
		t.Fatal("хвост короче 8 символов принят")
	}
}

func TestHostKeyRotation_EnvCarriesFingerprint(t *testing.T) {
	kh, ag, _, fp := rotFixture(t, "aa:bb:cc:dd:ee:ff")
	t.Setenv(hostKeyAcceptEnv, fp)
	s, err := connectAgentSSHManagedAsk(&State{}, ag, "pw", kh, "t", noAsk(t))
	if err != nil {
		t.Fatal(err)
	}
	s.Close()

	kh2, ag2, nw2, _ := rotFixture(t, "aa:bb:cc:dd:ee:ff")
	t.Setenv(hostKeyAcceptEnv, "SHA256:чужойотпечатокчужой")
	if _, err := connectAgentSSHManagedAsk(&State{}, ag2, "pw", kh2, "t", noAsk(t)); err == nil || atomic.LoadInt32(&nw2.passTries) != 0 {
		t.Fatal("чужой отпечаток в флаге принят")
	}
}

func TestHostKeyRotation_MACMismatchRollsBack(t *testing.T) {
	kh, ag, _, fp := rotFixture(t, "11:22:33:44:55:66")
	oldKey := kh.SavedHostKey("r1")
	before, _ := os.ReadFile(kh.path)
	_, err := connectAgentSSHManagedAsk(&State{}, ag, "pw", kh, "t", func(string, string) string { return fp })
	if err == nil || !strings.Contains(err.Error(), "MAC mismatch") {
		t.Fatalf("ожидалось несовпадение MAC, got %v", err)
	}
	after, _ := os.ReadFile(kh.path)
	if string(before) != string(after) {
		t.Fatal("known_hosts не откатан")
	}
	if got := kh.SavedHostKey("r1"); got == nil || ssh.FingerprintSHA256(got) != ssh.FingerprintSHA256(oldKey) {
		t.Fatal("после отката старый ключ не на месте")
	}
}

func TestHostKeyRotation_EnvTailRejected(t *testing.T) {
	kh, ag, nw, fp := rotFixture(t, "aa:bb:cc:dd:ee:ff")
	t.Setenv(hostKeyAcceptEnv, fp[len(fp)-8:])
	if _, err := connectAgentSSHManagedAsk(&State{}, ag, "pw", kh, "t", noAsk(t)); err == nil {
		t.Fatal("хвост в флаге принят")
	}
	if atomic.LoadInt32(&nw.passTries) != 0 || atomic.LoadInt32(&nw.authTries) != 0 {
		t.Fatal("на неподтверждённый ключ ушла аутентификация")
	}
}
