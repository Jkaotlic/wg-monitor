package awg3paneltest

import (
	"bytes"
	"crypto/sha256"
	"crypto/tls"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"image"
	"image/png"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"
)

// Iface и Peer -- формы ответа настоящей панели (issuer.IfaceMeta,
// issuer.PeerView без overrides).
type Iface struct {
	ID            string `json:"id"`
	Title         string `json:"title"`
	Interface     string `json:"interface"`
	InterfaceEdit bool   `json:"interface_edit"`
	PendingActive bool   `json:"pending_active"`
}

type Peer struct {
	ID             string `json:"id"`
	Name           string `json:"name"`
	Address        string `json:"address"`
	PublicKeyShort string `json:"public_key_short"`
	Enabled        bool   `json:"enabled"`
	LastHandshake  int64  `json:"last_handshake"`
	RxBytes        int64  `json:"rx_bytes"`
	TxBytes        int64  `json:"tx_bytes"`
	NeverConnected bool   `json:"never_connected"`
	CreatedAt      string `json:"created_at"`
}

// summary -- issuer.IfaceSummary.
type summary struct {
	Interface         string `json:"interface"`
	ListenPort        int    `json:"listen_port"`
	PublicKey         string `json:"public_key"`
	MTU               string `json:"mtu"`
	PeersTotal        int    `json:"peers_total"`
	PeersOnline       int    `json:"peers_online"`
	PeersStale        int    `json:"peers_stale"`
	PeersNever        int    `json:"peers_never"`
	RxBytes           int64  `json:"rx_bytes"`
	TxBytes           int64  `json:"tx_bytes"`
	LastHandshake     int64  `json:"last_handshake"`
	LastHandshakePeer string `json:"last_handshake_peer"`
	Version           string `json:"version,omitempty"`
}

type Options struct {
	User     string // пусто -- "admin"
	Password string
	Readonly bool
	Ifaces   []Iface           // пусто -- один awg1
	Peers    map[string][]Peer // по id интерфейса
	Now      func() time.Time
}

type Panel struct {
	URL string
	CA  *CA

	srv    *httptest.Server
	hellos atomic.Int64

	mu          sync.Mutex
	opts        Options
	peers       map[string][]Peer
	confs       map[string]string
	hits        []string
	fails       []time.Time
	bannedUntil time.Time
	override    func(http.ResponseWriter, *http.Request) bool
	delay       time.Duration
	seq         int
}

func Start(o Options) (*Panel, error) {
	ca, err := NewCA("awg3-test-ca")
	if err != nil {
		return nil, err
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.User == "" {
		o.User = "admin"
	}
	if len(o.Ifaces) == 0 {
		o.Ifaces = []Iface{{ID: "awg1", Title: "main", Interface: "awg1"}}
	}
	p := &Panel{CA: ca, opts: o, peers: map[string][]Peer{}, confs: map[string]string{}}
	for iface, list := range o.Peers {
		for _, peer := range list {
			p.addLocked(iface, peer)
		}
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/ifaces", p.handleIfaces)
	mux.HandleFunc("GET /api/ifaces/{iface}/peers", p.handlePeers)
	mux.HandleFunc("GET /api/ifaces/{iface}/summary", p.handleSummary)
	if !o.Readonly {
		mux.HandleFunc("POST /api/ifaces/{iface}/peers", p.handleAdd)
		mux.HandleFunc("GET /api/ifaces/{iface}/peers/{id}/config", p.handleConfig)
	}
	srvCert, err := ca.serverCert()
	if err != nil {
		return nil, err
	}
	p.srv = httptest.NewUnstartedServer(p.front(mux))
	p.srv.TLS = &tls.Config{
		MinVersion:   tls.VersionTLS12,
		Certificates: []tls.Certificate{srvCert},
		ClientAuth:   tls.RequireAndVerifyClientCert,
		ClientCAs:    ca.Pool,
		GetConfigForClient: func(*tls.ClientHelloInfo) (*tls.Config, error) {
			p.hellos.Add(1)
			return nil, nil
		},
	}
	p.srv.Config.ErrorLog = log.New(io.Discard, "", 0)
	p.srv.StartTLS()
	p.URL = p.srv.URL
	return p, nil
}

func (p *Panel) Close() { p.srv.Close() }

// Handshakes -- сколько TLS-рукопожатий начато: запрос, отвергнутый ещё на
// TLS, до HTTP не доходит и в Hits не попадает.
func (p *Panel) Handshakes() int64 { return p.hellos.Load() }

func (p *Panel) Hits(prefix string) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	n := 0
	for _, h := range p.hits {
		if strings.HasPrefix(h, prefix) {
			n++
		}
	}
	return n
}

func (p *Panel) TotalHits() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.hits)
}

func (p *Panel) ResetHits() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.hits = nil
}

// SetOverride -- ответ вместо панели (Caddy с HTML, 429, 502). true -- ответ
// уже записан. Запрос всё равно попадает в Hits.
func (p *Panel) SetOverride(fn func(http.ResponseWriter, *http.Request) bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.override = fn
}

func (p *Panel) SetDelay(d time.Duration) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.delay = d
}

func (p *Panel) SetPassword(pw string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.opts.Password = pw
	p.fails, p.bannedUntil = nil, time.Time{}
}

func (p *Panel) AddPeer(iface string, peer Peer) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.addLocked(iface, peer)
}

func (p *Panel) PeerList(iface string) []Peer {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]Peer(nil), p.peers[iface]...)
}

func (p *Panel) addLocked(iface string, peer Peer) {
	p.peers[iface] = append(p.peers[iface], peer)
	p.confs[peer.ID] = FakeConf(peer.Name, peer.Address)
}

// FakeConf -- клиентский конфиг-заглушка. Строка PrivateKey с маркером нужна
// тестам «конфиг не утёк в ответ и журнал».
func FakeConf(name, address string) string {
	return "[Interface]\nPrivateKey = FAKE-PRIVATE-KEY-" + name + "\nAddress = " + address +
		"\nDNS = 1.1.1.1\n\n[Peer]\nPublicKey = SRVPUBKEY\nEndpoint = 203.0.113.50:51820\nAllowedIPs = 0.0.0.0/0\n"
}

func (p *Panel) front(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p.mu.Lock()
		p.hits = append(p.hits, r.Method+" "+r.URL.Path)
		override, delay := p.override, p.delay
		p.mu.Unlock()
		if delay > 0 {
			select {
			case <-time.After(delay):
			case <-r.Context().Done():
				return
			}
		}
		if override != nil && override(w, r) {
			return
		}
		if !p.auth(w, r) {
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (p *Panel) auth(w http.ResponseWriter, r *http.Request) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	now := p.opts.Now()
	if now.Before(p.bannedUntil) {
		http.Error(w, "слишком много неудачных попыток, попробуйте позже", http.StatusTooManyRequests)
		return false
	}
	user, pass, ok := r.BasicAuth()
	if !ok || user != p.opts.User || pass != p.opts.Password {
		recent := p.fails[:0]
		for _, t := range p.fails {
			if now.Sub(t) < 5*time.Minute {
				recent = append(recent, t)
			}
		}
		p.fails = append(recent, now)
		if len(p.fails) >= 5 {
			p.bannedUntil = now.Add(15 * time.Minute)
		}
		w.Header().Set("WWW-Authenticate", `Basic realm="awg3-panel", charset="UTF-8"`)
		http.Error(w, "требуется авторизация", http.StatusUnauthorized)
		return false
	}
	p.fails = nil
	return true
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}

func (p *Panel) known(iface string) bool {
	for _, i := range p.opts.Ifaces {
		if i.ID == iface {
			return true
		}
	}
	return false
}

func (p *Panel) handleIfaces(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, p.opts.Ifaces)
}

func (p *Panel) handlePeers(w http.ResponseWriter, r *http.Request) {
	iface := r.PathValue("iface")
	if !p.known(iface) {
		writeErr(w, http.StatusNotFound, fmt.Sprintf("интерфейс %q не найден", iface))
		return
	}
	list := p.PeerList(iface)
	if list == nil {
		list = []Peer{}
	}
	writeJSON(w, http.StatusOK, list)
}

// handleSummary -- арифметика issuer.summarize: онлайн -- handshake не старше
// 180 с, «нет связи» -- старше суток, «никогда» -- 0; выключенные только в суммах.
func (p *Panel) handleSummary(w http.ResponseWriter, r *http.Request) {
	iface := r.PathValue("iface")
	if !p.known(iface) {
		writeErr(w, http.StatusNotFound, fmt.Sprintf("интерфейс %q не найден", iface))
		return
	}
	now := p.opts.Now()
	s := summary{Interface: iface, ListenPort: 51820, PublicKey: "SRVPUBKEY", MTU: "1376", Version: "amneziawg-go 0.3.0"}
	for _, peer := range p.PeerList(iface) {
		s.PeersTotal++
		s.RxBytes += peer.RxBytes
		s.TxBytes += peer.TxBytes
		if !peer.Enabled {
			continue
		}
		if peer.LastHandshake > s.LastHandshake {
			s.LastHandshake, s.LastHandshakePeer = peer.LastHandshake, peer.Name
		}
		switch {
		case peer.LastHandshake == 0:
			s.PeersNever++
		case peer.LastHandshake >= now.Add(-180*time.Second).Unix():
			s.PeersOnline++
		case peer.LastHandshake < now.Add(-24*time.Hour).Unix():
			s.PeersStale++
		}
	}
	writeJSON(w, http.StatusOK, s)
}

// validName -- issuer.validateName: обрезка, 1..40 рун, без \r \n [ ].
func validName(name string) (string, string) {
	n := strings.TrimSpace(name)
	switch {
	case n == "":
		return "", "имя пира не может быть пустым"
	case utf8.RuneCountInString(n) > 40:
		return "", "имя длиннее 40 символов"
	case strings.ContainsAny(n, "\r\n"):
		return "", "имя содержит перевод строки"
	case strings.ContainsAny(n, "[]"):
		return "", "имя содержит квадратные скобки"
	}
	return n, ""
}

func (p *Panel) handleAdd(w http.ResponseWriter, r *http.Request) {
	iface := r.PathValue("iface")
	if !p.known(iface) {
		writeErr(w, http.StatusNotFound, fmt.Sprintf("интерфейс %q не найден", iface))
		return
	}
	var body struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, `тело запроса не JSON вида {"name":"..."}`)
		return
	}
	name, problem := validName(body.Name)
	if problem != "" {
		writeErr(w, http.StatusBadRequest, "некорректный ввод: "+problem)
		return
	}
	p.mu.Lock()
	p.seq++
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s/%d", name, p.seq)))
	peer := Peer{
		ID: hex.EncodeToString(sum[:])[:12], Name: name, Address: fmt.Sprintf("10.66.0.%d/32", p.seq+10),
		PublicKeyShort: "PUBKEY" + fmt.Sprint(p.seq) + "…", Enabled: true, NeverConnected: true,
		CreatedAt: p.opts.Now().UTC().Format(time.RFC3339),
	}
	p.addLocked(iface, peer)
	conf := p.confs[peer.ID]
	p.mu.Unlock()
	writeJSON(w, http.StatusCreated, map[string]any{
		"id": peer.ID, "name": peer.Name, "address": peer.Address,
		"config": conf, "qr_png_base64": base64.StdEncoding.EncodeToString(tinyPNG()),
	})
}

func (p *Panel) handleConfig(w http.ResponseWriter, r *http.Request) {
	iface, id := r.PathValue("iface"), r.PathValue("id")
	if !p.known(iface) {
		writeErr(w, http.StatusNotFound, fmt.Sprintf("интерфейс %q не найден", iface))
		return
	}
	p.mu.Lock()
	var conf string
	for _, peer := range p.peers[iface] {
		if peer.ID == id {
			conf = p.confs[id]
		}
	}
	p.mu.Unlock()
	if conf == "" {
		writeErr(w, http.StatusNotFound, "не найдено: пир "+id)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="peer.conf"`)
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write([]byte(conf))
}

// tinyPNG -- настоящий PNG 1×1: экрану и Telegram нужна картинка, а не байты.
func tinyPNG() []byte {
	var buf bytes.Buffer
	_ = png.Encode(&buf, image.NewGray(image.Rect(0, 0, 1, 1)))
	return buf.Bytes()
}
