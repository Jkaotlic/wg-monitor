package awg3panel

import "log/slog"

// hiddenValue -- чем секрет печатается в журнал и строку (как
// miniappHiddenValue бэкенда).
const hiddenValue = "[скрыто]"

// Формы ответов awg3-panel@b5ee1c7. Лишние поля панели (interface_edit,
// overrides, public_key сервера) боту не нужны и не разбираются.
type Iface struct {
	ID        string `json:"id"`
	Title     string `json:"title"`
	Interface string `json:"interface"`
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

type Summary struct {
	Interface         string `json:"interface"`
	ListenPort        int    `json:"listen_port"`
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

// Issued -- ответ POST …/peers. Config -- приватный ключ клиента: печать
// структуры даёт «[скрыто]».
type Issued struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Address     string `json:"address"`
	Config      string `json:"config"`
	QRPNGBase64 string `json:"qr_png_base64"`
}

func (Issued) String() string       { return hiddenValue }
func (Issued) GoString() string     { return hiddenValue }
func (Issued) LogValue() slog.Value { return slog.StringValue(hiddenValue) }
