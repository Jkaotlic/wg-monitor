package actions

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Jkaotlic/wg-monitor/internal/agent/dnsref"
	"github.com/Jkaotlic/wg-monitor/pkg/wire"
)

// fakeDNSProbe -- подменная проба эталона: сервер (идентификатор строки) ->
// отвечает ли. Записывает, кого и каким именем спросили.
type fakeDNSProbe struct {
	mu    sync.Mutex
	dead  map[string]bool
	asked map[string]string // сервер -> домен
	delay time.Duration
}

func (p *fakeDNSProbe) probe(ctx context.Context, line, domain string) error {
	server := strings.Fields(line)[2]
	p.mu.Lock()
	if p.asked == nil {
		p.asked = map[string]string{}
	}
	if _, dup := p.asked[server]; dup {
		p.asked[server] = "DUPLICATE"
	} else {
		p.asked[server] = domain
	}
	p.mu.Unlock()
	if p.delay > 0 {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(p.delay):
		}
	}
	if p.dead[server] {
		return errors.New("i/o timeout")
	}
	return nil
}

var yandexHost = dnsref.YandexDoTHost()

// Каждый сервер эталона спрашивается один раз, своим именем: Яндекс -- ya.ru,
// заграничные -- example.com. Все живы -- применяется весь эталон, пробы в
// ответе.
func TestDNSResetProbesEachReferenceServerOnce(t *testing.T) {
	p := &fakeDNSProbe{}
	f := &replayDNSExec{configs: []string{sampleRunningConfig, configAfterApplyWithPorts()}}

	status, out, res := DNSResetProbed(context.Background(), f.exec, DNSResetOpts{Probe: p.probe})

	if status != "ok" {
		t.Fatalf("status=%q\n%s", status, out)
	}
	want := map[string]string{yandexHost: "ya.ru", "1.1.1.1": "example.com", "1.0.0.1": "example.com"}
	if len(p.asked) != len(want) {
		t.Fatalf("asked=%v", p.asked)
	}
	for s, d := range want {
		if p.asked[s] != d {
			t.Fatalf("%s asked with %q, want %q (all: %v)", s, p.asked[s], d, p.asked)
		}
	}
	if len(res.Probes) != 3 {
		t.Fatalf("probes=%+v", res.Probes)
	}
	for _, pr := range res.Probes {
		if !pr.OK || pr.Error != "" || (pr.Purpose != "ru" && pr.Purpose != "foreign") {
			t.Fatalf("probe=%+v", pr)
		}
	}
	if n := countPrefix(f.calls, "dns-proxy tls upstream"); n != len(dnsref.ReferenceDoTLines()) {
		t.Fatalf("applied %d lines, want full reference; calls=%v", n, f.calls)
	}
}

// Яндекс не отвечает -- ни одной его строки в роутер: ру-зоны уйдут на
// заграничные, это работает. Заграничные ставятся, конфиг сохраняется.
func TestDNSResetSkipsDeadYandexLines(t *testing.T) {
	p := &fakeDNSProbe{dead: map[string]bool{yandexHost: true}}
	f := &replayDNSExec{configs: []string{sampleRunningConfig, configAfterApplyWithPorts()}}

	status, out, res := DNSResetProbed(context.Background(), f.exec, DNSResetOpts{Probe: p.probe})

	if status != "ok" {
		t.Fatalf("status=%q\n%s", status, out)
	}
	for _, c := range f.calls {
		if strings.HasPrefix(c, "dns-proxy tls upstream "+yandexHost) {
			t.Fatalf("dead Yandex line applied: %s", c)
		}
	}
	if countPrefix(f.calls, "dns-proxy tls upstream") != 2 || !slices.Contains(f.calls, "system configuration save") {
		t.Fatalf("calls=%v", f.calls)
	}
	if !strings.Contains(out, "не отвечает") || !strings.Contains(out, yandexHost) {
		t.Fatalf("transcript does not name the dead server:\n%s", out)
	}
	var dead wire.DNSProbe
	for _, pr := range res.Probes {
		if pr.Server == yandexHost {
			dead = pr
		}
	}
	if dead.OK || dead.Error == "" || dead.Purpose != "ru" {
		t.Fatalf("yandex probe=%+v", dead)
	}
}

// Один заграничный мёртв -- ставится второй.
func TestDNSResetSkipsOneDeadForeign(t *testing.T) {
	p := &fakeDNSProbe{dead: map[string]bool{"1.1.1.1": true}}
	f := &replayDNSExec{configs: []string{sampleRunningConfig, configAfterApplyWithPorts()}}

	status, out, _ := DNSResetProbed(context.Background(), f.exec, DNSResetOpts{Probe: p.probe})

	if status != "ok" {
		t.Fatalf("status=%q\n%s", status, out)
	}
	if slices.Contains(f.calls, "dns-proxy tls upstream 1.1.1.1 sni cloudflare-dns.com") || !slices.Contains(f.calls, "dns-proxy tls upstream 1.0.0.1 sni cloudflare-dns.com") {
		t.Fatalf("calls=%v", f.calls)
	}
}

// Ни одного живого заграничного -- отказ reference_unreachable, ни одной
// команды; и в предпросмотре тоже.
func TestDNSResetRefusesWhenNoForeignAnswers(t *testing.T) {
	for _, dry := range []bool{false, true} {
		p := &fakeDNSProbe{dead: map[string]bool{"1.1.1.1": true, "1.0.0.1": true}}
		f := &replayDNSExec{configs: []string{sampleRunningConfig, configAfterApplyWithPorts()}}

		status, out, res := DNSResetProbed(context.Background(), f.exec, DNSResetOpts{Probe: p.probe, DryRun: dry, SnapshotDir: t.TempDir()})

		if status != "err" || !strings.HasPrefix(out, DNSReferenceUnreachableCode+":") {
			t.Fatalf("dry=%v status=%q out=%s", dry, status, out)
		}
		if len(f.calls) != 0 {
			t.Fatalf("dry=%v mutated: %v", dry, f.calls)
		}
		if len(res.Probes) != 3 {
			t.Fatalf("dry=%v probes=%+v", dry, res.Probes)
		}
	}
}

// Предпросмотр тоже пробует и обещает ровно то, что выполнится: мёртвый
// Яндекс -- в предпросмотре заменим на 2, не на 8.
func TestDNSResetDryRunProbesAndPromisesFilteredSet(t *testing.T) {
	p := &fakeDNSProbe{dead: map[string]bool{yandexHost: true}}
	f := &replayDNSExec{configs: []string{sampleRunningConfig}}

	status, out, res := DNSResetProbed(context.Background(), f.exec, DNSResetOpts{Probe: p.probe, DryRun: true})

	if status != "ok" || len(f.calls) != 0 {
		t.Fatalf("status=%q calls=%v\n%s", status, f.calls, out)
	}
	if !strings.HasPrefix(out, "Предпросмотр") || !strings.Contains(out, "Заменим на эталонные (2):") {
		t.Fatalf("preview:\n%s", out)
	}
	if len(res.Probes) != 3 {
		t.Fatalf("probes=%+v", res.Probes)
	}
}

// Пробы параллельно и с потолком 5 с каждая: зависший сервер не держит сброс.
func TestDNSResetProbesAreParallelAndBounded(t *testing.T) {
	p := &fakeDNSProbe{delay: time.Hour}
	f := &replayDNSExec{configs: []string{sampleRunningConfig}}
	saved := dnsProbeTimeout
	dnsProbeTimeout = 50 * time.Millisecond
	t.Cleanup(func() { dnsProbeTimeout = saved })

	start := time.Now()
	status, out, res := DNSResetProbed(context.Background(), f.exec, DNSResetOpts{Probe: p.probe, DryRun: true})

	if took := time.Since(start); took > 2*time.Second {
		t.Fatalf("probes took %v", took)
	}
	if status != "err" || !strings.HasPrefix(out, DNSReferenceUnreachableCode) || len(res.Probes) != 3 {
		t.Fatalf("status=%q probes=%+v out=%s", status, res.Probes, out)
	}
}

// Без пробы (старый вызов DNSReset, тесты) -- как раньше: никаких проб,
// весь эталон.
func TestDNSResetWithoutProbeKeepsOldBehaviour(t *testing.T) {
	f := &replayDNSExec{configs: []string{sampleRunningConfig, configAfterApplyWithPorts()}}
	status, _, res := DNSResetProbed(context.Background(), f.exec, DNSResetOpts{})
	if status != "ok" || len(res.Probes) != 0 || countPrefix(f.calls, "dns-proxy tls upstream") != len(dnsref.ReferenceDoTLines()) {
		t.Fatalf("status=%q probes=%v calls=%v", status, res.Probes, f.calls)
	}
}

// Диспетчер проводит пробу и кладёт пробы в Payload.
func TestDispatchDNSResetCarriesProbes(t *testing.T) {
	p := &fakeDNSProbe{dead: map[string]bool{yandexHost: true}}
	f := &replayDNSExec{configs: []string{sampleRunningConfig}}
	r := &Runner{Exec: f.exec, DNSProbe: p.probe}

	res := r.Execute(context.Background(), wire.Command{ID: "c", Action: "dns_reset", Args: map[string]any{"dry_run": true}})

	if res.Status != "ok" {
		t.Fatalf("%q %s", res.Status, res.Output)
	}
	var payload wire.DNSResetResult
	if err := json.Unmarshal(res.Payload, &payload); err != nil || len(payload.Probes) != 3 {
		t.Fatalf("payload=%s err=%v", res.Payload, err)
	}
}

func countPrefix(calls []string, prefix string) int {
	n := 0
	for _, c := range calls {
		if strings.HasPrefix(c, prefix) {
			n++
		}
	}
	return n
}
