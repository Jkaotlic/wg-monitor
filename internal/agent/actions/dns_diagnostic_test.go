package actions

import (
	"context"
	"github.com/Jkaotlic/wg-monitor/pkg/wire"
	"strings"
	"testing"
)

func TestDNSDiagnosticUsesProductionCheckAndRedactsURLs(t *testing.T) {
	called := false
	r := Runner{DNSCheck: func(ctx context.Context) wire.Check {
		called = true
		return wire.Check{Name: "dns", Status: "ok", Details: map[string]any{"endpoints_detail": []any{map[string]any{"target": "HTTPS://user:secret@users.noreply.github.com/private-token/dns-query?key=secret", "rkn": map[string]any{"example.com": map[string]any{"err": "Get \"https://dns.example.com/private-token?key=secret\": timeout"}}}}}}
	}}
	got := r.Execute(context.Background(), wire.Command{ID: "dns-diag", Action: "dns_probe"})
	if !called || got.Status != "ok" || !strings.Contains(got.Output, "timeout") {
		t.Fatalf("diagnostic: %+v", got)
	}
	for _, secret := range []string{"private-token", "secret", "user:"} {
		if strings.Contains(got.Output, secret) {
			t.Fatal("secret leaked")
		}
	}
}
