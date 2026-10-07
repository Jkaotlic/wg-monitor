package routerscripts

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// Скрипт смены порта гоняется настоящим sh с поддельными awg, ping, logger,
// sleep и date в PATH. Подделки держат состояние в файлах каталога FAKE:
// какие интерфейсы есть, их allowed-ips, порт, хендшейк, отвечает ли пинг.
// Каждый вызов awg дописывается в FAKE/calls -- по нему проверяется порядок
// «снять пира -> сменить порт -> вернуть пира».

const fakeAwg = `#!/bin/sh
echo "$*" >> "$FAKE/calls"
if [ "$1" = show ] && [ "$2" = interfaces ]; then cat "$FAKE/ifaces"; exit 0; fi
i=$2
case " $(cat "$FAKE/ifaces") " in *" $i "*) ;; *) exit 1 ;; esac
case "$1" in
show)
  case "$3" in
  '') echo "interface: $i" ;;
  allowed-ips) cat "$FAKE/$i.allowed" ;;
  latest-handshakes) printf 'PUBKEY\t%s\n' "$(cat "$FAKE/$i.hs" 2>/dev/null || echo 0)" ;;
  listen-port) cat "$FAKE/$i.port" ;;
  peers) echo PUBKEY ;;
  esac ;;
showconf)
  printf '[Interface]\nPrivateKey = x\nListenPort = %s\n\n[Peer]\nPublicKey = PUBKEY\nEndpoint = 203.0.113.5:51820\nAllowedIPs = 0.0.0.0/0\n' "$(cat "$FAKE/$i.port")" ;;
set)
  if [ "$3" = listen-port ]; then
    [ -f "$FAKE/$i.portslow" ] && { touch "$FAKE/inhop"; /bin/sleep 1; }
    [ -f "$FAKE/$i.portfail" ] && exit 1
    echo "$4" > "$FAKE/$i.port"
  fi ;;
addconf)
  grep -q '^\[Peer\]' "$3" || exit 1
  if [ -f "$FAKE/$i.heal" ]; then touch "$FAKE/$i.pingok"; cat "$FAKE/now" > "$FAKE/$i.hs"; fi ;;
esac
exit 0
`

const fakePing = `#!/bin/sh
# ping -c 1 -W 3 -I <iface> <target>
echo "$*" >> "$FAKE/pings"
while [ $# -gt 0 ]; do [ "$1" = -I ] && { iface=$2; break; }; shift; done
[ -f "$FAKE/$iface.pingok" ]
`

const fakeDate = `#!/bin/sh
if [ "$1" = +%s ]; then cat "$FAKE/now"; exit 0; fi
exec /bin/date "$@"
`

type porthopEnv struct {
	t      *testing.T
	fake   string
	script string
	log    string
	state  string
	conf   string
}

func newPorthopEnv(t *testing.T) *porthopEnv {
	t.Helper()
	for _, tool := range []string{"sh", "hexdump", "awk", "sed", "mktemp", "tail", "wc"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("нет %s на этой машине: скрипт смены порта гонять нечем", tool)
		}
	}
	root := t.TempDir()
	e := &porthopEnv{
		t:      t,
		fake:   filepath.Join(root, "fake"),
		script: filepath.Join(root, "awg-porthop.sh"),
		log:    filepath.Join(root, "log", "porthop.log"),
		state:  filepath.Join(root, "state"),
		conf:   filepath.Join(root, "porthop.conf"),
	}
	bin := filepath.Join(root, "bin")
	for _, d := range []string{e.fake, bin, filepath.Join(root, "tmp")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for name, body := range map[string]string{
		"awg":    fakeAwg,
		"ping":   fakePing,
		"date":   fakeDate,
		"logger": "#!/bin/sh\nexit 0\n",
		"sleep":  "#!/bin/sh\necho \"$*\" >> \"$FAKE/sleeps\"\n",
	} {
		if err := os.WriteFile(filepath.Join(bin, name), []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(e.script, []byte(Porthop), 0o755); err != nil {
		t.Fatal(err)
	}
	e.write("now", "1760000000")
	return e
}

func (e *porthopEnv) write(name, body string) {
	e.t.Helper()
	if err := os.WriteFile(filepath.Join(e.fake, name), []byte(body+"\n"), 0o644); err != nil {
		e.t.Fatal(err)
	}
}

func (e *porthopEnv) read(name string) string {
	b, _ := os.ReadFile(filepath.Join(e.fake, name))
	return string(b)
}

// iface заводит интерфейс: allowed-ips, порт, хендшейк давно, пинг по ok.
func (e *porthopEnv) iface(name, allowed string, port int, pingOK bool) {
	e.t.Helper()
	cur := strings.TrimSpace(e.read("ifaces"))
	e.write("ifaces", strings.TrimSpace(cur+" "+name))
	e.write(name+".allowed", "PUBKEY\t"+allowed)
	e.write(name+".port", strconv.Itoa(port))
	e.write(name+".hs", "1759999000")
	if pingOK {
		e.write(name+".pingok", "")
	}
}

func (e *porthopEnv) run(args ...string) string {
	e.t.Helper()
	cmd := exec.Command("sh", append([]string{e.script}, args...)...)
	root := filepath.Dir(e.script)
	cmd.Env = append(os.Environ(),
		"FAKE="+e.fake,
		"PORTHOP_PATH="+filepath.Join(root, "bin")+":/usr/bin:/bin",
		"PORTHOP_LOG="+e.log,
		"PORTHOP_STATE="+e.state,
		"PORTHOP_CONF="+e.conf,
		"TMPDIR="+filepath.Join(root, "tmp"),
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		e.t.Fatalf("script failed: %v\n%s", err, out)
	}
	return string(out)
}

func (e *porthopEnv) logText() string {
	b, _ := os.ReadFile(e.log)
	return string(b)
}

func TestPorthopAutoWatchesOnlyFullRouteInterfaces(t *testing.T) {
	e := newPorthopEnv(t)
	e.iface("opkgtun10", "0.0.0.0/0", 30000, true)
	e.iface("opkgtun12", "0.0.0.0/0 ::/0", 30001, true)
	e.iface("nwg0", "10.8.0.0/24", 30002, true)
	e.iface("opkgtun13", "10.0.0.0/0x", 30003, true) // похожее, но не 0.0.0.0/0
	if err := os.WriteFile(e.conf, []byte("IFACES=auto\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	for _, args := range [][]string{{"--once"}, {"--once", "auto"}} {
		_ = os.Remove(filepath.Join(e.fake, "pings"))
		e.run(args...)
		pings := e.read("pings")
		for _, want := range []string{"-I opkgtun10 ", "-I opkgtun12 "} {
			if !strings.Contains(pings, want) {
				t.Fatalf("%v: %s not watched; pings:\n%s", args, want, pings)
			}
		}
		for _, bad := range []string{"-I nwg0 ", "-I opkgtun13 "} {
			if strings.Contains(pings, bad) {
				t.Fatalf("%v: %s watched without 0.0.0.0/0; pings:\n%s", args, bad, pings)
			}
		}
	}
	if !strings.Contains(e.logText(), "сторожу: opkgtun10 opkgtun12") {
		t.Fatalf("watched set not logged:\n%s", e.logText())
	}
}

// Пустая настройка -- тоже auto; явный список сторожится как есть.
func TestPorthopDefaultIsAutoAndExplicitListIsKept(t *testing.T) {
	e := newPorthopEnv(t)
	e.iface("opkgtun10", "0.0.0.0/0", 30000, true)
	e.iface("nwg0", "10.8.0.0/24", 30002, true)

	e.run("--once")
	if p := e.read("pings"); !strings.Contains(p, "-I opkgtun10 ") || strings.Contains(p, "-I nwg0 ") {
		t.Fatalf("no conf: want auto; pings:\n%s", p)
	}

	_ = os.Remove(filepath.Join(e.fake, "pings"))
	if err := os.WriteFile(e.conf, []byte("IFACES=\"nwg0\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	e.run("--once")
	if p := e.read("pings"); !strings.Contains(p, "-I nwg0 ") || strings.Contains(p, "-I opkgtun10 ") {
		t.Fatalf("conf list: want only nwg0; pings:\n%s", p)
	}
}

var awgCallRe = regexp.MustCompile(`(?m)^(set opkgtun10 peer PUBKEY remove|set opkgtun10 listen-port (\d+)|addconf opkgtun10 \S+)$`)

func TestPorthopHopsAfterThreeBadPassesInOrder(t *testing.T) {
	e := newPorthopEnv(t)
	e.iface("opkgtun10", "0.0.0.0/0", 30000, false)
	e.write("opkgtun10.heal", "")

	e.run("--once")
	e.run("--once")
	if strings.Contains(e.read("calls"), "listen-port 3") || strings.Contains(e.read("calls"), "remove") {
		t.Fatalf("hopped before 3 bad passes:\n%s", e.read("calls"))
	}
	e.run("--once")

	got := awgCallRe.FindAllStringSubmatch(e.read("calls"), -1)
	if len(got) != 3 {
		t.Fatalf("want remove, listen-port, addconf; calls:\n%s", e.read("calls"))
	}
	if got[0][1] != "set opkgtun10 peer PUBKEY remove" || !strings.HasPrefix(got[1][1], "set opkgtun10 listen-port ") || !strings.HasPrefix(got[2][1], "addconf opkgtun10 ") {
		t.Fatalf("wrong order: %v", got)
	}
	port, _ := strconv.Atoi(got[1][2])
	if port == 30000 || port < 20000 || port > 59999 {
		t.Fatalf("new port %d: want != 30000 and in 20000-59999", port)
	}
	log := e.logText()
	if !strings.Contains(log, fmt.Sprintf("opkgtun10: порт 30000 -> %d, поток ожил", port)) {
		t.Fatalf("no recovered line:\n%s", log)
	}
	if f := strings.TrimSpace(readFile(t, filepath.Join(e.state, "opkgtun10.fails"))); f != "0" {
		t.Fatalf("fails after recovery = %q", f)
	}
	// Временный конфиг пира убран.
	if left, _ := filepath.Glob(filepath.Join(filepath.Dir(e.script), "tmp", "porthop.*")); len(left) != 0 {
		t.Fatalf("temp peer config left: %v", left)
	}
}

func TestPorthopCooldownAfterThreeFailedHops(t *testing.T) {
	e := newPorthopEnv(t)
	e.iface("opkgtun10", "0.0.0.0/0", 30000, false)

	for i := 0; i < 3; i++ {
		e.run("--once")
	}
	if n := strings.Count(e.read("calls"), "listen-port "); n != 3 {
		t.Fatalf("want 3 hops, got %d:\n%s", n, e.read("calls"))
	}
	log := e.logText()
	if strings.Count(log, "не ожил") != 3 || !strings.Contains(log, "3 смены не помогли, пауза 600 с") {
		t.Fatalf("no cooldown:\n%s", log)
	}

	e.run("--once")
	if n := strings.Count(e.read("calls"), "listen-port "); n != 3 {
		t.Fatalf("hopped during cooldown:\n%s", e.read("calls"))
	}
	if !strings.Contains(e.logText(), "пауза после неудач, не трогаю") {
		t.Fatalf("cooldown not logged:\n%s", e.logText())
	}
	// Пауза прошла -- снова лечит.
	e.write("now", "1760000601")
	e.run("--once")
	if n := strings.Count(e.read("calls"), "listen-port "); n == 3 {
		t.Fatalf("no hop after cooldown:\n%s", e.read("calls"))
	}
}

func TestPorthopHourLimit(t *testing.T) {
	e := newPorthopEnv(t)
	e.iface("opkgtun10", "0.0.0.0/0", 30000, false)
	if err := os.MkdirAll(e.state, 0o755); err != nil {
		t.Fatal(err)
	}
	// Шесть смен за последний час, одна старая -- не в счёт.
	hops := "1759990000 1759997000 1759997100 1759997200 1759997300 1759997400 1759997500"
	for name, v := range map[string]string{"opkgtun10.hops": hops, "opkgtun10.fails": "2"} {
		if err := os.WriteFile(filepath.Join(e.state, name), []byte(v+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	e.run("--once")

	if strings.Contains(e.read("calls"), "listen-port ") {
		t.Fatalf("hopped over the hour limit:\n%s", e.read("calls"))
	}
	if !strings.Contains(e.logText(), "лимит 6 смен в час, жду") {
		t.Fatalf("limit not logged:\n%s", e.logText())
	}
	if c := strings.TrimSpace(readFile(t, filepath.Join(e.state, "opkgtun10.cooldown"))); c != "1760000600" {
		t.Fatalf("cooldown = %q", c)
	}
}

func TestPorthopDryRunChangesNothing(t *testing.T) {
	e := newPorthopEnv(t)
	e.iface("opkgtun10", "0.0.0.0/0", 30000, false)

	for i := 0; i < 3; i++ {
		e.run("--once", "--dry-run")
	}

	calls := e.read("calls")
	for _, bad := range []string{"remove", "listen-port 2", "listen-port 3", "listen-port 4", "listen-port 5", "addconf"} {
		if strings.Contains(calls, " "+bad) || strings.HasPrefix(calls, bad) {
			t.Fatalf("dry-run mutated (%s):\n%s", bad, calls)
		}
	}
	if strings.TrimSpace(e.read("opkgtun10.port")) != "30000" {
		t.Fatal("port changed in dry-run")
	}
	if !strings.Contains(e.logText(), "[dry-run] сменил бы порт 30000 -> ") {
		t.Fatalf("no dry-run line:\n%s", e.logText())
	}
}

func TestPorthopTrimsLog(t *testing.T) {
	e := newPorthopEnv(t)
	e.iface("opkgtun10", "0.0.0.0/0", 30000, true)
	if err := os.MkdirAll(filepath.Dir(e.log), 0o755); err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	for b.Len() < 80*1024 {
		fmt.Fprintf(&b, "2026-10-01 12:00:00 opkgtun10: плохо 1/3 (хендшейк 99999 с, пинг нет) строка %d\n", b.Len())
	}
	if err := os.WriteFile(e.log, []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}

	e.run("--once")

	log := e.logText()
	if len(log) > 64*1024 {
		t.Fatalf("log not trimmed: %d bytes", len(log))
	}
	if !strings.HasPrefix(log, "2026-10-01 12:00:00 ") {
		t.Fatalf("trimmed log starts mid-line: %q", log[:60])
	}
	if !strings.Contains(log, "старт: auto") {
		t.Fatal("fresh lines lost by trimming")
	}
}

func readFile(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// Порт не сменился (listen-port упал) -- пира всё равно возвращаем: снятый
// и не возвращённый пир -- туннель без пира до перезапуска.
func TestPorthopReaddsPeerWhenListenPortFails(t *testing.T) {
	e := newPorthopEnv(t)
	e.iface("opkgtun10", "0.0.0.0/0", 30000, false)
	e.write("opkgtun10.portfail", "")
	seedFails(t, e, "opkgtun10", "2")

	e.run("--once")

	calls := e.read("calls")
	rm := strings.Index(calls, "set opkgtun10 peer PUBKEY remove")
	lp := strings.Index(calls, "set opkgtun10 listen-port ")
	add := strings.Index(calls, "addconf opkgtun10 ")
	if rm < 0 || lp < rm || add < lp {
		t.Fatalf("peer not re-added after failed listen-port:\n%s", calls)
	}
	if strings.Count(calls, "addconf opkgtun10 ") != strings.Count(calls, "peer PUBKEY remove") {
		t.Fatalf("every remove needs its addconf:\n%s", calls)
	}
	log := e.logText()
	// Одна неудачная смена -- одна строка «ОШИБКА смены порта»: по ней агент
	// считает неудачи за сутки.
	if !strings.Contains(log, "listen-port ") || !strings.Contains(log, "пира возвращаю") ||
		strings.Count(log, "ОШИБКА смены порта") != strings.Count(calls, "peer PUBKEY remove") {
		t.Fatalf("listen-port failure not logged once per hop:\n%s", log)
	}
}

func seedFails(t *testing.T, e *porthopEnv, iface, n string) {
	t.Helper()
	if err := os.MkdirAll(e.state, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(e.state, iface+".fails"), []byte(n+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}
