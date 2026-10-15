package dnswatch

import (
	"context"
	"crypto/tls"
	"errors"
	"testing"
	"time"
)

// AG-09: проба эталона перед «починить DNS» -- 5 с на всё. Если провайдер
// молча режет UDP/53 к чужим серверам, plain-lookup имени Яндекса съедал
// весь бюджет, и системный резолвер получал уже истёкший ctx: живой Яндекс
// записывался мёртвым. У plain-lookup свой срок -- запасной путь успевает.
func TestProbeLine_PlainLookupHangs_SystemFallbackFitsBudget(t *testing.T) {
	p := newLineProber()
	var systemCalled bool
	p.lookupFn = func(ctx context.Context, host string) (string, error) {
		<-ctx.Done() // UDP/53 молча отброшен
		return "", ctx.Err()
	}
	p.systemLookupFn = func(ctx context.Context, host string) (string, error) {
		systemCalled = true
		if err := ctx.Err(); err != nil {
			return "", err
		}
		return "203.0.113.5", nil
	}
	p.dotFn = func(ctx context.Context, addr, sni, domain string, cfg *tls.Config, timeout time.Duration) ([]string, error) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if addr != "203.0.113.5:853" {
			return nil, errors.New("wrong addr " + addr)
		}
		return []string{"203.0.113.7"}, nil
	}

	const budget = 5 * time.Second // dnsProbeTimeout сброса DNS
	ctx, cancel := context.WithTimeout(context.Background(), budget)
	defer cancel()
	start := time.Now()
	err := p.ProbeCandidate(ctx, "tls upstream common.dot.dns.yandex.net", "ya.ru")
	if err != nil {
		t.Fatalf("probe failed within the shared budget: %v (system lookup called: %v)", err, systemCalled)
	}
	if !systemCalled {
		t.Fatal("system resolver fallback not used")
	}
	if el := time.Since(start); el >= budget {
		t.Fatalf("probe took %v, budget %v", el, budget)
	}
}
