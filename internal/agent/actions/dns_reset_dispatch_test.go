package actions

import (
	"context"
	"path/filepath"
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

// Снимок «до» пишется в бою, а не только в тесте DNSReset. Раньше диспетчер не
// передавал SnapshotDir вовсе, и DNSReset молча пропускал снимок: код был,
// файла не было, экран обещал бы путь к несуществующему файлу. Кладём рядом с
// конфигом агента -- /opt/etc/wg-monitor на роутере.
func TestDispatchDNSResetWritesSnapshotNextToConfig(t *testing.T) {
	const before = `dns-proxy
    tls upstream 9.9.9.9:853 sni dns.quad9.net
!
`
	dir := t.TempDir()
	f := &replayDNSExec{configs: []string{before, configAfterApplyWithPorts()}}
	var changed int
	r := &Runner{Exec: f.exec, ConfigPath: filepath.Join(dir, "config.yaml"), DNSChanged: func() { changed++ }}

	res := r.Execute(context.Background(), wire.Command{ID: "c1", Action: "dns_reset"})

	files, _ := filepath.Glob(filepath.Join(dir, "dns-before-*.txt"))
	if len(files) != 1 {
		t.Fatalf("снимков «до» %d, ожидался 1:\n%s", len(files), res.Output)
	}
	if !strings.Contains(res.Output, "снимок «до»: "+files[0]) {
		t.Errorf("путь снимка не назван в ответе — экрану нечего показать:\n%s", res.Output)
	}
	if changed != 1 {
		t.Errorf("DNSChanged вызван %d раз, ожидался 1: проверка раздельного DNS отвечала бы по старым настройкам", changed)
	}
}

// Предпросмотр не пишет ничего и не сбрасывает ничьих кешей: он ничего не менял.
func TestDispatchDNSResetDryRunTouchesNothing(t *testing.T) {
	const before = `dns-proxy
    tls upstream 9.9.9.9:853 sni dns.quad9.net
!
`
	dir := t.TempDir()
	f := &replayDNSExec{configs: []string{before}}
	var changed int
	r := &Runner{Exec: f.exec, ConfigPath: filepath.Join(dir, "config.yaml"), DNSChanged: func() { changed++ }}

	res := r.Execute(context.Background(), wire.Command{ID: "c1", Action: "dns_reset", Args: map[string]any{"dry_run": true}})

	if files, _ := filepath.Glob(filepath.Join(dir, "dns-before-*.txt")); len(files) != 0 {
		t.Errorf("предпросмотр записал снимок: %v", files)
	}
	if changed != 0 {
		t.Errorf("предпросмотр вызвал DNSChanged %d раз", changed)
	}
	if !strings.Contains(res.Output, "Предпросмотр") {
		t.Errorf("это был не предпросмотр:\n%s", res.Output)
	}
}
