// Package awgmgr is a typed REST client for the local awg-manager daemon
// (hoaxisr/awg-manager 2.8+) listening on 127.0.0.1:2222 on Keenetic routers.
// All endpoints require the X-Requested-With: XMLHttpRequest header — without
// it the daemon serves the SvelteKit SPA shell instead of JSON.
package awgmgr

import "strings"

type Envelope[T any] struct {
	Success bool `json:"success"`
	Data    T    `json:"data"`
}

// Tunnel mirrors one entry of /api/tunnels/all .data.tunnels[].
type Tunnel struct {
	ID                   string         `json:"id"`
	Name                 string         `json:"name"`
	Type                 string         `json:"type"`
	Status               string         `json:"status"`
	State                string         `json:"state"`
	Enabled              bool           `json:"enabled"`
	DefaultRoute         bool           `json:"defaultRoute"`
	ResolvedISPInterface string         `json:"resolvedIspInterface"`
	Endpoint             string         `json:"endpoint"`
	Address              string         `json:"address"`
	InterfaceName        string         `json:"interfaceName"`
	NDMSName             string         `json:"ndmsName"`
	HasAddressConflict   bool           `json:"hasAddressConflict"`
	RxBytes              int64          `json:"rxBytes"`
	TxBytes              int64          `json:"txBytes"`
	LastHandshake        nullableTime   `json:"lastHandshake"`
	Backend              string         `json:"backend"`
	BackendType          string         `json:"backendType"`
	AWGVersion           string         `json:"awgVersion"`
	MTU                  int            `json:"mtu"`
	StartedAt            nullableTime   `json:"startedAt"`
	PingCheck            PingCheckBrief `json:"pingCheck"`
}

type PingCheckBrief struct {
	Status        string `json:"status"`
	RestartCount  int    `json:"restartCount"`
	FailCount     int    `json:"failCount"`
	FailThreshold int    `json:"failThreshold"`
}

type TunnelsAll struct {
	External []Tunnel `json:"external"`
	System   []Tunnel `json:"system"`
	Tunnels  []Tunnel `json:"tunnels"`
}

// PingCheckTunnel mirrors /api/pingcheck/status .data.tunnels[].
type PingCheckTunnel struct {
	TunnelID    string       `json:"tunnelId"`
	TunnelName  string       `json:"tunnelName"`
	Enabled     bool         `json:"enabled"`
	Backend     string       `json:"backend"`
	Status      string       `json:"status"`
	Method      string       `json:"method"`
	LastCheck   nullableTime `json:"lastCheck"`
	LastLatency int          `json:"lastLatency"`
	FailCount   int          `json:"failCount"`
	// SuccessCount is absent from awg-manager 2.16+ responses. A pointer keeps
	// "the router did not report it" distinct from "the router reported zero" --
	// conflating the two is what printed a permanent ✓0 to the operator.
	SuccessCount  *int64 `json:"successCount"`
	FailThreshold int    `json:"failThreshold"`
	RestartCount  int    `json:"restartCount"`
	TunnelRunning bool   `json:"tunnelRunning"`
	// NDMSName is the Keenetic interface name (e.g. "Wireguard0"),
	// resolved by the agent from /api/tunnels/all. Empty if the
	// tunnel id can't be matched (rare — would mean awg-mgr is
	// returning pingcheck status for a tunnel it doesn't know about).
	NDMSName string `json:"ndmsName"`
}

type PingCheckStatus struct {
	Enabled bool              `json:"enabled"`
	Tunnels []PingCheckTunnel `json:"tunnels"`
}

// SystemInfo mirrors /api/system/info .data.
type SystemInfo struct {
	ActiveBackend       string  `json:"activeBackend"`
	FirmwareVersion     string  `json:"firmwareVersion"`
	GoArch              string  `json:"goArch"`
	GoOS                string  `json:"goOS"`
	IsAarch64           bool    `json:"isAarch64"`
	IsLowMemory         bool    `json:"isLowMemory"`
	IsOS5               bool    `json:"isOS5"`
	KeeneticOS          string  `json:"keeneticOS"`
	KernelModuleExists  bool    `json:"kernelModuleExists"`
	KernelModuleLoaded  bool    `json:"kernelModuleLoaded"`
	KernelModuleModel   string  `json:"kernelModuleModel"`
	KernelModuleVersion string  `json:"kernelModuleVersion"`
	RouterIP            string  `json:"routerIP"`
	Singbox             Singbox `json:"singbox"`
	SupportsPingCheck   bool    `json:"supportsPingCheck"`
	TotalMemoryMB       int     `json:"totalMemoryMB"`
	Version             string  `json:"version"`
}

type Singbox struct {
	Installed bool   `json:"installed"`
	Version   string `json:"version"`
}

type HydraRouteStatus struct {
	Installed bool `json:"installed"`
	Running   bool `json:"running"`
}

// Settings mirrors the subset of /api/settings/get .data we need. awg-manager
// records the active default-route tunnel in download.routeTag (form
// "<routeKind>-<tunnelID>", e.g. "awg-awg12"). When several tunnels each carry
// defaultRoute=true in /api/tunnels/all, this is the authoritative signal for
// which one HR-Neo policy traffic actually egresses through.
type Settings struct {
	Download      SettingsDownload      `json:"download"`
	SingboxRouter SettingsSingboxRouter `json:"singboxRouter"`
}

type SettingsDownload struct {
	RouteTag  string `json:"routeTag"`
	RouteKind string `json:"routeKind"`
}

// SettingsSingboxRouter mirrors settings.singboxRouter from /api/settings/get.
// When Enabled, the sing-box router is the active routing method (it routes per
// its own policy / deviceMode using the AWG tunnels as outbounds), so the NDMS
// default-route / HR-Neo signals do not reflect the real per-destination path.
type SettingsSingboxRouter struct {
	Enabled    bool   `json:"enabled"`
	DeviceMode string `json:"deviceMode"`
	PolicyName string `json:"policyName"`
}

// SingboxRouterActive reports whether sing-box is the active routing method.
// Agents must not raise AWG-default-route / external-reach routing alarms when
// this is true — the router-side default path does not reflect sing-box's
// per-destination routing of the actual client traffic.
func (s *Settings) SingboxRouterActive() bool {
	return s != nil && s.SingboxRouter.Enabled
}

// ActiveDefaultTunnelID extracts the authoritative active default-route tunnel
// id from download.routeTag (form "<routeKind>-<tunnelID>", e.g. "awg-awg12");
// the kind prefix is stripped to recover the id used in /api/tunnels/all.
// Returns "" when settings are unavailable or routeTag is empty, so callers
// fall back to the legacy first-defaultRoute heuristic. When several tunnels
// each carry defaultRoute=true, this is the only reliable signal for which one
// awg-manager actually routes default / HR-Neo-fall-through traffic through.
func (s *Settings) ActiveDefaultTunnelID() string {
	if s == nil {
		return ""
	}
	tag := strings.TrimSpace(s.Download.RouteTag)
	if tag == "" {
		return ""
	}
	if kind := strings.TrimSpace(s.Download.RouteKind); kind != "" {
		tag = strings.TrimPrefix(tag, kind+"-")
	}
	return strings.TrimSpace(tag)
}

// CreateTunnelRequest is the body for POST /api/tunnels/create.
type CreateTunnelRequest struct {
	Name         string          `json:"name"`
	Type         string          `json:"type"`
	Interface    InterfaceConfig `json:"interface"`
	Peer         PeerConfig      `json:"peer"`
	DefaultRoute bool            `json:"defaultRoute"`
	Enabled      bool            `json:"enabled"`
}

type InterfaceConfig struct {
	PrivateKey string `json:"privateKey"`
	Jc         int    `json:"jc"`
	Jmin       int    `json:"jmin"`
	Jmax       int    `json:"jmax"`
	S1         int    `json:"s1"`
	S2         int    `json:"s2"`
	S3         int    `json:"s3,omitempty"`
	S4         int    `json:"s4,omitempty"`
	H1         string `json:"h1"`
	H2         string `json:"h2"`
	H3         string `json:"h3"`
	H4         string `json:"h4"`
}

type PeerConfig struct {
	PublicKey    string   `json:"publicKey"`
	PresharedKey string   `json:"presharedKey,omitempty"`
	Endpoint     string   `json:"endpoint"`
	AllowedIPs   []string `json:"allowedIPs"`
}

// TunnelTraffic -- ряд обмена по туннелю из /api/tunnels/traffic.
//
// Формы сверены с живым роутером и его же OpenAPI (awg-manager 2.17.2+r21,
// 20.08.2026), и обе прежние догадки оказались неверны:
//
//   - t -- unix-секунды ЧИСЛОМ, а не строкой RFC3339. Строка здесь роняла бы
//     разбор ответа целиком, и заметить это можно было только на роутере.
//   - rx/tx в точке -- это СКОРОСТИ (байт/сек, дробные), а не счётчики.
//     Складывать их бессмысленно: сумма скоростей -- не объём.
//
// Объём за период роутер считает сам и кладёт в stats (volumeRx/volumeTx --
// «Σ rxRate×Δt на сырых отсчётах»). Оттуда его и надо брать: своя формула по
// точкам разошлась бы с показаниями самого роутера.
type TunnelTraffic struct {
	Points []TrafficPoint `json:"points"`
	Stats  TrafficStats   `json:"stats"`
}

type TrafficPoint struct {
	T  int64   `json:"t"`
	RX float64 `json:"rx"`
	TX float64 `json:"tx"`
}

type TrafficStats struct {
	Points    int     `json:"points"`
	PeakRate  float64 `json:"peakRate"`
	AvgRx     float64 `json:"avgRx"`
	AvgTx     float64 `json:"avgTx"`
	CurrentRx float64 `json:"currentRx"`
	CurrentTx float64 `json:"currentTx"`
	VolumeRx  int64   `json:"volumeRx"`
	VolumeTx  int64   `json:"volumeTx"`
}
