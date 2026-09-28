package backend

import (
	"encoding/json"

	"github.com/Jkaotlic/wg-monitor/internal/backend/db"
	"github.com/Jkaotlic/wg-monitor/internal/backend/timeline"
	"github.com/Jkaotlic/wg-monitor/pkg/wire"
)

// miniappAwgmCoverage читает блок ping_log роутера. Нет блока, он не «ok»
// или битый -- покрытия нет, и «связь не терял» экран не скажет.
func miniappAwgmCoverage(d Deps, routerID int64) timeline.AwgmCoverage {
	all, err := d.DB.RouterFacts().All(routerID)
	if err != nil {
		return timeline.AwgmCoverage{}
	}
	f, ok := all[db.FactPingLog]
	if !ok {
		return timeline.AwgmCoverage{}
	}
	var pl wire.PingLogFacts
	if json.Unmarshal(f.Body, &pl) != nil || pl.State != "ok" || pl.At.IsZero() {
		return timeline.AwgmCoverage{}
	}
	// P2: верхняя граница -- когда бэкенд ПОЛУЧИЛ этот блок, а не «сейчас».
	// Агент, переставший слать ping_log (откат на версию без него), застывает
	// на старом ReceivedAt, и происшествия после него clean не получают.
	cov := timeline.AwgmCoverage{Since: pl.At, Until: f.ReceivedAt, Tunnels: map[string]bool{}}
	for _, id := range pl.Tunnels {
		cov.Tunnels[id] = true
	}
	return cov
}
