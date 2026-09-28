package keenetic

import "testing"

// rcWAN -- сверка С3 на workrouter, KeenOS 5.02.A.9 (28.09.2026): вывод
// `show running-config`, блоки интерфейсов WAN и провайдера (сокращено,
// адреса санитизированы поставщиком фикстуры). GigabitEthernet1 (провайдер,
// priority 58248) без Ping-Check; CdcEthernet0 (резерв, priority 36405) --
// с профилем; OpkgTun* -- туннели Entware, тоже с `ip global`, но не WAN.
const rcWAN = `
interface GigabitEthernet1
    description "\xd0\x9f\xd0\xbe\xd0\xb4\xd0\xba\xd0\xbb\xd1\x8e\xd1\x87\xd0\xb5\xd0\xbd\xd0\xb8\xd0\xb5 Ethernet"
    ip global 58248
!
interface CdcEthernet0
    description "Huawei Mobile Broadband"
    ip global 36405
    ping-check profile default
!
interface OpkgTun10
    description macoffice
    ip global 4550
!
interface OpkgTun11
    description macmini(15)
    ip global 2275
!
`

func TestParsePingCheckByPriority(t *testing.T) {
	got := ParsePingCheckByPriority(rcWAN)
	want := map[int]string{58248: "", 36405: "default", 4550: "", 2275: ""}
	if len(got) != len(want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Fatalf("priority %d = %q, want %q (got %+v)", k, got[k], v, got)
		}
	}
}

func TestParsePingCheckByPriorityEmptyConfig(t *testing.T) {
	if got := ParsePingCheckByPriority(""); len(got) != 0 {
		t.Fatalf("got %+v", got)
	}
}

func TestParsePingCheckByPriorityOldFirmwareFormat(t *testing.T) {
	// Прошивка другого формата / мусор вместо конфига не должны обрушить
	// парсер -- просто сопоставлять нечего.
	got := ParsePingCheckByPriority("garbage\nnot a config at all\n\tindented nonsense\n")
	if len(got) != 0 {
		t.Fatalf("got %+v", got)
	}
}

func TestWANPingCheckByPriority(t *testing.T) {
	// Форма (name -> priority) -- как agent строит её из /api/wan/status
	// (см. wanfacts.wanServer's liveWAN: eth3=58248, cdc_br0=36405).
	priorities := map[string]int{"eth3": 58248, "cdc_br0": 36405, "apcli0": 0}
	got := WANPingCheckByPriority(rcWAN, priorities)
	if p, ok := got["eth3"]; !ok || p != "" {
		t.Fatalf(`eth3 = %q, %v; want "" (профиля нет), true`, p, ok)
	}
	if got["cdc_br0"] != "default" {
		t.Fatalf("cdc_br0 = %q, want %q", got["cdc_br0"], "default")
	}
	if _, ok := got["apcli0"]; ok {
		t.Fatal("priority 0 не участвует -- ключа для apcli0 быть не должно")
	}
}

func TestWANPingCheckByPriorityNoMatch(t *testing.T) {
	// Приоритет из /api/wan/status не встретился в running-config (роутер
	// другой модели, конфиг устарел) -- молча ничего, не паника.
	got := WANPingCheckByPriority(rcWAN, map[string]int{"eth9": 99999})
	if len(got) != 0 {
		t.Fatalf("got %+v", got)
	}
}

func TestWANPingCheckByPriorityUnreadableConfig(t *testing.T) {
	got := WANPingCheckByPriority("", map[string]int{"eth3": 58248})
	if len(got) != 0 {
		t.Fatalf("got %+v", got)
	}
}
