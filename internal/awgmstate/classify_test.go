package awgmstate

import "testing"

// Таблица перенесена из cmd/deploy/awgm_deferred_test.go без изменений и
// дополнена случаями, на которые опирается опрос панели бэкендом.
func TestClassify(t *testing.T) {
	tests := []struct {
		name string
		msg  string
		want string
	}{
		{name: "dns", msg: "urlopen error [Errno -2] Name or service not known", want: DNSError},
		{name: "dns-go", msg: "dial tcp: lookup awg.example.com: no such host", want: DNSError},
		{name: "tls", msg: "x509: certificate is valid for *.ddns.example.com, not awg.example.com", want: TLSError},
		{name: "tls-go", msg: "tls: failed to verify certificate: x509: certificate signed by unknown authority", want: TLSError},
		{name: "auth", msg: "awgm POST /api/auth/login: HTTP 401: unauthorized", want: AuthError},
		{name: "forbidden", msg: "HTTP 403: forbidden", want: AuthError},
		{name: "offline-503", msg: "awgm GET /api/system/info: HTTP 503: service unavailable", want: Offline},
		{name: "offline-502", msg: "HTTP 502", want: Offline},
		{name: "offline-504", msg: "HTTP 504", want: Offline},
		{name: "refused", msg: "dial tcp 203.0.113.7:443: connect: connection refused", want: Offline},
		{name: "reset", msg: "read: connection reset by peer", want: Offline},
		{name: "timeout", msg: "context deadline exceeded (Client.Timeout exceeded while awaiting headers)", want: Offline},
		{name: "empty", msg: "   ", want: Pending},
		{name: "unknown", msg: "unexpected relay failure", want: Pending},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Classify(tt.msg); got != tt.want {
				t.Fatalf("Classify(%q) = %q, want %q", tt.msg, got, tt.want)
			}
		})
	}
}

// CHK-08: любое «tls» в тексте (имя хоста, путь) давало tls_error --
// спящий роутер или отказ входа назывались «проблемой сертификата».
func TestClassifyTLSOnlyForRealTLSErrors(t *testing.T) {
	for _, tc := range []struct{ msg, want string }{
		{"dial tcp: lookup tlsrouter.example.com: no such host", DNSError},
		{"awgm POST https://mytls.example.com/api/auth/login: HTTP 401: unauthorized", AuthError},
		{"Get \"https://tls-gw.example.com/api/system/info\": dial tcp 203.0.113.7:443: connect: connection refused", Offline},
		{"Get \"https://router.example.com\": net/http: TLS handshake timeout", TLSError},
		{"remote error: tls: bad certificate", TLSError},
		{"http: server gave HTTP response to HTTPS client; first record does not look like a TLS handshake", TLSError},
	} {
		if got := Classify(tc.msg); got != tc.want {
			t.Errorf("Classify(%q) = %q, want %q", tc.msg, got, tc.want)
		}
	}
}
