// Package awgmstate -- общий словарь состояний панели роутера и правила,
// по которым текст ошибки превращается в состояние. Раньше жил только в CLI
// (cmd/deploy/awgm_deferred.go); теперь его же зовёт опрос панели в бэкенде
// (internal/backend/revive), и два места не могут разойтись в правилах.
package awgmstate

import "strings"

const (
	Pending   = "pending"
	Offline   = "offline"
	Reachable = "reachable"
	AuthError = "auth_error"
	TLSError  = "tls_error"
	DNSError  = "dns_error"
)

// Classify относит текст ошибки к состоянию панели. Порядок проверок важен:
// TLS раньше таймаута («tls handshake timeout» -- это сертификат/рукопожатие,
// а не спящий роутер), DNS раньше «offline».
func Classify(reason string) string {
	s := strings.ToLower(strings.TrimSpace(reason))
	switch {
	case s == "":
		return Pending
	case strings.Contains(s, "certificate is valid for") ||
		strings.Contains(s, "x509:") ||
		strings.Contains(s, "hostname") && strings.Contains(s, "certificate") ||
		strings.Contains(s, "tls"):
		return TLSError
	case strings.Contains(s, "name or service not known") ||
		strings.Contains(s, "no such host") ||
		strings.Contains(s, "nxdomain") ||
		strings.Contains(s, "temporary failure in name resolution"):
		return DNSError
	case strings.Contains(s, "http 401") ||
		strings.Contains(s, "unauthorized") ||
		strings.Contains(s, "http 403") ||
		strings.Contains(s, "forbidden"):
		return AuthError
	case strings.Contains(s, "http 502") ||
		strings.Contains(s, "http 503") ||
		strings.Contains(s, "http 504") ||
		strings.Contains(s, "service unavailable") ||
		strings.Contains(s, "gateway timeout") ||
		strings.Contains(s, "timeout") ||
		strings.Contains(s, "connection refused") ||
		strings.Contains(s, "connection reset"):
		return Offline
	default:
		return Pending
	}
}
