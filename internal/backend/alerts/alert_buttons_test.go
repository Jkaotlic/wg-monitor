package alerts

import (
	"regexp"
	"strings"
	"testing"

	"github.com/Jkaotlic/wg-monitor/pkg/wire"
)

// Ответ на кнопку под тревогой уходит туда же, где её нажали, -- владельцу в
// личку. Раньше он печатал сырой ответ агента («pingcheck-now triggered»,
// «agent report kicked»), подпись «Force recheck», а на ошибке -- совет зайти
// по SSH и выполнить команду на роутере. Владельцу это не прочесть и не
// выполнить.
func TestFormatCommandResult_AlertButtonsSpeakToOwner(t *testing.T) {
	type tc struct {
		action string
		res    wire.CommandResult
	}
	var cases []tc
	for action, out := range map[string]string{
		"restart_tunnel": "все туннели перезапущены",
		"pingcheck_now":  "pingcheck-now triggered",
		"force_recheck":  "agent report kicked",
	} {
		cases = append(cases, tc{action, wire.CommandResult{Status: "ok", Output: out, DurationMs: 120}})
	}
	for _, action := range []string{"restart_tunnel", "pingcheck_now", "force_recheck", "diag_now"} {
		for _, r := range []wire.CommandResult{
			{Status: "err", Output: "awgmgr client not configured"},
			{Status: "err", Output: "HTTP_502: bad gateway"},
			{Status: "err", Output: "dial tcp 127.0.0.1:2222: connect: connection refused"},
			{Status: "err", Output: "HTTP_401: unauthorized"},
			{Status: "timeout"},
			{Status: "locked"},
		} {
			cases = append(cases, tc{action, r})
		}
	}
	// SSH, пути на роутере, команды, «покажи админу» -- советы админской
	// панели; «Force», «triggered», «kicked» -- сырой ответ агента.
	forbid := []string{"ssh", "/opt", "netstat", "logread", "`", "lock", "wizard", "админ",
		"деталь", "awg-manager", "awgmgr", "force", "triggered", "kicked"}
	for _, c := range cases {
		body := strings.Join(FormatCommandResult(c.action, c.res, 3500), "\n")
		what := c.action + "/" + c.res.Status + " " + c.res.Output
		low := strings.ToLower(body)
		for _, f := range forbid {
			if strings.Contains(low, f) {
				t.Errorf("%s: владелец читает %q:\n%s", what, f, body)
			}
		}
		if words := ownerLatin(body); len(words) > 0 {
			t.Errorf("%s: латиница %v:\n%s", what, words, body)
		}
		assertSaysVPNTunnel(t, what, body)
	}
}

// ownerLatin -- латинские слова вне ёлочек, кроме имён, которые владелец
// знает по приложению: VPN и HydraRoute Neo.
var (
	ownerQuotedRe = regexp.MustCompile(`«[^»]*»`)
	ownerLatinRe  = regexp.MustCompile(`[A-Za-z]{2,}`)
)

func ownerLatin(text string) []string {
	bare := ownerQuotedRe.ReplaceAllString(text, "«»")
	var out []string
	for _, w := range ownerLatinRe.FindAllString(bare, -1) {
		switch w {
		case "VPN", "HydraRoute", "Neo":
			continue
		}
		out = append(out, w)
	}
	return out
}
