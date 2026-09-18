package actions

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// dnsLineKept -- оставляем ли строку конфига нетронутой. Прежде всего это свой
// резолвер оператора: снести его сбросом значило бы увести сторожа в idle ровно
// тем действием, которым человек чинит DNS.
func dnsLineKept(line string, keep []string) bool {
	if len(keep) == 0 {
		return false
	}
	f := strings.Fields(line)
	if len(f) < 3 {
		return false
	}
	id := f[2]
	for _, host := range keep {
		if host != "" && strings.Contains(id, host) {
			return true
		}
	}
	return false
}

// ownResolverHosts превращает адрес своего резолвера (cfg.DNSWatchdog.Endpoint,
// вида https://host/<секрет>) в список хостов для KeepHosts. Знание о том, что
// эндпоинт -- это URL, живёт здесь, а не в сборке агента.
//
// Секретный путь наружу не выносится: берётся только имя хоста. Пустой или
// неразбираемый адрес -> защищать нечего, и это НЕ ошибка: сторож может быть не
// настроен вовсе.
func ownResolverHosts(endpoint string) []string {
	endpoint = strings.TrimSpace(endpoint)
	if endpoint == "" {
		return nil
	}
	if u, err := url.Parse(endpoint); err == nil && u.Hostname() != "" {
		return []string{u.Hostname()}
	}
	// Адрес без схемы URL не разбирает как хост, а в конфиге он допустим.
	if host, _, err := net.SplitHostPort(endpoint); err == nil && host != "" {
		return []string{host}
	}
	if !strings.ContainsAny(endpoint, "/: ") {
		return []string{endpoint}
	}
	return nil
}

// writeDNSSnapshot кладёт конфиг «до» файлом на роутер и возвращает путь.
// Транскрипт команды живёт час и архивом прежних настроек не годится, а
// автоматической кнопки отката в этом цикле нет намеренно: откат -- мутация с
// клиентским вводом, ей нужна своя спека. Вместо кнопки -- файл и путь к нему.
func writeDNSSnapshot(dir, content string) (string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	name := filepath.Join(dir, fmt.Sprintf("dns-before-%d.txt", time.Now().Unix()))
	if err := os.WriteFile(name, []byte(content), 0o600); err != nil {
		return "", err
	}
	pruneDNSSnapshots(dir, 5)
	return name, nil
}

// pruneDNSSnapshots оставляет последние keep снимков: место на роутере
// ограничено, а бесконечная история снимков не нужна никому.
func pruneDNSSnapshots(dir string, keep int) {
	files, err := filepath.Glob(filepath.Join(dir, "dns-before-*.txt"))
	if err != nil || len(files) <= keep {
		return
	}
	// Имена содержат метку времени, поэтому лексикографический порядок совпадает
	// с хронологическим до 2286 года.
	for i := 0; i < len(files)-keep; i++ {
		_ = os.Remove(files[i])
	}
}

// dnsLineKey -- смысл строки dns-proxy: транспорт, кого спрашиваем и для какой
// зоны. Порт :853 у DoT и уточнение sni в ключ не входят: роутер вправе дописать
// или опустить их, и это та же самая строка.
//
// Именно этот ключ делает возможным «подтверждение по факту» вместо сравнения с
// ожидаемым текстом: форма, закреплённая в коде, была бы допущением о чужой
// прошивке, молча протухающим при её обновлении.
func dnsLineKey(line string) string {
	f := strings.Fields(line)
	if len(f) < 3 || f[1] != "upstream" || (f[0] != "tls" && f[0] != "https") {
		return ""
	}
	id := f[2]
	if f[0] == "tls" {
		if host, port, err := net.SplitHostPort(id); err == nil && port == "853" {
			id = host
		}
	}
	var zone string
	for i := 3; i+1 < len(f); i++ {
		if f[i] == "domain" {
			zone = f[i+1]
			break
		}
	}
	return f[0] + "|" + id + "|" + zone
}

// confirmDNSReferenceApplied перечитывает конфиг и проверяет, что каждая строка
// эталона там ЕСТЬ. Успех -- это найденная строка, а не отсутствие ошибки у
// команды: роутер мог принять команду и записать её иначе, а мог не принять
// вовсе. Возвращает число неподтверждённых строк.
func confirmDNSReferenceApplied(ctx context.Context, exec ExecFunc, b *strings.Builder, reference []string) int {
	b.WriteString("\nподтверждение по факту:\n")
	rc, err := exec(ctx, "ndmc", "-c", "show running-config")
	if err != nil {
		fmt.Fprintf(b, "  ✗ конфиг не перечитан: %v — применение НЕ подтверждено\n", err)
		return 1
	}
	present := make(map[string]bool)
	for _, line := range parseDNSProxyUpstreams(string(rc)) {
		if k := dnsLineKey(line); k != "" {
			present[k] = true
		}
	}
	var missing []string
	for _, want := range reference {
		if k := dnsLineKey(want); k == "" || !present[k] {
			missing = append(missing, want)
		}
	}
	if len(missing) == 0 {
		fmt.Fprintf(b, "  ✓ подтверждено: все %d строк эталона найдены в конфиге\n", len(reference))
		return 0
	}
	fmt.Fprintf(b, "  ✗ не применилось строк: %d\n", len(missing))
	for _, m := range missing {
		fmt.Fprintf(b, "      %s\n", m)
	}
	return len(missing)
}
