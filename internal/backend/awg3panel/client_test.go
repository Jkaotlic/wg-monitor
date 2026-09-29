package awg3panel

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Jkaotlic/wg-monitor/internal/backend/awg3panel/awg3paneltest"
)

const testPanelPass = "panel-pw-MUST-NOT-LEAK"

func startPanel(t *testing.T, o awg3paneltest.Options) *awg3paneltest.Panel {
	t.Helper()
	if o.Password == "" {
		o.Password = testPanelPass
	}
	p, err := awg3paneltest.Start(o)
	if err != nil {
		t.Fatalf("панель: %v", err)
	}
	t.Cleanup(p.Close)
	return p
}

func clientFor(t *testing.T, p *awg3paneltest.Panel, pass string, timeout time.Duration) *Client {
	t.Helper()
	certPEM, keyPEM, err := p.CA.ClientPEM("anex")
	if err != nil {
		t.Fatal(err)
	}
	c, err := NewClient(Credentials{BaseURL: p.URL, User: "admin", Password: pass, CertPEM: certPEM, KeyPEM: keyPEM},
		ClientOptions{RootCAs: p.CA.Pool, Timeout: timeout})
	if err != nil {
		t.Fatalf("клиент: %v", err)
	}
	return c
}

func wantKind(t *testing.T, err error, want Kind) {
	t.Helper()
	if KindOf(err) != want {
		t.Fatalf("класс ошибки = %q (%v), ждали %q", KindOf(err), err, want)
	}
}

func TestTypesMatchPanelJSON(t *testing.T) {
	// Строки -- дословно формы awg3-panel@b5ee1c7 (PeerView c overrides,
	// IfaceMeta, IfaceSummary с version).
	var peers []Peer
	if err := json.Unmarshal([]byte(`[{"id":"a1b2c3d4e5f6","name":"wgmon-router-owned","address":"10.66.0.2/32","public_key_short":"AbCdEfGh…","enabled":true,"last_handshake":1790000000,"rx_bytes":1048576,"tx_bytes":2048,"never_connected":false,"created_at":"2026-09-01T10:00:00Z","overrides":{"dns":"1.1.1.1"}}]`), &peers); err != nil {
		t.Fatal(err)
	}
	if p := peers[0]; p.ID != "a1b2c3d4e5f6" || p.Name != "wgmon-router-owned" || p.LastHandshake != 1790000000 || p.RxBytes != 1048576 || p.CreatedAt != "2026-09-01T10:00:00Z" || !p.Enabled {
		t.Fatalf("пир: %+v", p)
	}
	var ifaces []Iface
	if err := json.Unmarshal([]byte(`[{"id":"awg1","title":"main","interface":"awg1","interface_edit":false,"pending_active":false}]`), &ifaces); err != nil || ifaces[0].Title != "main" {
		t.Fatalf("интерфейсы: %+v %v", ifaces, err)
	}
	var s Summary
	if err := json.Unmarshal([]byte(`{"interface":"awg1","listen_port":51820,"public_key":"X","mtu":"1376","peers_total":6,"peers_online":4,"peers_stale":1,"peers_never":1,"rx_bytes":10,"tx_bytes":20,"last_handshake":1790000000,"last_handshake_peer":"iphone","version":"amneziawg-go 0.3.0"}`), &s); err != nil {
		t.Fatal(err)
	}
	if s.PeersTotal != 6 || s.PeersOnline != 4 || s.MTU != "1376" || s.LastHandshakePeer != "iphone" {
		t.Fatalf("сводка: %+v", s)
	}
}

func TestClientReadsPanelShapes(t *testing.T) {
	now := time.Now()
	p := startPanel(t, awg3paneltest.Options{
		Ifaces: []awg3paneltest.Iface{{ID: "awg1", Title: "main", Interface: "awg1"}, {ID: "awg2", Title: "reserve", Interface: "awg2"}},
		Peers: map[string][]awg3paneltest.Peer{"awg1": {
			{ID: "aaaaaaaaaaa1", Name: "iphone", Address: "10.66.0.2/32", Enabled: true, LastHandshake: now.Add(-time.Minute).Unix(), RxBytes: 100, TxBytes: 200, CreatedAt: "2026-09-01T10:00:00Z"},
			{ID: "aaaaaaaaaaa2", Name: "laptop", Address: "10.66.0.3/32", Enabled: true, NeverConnected: true},
		}},
	})
	c := clientFor(t, p, testPanelPass, 0)
	ctx := context.Background()
	ifaces, err := c.Ifaces(ctx)
	if err != nil || len(ifaces) != 2 || ifaces[1].ID != "awg2" || ifaces[1].Title != "reserve" {
		t.Fatalf("интерфейсы: %+v %v", ifaces, err)
	}
	peers, err := c.Peers(ctx, "awg1")
	if err != nil || len(peers) != 2 || peers[0].Name != "iphone" || peers[0].RxBytes != 100 || !peers[1].NeverConnected {
		t.Fatalf("пиры: %+v %v", peers, err)
	}
	s, err := c.Summary(ctx, "awg1")
	if err != nil || s.PeersTotal != 2 || s.PeersOnline != 1 || s.PeersNever != 1 {
		t.Fatalf("сводка: %+v %v", s, err)
	}
	if got := p.Hits("GET /api/"); got != 3 {
		t.Fatalf("запросов %d, ждали 3", got)
	}
}

func TestClientAddPeerAndConfig(t *testing.T) {
	p := startPanel(t, awg3paneltest.Options{})
	c := clientFor(t, p, testPanelPass, 0)
	issued, err := c.AddPeer(context.Background(), "awg1", "iphone-anex")
	if err != nil {
		t.Fatal(err)
	}
	if issued.ID == "" || issued.Name != "iphone-anex" || !strings.Contains(issued.Config, "[Interface]") || issued.QRPNGBase64 == "" {
		t.Fatalf("выпуск: id=%q name=%q", issued.ID, issued.Name)
	}
	conf, err := c.PeerConfig(context.Background(), "awg1", issued.ID)
	if err != nil || string(conf) != issued.Config {
		t.Fatalf("повторная выдача: %v", err)
	}
	if strings.Contains(issued.String(), "PRIVATE") || strings.Contains(issued.String(), "FAKE-PRIVATE-KEY") {
		t.Fatal("Issued печатает конфиг")
	}
}

func TestClientWrongPasswordIs401(t *testing.T) {
	p := startPanel(t, awg3paneltest.Options{})
	_, err := clientFor(t, p, "wrong", 0).Ifaces(context.Background())
	wantKind(t, err, KindBadPassword)
	var e *Error
	if !errors.As(err, &e) || e.Status != http.StatusUnauthorized {
		t.Fatalf("статус: %v", err)
	}
	if strings.Contains(err.Error(), "wrong") {
		t.Fatal("пароль в тексте ошибки")
	}
}

func TestClientBanIs429(t *testing.T) {
	p := startPanel(t, awg3paneltest.Options{})
	bad := clientFor(t, p, "wrong", 0)
	for range 5 {
		_, _ = bad.Ifaces(context.Background())
	}
	_, err := clientFor(t, p, testPanelPass, 0).Ifaces(context.Background())
	wantKind(t, err, KindBanned)
}

func TestClientCertFromForeignCA(t *testing.T) {
	p := startPanel(t, awg3paneltest.Options{})
	foreign, err := awg3paneltest.NewCA("чужой")
	if err != nil {
		t.Fatal(err)
	}
	certPEM, keyPEM, _ := foreign.ClientPEM("anex")
	c, err := NewClient(Credentials{BaseURL: p.URL, User: "admin", Password: testPanelPass, CertPEM: certPEM, KeyPEM: keyPEM}, ClientOptions{RootCAs: p.CA.Pool})
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.Ifaces(context.Background())
	wantKind(t, err, KindCert)
	if p.TotalHits() != 0 {
		t.Fatal("запрос дошёл до HTTP без принятого сертификата")
	}
}

// TestClientUntrustedServerCert -- НАШ клиент не может проверить сертификат
// ПАНЕЛИ (чужой CA). Это KindServerCert, а не KindCert: дело не в нашем .p12,
// а в адресе или сертификате сервера -- сообщение должно быть другим.
func TestClientUntrustedServerCert(t *testing.T) {
	p := startPanel(t, awg3paneltest.Options{})
	certPEM, keyPEM, _ := p.CA.ClientPEM("anex")
	// RootCAs nil -- системные корни, тестового CA среди них нет.
	c, _ := NewClient(Credentials{BaseURL: p.URL, User: "admin", Password: testPanelPass, CertPEM: certPEM, KeyPEM: keyPEM}, ClientOptions{})
	_, err := c.Ifaces(context.Background())
	wantKind(t, err, KindServerCert)
	if strings.Contains(err.Error(), ".p12") {
		t.Fatalf("сообщение про сертификат панели не должно звать пересохранить .p12: %v", err)
	}
}

// TestClientNoCertIsRejectedAsClientCert -- панель требует клиентский
// сертификат (RequireAndVerifyClientCert); если мы его не предъявили,
// панель шлёт TLS-алерт нам, а не наоборот -- это KindCert («загрузите .p12
// заново»), а не KindServerCert.
func TestClientNoCertIsRejectedAsClientCert(t *testing.T) {
	p := startPanel(t, awg3paneltest.Options{})
	tr := &http.Transport{
		TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: p.CA.Pool},
	}
	c := &Client{base: p.URL, user: "admin", pass: testPanelPass, hc: &http.Client{Transport: tr, Timeout: 5 * time.Second}}
	_, err := c.Ifaces(context.Background())
	wantKind(t, err, KindCert)
	if strings.Contains(err.Error(), "проверьте адрес") {
		t.Fatalf("сообщение не должно звать проверить сертификат сервера: %v", err)
	}
}

// TestClassifyTransportTLSKinds -- прямая таблица классов TLS-отказов:
// панель отвергла наш сертификат (несколько форм алерта) -> KindCert; мы не
// смогли проверить сертификат панели -> KindServerCert; временный сбой TLS
// («internal error») -- НЕ класс сертификата вовсе, предохранитель не должен
// взводиться -- KindUnreachable.
func TestClassifyTransportTLSKinds(t *testing.T) {
	cases := []struct {
		name string
		err  error
		kind Kind
	}{
		{"панель: bad certificate", errors.New("remote error: tls: bad certificate"), KindCert},
		{"панель: certificate required", errors.New("remote error: tls: certificate required"), KindCert},
		{"панель: unknown certificate authority", errors.New("remote error: tls: unknown certificate authority"), KindCert},
		{"панель: certificate expired", errors.New("remote error: tls: expired certificate"), KindCert},
		{"временный: internal error", errors.New("remote error: tls: internal error"), KindUnreachable},
		{"мы: неизвестный CA панели", x509.UnknownAuthorityError{}, KindServerCert},
		{"мы: не то имя хоста", x509.HostnameError{Certificate: &x509.Certificate{}, Host: "x"}, KindServerCert},
		{"мы: сертификат панели истёк", x509.CertificateInvalidError{Cert: &x509.Certificate{}, Reason: x509.Expired}, KindServerCert},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := classifyTransport(tc.err)
			if KindOf(err) != tc.kind {
				t.Fatalf("%v: класс = %q, ждали %q", err, KindOf(err), tc.kind)
			}
		})
	}
}

// TestClientPrintsNothingSecret -- *Client хранит пароль в поле pass; без
// String/GoString/LogValue печать структуры (в лог, в отладочный вывод)
// выдаёт его через рефлексию, как и у Credentials.
func TestClientPrintsNothingSecret(t *testing.T) {
	p := startPanel(t, awg3paneltest.Options{})
	c := clientFor(t, p, testPanelPass, 0)
	var logs bytes.Buffer
	slog.New(slog.NewTextHandler(&logs, nil)).Info("x", "client", c)
	out := fmt.Sprintf("%v %+v %#v %s", c, c, c, logs.String())
	if strings.Contains(out, testPanelPass) {
		t.Fatalf("печать клиента выдала пароль: %s", out)
	}
}

func TestClientHTMLFromProxy(t *testing.T) {
	p := startPanel(t, awg3paneltest.Options{})
	c := clientFor(t, p, testPanelPass, 0)
	html := func(code int) func(http.ResponseWriter, *http.Request) bool {
		return func(w http.ResponseWriter, _ *http.Request) bool {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.WriteHeader(code)
			_, _ = w.Write([]byte("<html><body><h1>502 Bad Gateway</h1></body></html>"))
			return true
		}
	}
	p.SetOverride(html(http.StatusBadGateway))
	_, err := c.Ifaces(context.Background())
	wantKind(t, err, KindUnreachable)
	p.SetOverride(html(http.StatusOK))
	_, err = c.Peers(context.Background(), "awg1")
	wantKind(t, err, KindBadResponse)
	p.SetOverride(html(http.StatusForbidden))
	_, err = c.Summary(context.Background(), "awg1")
	wantKind(t, err, KindBadResponse)
	if strings.Contains(err.Error(), "<html>") {
		t.Fatal("тело HTML в тексте ошибки")
	}
}

func TestClientDoesNotFollowRedirects(t *testing.T) {
	p := startPanel(t, awg3paneltest.Options{})
	c := clientFor(t, p, testPanelPass, 0)
	p.SetOverride(func(w http.ResponseWriter, r *http.Request) bool {
		if r.URL.Path == "/login" {
			w.WriteHeader(http.StatusTeapot)
			return true
		}
		http.Redirect(w, r, "/login", http.StatusFound)
		return true
	})
	_, err := c.Ifaces(context.Background())
	wantKind(t, err, KindBadResponse)
	if p.Hits("GET /login") != 0 {
		t.Fatal("клиент пошёл по редиректу")
	}
}

func TestClientReadonlyPanel(t *testing.T) {
	p := startPanel(t, awg3paneltest.Options{Readonly: true})
	c := clientFor(t, p, testPanelPass, 0)
	_, err := c.AddPeer(context.Background(), "awg1", "iphone")
	wantKind(t, err, KindReadonly) // 405 от mux: на пути есть только GET
	_, err = c.PeerConfig(context.Background(), "awg1", "aaaaaaaaaaaa")
	wantKind(t, err, KindReadonly) // 404 text/plain: маршрута нет вовсе
}

func TestClientNotFoundAndInvalidAreJSON(t *testing.T) {
	p := startPanel(t, awg3paneltest.Options{})
	c := clientFor(t, p, testPanelPass, 0)
	_, err := c.Peers(context.Background(), "nope")
	wantKind(t, err, KindNotFound)
	_, err = c.PeerConfig(context.Background(), "awg1", "000000000000")
	wantKind(t, err, KindNotFound) // 404 JSON на мутационном маршруте -- не readonly
	_, err = c.AddPeer(context.Background(), "awg1", "bad[name]")
	wantKind(t, err, KindInvalid)
	if !strings.Contains(err.Error(), "квадратные скобки") {
		t.Fatalf("текст отказа панели потерян: %v", err)
	}
}

func TestClientTimeout(t *testing.T) {
	p := startPanel(t, awg3paneltest.Options{})
	p.SetDelay(500 * time.Millisecond)
	_, err := clientFor(t, p, testPanelPass, 100*time.Millisecond).Ifaces(context.Background())
	wantKind(t, err, KindUnreachable)
}

func TestNewClientRejectsBadInput(t *testing.T) {
	p := startPanel(t, awg3paneltest.Options{})
	certPEM, keyPEM, _ := p.CA.ClientPEM("anex")
	for _, base := range []string{"http://panel.example.com", "ftp://x", "https://", "https://u:p@example.com"} {
		if _, err := NewClient(Credentials{BaseURL: base, User: "admin", Password: "x", CertPEM: certPEM, KeyPEM: keyPEM}, ClientOptions{}); err == nil {
			t.Errorf("%s: ждали отказ", base)
		}
	}
	if _, err := NewClient(Credentials{BaseURL: p.URL, User: "admin", Password: "x", CertPEM: certPEM, KeyPEM: []byte("junk")}, ClientOptions{}); KindOf(err) != KindCert {
		t.Fatalf("битый ключ: %v", err)
	}
	cred := Credentials{BaseURL: p.URL, User: "admin", Password: testPanelPass, CertPEM: certPEM, KeyPEM: keyPEM}
	if s := cred.String(); strings.Contains(s, testPanelPass) || strings.Contains(s, "PRIVATE KEY") {
		t.Fatal("Credentials печатает секреты")
	}
}
