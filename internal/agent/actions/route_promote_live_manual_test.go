//go:build manual

package actions

import (
	"context"
	"os"
	"testing"

	"github.com/Jkaotlic/wg-monitor/internal/agent/awgmgr"
	"github.com/Jkaotlic/wg-monitor/pkg/wire"
)

// Живая проверка «сделать главным» с откатом. Мутация обратимая: туннель
// возвращается на прежнее место тем же действием, состояние сверяется до и
// после.
//
//	AWGM_URL=http://192.168.0.1:2222 PROMOTE_POLICY=RU PROMOTE_TO=awg10 PROMOTE_BACK=... \
//	  go test -tags manual ./internal/agent/actions/ -run LivePolicyPromote -v
func TestLivePolicyPromote(t *testing.T) {
	base := os.Getenv("AWGM_URL")
	policy := os.Getenv("PROMOTE_POLICY")
	to := os.Getenv("PROMOTE_TO")
	back := os.Getenv("PROMOTE_BACK")
	if base == "" || policy == "" || to == "" || back == "" {
		t.Skip("нужны AWGM_URL, PROMOTE_POLICY, PROMOTE_TO, PROMOTE_BACK")
	}
	c := awgmgr.New(base)
	ctx := context.Background()

	before, err := c.AccessPoliciesFresh(ctx)
	if err != nil {
		t.Fatalf("состояние до: %v", err)
	}
	t.Logf("ДО: %s", chainOf(before, policy))

	out, err := RoutePolicyPromoteJSON(ctx, c, wire.RoutePolicyPromoteRequest{PolicyName: policy, TunnelID: to})
	if err != nil {
		t.Fatalf("promote %s: %v", to, err)
	}
	t.Logf("ПОСЛЕ: %s", out)

	if _, err := RoutePolicyPromoteJSON(ctx, c, wire.RoutePolicyPromoteRequest{PolicyName: policy, TunnelID: back}); err != nil {
		t.Fatalf("ОТКАТ НЕ УДАЛСЯ (%s обратно): %v", back, err)
	}
	after, err := c.AccessPoliciesFresh(ctx)
	if err != nil {
		t.Fatalf("состояние после отката: %v", err)
	}
	t.Logf("ПОСЛЕ ОТКАТА: %s", chainOf(after, policy))
	if chainOf(before, policy) != chainOf(after, policy) {
		t.Fatalf("состояние не восстановлено:\n до: %s\nпосле: %s", chainOf(before, policy), chainOf(after, policy))
	}
}

func chainOf(policies []awgmgr.AccessPolicy, name string) string {
	for _, p := range policies {
		if p.Name != name {
			continue
		}
		s := p.Name + ": "
		for i, iface := range p.Interfaces {
			if i > 0 {
				s += " -> "
			}
			s += iface.Name
		}
		return s
	}
	return "(политика не найдена)"
}
