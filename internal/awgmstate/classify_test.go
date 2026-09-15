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
