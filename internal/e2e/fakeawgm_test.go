package e2e

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// Подделка awg-manager -- край системы. Формы ответов сверены с
// internal/agent/awgmgr/testdata/live-2172 и типами пакета awgmgr; всё, что
// выше HTTP (клиент, проверки, снимок маршрутов), -- настоящее.

// neverHandshake -- обмена ключами не было ни разу (lastHandshake пустой).
const neverHandshake = time.Duration(-1)

// fakeTunnel -- VPN-туннель на поддельном роутере. Рукопожатие задаётся
// возрастом: момент считается при каждом ответе, как у живого роутера.
type fakeTunnel struct {
	ID      string
	Name    string
	Backend string // nativewg | kernel
	// NDMS -- имя интерфейса в NDMS: у nativewg есть, у opkg-туннелей пусто
	// (живая форма: связь со звеном политики идёт по метке).
	NDMS          string
	PolicyIface   string // имя звена в политике: Wireguard2, OpkgTun10
	Iface         string // интерфейс ядра: nwg2, opkgtun10
	Status        string
	StatusDetails string
	Enabled       bool
	HandshakeAge  time.Duration
	PingStatus    string
}

// Promotion -- принятый роутером permit с order 0 («сделать главным»).
type Promotion struct {
	Policy   string
	Iface    string
	TunnelID string
}

type fakePolicy struct {
	Name  string
	Links []string // имена звеньев по порядку приоритета
}

// fakeAWGM -- поддельный awg-manager. Все методы безопасны для параллельного
// вызова: агент читает его из своих горутин.
type fakeAWGM struct {
	srv *httptest.Server

	mu            sync.Mutex
	tunnels       []*fakeTunnel
	policies      []*fakePolicy
	matrix        map[string]bool // id туннеля -> проба прошла; нет ключа -- ячеек нет
	matrixMissing bool            // старая сборка: 404 на /api/monitoring/matrix
	down          bool            // демон молчит: соединение рвётся без ответа
	promotions    []Promotion
	calls         []string // журнал мутаций по порядку
	unknown       []string // пути, которых подделка не знает
	onRestart     func(id string)
}

func newFakeAWGM(t *testing.T) *fakeAWGM {
	t.Helper()
	f := &fakeAWGM{matrix: map[string]bool{}}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

// SetTunnel добавляет туннель или заменяет его целиком (по ID).
func (f *fakeAWGM) SetTunnel(tu fakeTunnel) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i, have := range f.tunnels {
		if have.ID == tu.ID {
			f.tunnels[i] = &tu
			return
		}
	}
	f.tunnels = append(f.tunnels, &tu)
}

// UpdateTunnel правит туннель на месте.
func (f *fakeAWGM) UpdateTunnel(id string, mutate func(*fakeTunnel)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, have := range f.tunnels {
		if have.ID == id {
			mutate(have)
			return
		}
	}
	panic("fakeAWGM: нет туннеля " + id)
}

// SetMatrix -- итог пробы через туннель: ok=false -- все цели не ответили.
func (f *fakeAWGM) SetMatrix(id string, ok bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.matrix[id] = ok
}

// ClearMatrix -- матрица ещё не пробовала этот туннель (сразу после старта).
func (f *fakeAWGM) ClearMatrix(id string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.matrix, id)
}

// SetMatrixMissing -- сборка без матрицы (awg-manager старше 2.18): 404.
func (f *fakeAWGM) SetMatrixMissing(missing bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.matrixMissing = missing
}

// SetPolicy заводит политику с цепочкой звеньев (имена NDMS) по приоритету.
func (f *fakeAWGM) SetPolicy(name string, links ...string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, p := range f.policies {
		if p.Name == name {
			p.Links = append([]string(nil), links...)
			return
		}
	}
	f.policies = append(f.policies, &fakePolicy{Name: name, Links: append([]string(nil), links...)})
}

// PolicyLinks -- текущий порядок звеньев политики.
func (f *fakeAWGM) PolicyLinks(name string) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, p := range f.policies {
		if p.Name == name {
			return append([]string(nil), p.Links...)
		}
	}
	return nil
}

// SetDown -- демон молчит: любой запрос рвётся без ответа.
func (f *fakeAWGM) SetDown(down bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.down = down
}

// OnRestart -- что происходит с туннелем при /api/control/restart.
func (f *fakeAWGM) OnRestart(fn func(id string)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.onRestart = fn
}

// Promotions -- все принятые «сделать главным» по порядку.
func (f *fakeAWGM) Promotions() []Promotion {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]Promotion(nil), f.promotions...)
}

// Calls -- журнал мутаций: "promote:<id>", "restart:<id>", "start:<id>", ...
func (f *fakeAWGM) Calls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

// Unknown -- запросы, которых подделка не знает: агент ждёт ручку, а контур
// её не показал. Непустой список -- повод дополнить подделку, а не молчать.
func (f *fakeAWGM) Unknown() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.unknown...)
}

func envelope(data any) map[string]any { return map[string]any{"success": true, "data": data} }

func (f *fakeAWGM) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	down := f.down
	f.mu.Unlock()
	if down {
		// Рвём соединение без ответа: так выглядит лежащий демон для клиента.
		if hj, ok := w.(http.Hijacker); ok {
			if c, _, err := hj.Hijack(); err == nil {
				_ = c.Close()
				return
			}
		}
		w.WriteHeader(http.StatusBadGateway)
		return
	}
	// Без этого заголовка живой демон отдаёт оболочку интерфейса, а не JSON.
	if r.Header.Get("X-Requested-With") != "XMLHttpRequest" {
		http.Error(w, "<html>", http.StatusOK)
		return
	}
	var (
		out    any
		status = http.StatusOK
	)
	key := r.Method + " " + r.URL.Path
	f.mu.Lock()
	switch key {
	case "GET /api/tunnels/all":
		out = envelope(map[string]any{"external": []any{}, "system": []any{}, "tunnels": f.tunnelsJSONLocked()})
	case "GET /api/pingcheck/status":
		out = envelope(map[string]any{"enabled": false, "tunnels": f.pingStatusLocked()})
	case "GET /api/pingcheck/logs":
		out = envelope([]any{})
	case "POST /api/pingcheck/check-now":
		out = map[string]any{"success": true}
	case "GET /api/settings/get":
		// Живая форма: весь незаявленный трафик идёт напрямую, обход -- правилами.
		out = envelope(map[string]any{"download": map[string]any{"routeTag": "direct", "routeKind": "direct"}})
	case "GET /api/dns-routes/list":
		out = envelope(f.dnsRoutesLocked())
	case "GET /api/static-routes/list":
		out = envelope([]any{})
	case "GET /api/monitoring/matrix":
		if f.matrixMissing {
			status, out = http.StatusNotFound, map[string]any{"success": false, "message": "not found"}
		} else {
			out = envelope(f.matrixLocked())
		}
	case "GET /api/routing/access-policies", "GET /api/access-policies":
		out = envelope(f.policiesLocked())
	case "GET /api/routing/policy-interfaces":
		out = envelope(f.policyIfacesLocked())
	case "GET /api/routing/tunnels":
		out = envelope(f.routingTunnelsLocked())
	case "GET /api/system/info":
		out = envelope(map[string]any{
			"version": "2.19.9", "activeBackend": "kernel", "firmwareVersion": "5.01.C.3.0-1", "keeneticOS": "5.0",
			"goArch": "arm64", "goOS": "linux", "isAarch64": true, "isOS5": true, "kernelModuleExists": true, "kernelModuleLoaded": true,
			"routerIP": "198.51.100.1", "singbox": map[string]any{"installed": false}, "supportsPingCheck": true, "totalMemoryMB": 1024,
		})
	case "GET /api/system/hydraroute-status":
		out = envelope(map[string]any{"installed": true, "running": true})
	case "GET /api/wan/status":
		out = envelope(map[string]any{"anyWANUp": true, "interfaces": map[string]any{
			"eth3": map[string]any{"up": true, "label": "Подключение Ethernet", "priority": 1000},
		}})
	case "GET /api/test/ip":
		// Через мёртвый туннель адрес выхода не узнать: живой демон отвечает
		// 400 IP_CHECK_FAILED (сверка С1).
		if ok, known := f.matrix[r.URL.Query().Get("id")]; known && !ok {
			status, out = http.StatusBadRequest, map[string]any{"success": false, "code": "IP_CHECK_FAILED", "message": "ip check failed"}
		} else {
			out = envelope(map[string]any{"directIp": "203.0.113.10", "vpnIp": "198.51.100.50", "endpointIp": "198.51.100.60", "ipChanged": true})
		}
	case "POST /api/access-policies/permit":
		status, out = f.permitLocked(r)
	case "POST /api/control/restart":
		id := r.URL.Query().Get("id")
		f.calls = append(f.calls, "restart:"+id)
		if fn := f.onRestart; fn != nil {
			f.mu.Unlock()
			fn(id)
			f.mu.Lock()
		}
		out = map[string]any{"success": true}
	case "POST /api/control/start", "POST /api/control/stop", "POST /api/control/toggle-enabled":
		f.calls = append(f.calls, strings.TrimPrefix(r.URL.Path, "/api/control/")+":"+r.URL.Query().Get("id"))
		out = map[string]any{"success": true}
	default:
		f.unknown = append(f.unknown, key)
		status, out = http.StatusNotFound, map[string]any{"success": false, "message": "not found"}
	}
	f.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(out)
}

func (f *fakeAWGM) tunnelByLinkLocked(link string) *fakeTunnel {
	for _, tu := range f.tunnels {
		if strings.EqualFold(tu.PolicyIface, link) {
			return tu
		}
	}
	return nil
}

// permitLocked -- как роутер: order 0 у звена в цепочке двигает его первым, а
// остальные сдвигаются сами. Отдельной ручки переупорядочивания у него нет.
func (f *fakeAWGM) permitLocked(r *http.Request) (int, any) {
	var body struct {
		Name      string `json:"name"`
		Interface string `json:"interface"`
		Order     int    `json:"order"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		return http.StatusBadRequest, map[string]any{"success": false, "message": "bad json"}
	}
	for _, p := range f.policies {
		if p.Name != body.Name {
			continue
		}
		next := []string{body.Interface}
		found := false
		for _, l := range p.Links {
			if l == body.Interface {
				found = true
				continue
			}
			next = append(next, l)
		}
		if !found {
			return http.StatusBadRequest, map[string]any{"success": false, "message": "interface not in policy"}
		}
		p.Links = next
		id := ""
		if tu := f.tunnelByLinkLocked(body.Interface); tu != nil {
			id = tu.ID
		}
		f.promotions = append(f.promotions, Promotion{Policy: body.Name, Iface: body.Interface, TunnelID: id})
		f.calls = append(f.calls, "promote:"+id)
		return http.StatusOK, map[string]any{"success": true}
	}
	return http.StatusNotFound, map[string]any{"success": false, "message": "no such policy"}
}

func (f *fakeAWGM) tunnelsJSONLocked() []map[string]any {
	out := make([]map[string]any, 0, len(f.tunnels))
	now := time.Now().UTC()
	for _, tu := range f.tunnels {
		hs := ""
		if tu.HandshakeAge >= 0 {
			hs = now.Add(-tu.HandshakeAge).Format(time.RFC3339)
		}
		row := map[string]any{
			"id": tu.ID, "name": tu.Name, "type": "awg", "status": tu.Status, "enabled": tu.Enabled,
			"defaultRoute": true, "resolvedIspInterface": "eth3", "endpoint": "198.51.100.70:443", "address": "10.11.0.7/32",
			"interfaceName": tu.Iface, "hasAddressConflict": false, "rxBytes": 1000, "txBytes": 1000, "lastHandshake": hs,
			"backend": tu.Backend, "backendType": tu.Backend, "awgVersion": "awg2", "mtu": 1280,
			"pingCheck": map[string]any{"status": tu.PingStatus, "restartCount": 0, "failCount": 0, "failThreshold": 3},
		}
		if tu.NDMS != "" {
			row["ndmsName"] = tu.NDMS
		}
		if tu.StatusDetails != "" {
			row["statusDetails"] = tu.StatusDetails
		}
		if tu.Status == "running" {
			row["startedAt"] = now.Add(-time.Hour).Format(time.RFC3339)
		}
		out = append(out, row)
	}
	return out
}

func (f *fakeAWGM) pingStatusLocked() []map[string]any {
	out := make([]map[string]any, 0, len(f.tunnels))
	for _, tu := range f.tunnels {
		out = append(out, map[string]any{
			"tunnelId": tu.ID, "tunnelName": tu.Name, "enabled": tu.Enabled, "backend": tu.Backend,
			"status": tu.PingStatus, "method": "icmp", "lastCheck": nil, "lastLatency": 0,
			"failCount": 0, "failThreshold": 3, "restartCount": 0, "tunnelRunning": tu.Status == "running",
		})
	}
	return out
}

// dnsRoutesLocked -- правила HydraRoute Neo на политике, как на живом роутере:
// routes пустые, привязка лежит на цепочке политики.
func (f *fakeAWGM) dnsRoutesLocked() []map[string]any {
	out := []map[string]any{}
	for _, p := range f.policies {
		for _, name := range []string{"sites", "video"} {
			out = append(out, map[string]any{
				"id": "hr:" + name, "name": name, "domains": []string{"geosite:" + name}, "manualDomains": []string{"geosite:" + name},
				"routes": nil, "enabled": true, "createdAt": "", "updatedAt": "", "backend": "hydraroute",
				"hrRouteMode": "policy", "hrPolicyName": p.Name,
			})
		}
	}
	return out
}

func (f *fakeAWGM) matrixLocked() map[string]any {
	ts := time.Now().UTC().Format(time.RFC3339)
	targets := []string{"cc", "dns"}
	cells := []map[string]any{}
	tunnels := []map[string]any{}
	for _, tu := range f.tunnels {
		tunnels = append(tunnels, map[string]any{"id": tu.ID, "name": tu.Name, "ifaceName": tu.Iface})
		ok, known := f.matrix[tu.ID]
		if !known {
			continue
		}
		for _, target := range targets {
			latency := 0
			if ok {
				latency = 42
			}
			cells = append(cells, map[string]any{"targetId": target, "tunnelId": tu.ID, "latencyMs": latency, "ok": ok, "isSelf": false, "ts": ts})
		}
	}
	return map[string]any{
		"targets": []map[string]any{
			{"id": "cc", "host": "cc.example.com", "name": "cc"},
			{"id": "dns", "host": "dns.example.com", "name": "dns"},
		},
		"tunnels": tunnels, "cells": cells, "updatedAt": ts,
	}
}

func (f *fakeAWGM) labelLocked(link string) string {
	if tu := f.tunnelByLinkLocked(link); tu != nil {
		return tu.Name
	}
	return link
}

func (f *fakeAWGM) policiesLocked() []map[string]any {
	out := make([]map[string]any, 0, len(f.policies))
	for _, p := range f.policies {
		links := make([]map[string]any, 0, len(p.Links))
		for i, l := range p.Links {
			links = append(links, map[string]any{"name": l, "label": f.labelLocked(l), "order": i})
		}
		out = append(out, map[string]any{"name": p.Name, "description": "", "standalone": false, "interfaces": links, "deviceCount": 0, "isStandard": false})
	}
	return out
}

// policyIfacesLocked: up -- интерфейс поднят. Провал пробы его не роняет: без
// ping-check роутер продолжает считать туннель живым, и политика ведёт в него.
func (f *fakeAWGM) policyIfacesLocked() []map[string]any {
	out := []map[string]any{{"name": "GigabitEthernet1", "label": "Подключение Ethernet", "up": true}}
	for _, tu := range f.tunnels {
		out = append(out, map[string]any{"name": tu.PolicyIface, "label": tu.Name, "up": tu.Enabled && tu.Status == "running"})
	}
	return out
}

func (f *fakeAWGM) routingTunnelsLocked() []map[string]any {
	out := []map[string]any{{"id": "wan:eth3", "name": "Подключение Ethernet", "iface": "eth3", "type": "wan", "status": "up", "available": true}}
	for _, tu := range f.tunnels {
		out = append(out, map[string]any{"id": tu.ID, "name": tu.Name, "iface": tu.Iface, "type": "managed", "status": tu.Status, "available": true})
	}
	return out
}
