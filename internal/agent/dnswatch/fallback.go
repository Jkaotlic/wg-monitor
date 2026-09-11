package dnswatch

// BuildFallbackSet returns the dns-proxy lines to ADD when going to fallback.
// liveRU / liveForeign are candidate lines that passed their probe, in config
// order.
//
// The set mirrors the operator's AdGuard Home (split, parallel):
//   - foreign: the first MaxForeign distinct lines of liveForeign, without a
//     domain — ndnproxy races them like AGH's upstream_mode: parallel;
//   - RU: liveRU[0] + " domain <zone>" for every RU zone; with no live RU
//     candidate ruDegraded is true and the RU zones fall through to the
//     foreign pool;
//   - pinned: PinnedCandidate + " domain <zone>" for every pinned zone, only
//     when PinnedCandidate itself is in liveForeign.
//
// ok is false when liveForeign is empty: nothing would carry the bulk of the
// traffic, so the caller must not switch (no_live_fallback). The order is
// deterministic (foreign, RU, pinned) and no line appears twice.
func BuildFallbackSet(cfg Config, liveRU, liveForeign []string) (lines []string, ruDegraded bool, ok bool) {
	if len(liveForeign) == 0 {
		return nil, len(liveRU) == 0, false
	}
	limit := cfg.MaxForeign
	if limit <= 0 {
		limit = defaultMaxForeign
	}
	seen := make(map[string]bool)
	add := func(line string) {
		if !seen[line] {
			seen[line] = true
			lines = append(lines, line)
		}
	}

	foreign := 0
	for _, line := range liveForeign {
		if foreign == limit {
			break
		}
		if !seen[line] {
			add(line)
			foreign++
		}
	}

	if len(liveRU) == 0 {
		ruDegraded = true
	} else {
		for _, zone := range cfg.RUZones {
			add(liveRU[0] + " domain " + zone)
		}
	}

	if cfg.PinnedCandidate != "" && contains(liveForeign, cfg.PinnedCandidate) {
		for _, zone := range cfg.PinnedZones {
			add(cfg.PinnedCandidate + " domain " + zone)
		}
	}
	return lines, ruDegraded, true
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
