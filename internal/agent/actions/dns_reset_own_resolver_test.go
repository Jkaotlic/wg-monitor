package actions

import (
	"reflect"
	"testing"
)

// Адрес своего резолвера приходит в диспетчер сырым (cfg.DNSWatchdog.Endpoint),
// и превратить его в хост -- единственное место, где можно ошибиться молча:
// вернёшь пустое -- и боевой сброс снесёт собственный резолвер оператора,
// уведя сторожа в idle ровно тем действием, которым человек чинит DNS.
func TestOwnResolverHostsFromEndpoint(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want []string
	}{
		{"URL с секретным путём -- берём только хост", "https://dns.example.com/s3cret", []string{"dns.example.com"}},
		{"URL с портом", "https://dns.example.com:8443/s3cret", []string{"dns.example.com"}},
		{"голый хост без схемы", "dns.example.com", []string{"dns.example.com"}},
		{"хост с портом без схемы", "dns.example.com:853", []string{"dns.example.com"}},
		{"пусто -- защищать нечего, это не ошибка", "", nil},
		{"пробелы вокруг", "  https://dns.example.com/x  ", []string{"dns.example.com"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := ownResolverHosts(c.in)
			if !reflect.DeepEqual(got, c.want) {
				t.Errorf("ownResolverHosts(%q) = %#v, хотим %#v", c.in, got, c.want)
			}
		})
	}
}

// Секрет из адреса не должен утечь в список защищаемых хостов: KeepHosts
// попадает в транскрипт команды, который видит человек.
func TestOwnResolverHostsDropsSecretPath(t *testing.T) {
	for _, h := range ownResolverHosts("https://dns.example.com/very-secret-path") {
		if h == "" || h != "dns.example.com" {
			t.Errorf("в KeepHosts попало лишнее: %q", h)
		}
	}
}
