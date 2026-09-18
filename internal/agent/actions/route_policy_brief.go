package actions

import (
	"context"
	"fmt"

	"golang.org/x/sync/errgroup"

	"github.com/Jkaotlic/wg-monitor/internal/agent/awgmgr"
	"github.com/Jkaotlic/wg-monitor/pkg/wire"
)

// PolicyBriefs -- сводка политик доступа для ежеминутной проверки hydraroute:
// кто несёт каждую политику сейчас и что лежит в запасе. Роли звеньев и
// счётчики правил считает ТОТ ЖЕ buildRouteSnapshot, что у route_status: экран
// и команда не могут расходиться в ответе «через какой туннель идёт обход».
//
// Проверка живёт в пакете checks, который actions уже импортирует, поэтому
// функция доходит до неё через поле HydraRouteCheck.Policies (cmd/agent), а не
// импортом. dns -- список правил, который проверка уже прочитала: второй раз
// за ним не ходим. Дополнительно -- три параллельных чтения: туннели, политики
// и интерфейсы политик.
//
// (nil, nil) -- сборка без политик (404) или политик нет: сводки нет, бэкенд
// отвечает по-старому. Любой другой отказ -- ошибка: без ролей сводка читалась
// бы как «ни одного живого звена».
func PolicyBriefs(ctx context.Context, c *awgmgr.Client, dns []awgmgr.DNSRoute) ([]wire.PolicyBrief, error) {
	var (
		tunnels      *awgmgr.TunnelsAll
		tunnelsErr   error
		policies     []awgmgr.AccessPolicy
		policiesErr  error
		polIfaces    []awgmgr.PolicyInterface
		polIfacesErr error
	)
	var g errgroup.Group
	g.Go(func() error { tunnels, tunnelsErr = c.TunnelsAll(ctx); return nil })
	g.Go(func() error { policies, policiesErr = c.AccessPolicies(ctx); return nil })
	g.Go(func() error { polIfaces, polIfacesErr = c.PolicyInterfaces(ctx); return nil })
	_ = g.Wait()

	if policiesErr != nil {
		if awgmgr.IsEndpointMissing(policiesErr) {
			return nil, nil
		}
		return nil, fmt.Errorf("access-policies: %w", policiesErr)
	}
	if len(policies) == 0 {
		return nil, nil
	}
	// Политики прочитались -- сборка новая, и 404 интерфейсов здесь уже не
	// «старая модель», а недостающие данные (то же правило, что в RouteStatus).
	if polIfacesErr != nil {
		return nil, fmt.Errorf("policy-interfaces: %w", polIfacesErr)
	}
	if tunnelsErr != nil {
		return nil, fmt.Errorf("tunnels: %w", tunnelsErr)
	}
	// Главный выход и каталог NDMS на роли звеньев и счётчики политик не
	// влияют: правило политики находится по имени раньше, чем до них доходит
	// ruleEgress, а резолвер берёт только наши (managed) туннели.
	snap := buildRouteSnapshot(nil, tunnels, nil, dns, nil, "", policies, polIfaces, false)
	// Первые len(policies) строк -- сами политики в порядке роутера; хвост,
	// если он есть, дописан легаси-веткой hrPolicyInterfaces и цепочки не несёт.
	n := len(policies)
	if n > len(snap.Policies) {
		n = len(snap.Policies)
	}
	out := make([]wire.PolicyBrief, 0, n)
	for _, p := range snap.Policies[:n] {
		b := wire.PolicyBrief{
			Name:           p.Name,
			ActiveTunnelID: p.ActiveTunnelID,
			ViaVPN:         p.ViaVPN,
			DNS:            p.DNS,
			HRNeo:          p.HRNeo,
			Links:          make([]wire.PolicyBriefLink, 0, len(p.Interfaces)),
		}
		for _, it := range p.Interfaces {
			b.Links = append(b.Links, wire.PolicyBriefLink{TunnelID: it.TunnelID, Role: it.Role})
		}
		out = append(out, b)
	}
	return out, nil
}
