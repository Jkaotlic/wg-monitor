package actions

import (
	"context"
	"strings"
	"testing"

	"github.com/Jkaotlic/wg-monitor/pkg/wire"
)

// Защита своего резолвера живёт в DNSResetOpts, но в бою её включает диспетчер.
// Пока эту проводку не сторожил ни один тест, обезвреженный `KeepHosts: nil`
// компилировался и весь набор оставался зелёным — то есть запрет был, а
// сторожить его было нечем.
//
// Конфиг «до» содержит свой резолвер оператора и чужой апстрим: у теста есть
// обе половины, иначе он прошёл бы вхолостую на коде, который не делает ничего.
func TestDispatchDNSResetKeepsOwnResolver(t *testing.T) {
	const before = `dns-proxy
    https upstream https://dns.example.com/dns-query
    tls upstream 9.9.9.9:853 sni dns.quad9.net
!
`
	f := &replayDNSExec{configs: []string{before, configAfterApplyWithPorts()}}
	r := &Runner{Exec: f.exec, OwnResolverEndpoint: "https://dns.example.com/s3cret"}

	res := r.Execute(context.Background(), wire.Command{ID: "c1", Action: "dns_reset"})

	var touchedOwn, removedForeign bool
	for _, c := range f.calls {
		if strings.Contains(c, "no https upstream") && strings.Contains(c, "dns.example.com") {
			touchedOwn = true
		}
		if c == "dns-proxy no tls upstream 9.9.9.9" {
			removedForeign = true
		}
	}
	if touchedOwn {
		t.Errorf("сброс снёс свой резолвер оператора:\n%v\n%s", f.calls, res.Output)
	}
	if !removedForeign {
		t.Errorf("сброс не тронул чужой апстрим — тест прошёл бы вхолостую: %v", f.calls)
	}
	// Секретный путь эндпоинта не должен попасть в транскрипт: его видит человек.
	if strings.Contains(res.Output, "s3cret") {
		t.Errorf("секрет из адреса эндпоинта утёк в транскрипт:\n%s", res.Output)
	}
}

// Предпросмотр из аргументов команды доезжает до действия: без этого кнопка
// «посмотреть» молча делала бы настоящий сброс.
func TestDispatchDNSResetHonoursDryRun(t *testing.T) {
	f := &replayDNSExec{configs: []string{sampleRunningConfig}}
	r := &Runner{Exec: f.exec}

	res := r.Execute(context.Background(), wire.Command{
		ID: "c2", Action: "dns_reset", Args: map[string]any{"dry_run": true},
	})

	if len(f.calls) != 0 {
		t.Errorf("предпросмотр выполнил мутации: %v", f.calls)
	}
	if !strings.Contains(res.Output, "Заменим на эталонные") {
		t.Errorf("предпросмотр не доехал до действия:\n%s", res.Output)
	}
}
