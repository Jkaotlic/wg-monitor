package actions

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Jkaotlic/wg-monitor/internal/agent/dnsref"
)

// replayDNSExec отдаёт РАЗНЫЕ конфиги на последовательные `show running-config`:
// первый -- состояние до правки, второй -- после. Существующий fakeDNSExec для
// этого не годится, он всегда возвращает одно и то же, и проверка «строка
// действительно появилась» на нём зеленела бы всегда — то есть была бы фикцией.
type replayDNSExec struct {
	configs []string // по одному на каждый последующий read
	reads   int
	calls   []string
	failOn  map[string]bool
}

func (f *replayDNSExec) exec(_ context.Context, name string, args ...string) ([]byte, error) {
	if name != "ndmc" || len(args) != 2 || args[0] != "-c" {
		return nil, fmt.Errorf("unexpected exec: %s %v", name, args)
	}
	cmd := args[1]
	if cmd == "show running-config" {
		i := f.reads
		if i >= len(f.configs) {
			i = len(f.configs) - 1
		}
		f.reads++
		return []byte(f.configs[i]), nil
	}
	f.calls = append(f.calls, cmd)
	if f.failOn[cmd] {
		return []byte("ndmc: rejected"), fmt.Errorf("command rejected")
	}
	return nil, nil
}

// Конфиг «после» в форме, отличной от той, которой мы просили: KeenOS дописал
// порт :853. Решение оператора 13.09 («делаем по факту») требует, чтобы такую
// строку проверка УЗНАЛА, а не сочла пропажей.
func configAfterApplyWithPorts() string {
	var b strings.Builder
	b.WriteString("! configuration\ndns-proxy\n")
	for _, line := range dnsref.ReferenceDoTLines() {
		f := strings.Fields(line)
		// "tls upstream <host> ..." -> "tls upstream <host>:853 ..."
		f[2] += ":853"
		b.WriteString("    " + strings.Join(f, " ") + "\n")
	}
	b.WriteString("!\n")
	return b.String()
}

// Предпросмотр обязан не менять НИЧЕГО: ни одной мутирующей команды, ни файла
// снимка. Человек нажал «посмотреть», а не «сделать».
func TestDNSResetDryRunChangesNothing(t *testing.T) {
	dir := t.TempDir()
	f := &replayDNSExec{configs: []string{sampleRunningConfig}}
	status, out := DNSReset(context.Background(), f.exec, DNSResetOpts{DryRun: true, SnapshotDir: dir})

	if status != "ok" {
		t.Fatalf("status = %q, хотим ok\n%s", status, out)
	}
	if len(f.calls) != 0 {
		t.Errorf("предпросмотр выполнил мутации: %v", f.calls)
	}
	if files, _ := filepath.Glob(filepath.Join(dir, "*")); len(files) != 0 {
		t.Errorf("предпросмотр написал файлы: %v", files)
	}
	if !strings.Contains(out, "Заменим на эталонные") {
		t.Errorf("предпросмотр не сказал, что изменится:\n%s", out)
	}
}

// Свой резолвер оператора сбросом не снимается. Иначе «починить DNS» кнопкой
// уводило бы сторожа в idle ровно тем действием, которым человек чинит DNS.
func TestDNSResetKeepsOwnResolver(t *testing.T) {
	const cfg = `dns-proxy
    https upstream https://dns.example.com/dns-query
    tls upstream 9.9.9.9:853 sni dns.quad9.net
!
`
	f := &replayDNSExec{configs: []string{cfg, configAfterApplyWithPorts()}}
	_, out := DNSReset(context.Background(), f.exec, DNSResetOpts{KeepHosts: []string{"dns.example.com"}})
	for _, c := range f.calls {
		if strings.Contains(c, "no https upstream") && strings.Contains(c, "dns.example.com") {
			t.Errorf("сброс снёс свой резолвер оператора: %q\n%s", c, out)
		}
	}
	var removedQuad bool
	for _, c := range f.calls {
		if c == "dns-proxy no tls upstream 9.9.9.9" {
			removedQuad = true
		}
	}
	if !removedQuad {
		t.Errorf("сброс не тронул чужой апстрим — KeepHosts защитил лишнее: %v", f.calls)
	}
}

// Транскрипт живёт час и архивом прежних настроек не годится, поэтому снимок
// «до» ложится файлом на роутер, и путь к нему назван человеку.
func TestDNSResetWritesSnapshotFileAndNamesIt(t *testing.T) {
	dir := t.TempDir()
	f := &replayDNSExec{configs: []string{sampleRunningConfig, configAfterApplyWithPorts()}}
	_, out := DNSReset(context.Background(), f.exec, DNSResetOpts{SnapshotDir: dir})

	files, _ := filepath.Glob(filepath.Join(dir, "dns-before-*.txt"))
	if len(files) != 1 {
		t.Fatalf("снимков %d, хотим 1", len(files))
	}
	if !strings.Contains(out, filepath.Base(files[0])) {
		t.Errorf("путь к снимку не назван в результате:\n%s", out)
	}
}

// Решение оператора 13.09: успехом считается СТРОКА, найденная в конфиге после
// применения, а не отсутствие ошибки у команды. Форма в коде не закрепляется:
// роутер вправе записать строку иначе (здесь -- с портом :853), и это то же
// самое по смыслу.
func TestDNSResetConfirmsByFactAcceptingOtherForm(t *testing.T) {
	f := &replayDNSExec{configs: []string{sampleRunningConfig, configAfterApplyWithPorts()}}
	status, out := DNSReset(context.Background(), f.exec, DNSResetOpts{})
	if status != "ok" {
		t.Fatalf("status = %q, хотим ok: роутер записал строки в другой форме, это не отказ\n%s", status, out)
	}
	if !strings.Contains(out, "подтверждено") {
		t.Errorf("результат не говорит, что применение подтверждено чтением:\n%s", out)
	}
}

// Обратный случай: строки после применения в конфиге нет. Это ОТКАЗ, о котором
// сказано человеку, а не молчаливый успех.
func TestDNSResetSaysWhatDidNotApply(t *testing.T) {
	const after = `dns-proxy
    tls upstream 9.9.9.9:853 sni dns.quad9.net
!
`
	f := &replayDNSExec{configs: []string{sampleRunningConfig, after}}
	status, out := DNSReset(context.Background(), f.exec, DNSResetOpts{})
	if status == "ok" {
		t.Fatalf("применение не подтвердилось, а статус ok — молчаливый успех\n%s", out)
	}
	if !strings.Contains(out, "domain ru") {
		t.Errorf("не сказано, какая именно строка не применилась:\n%s", out)
	}
}
