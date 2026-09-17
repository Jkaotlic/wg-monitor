package backend

import (
	"encoding/json"
	"regexp"
	"strings"

	"github.com/Jkaotlic/wg-monitor/internal/installtmpl"
	"github.com/Jkaotlic/wg-monitor/internal/releaseorigin"
)

// buildManualInstallCommand -- команда установки агента руками, по SSH на
// роутере (Entware), для пути «только выдать токен». Старый дашборд её не
// показывал вовсе -- только токен; приложение показывает готовый скрипт.
//
// Скрипт повторяет установку relay (internal/awgmrelay/awgm-relay.py,
// build_deferred_bootstrap_script): та же версия, что у бэкенда, с его же
// зеркала; config.yaml -- тех же ключей, что пишет relay; init-скрипт -- из
// installtmpl.
//
// Бинарь качается через зеркало /v1/releases/download, а оно подпись выпуска
// не проверяет (release_proxy.go). Поэтому checksums.txt тем же путём на
// роутере не скачивается -- сверка с ним ничего бы не защищала. Суммы sums
// сервер получает сам через проверку подписи (provisionChecksums) и вписывает
// в скрипт константами по архитектурам; роутер сверяет с ними sha256 бинаря.
//
// Пусто -- скачивать нечего, неоткуда или не с чем сверить: бэкенд без номера
// выпуска, адрес не https, нет проверенной суммы для одной из архитектур.
// Экран тогда покажет только токен.
func buildManualInstallCommand(backendURL, nickname, rawToken, version string, sums map[string]string) string {
	base := strings.TrimRight(strings.TrimSpace(backendURL), "/")
	tag, err := releaseorigin.ValidateReleaseTag(strings.TrimSpace(version))
	if err != nil || !strings.HasPrefix(base, "https://") || len(base) == len("https://") {
		return ""
	}
	wantArm64 := strings.ToLower(strings.TrimSpace(sums["wg-monitor-agent-linux-arm64"]))
	wantMipsle := strings.ToLower(strings.TrimSpace(sums["wg-monitor-agent-linux-mipsle"]))
	if !sha256HexRe.MatchString(wantArm64) || !sha256HexRe.MatchString(wantMipsle) {
		return ""
	}
	config := strings.Join([]string{
		"backend:",
		"  url: " + yamlQuoted(base),
		"  token: " + yamlQuoted(rawToken),
		"",
		"agent:",
		"  nickname: " + yamlQuoted(nickname),
		"  interval_sec: 60",
		"",
		"awg_manager:",
		"  base_url: http://127.0.0.1:2222",
		"",
		"checks:",
		"  dns:",
		"    auto_discover: true",
		`    test_domain: "example.com"`,
		"    fail_threshold: 2",
		"",
		"state:",
		"  path: /opt/var/wg-monitor/state.json",
		"",
		"maintenance:",
		"  allow_router_reboot: true",
		"  allow_firmware_install: true",
	}, "\n")
	return strings.Join([]string{
		"set -eu",
		"NICKNAME=" + shellSingleQuote(nickname),
		"BASE=" + shellSingleQuote(base+"/v1/releases/download/"+tag),
		`case "$(uname -m)" in`,
		`  aarch64|arm64) ARCH=arm64 ;;`,
		`  mips|mipsel|mipsle) ARCH=mipsle ;;`,
		`  *) echo "wg-monitor: архитектура $(uname -m) не поддерживается"; exit 1 ;;`,
		`esac`,
		`ASSET="wg-monitor-agent-linux-$ARCH"`,
		`CONFIG=/opt/etc/wg-monitor/config.yaml`,
		`INIT=/opt/etc/init.d/S99wg-monitor`,
		`if [ ! -d /opt ]; then echo "wg-monitor: Entware (/opt) не смонтирован"; exit 10; fi`,
		`if [ -f "$CONFIG" ]; then`,
		`  echo "wg-monitor: агент уже установлен ($CONFIG) -- для него в приложении есть «Переустановить агент»"`,
		`  exit 11`,
		`fi`,
		`mkdir -p /opt/etc/wg-monitor /opt/var/wg-monitor /opt/var/run /opt/tmp /opt/bin /opt/etc/init.d`,
		`fetch() {`,
		`  if command -v curl >/dev/null 2>&1; then curl -fsSL "$1" -o "$2"`,
		`  elif command -v wget >/dev/null 2>&1; then wget -q "$1" -O "$2"`,
		`  else echo "wg-monitor: нет ни curl, ни wget"; exit 12; fi`,
		`}`,
		`case "$ARCH" in`,
		`  arm64) WANT=` + shellSingleQuote(wantArm64) + ` ;;`,
		`  mipsle) WANT=` + shellSingleQuote(wantMipsle) + ` ;;`,
		`esac`,
		`fetch "$BASE/$ASSET" /opt/tmp/wg-monitor.new`,
		`GOT=$(sha256sum /opt/tmp/wg-monitor.new | awk '{print $1}')`,
		`if [ -z "$WANT" ] || [ "$WANT" != "$GOT" ]; then echo "wg-monitor: контрольная сумма не совпала"; rm -f /opt/tmp/wg-monitor.new; exit 14; fi`,
		`chmod 755 /opt/tmp/wg-monitor.new`,
		`mv /opt/tmp/wg-monitor.new /opt/bin/wg-monitor`,
		`cat >"$CONFIG" <<'WG_MONITOR_AGENT_CONFIG'`,
		config,
		`WG_MONITOR_AGENT_CONFIG`,
		`chmod 600 "$CONFIG"`,
		`cat >"$INIT" <<'WG_MONITOR_INIT'`,
		installtmpl.InitScript(),
		`WG_MONITOR_INIT`,
		`chmod 755 "$INIT"`,
		`"$INIT" restart || "$INIT" start`,
		`echo "wg-monitor: агент $NICKNAME установлен"`,
	}, "\n") + "\n"
}

// yamlQuoted -- строка YAML в двойных кавычках. JSON-строка -- корректный
// YAML-скаляр; так же кавычит relay (yaml_quote = json.dumps).
func yamlQuoted(s string) string {
	raw, _ := json.Marshal(s)
	return string(raw)
}

// sha256HexRe -- сумма sha256 строкой: 64 шестнадцатеричных знака. Всё прочее в
// скрипт не вписывается, даже в кавычках.
var sha256HexRe = regexp.MustCompile(`^[0-9a-f]{64}$`)
