package selfhostedamnezia

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

// revokeRunner -- контейнер в памяти с журналом вызовов wg.
type revokeRunner struct {
	mu    sync.Mutex
	files map[string][]byte
	wg    [][]string
	wgErr error
}

const (
	pubA = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="
	pubB = "BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB="
)

func newRevokeRunner() *revokeRunner {
	return &revokeRunner{files: map[string][]byte{
		"/opt/amnezia/awg/awg0.conf": []byte("[Interface]\nPrivateKey = server-private\nAddress = 10.8.1.0/24\nListenPort = 47567\n" +
			"\n[Peer]\nPublicKey = " + pubA + "\nPresharedKey = psk\nAllowedIPs = 10.8.1.2/32\n" +
			"\n[Peer]\nPublicKey = " + pubB + "\nPresharedKey = psk\nAllowedIPs = 10.8.1.3/32\n"),
		"/opt/amnezia/awg/clientsTable": []byte(`[
  {"clientId":"` + pubA + `","userData":{"allowedIps":"10.8.1.2/32","clientName":"wgmon-home-20261001-100000","creationDate":"Thu Oct 01 10:00:00 2026"}},
  {"clientId":"` + pubB + `","userData":{"allowedIps":"10.8.1.3/32","clientName":"wgmon-home-20261003-120000","creationDate":"Sat Oct 03 12:00:00 2026"}}
]`),
	}}
}

func (r *revokeRunner) Run(_ context.Context, args []string, stdin []byte) ([]byte, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	switch {
	case len(args) >= 2 && args[0] == "cat":
		return append([]byte{}, r.files[args[1]]...), nil
	case len(args) >= 5 && args[0] == "sh":
		r.files[args[4]] = append([]byte{}, stdin...)
	case len(args) >= 4 && args[0] == "mv":
		r.files[args[3]] = r.files[args[2]]
		delete(r.files, args[2])
	case len(args) >= 1 && args[0] == "wg":
		r.wg = append(r.wg, args)
		return nil, r.wgErr
	}
	return nil, nil
}

func revokeService(t *testing.T, r Runner) *Service {
	t.Helper()
	s := newTestService(t)
	s.newRunner = func(Config, HostKeyPolicy) Runner { return r }
	if err := s.Create(homeInstance()); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestClientsListsIssuedConnections(t *testing.T) {
	s := revokeService(t, newRevokeRunner())
	got, inst, err := s.Clients(context.Background(), "home")
	if err != nil || inst.ID != "home" {
		t.Fatalf("%+v %v", inst, err)
	}
	if len(got) != 2 || got[0].PublicKey != pubA || got[0].Name != "wgmon-home-20261001-100000" ||
		got[0].Address != "10.8.1.2/32" || !got[1].CreatedAt.After(got[0].CreatedAt) {
		t.Fatalf("список: %+v", got)
	}
}

func TestRevokeRemovesPeerEverywhere(t *testing.T) {
	r := newRevokeRunner()
	s := revokeService(t, r)
	entry, _, err := s.Revoke(context.Background(), "home", pubA)
	if err != nil || entry.Name != "wgmon-home-20261001-100000" {
		t.Fatalf("%+v %v", entry, err)
	}
	conf := string(r.files["/opt/amnezia/awg/awg0.conf"])
	if strings.Contains(conf, pubA) || !strings.Contains(conf, pubB) || !strings.Contains(conf, "PrivateKey = server-private") || !strings.Contains(conf, "ListenPort = 47567") {
		t.Fatalf("серверный конфиг после отзыва:\n%s", conf)
	}
	tbl := string(r.files["/opt/amnezia/awg/clientsTable"])
	if strings.Contains(tbl, pubA) || !strings.Contains(tbl, pubB) {
		t.Fatalf("таблица после отзыва:\n%s", tbl)
	}
	if len(r.wg) != 1 || strings.Join(r.wg[0], " ") != "wg set awg0 peer "+pubA+" remove" {
		t.Fatalf("wg: %v", r.wg)
	}
	left, _, _ := s.Clients(context.Background(), "home")
	if len(left) != 1 || left[0].PublicKey != pubB {
		t.Fatalf("осталось: %+v", left)
	}
}

func TestRevokeUnknownClientTouchesNothing(t *testing.T) {
	r := newRevokeRunner()
	s := revokeService(t, r)
	before := string(r.files["/opt/amnezia/awg/awg0.conf"])
	if _, _, err := s.Revoke(context.Background(), "home", "CCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCC="); !errors.Is(err, ErrClientNotFound) {
		t.Fatalf("неизвестный: %v", err)
	}
	if _, _, err := s.Revoke(context.Background(), "home", "x; rm -rf /"); !errors.Is(err, ErrClientNotFound) {
		t.Fatalf("мусор вместо ключа: %v", err)
	}
	if string(r.files["/opt/amnezia/awg/awg0.conf"]) != before || len(r.wg) != 0 {
		t.Fatal("конфиг или wg тронуты")
	}
	if _, _, err := s.Revoke(context.Background(), "nope", pubA); !errors.Is(err, ErrInstanceNotFound) {
		t.Fatalf("нет сервера: %v", err)
	}
}

// Сбой wg: отзыв не засчитан, файлы целы -- повтор безопасен.
func TestRevokeWgFailureLeavesFilesAndRetrySucceeds(t *testing.T) {
	r := newRevokeRunner()
	r.wgErr = errors.New("wg: busy")
	s := revokeService(t, r)
	if _, _, err := s.Revoke(context.Background(), "home", pubA); err == nil {
		t.Fatal("ждали ошибку")
	}
	if !strings.Contains(string(r.files["/opt/amnezia/awg/awg0.conf"]), pubA) {
		t.Fatal("файл изменён при сбое wg")
	}
	r.wgErr = nil
	if _, _, err := s.Revoke(context.Background(), "home", pubA); err != nil {
		t.Fatalf("повтор: %v", err)
	}
}

func TestRevokeHostKeyChangedHasLabel(t *testing.T) {
	s := newTestService(t)
	s.newRunner = func(Config, HostKeyPolicy) Runner { return changedRunner{} }
	if err := s.Create(homeInstance()); err != nil {
		t.Fatal(err)
	}
	var hk *HostKeyChangedError
	if _, _, err := s.Revoke(context.Background(), "home", pubA); !errors.As(err, &hk) || hk.Label != "Дом" {
		t.Fatalf("%v", err)
	}
	if _, _, err := s.Clients(context.Background(), "home"); !errors.As(err, &hk) {
		t.Fatalf("список: %v", err)
	}
}

type changedRunner struct{}

func (changedRunner) Run(context.Context, []string, []byte) ([]byte, error) {
	return nil, ErrHostKeyChanged
}

func TestClientNameBelongsTo(t *testing.T) {
	ts := time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC).Format("20060102-150405")
	cases := []struct {
		name, nick string
		want       bool
	}{
		{"wgmon-home-" + ts, "home", true},
		{"wgmon-home-b-" + ts, "home", false}, // чужой ник «home-b»
		{"wgmon-home-b-" + ts, "home-b", true},
		{"phone-of-ann", "home", false},
		{"wgmon-home", "home", false},
		{sanitizeName("wgmon-averyveryverylongrouternick-" + ts), "averyveryverylongrouternick", true}, // имя обрезано до 32
	}
	for _, c := range cases {
		if got := ClientNameBelongsTo(c.name, c.nick); got != c.want {
			t.Errorf("%q / %q: %v, ждали %v", c.name, c.nick, got, c.want)
		}
	}
}
