package backend

import (
	"encoding/json"
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
// зеркала; sha256 сверяется по checksums.txt; config.yaml -- тех же ключей,
// что пишет relay; init-скрипт -- из installtmpl. Подпись checksums.txt на
// роутере не проверяется (нечем): файл и бинарь приходят с этого же сервера по
// https, а сервер свою копию подписью проверяет при установке через панель.
//
// Пусто -- бэкенд собран без номера выпуска или без публичного адреса:
// скачивать нечего и неоткуда, экран покажет только токен.
func buildManualInstallCommand(backendURL, nickname, rawToken, version string) string {
	base := strings.TrimRight(strings.TrimSpace(backendURL), "/")
	tag, err := releaseorigin.ValidateReleaseTag(strings.TrimSpace(version))
	if base == "" || err != nil {
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
		`fetch "$BASE/$ASSET" /opt/tmp/wg-monitor.new`,
		`fetch "$BASE/checksums.txt" /opt/tmp/wg-monitor.sums`,
		`WANT=$(awk -v a="$ASSET" '$2==a || $2=="*"a {print $1}' /opt/tmp/wg-monitor.sums)`,
		`GOT=$(sha256sum /opt/tmp/wg-monitor.new | awk '{print $1}')`,
		`rm -f /opt/tmp/wg-monitor.sums`,
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
