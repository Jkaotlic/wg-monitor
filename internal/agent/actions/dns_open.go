package actions

import (
	"context"
	"fmt"
	"net"
	"strings"
	"time"
)

// dnsOpenDial -- точка подмены для тестов: дозвон до сайта без сети проверить
// иначе нечем, а подпись DNSOpen менять ради этого не хочется.
var dnsOpenDial = net.DialTimeout

// dnsOpenTimeout -- бюджет на дозвон. Проверка отвечает человеку, который
// нажал кнопку и ждёт, поэтому лучше сказать «не открылся» быстро, чем висеть.
const dnsOpenTimeout = 4 * time.Second

// DNSOpen отвечает на вопрос «открывается ли сайт с этого роутера»: имя
// разрешается ЧЕРЕЗ dns-proxy самого роутера, затем на полученный адрес идёт
// TCP на 443.
//
// Разрешать имя резолвером агента было бы подменой вопроса: важно, что увидит
// человек за этим роутером, а не что видит агент со своей машины.
//
// Исходы разделены намеренно:
//
//	err     -- имя не разрешилось: чинить настройку DNS;
//	partial -- имя разрешилось, но сайт не открылся: чинить маршрут или
//	           разбираться с блокировкой;
//	ok      -- открылся.
//
// Слить два первых исхода в один значило бы послать человека чинить не то.
func DNSOpen(ctx context.Context, exec ExecFunc, domain string) (status, output string) {
	domain = strings.TrimSpace(domain)
	if domain == "" {
		return "err", "имя сайта не задано"
	}

	out, err := exec(ctx, "nslookup", domain, "127.0.0.1")
	addrs := parseNslookupAddrs(string(out))
	if err != nil || len(addrs) == 0 {
		var b strings.Builder
		fmt.Fprintf(&b, "Имя %s не разрешилось через dns-proxy роутера.\n", domain)
		if err != nil {
			fmt.Fprintf(&b, "Ошибка: %v\n", err)
		}
		if s := strings.TrimSpace(string(out)); s != "" {
			fmt.Fprintf(&b, "\nОтвет роутера:\n%s\n", s)
		}
		b.WriteString("\nЭто вопрос настройки DNS, а не маршрута.\n")
		return "err", b.String()
	}

	addr := addrs[0]
	conn, dialErr := dnsOpenDial("tcp", net.JoinHostPort(addr, "443"), dnsOpenTimeout)
	if dialErr != nil {
		return "partial", fmt.Sprintf(
			"Имя %s разрешилось в %s, но сайт не открыл соединение на 443.\nОшибка: %v\n\n"+
				"Имя работает — дело в маршруте или блокировке, а не в DNS.\n",
			domain, addr, dialErr)
	}
	_ = conn.Close()
	return "ok", fmt.Sprintf("Имя %s разрешилось в %s, сайт открылся (TCP 443).\n", domain, addr)
}

// parseNslookupAddrs достаёт адреса из вывода nslookup. Первым там идёт сам
// сервер (петля), поэтому петлевые адреса пропускаем: иначе проверка радостно
// открывала бы сама себя.
func parseNslookupAddrs(out string) []string {
	var addrs []string
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "Address") {
			continue
		}
		i := strings.Index(line, ":")
		if i < 0 {
			continue
		}
		for _, f := range strings.Fields(line[i+1:]) {
			ip := net.ParseIP(f)
			if ip == nil || ip.IsLoopback() {
				continue
			}
			addrs = append(addrs, ip.String())
			break
		}
	}
	return addrs
}
