// Package awgmgr is a typed REST client for the local awg-manager daemon
// (hoaxisr/awg-manager 2.8+) listening on 127.0.0.1:2222 on Keenetic routers.
// All endpoints require the X-Requested-With: XMLHttpRequest header — without
// it the daemon serves the SvelteKit SPA shell instead of JSON.
package awgmgr

import "time"

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
	LastHandshake        *time.Time     `json:"lastHandshake"`
	Backend              string         `json:"backend"`
	BackendType          string         `json:"backendType"`
	AWGVersion           string         `json:"awgVersion"`
	MTU                  int            `json:"mtu"`
	StartedAt            *time.Time     `json:"startedAt"`
	PingCheck            PingCheckBrief `json:"pingCheck"`
}

type PingCheckBrief struct {
	Status         string `json:"status"`
	RestartCount   int    `json:"restartCount"`
	FailCount      int    `json:"failCount"`
	FailThreshold  int    `json:"failThreshold"`
}

type TunnelsAll struct {
	External []Tunnel `json:"external"`
	System   []Tunnel `json:"system"`
	Tunnels  []Tunnel `json:"tunnels"`
}

// PingCheckTunnel mirrors /api/pingcheck/status .data.tunnels[].
type PingCheckTunnel struct {
	TunnelID       string     `json:"tunnelId"`
	TunnelName     string     `json:"tunnelName"`
	Enabled        bool       `json:"enabled"`
	Backend        string     `json:"backend"`
	Status         string     `json:"status"`
	Method         string     `json:"method"`
	LastCheck      *time.Time `json:"lastCheck"`
	LastLatency    int        `json:"lastLatency"`
	FailCount      int        `json:"failCount"`
	SuccessCount   int64      `json:"successCount"`
	FailThreshold  int        `json:"failThreshold"`
	RestartCount   int        `json:"restartCount"`
	TunnelRunning  bool       `json:"tunnelRunning"`
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
