package actions

import (
	"context"
	"encoding/json"
	"slices"
	"strings"

	"github.com/Jkaotlic/wg-monitor/internal/agent/awgmgr"
	"github.com/Jkaotlic/wg-monitor/pkg/wire"
)

// Коды Notes ответа route_lookup. Слова к ним подбирает экран: агент говорит
// фактами, а не прозой.
const (
	lookupNoteHRNotRunning     = "hr_not_running"
	lookupNotePoliciesUnknown  = "policies_unknown"
	lookupNoteSingboxRouter    = "singbox_router"
	lookupNoteIPRulesUnchecked = "ip_rules_unchecked"
	lookupNoteRegexpUnchecked  = "regexp_unchecked"
	lookupNoteGeoExpandFailed  = "geo_expand_failed:" // + тег
	lookupNoteExitUnrecognized = "exit_unrecognized:" // + имя подключения
)

// RouteLookup answers where one site goes according to the router's own rules
// and returns the answer JSON-encoded for wire.CommandResult.Output.
//
// Read-only: it reads the same inputs as RouteStatus and asks awg-manager to
// expand geosite tags, nothing else.
func RouteLookup(ctx context.Context, c *awgmgr.Client, domain string) (string, error) {
	in, err := fetchRouteInputs(ctx, c)
	if err != nil {
		return "", err
	}
	res := lookupRoute(domain, in, func(tag string) ([]string, error) {
		return c.GeoExpand(ctx, "geosite", tag)
	})
	b, err := json.Marshal(res)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// lookupRoute is the pure core of route_lookup: which enabled rules name the
// site and where each of them leads, by the very binding route_status credits
// the rule with (ruleEgress). No rule naming the site means the router's main
// exit carries it -- and that exit may well be a VPN tunnel, never assumed to
// be the provider.
//
// expand is called at most once per tag within one call.
func lookupRoute(domain string, in routeInputs, expand func(tag string) ([]string, error)) wire.RouteLookupResult {
	host := strings.TrimRight(strings.ToLower(strings.TrimSpace(domain)), ".")
	res := wire.RouteLookupResult{Domain: host, Matches: []wire.RouteLookupMatch{}}

	// sing-box раскладывает трафик по своим правилам, и ответ по правилам
	// роутера был бы уверенной неправдой.
	if in.settings.SingboxRouterActive() {
		res.Verdict = wire.LookupViaUnknown
		res.Notes = []string{lookupNoteSingboxRouter}
		return res
	}

	snap, env := routeSnapshotBase(in.hr, in.tunnels, in.routing, activeDefaultTunnelID(in.settings), in.policies, in.polIfaces, in.policiesUnknown)
	// hr == nil -- состояние движка не прочиталось. Объявить его правила
	// недействующими по такому поводу значило бы выдать догадку за факт.
	hrStopped := in.hr != nil && !in.hr.Running
	geo := &geoCache{expand: expand, cache: map[string]geoListEntry{}}
	exits := lookupExits{env: env, catalogue: newPolicyIfaceResolver(snap.Tunnels, in.polIfaces), polIfaces: in.polIfaces}
	var notes lookupNotes

	for _, r := range in.dns {
		if !r.Enabled {
			continue
		}
		if hrStopped && isHydraRouteBackend(r) {
			notes.add(lookupNoteHRNotRunning)
			continue
		}
		pattern, unchecked := ruleNamesHost(host, r, geo)
		if pattern == "" {
			// Непроверенное в силе, только пока правило сайт не назвало.
			notes.add(unchecked...)
			continue
		}
		via, id, name, exitNote := exits.ruleVia(r)
		if exitNote != "" {
			notes.add(exitNote)
		}
		if via == wire.LookupViaUnknown && in.policiesUnknown {
			notes.add(lookupNotePoliciesUnknown)
		}
		res.Matches = append(res.Matches, wire.RouteLookupMatch{
			RuleName: firstNonEmptyRoute(r.Name, r.ID), Pattern: pattern,
			Via: via, TunnelID: id, TunnelName: name,
		})
	}
	// Статический маршрут -- правило по адресу сети: по имени сайта его не
	// проверить, а сайт может оказаться за ним.
	for _, s := range in.statics {
		if s.Enabled && len(s.Subnets) > 0 {
			notes.add(lookupNoteIPRulesUnchecked)
			break
		}
	}

	if len(res.Matches) == 0 {
		res.ByDefault = true
		var exitNote string
		res.Verdict, res.TunnelID, res.TunnelName, exitNote = exits.defaultVia(snap.DefaultEgress)
		if exitNote != "" {
			notes.add(exitNote)
		}
	} else {
		first := res.Matches[0]
		res.Verdict, res.TunnelID, res.TunnelName = first.Via, first.TunnelID, first.TunnelName
		for _, m := range res.Matches[1:] {
			if m.Via != first.Via || m.TunnelID != first.TunnelID {
				res.Verdict, res.TunnelID, res.TunnelName = wire.LookupMixed, "", ""
				break
			}
		}
	}
	if len(notes) > 0 {
		res.Notes = notes
	}
	return res
}

// lookupExits turns where traffic leaves into the answer's vocabulary. The
// words come from the TYPE of the exit in the router's own catalogue: managed
// and system -- VPN-туннель, wan -- провайдер, anything else or not found --
// неизвестно, with the exit named in a note. An empty tunnel id from ruleEgress
// says only "not a tunnel of ours", and that is not the same as "provider": a
// system WireGuard interface, a bridge or a guest network are not ours either.
type lookupExits struct {
	env *routeEgressEnv
	// catalogue maps any exit name (NDMS name, iface, id, label) to a snapshot
	// entry of ANY type. route_status's resolver knows our managed tunnels
	// only -- for its counters "not ours" is all it needs to know.
	catalogue *policyIfaceResolver
	polIfaces []awgmgr.PolicyInterface
}

// ruleVia answers for one enabled rule naming the site. note is an
// exit_unrecognized code when the exit could not be told apart.
func (x lookupExits) ruleVia(r awgmgr.DNSRoute) (via, id, name, note string) {
	bind, tunnelID, known, credit := ruleEgress(r, x.env)
	switch {
	case !known:
		return wire.LookupViaUnknown, "", "", ""
	case tunnelID != "":
		return x.endpointVia(tunnelID)
	case credit.policy >= 0:
		// Живое звено общего набора -- не наш туннель. Что это за
		// подключение, говорит его тип в каталоге роутера, а не догадка.
		for _, link := range x.env.policies[credit.policy].Interfaces {
			if link.Role == "active" {
				return x.linkVia(link.Bind, link.Name)
			}
		}
		return wire.LookupViaUnknown, "", "", ""
	case bind != "":
		// Правило приколочено к интерфейсу, который не наш туннель.
		return x.linkVia(bind, x.label(bind))
	}
	// Правило HydraRoute Neo с набором «напрямую» (isDirectProviderHRNeoPolicy):
	// провайдера здесь назвал сам роутер.
	return wire.LookupViaDirect, "", "", ""
}

// defaultVia answers for a site no rule names: it goes wherever the router's
// main exit (RouteSnapshot.DefaultEgress) goes. "" is the router's own silence
// and stays unknown.
func (x lookupExits) defaultVia(defaultEgress string) (via, id, name, note string) {
	switch defaultEgress {
	case "":
		return wire.LookupViaUnknown, "", "", ""
	case wire.DefaultEgressDirect:
		return wire.LookupViaDirect, "", "", ""
	}
	return x.endpointVia(defaultEgress)
}

// linkVia resolves an interface known only by name (a policy chain link, an
// interface a rule is pinned to) through the catalogue of all exits. names go
// strongest first; the last non-empty one is what the note shows a person.
func (x lookupExits) linkVia(names ...string) (via, id, name, note string) {
	if id, ok := x.catalogue.tunnelForNames(names...); ok {
		return x.endpointVia(id)
	}
	shown := ""
	for _, n := range names {
		if n = strings.TrimSpace(n); n != "" {
			shown = n
		}
	}
	return wire.LookupViaUnknown, "", "", lookupNoteExitUnrecognized + shown
}

// endpointVia classifies a snapshot exit by its type.
func (x lookupExits) endpointVia(tunnelID string) (via, id, name, note string) {
	t, ok := routeTunnelByID(x.env.tunnels, tunnelID)
	if !ok {
		return wire.LookupViaUnknown, "", "", lookupNoteExitUnrecognized + tunnelID
	}
	switch strings.ToLower(strings.TrimSpace(t.Type)) {
	case "managed", "system":
		return wire.LookupViaTunnel, t.ID, t.Name, ""
	case "wan":
		return wire.LookupViaDirect, "", "", ""
	}
	return wire.LookupViaUnknown, "", "", lookupNoteExitUnrecognized + firstNonEmptyRoute(t.Name, t.ID)
}

// label is the human name the router gives a policy interface, "" if none.
func (x lookupExits) label(name string) string {
	for _, pi := range x.polIfaces {
		if strings.EqualFold(strings.TrimSpace(pi.Name), strings.TrimSpace(name)) {
			return pi.Label
		}
	}
	return ""
}

// ruleNamesHost returns the first target of r that names host ("" when none
// does). unchecked lists the codes of targets a site name cannot be checked
// against; they matter only when the rule names nothing.
func ruleNamesHost(host string, r awgmgr.DNSRoute, geo *geoCache) (pattern string, unchecked []string) {
	targets := make([]string, 0, len(r.Domains)+len(r.ManualDomains)+len(r.Subnets))
	targets = append(targets, r.Domains...)
	targets = append(targets, r.ManualDomains...)
	targets = append(targets, r.Subnets...)
	note := func(code string) {
		if !slices.Contains(unchecked, code) {
			unchecked = append(unchecked, code)
		}
	}
	// linesName checks a list of v2ray-style lines; a regexp line is noted,
	// never guessed at.
	linesName := func(lines ...string) bool {
		for _, line := range lines {
			hit, isRegexp := geoLineNamesHost(host, line)
			if hit {
				return true
			}
			if isRegexp {
				note(lookupNoteRegexpUnchecked)
			}
		}
		return false
	}
	for _, raw := range targets {
		t := strings.TrimSpace(raw)
		lower := strings.ToLower(t)
		switch {
		case t == "":
		case strings.HasPrefix(lower, "geosite:"):
			tag := strings.TrimSpace(t[len("geosite:"):])
			lines, err := geo.lines(tag)
			if err != nil {
				// Не раскрытый список считается несовпавшим: сказать «идёт
				// через туннель» по списку, которого никто не видел, нельзя.
				note(lookupNoteGeoExpandFailed + tag)
				continue
			}
			if linesName(lines...) {
				return t, nil
			}
		case strings.HasPrefix(lower, "geoip:"):
			note(lookupNoteIPRulesUnchecked)
		default:
			switch target := normalizeRouteTarget(t); target.Type {
			case "cidr":
				note(lookupNoteIPRulesUnchecked)
			case "domain":
				if routeTargetMatchesHost(host, target.Value) {
					return t, nil
				}
			default:
				// ".claude.ai", "full:x" и прочее, что правило может нести
				// тем же синтаксисом, что и гео-список.
				if linesName(t) {
					return t, nil
				}
			}
		}
	}
	return "", unchecked
}

// geoLineNamesHost matches one line of a v2ray-style geosite list against
// host: ".x", "x" and "domain:x" name x and everything under it; "full:x" names
// x alone; "keyword:x" names any host containing x. "regexp:" lines are not
// evaluated (isRegexp=true) -- a pattern checked by a different engine than
// the router's would answer for the router without being it.
func geoLineNamesHost(host, line string) (hit, isRegexp bool) {
	l := strings.ToLower(strings.TrimSpace(line))
	switch {
	case l == "" || strings.HasPrefix(l, "#"):
		return false, false
	case strings.HasPrefix(l, "regexp:"):
		return false, true
	}
	// Атрибуты списка ("x @ads", "x:@ads") к имени не относятся.
	if i := strings.IndexAny(l, " \t@"); i >= 0 {
		l = strings.TrimRight(strings.TrimSpace(l[:i]), ":")
	}
	switch {
	case strings.HasPrefix(l, "full:"):
		return host == strings.TrimPrefix(l, "full:"), false
	case strings.HasPrefix(l, "keyword:"):
		kw := strings.TrimPrefix(l, "keyword:")
		return kw != "" && strings.Contains(host, kw), false
	}
	suffix := strings.Trim(strings.TrimPrefix(l, "domain:"), ".")
	return suffix != "" && (host == suffix || strings.HasSuffix(host, "."+suffix)), false
}

// geoCache expands each geosite tag at most once per lookup: one rule set
// routinely names the same tag from several rules, and every expansion is a
// round-trip to the router.
type geoCache struct {
	expand func(tag string) ([]string, error)
	cache  map[string]geoListEntry
}

type geoListEntry struct {
	lines []string
	err   error
}

func (g *geoCache) lines(tag string) ([]string, error) {
	if e, ok := g.cache[tag]; ok {
		return e.lines, e.err
	}
	lines, err := g.expand(tag)
	g.cache[tag] = geoListEntry{lines: lines, err: err}
	return lines, err
}

// lookupNotes keeps codes in first-seen order, each once.
type lookupNotes []string

func (n *lookupNotes) add(codes ...string) {
	for _, c := range codes {
		if !slices.Contains(*n, c) {
			*n = append(*n, c)
		}
	}
}
