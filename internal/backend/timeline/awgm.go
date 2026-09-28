package timeline

import (
	"sort"
	"strings"
	"time"

	"github.com/Jkaotlic/wg-monitor/internal/backend/db"
)

const tunnelCheckPrefix = "tunnel_"

// awgmLead -- awg-manager замечает неудачу раньше нашей проверки: серия,
// начавшаяся до пяти минут раньше происшествия, относится к нему.
const awgmLead = 5 * time.Minute

// AwgmCoverage -- окно, за которое агент читал журнал пингчека, и у каких
// VPN-туннелей он есть. Ограничена с ОБЕИХ сторон (постановление P2):
// Until -- ReceivedAt блока ping_log, а не «сейчас». Агент, откатившийся на
// версию без чтения журнала (например, на v0.46), перестаёт слать блок --
// Until застывает в прошлом, и происшествие после него «awg-manager связь не
// терял» не скажет: без верхней границы застывший факт молча выдавался бы за
// текущее покрытие.
type AwgmCoverage struct {
	Since   time.Time
	Until   time.Time
	Tunnels map[string]bool
}

// FoldWithAwgm -- Fold плюс серии awg-manager: пересекающиеся подписывают
// происшествие VPN-туннеля, не пересёкшиеся и дошедшие до падения становятся
// тихими «морганиями». Серии ниже порога в ленту не идут.
func FoldWithAwgm(rows []db.EventRow, runs []db.PingRunRow, cov AwgmCoverage, now time.Time) []Incident {
	out := Fold(rows, now)
	used := make([]bool, len(runs))
	for i := range out {
		inc := &out[i]
		tid, ok := strings.CutPrefix(inc.CheckName, tunnelCheckPrefix)
		if !ok || tid == "" {
			continue
		}
		end := inc.To
		if inc.Ongoing || end.IsZero() {
			end = now
		}
		for j, r := range runs {
			if r.TunnelID != tid || r.To.Before(inc.From.Add(-awgmLead)) || r.From.After(end) {
				continue
			}
			used[j] = true
			if inc.AwgmFirstFail.IsZero() || r.From.Before(inc.AwgmFirstFail) {
				inc.AwgmFirstFail = r.From
			}
			inc.AwgmFails += r.Fails
			inc.AwgmWentDown = inc.AwgmWentDown || r.WentDown
		}
		// P2: обе границы обязательны -- Since <= inc.From <= Until. Нулевой
		// Until (тест или агент, ни разу не прислававший ping_log) не
		// пропускает ничего мимо тем же нулём: inc.From из происшествия
		// всегда позже "1 января 0001".
		if inc.AwgmFails == 0 && !cov.Since.IsZero() && !cov.Until.IsZero() &&
			!inc.From.Before(cov.Since) && !inc.From.After(cov.Until) && cov.Tunnels[tid] {
			inc.AwgmClean = true
		}
	}
	byCheck := map[string][]Incident{}
	for j, r := range runs {
		if used[j] || !r.WentDown {
			continue
		}
		name := tunnelCheckPrefix + r.TunnelID
		byCheck[name] = append(byCheck[name], Incident{
			CheckName: name, From: r.From, To: r.To, DownSec: int(r.To.Sub(r.From).Seconds()), Flaps: 1,
			AwgmOnly: true, AwgmFirstFail: r.From, AwgmFails: r.Fails, AwgmWentDown: true,
		})
	}
	for _, list := range byCheck {
		sort.Slice(list, func(i, j int) bool { return list[i].From.Before(list[j].From) })
		out = append(out, mergeAwgmOnly(list)...)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].From.After(out[j].From) })
	if len(out) > MaxIncidents {
		out = out[:MaxIncidents]
	}
	return out
}

func mergeAwgmOnly(list []Incident) []Incident {
	merged := []Incident{list[0]}
	for _, inc := range list[1:] {
		prev := &merged[len(merged)-1]
		if inc.From.Sub(prev.To) < FlapGap {
			if inc.To.After(prev.To) {
				prev.To = inc.To
			}
			prev.DownSec += inc.DownSec
			prev.Flaps += inc.Flaps
			prev.AwgmFails += inc.AwgmFails
			continue
		}
		merged = append(merged, inc)
	}
	return merged
}
