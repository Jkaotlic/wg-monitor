package backend

import (
	"encoding/json"
	"strings"
)

// miniappCheckFacts — измеримое, что проверка узнала о роутере.
//
// Это БЕЛЫЙ СПИСОК, а не проброс events.details_json наружу, ровно по той же
// причине, что и miniappTunnel: в том же details агент держит топологию
// (base_url, router_ip, via_interface, поимённые списки целей), которая
// уместна на админском дашборде и не должна доезжать до оператора мини-аппа.
// Сюда попадают только числа и версии — то, что экран диагностики печатает
// значением строки данных.
//
// Указатели там, где значимо ОТСУТСТВИЕ: агент постарше или неразобранный
// details обязан отрисоваться как «неизвестно», а не как ноль. Ноль про
// проваленные резолверы и «мы не знаем, сколько их» — разные ответы.
type miniappCheckFacts struct {
	// dns
	Resolvers       *int `json:"resolvers,omitempty"`
	ResolversFailed *int `json:"resolvers_failed,omitempty"`
	RKNProbed       *int `json:"rkn_probed,omitempty"`
	RKNSuspect      *int `json:"rkn_suspect,omitempty"`
	// hydraroute
	RoutesHRNeo         *int   `json:"routes_hr_neo,omitempty"`
	RoutesNDMS          *int   `json:"routes_ndms,omitempty"`
	RoutesStatic        *int   `json:"routes_static,omitempty"`
	ActiveBackend       string `json:"active_backend,omitempty"`
	SingboxRouterActive bool   `json:"singbox_router_active,omitempty"`
	// awg_manager
	Version  string `json:"version,omitempty"`
	Firmware string `json:"firmware,omitempty"`
	// external_reach
	TargetsTotal    *int `json:"targets_total,omitempty"`
	TargetsFailed   *int `json:"targets_failed,omitempty"`
	TargetsDegraded *int `json:"targets_degraded,omitempty"`
}

// checkDetails — форма details_json, из которой белый список и собирается.
// Ключи здесь и в internal/agent/checks обязаны совпадать; расходятся они
// молча, поэтому тест на каждую группу полей сидит рядом.
type checkDetails struct {
	// dns (checks/dns.go)
	Endpoints   *int `json:"endpoints"`
	FailedCount *int `json:"failed_count"`
	RKNProbed   *int `json:"rkn_probed"`
	RKNSuspect  *int `json:"rkn_suspect"`
	// hydraroute (checks/hydraroute.go)
	RoutesHRNeo         *int   `json:"routes_hrneo"`
	RoutesNDMS          *int   `json:"routes_ndms"`
	RoutesStatic        *int   `json:"routes_static"`
	ActiveBackend       string `json:"active_backend"`
	SingboxRouterActive bool   `json:"singbox_router_active"`
	// awg_manager (checks/awgmgr_check.go)
	Version  string `json:"version"`
	Firmware string `json:"firmware"`
	// external_reach (checks/external_reach.go): провалы и деградации приезжают
	// СПИСКАМИ объектов с именами и ошибками целей. Наружу идёт их количество:
	// «1 из 3» — это ответ, а имена целей — то, чего мини-аппу знать незачем.
	TargetsTotal    *int              `json:"targets_total"`
	TargetsFailed   []json.RawMessage `json:"targets_failed"`
	TargetsDegraded []json.RawMessage `json:"targets_degraded"`
}

// miniappCheckFactsFrom строит проекцию по имени проверки. Имя обязательно:
// одни и те же ключи у разных проверок значат разное (routes_static есть и у
// hydraroute, и у туннеля), и разбирать details «вообще» значило бы смешать
// их в одну кучу.
//
// Проверки туннелей своей проекции здесь не получают: у них уже есть
// miniappTunnel, и второй набор тех же чисел рядом развёл бы два источника
// правды об одном.
func miniappCheckFactsFrom(checkName, detailsJSON string) *miniappCheckFacts {
	if strings.TrimSpace(detailsJSON) == "" || strings.HasPrefix(checkName, miniappTunnelPrefix) {
		return nil
	}
	var d checkDetails
	if err := json.Unmarshal([]byte(detailsJSON), &d); err != nil {
		return nil
	}
	switch checkName {
	case "dns":
		if d.Endpoints == nil && d.FailedCount == nil && d.RKNProbed == nil {
			return nil
		}
		return &miniappCheckFacts{
			Resolvers:       d.Endpoints,
			ResolversFailed: d.FailedCount,
			RKNProbed:       d.RKNProbed,
			RKNSuspect:      d.RKNSuspect,
		}
	case "hydraroute":
		if d.RoutesHRNeo == nil && d.RoutesNDMS == nil && d.RoutesStatic == nil && d.ActiveBackend == "" {
			return nil
		}
		return &miniappCheckFacts{
			RoutesHRNeo:         d.RoutesHRNeo,
			RoutesNDMS:          d.RoutesNDMS,
			RoutesStatic:        d.RoutesStatic,
			ActiveBackend:       d.ActiveBackend,
			SingboxRouterActive: d.SingboxRouterActive,
		}
	case "awg_manager":
		if d.Version == "" && d.Firmware == "" {
			return nil
		}
		return &miniappCheckFacts{Version: d.Version, Firmware: d.Firmware}
	case "external_reach":
		if d.TargetsTotal == nil {
			return nil
		}
		failed := len(d.TargetsFailed)
		degraded := len(d.TargetsDegraded)
		facts := &miniappCheckFacts{TargetsTotal: d.TargetsTotal, TargetsFailed: &failed}
		if degraded > 0 {
			facts.TargetsDegraded = &degraded
		}
		return facts
	default:
		return nil
	}
}
