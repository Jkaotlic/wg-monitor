package actions

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/Jkaotlic/wg-monitor/internal/agent/dnsref"
	"github.com/Jkaotlic/wg-monitor/pkg/wire"
)

// DNSReferenceUnreachableCode -- начало вывода dns_reset, когда не ответил
// ни один заграничный сервер эталона: ничего не изменено. Контракт с
// бэкендом и мини-аппом (экран говорит это человеческим текстом).
const DNSReferenceUnreachableCode = "reference_unreachable"

// dnsProbeTimeout -- потолок одной пробы. Переменная -- для теста.
var dnsProbeTimeout = 5 * time.Second

// dnsProbeDomain -- каким именем спрашивать: Яндекс несёт только русские
// зоны и на example.com отвечать не обязан, заграничные -- любое имя.
func dnsProbeDomain(p dnsref.Purpose) string {
	if p == dnsref.PurposeRU {
		return dnsref.RUCanary()
	}
	return "example.com"
}

// dnsServerID -- кого спрашивает строка: идентификатор после `upstream`
// без порта :853 (как у снятия -- dnsProxyRemovalCommand). У Яндекса шесть
// строк, по одной на зону, а сервер один: и проба одна.
func dnsServerID(line string) string {
	cmd, ok := dnsProxyRemovalCommand(line)
	if !ok {
		return line
	}
	f := strings.Fields(cmd)
	return f[len(f)-1]
}

// probeDNSReference пробует каждый сервер эталона один раз, параллельно,
// не дольше dnsProbeTimeout каждый. Порядок -- как в эталоне.
func probeDNSReference(ctx context.Context, probe DNSLineProbe) []wire.DNSProbe {
	var (
		probes []wire.DNSProbe
		lines  []string
		seen   = map[string]bool{}
	)
	for _, u := range dnsref.ReferenceUpstreams() {
		id := dnsServerID(u.Line)
		if seen[id] {
			continue
		}
		seen[id] = true
		probes = append(probes, wire.DNSProbe{Server: id, Purpose: string(u.Purpose)})
		lines = append(lines, u.Line)
	}
	var wg sync.WaitGroup
	for i := range probes {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			pctx, cancel := context.WithTimeout(ctx, dnsProbeTimeout)
			defer cancel()
			done := make(chan error, 1)
			go func() { done <- probe(pctx, lines[i], dnsProbeDomain(dnsref.Purpose(probes[i].Purpose))) }()
			var err error
			select {
			case err = <-done:
			case <-pctx.Done():
				// Проба, не уважающая ctx, не держит сброс.
				err = fmt.Errorf("нет ответа за %s", dnsProbeTimeout)
			}
			if err != nil {
				probes[i].Error = truncateProbeError(err.Error())
				return
			}
			probes[i].OK = true
		}(i)
	}
	wg.Wait()
	return probes
}

func truncateProbeError(s string) string {
	const max = 200
	if len(s) <= max {
		return s
	}
	// по границе руны
	for i := max; i > 0; i-- {
		if (s[i] & 0xC0) != 0x80 {
			return s[:i] + "…"
		}
	}
	return s[:max]
}

// dnsReferenceAlive -- эталон без строк мёртвых серверов (у Яндекса -- все
// строки его хоста: ру-зоны тогда уйдут на заграничные, это работает).
// foreignOK -- ответил хотя бы один заграничный: без него менять нечего,
// так бывает и когда у роутера нет интернета.
func dnsReferenceAlive(probes []wire.DNSProbe) (alive, dead []string, foreignOK bool) {
	ok := map[string]bool{}
	for _, p := range probes {
		ok[p.Server] = p.OK
		if p.OK && p.Purpose == string(dnsref.PurposeForeign) {
			foreignOK = true
		}
	}
	for _, line := range dnsReferenceUpstreams {
		if ok[dnsServerID(line)] {
			alive = append(alive, line)
		} else {
			dead = append(dead, line)
		}
	}
	return alive, dead, foreignOK
}

func dnsReferenceUnreachableText(probes []wire.DNSProbe) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s: не ответил ни один заграничный DNS-сервер эталона — сброс не применён, ничего не изменено. Так бывает и когда у роутера нет интернета.\n\n", DNSReferenceUnreachableCode)
	writeDNSProbes(&b, probes, nil)
	return b.String()
}

// writeDNSProbes -- раздел транскрипта с пробами и строками, которые из-за
// них не ставятся. Пусто (пробы не делались) -- ничего.
func writeDNSProbes(b *strings.Builder, probes []wire.DNSProbe, dead []string) {
	if len(probes) == 0 {
		return
	}
	fmt.Fprintf(b, "проверка доступности эталона (%d):\n", len(probes))
	for _, p := range probes {
		if p.OK {
			fmt.Fprintf(b, "  ✓ %s — отвечает\n", p.Server)
		} else {
			fmt.Fprintf(b, "  ✗ %s — не отвечает: %s\n", p.Server, p.Error)
		}
	}
	if len(dead) > 0 {
		fmt.Fprintf(b, "сервер не отвечает, не ставим (%d):\n", len(dead))
		for _, l := range dead {
			fmt.Fprintf(b, "  · %s\n", l)
		}
	}
	b.WriteString("\n")
}
