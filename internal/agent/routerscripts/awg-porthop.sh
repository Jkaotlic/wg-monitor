#!/bin/sh
# awg-porthop — лечит AWG-туннель, чей UDP-поток убил ТСПУ, сменой исходящего порта.
# Смерть потока липкая и привязана к 5-кортежу; рестарт awg-manager порт сохраняет.
# Порядок важен: снять пира -> сменить порт -> вернуть пира, чтобы первым пакетом
# нового потока ушёл хендшейк с I1 (иначе новый поток убивается на 2-3 пакете).
#
#   awg-porthop.sh [--dry-run] [--once] [auto | iface ...]
#
# Без интерфейсов в аргументах — IFACES из porthop.conf (по умолчанию auto).
# auto: список перечитывается на каждом проходе — все интерфейсы `awg show interfaces`,
# у которых в allowed-ips есть 0.0.0.0/0. Без полного маршрута пинг в 8.8.8.8 через
# туннель не пройдёт никогда, и скрипт менял бы порт здоровому туннелю 6 раз в час.
# Жёсткий список устаревал: VPN-туннель пересоздан (руками или автопочинкой) —
# имя интерфейса другое, а скрипт сторожит прежнее.
#
# Источник — репозиторий wg-monitor (internal/agent/routerscripts), ставит агент
# командой porthop_install; ручная установка — README.md рядом.
# Переменные PORTHOP_* — только для тестов на чужой машине.

export PATH="${PORTHOP_PATH:-/opt/bin:/opt/sbin:/usr/sbin:/usr/bin:/sbin:/bin}"

INTERVAL=20          # с между проходами
FAILS_NEEDED=3       # подряд плохих проходов до лечения (~1 мин)
TRIES=3              # смен порта подряд, потом пауза
COOLDOWN=600         # с тишины после TRIES неудач
HOUR_MAX=6           # смен порта в час на интерфейс
TARGETS="8.8.8.8 1.1.1.1"
MAX_LOG_KB=64        # журнал держим не больше, хвост
LOG="${PORTHOP_LOG:-/opt/var/log/wg-monitor/porthop.log}"
# состояние -- на tmpfs: /opt -- флешка, а счётчики пишутся каждый проход;
# после перезагрузки счёт начинается заново, это не страшно
STATE="${PORTHOP_STATE:-/tmp/wg-monitor-porthop}"
CONF="${PORTHOP_CONF:-/opt/etc/wg-monitor/porthop.conf}"

DRY=0; ONCE=0; IFACES=""
for a in "$@"; do
  case "$a" in
    --dry-run) DRY=1 ;;
    --once) ONCE=1 ;;
    *) IFACES="$IFACES $a" ;;
  esac
done
# настройка: строка IFACES=auto или IFACES="opkgtun10 opkgtun12"; файл не исполняем
[ -n "$IFACES" ] || IFACES=$(sed -n 's/^IFACES=//p' "$CONF" 2>/dev/null | tail -n 1 | tr -d "\"'")
IFACES=$(echo $IFACES)
[ -n "$IFACES" ] || IFACES=auto
mkdir -p "$STATE" "$(dirname "$LOG")"

# Остановка (init stop) посреди смены порта оставила бы туннель без пира: снят,
# а вернуть некому. Поэтому внутри смены сигнал только запоминается.
BUSY=0; STOP=0
trap 'if [ "$BUSY" = 1 ]; then STOP=1; else exit 0; fi' TERM INT

log() {
  msg="$(date '+%F %T') $*"
  echo "$msg" >> "$LOG"
  [ "$ONCE" = 1 ] && echo "$msg"
  logger -t awg-porthop "$*" 2>/dev/null
}

trim_log() {
  max=$(( MAX_LOG_KB * 1024 ))
  size=$(wc -c < "$LOG" 2>/dev/null | tr -d ' ')
  [ -n "$size" ] && [ "$size" -gt "$max" ] || return 0
  # оставляем половину потолка: иначе каждый следующий проход снова упирался
  # бы в потолок и переписывал журнал целиком; хвост -- с первой целой строки
  tail -c $(( max / 2 )) "$LOG" | sed '1d' > "$LOG.tmp" && mv "$LOG.tmp" "$LOG"
}

# состояние интерфейса в файлах: fails, cooldown_until, hops (отметки времени смен)
get() { cat "$STATE/$1.$2" 2>/dev/null || echo "${3:-0}"; }
put() { [ "$(cat "$STATE/$1.$2" 2>/dev/null)" = "$3" ] || echo "$3" > "$STATE/$1.$2"; }   # пишем только перемену

# что сторожим на этом проходе
watched() {
  if [ "$IFACES" != auto ]; then echo "$IFACES"; return; fi
  for w in $(awg show interfaces 2>/dev/null); do
    awg show "$w" allowed-ips 2>/dev/null |
      awk '{ for (k = 2; k <= NF; k++) if ($k == "0.0.0.0/0") f = 1 } END { exit !f }' && echo "$w"
  done
}

hs_age() {
  hs=$(awg show "$1" latest-handshakes 2>/dev/null | awk 'NR==1{print $2}')
  [ -n "$hs" ] && [ "$hs" != 0 ] || { echo 99999; return; }
  echo $(( $(date +%s) - hs ))
}

ping_ok() {
  for t in $TARGETS; do
    ping -c 1 -W 3 -I "$1" "$t" >/dev/null 2>&1 && return 0
  done
  return 1
}

rand_port() {  # $1 = текущий порт, новый обязан отличаться
  while :; do
    n=$(hexdump -n2 -e '"%u"' /dev/urandom)   # busybox od не умеет -tu2 и молча даёт пусто
    case "$n" in ''|*[!0-9]*) n=$(awk -v s="$$$(date +%s)" 'BEGIN{srand(s); print int(rand()*65536)}') ;; esac
    p=$(( 20000 + n % 40000 ))
    [ "$p" != "$1" ] && { echo "$p"; return; }
  done
}

hops_last_hour() {
  now=$(date +%s); keep=""
  for t in $(get "$1" hops ""); do
    [ $(( now - t )) -lt 3600 ] && keep="$keep $t"
  done
  put "$1" hops "$keep"
  echo $keep | wc -w
}

hop() {  # $1 iface; 0 = поток ожил
  i=$1; old=$(awg show "$i" listen-port); new=$(rand_port "$old")
  if [ "$DRY" = 1 ]; then log "$i: [dry-run] сменил бы порт $old -> $new"; return 1; fi
  conf=$(mktemp "${TMPDIR:-/tmp}/porthop.XXXXXX") || return 1
  chmod 600 "$conf"
  awg showconf "$i" | awk '/^\[Peer\]/{p=1} p' > "$conf"
  pub=$(awg show "$i" peers | head -1)
  if [ -z "$pub" ] || ! grep -q '^\[Peer\]' "$conf"; then
    rm -f "$conf"; log "$i: нет пира, пропускаю"; return 1
  fi
  BUSY=1
  # пира, снятого успешно, возвращаем ВСЕГДА, даже если порт не сменился:
  # снятый и не возвращённый пир -- туннель без пира до перезапуска
  # (отступление от ручной копии, где `&&` на неудаче listen-port пира терял)
  if awg set "$i" peer "$pub" remove; then
    awg set "$i" listen-port "$new"; prc=$?
    [ "$prc" = 0 ] || log "$i: listen-port $new не сработал (rc=$prc), пира возвращаю"
    awg addconf "$i" "$conf"; arc=$?
    if [ "$prc" != 0 ]; then rc=$prc; else rc=$arc; fi
  else
    rc=$?
  fi
  rm -f "$conf"; BUSY=0
  put "$i" hops "$(get "$i" hops "") $(date +%s)"
  if [ "$rc" != 0 ]; then log "$i: ОШИБКА смены порта $old -> $new (rc=$rc)"; return 1; fi
  ping_ok "$i" >/dev/null   # трафик запускает хендшейк: первым в новом потоке идёт I1
  sleep 10
  age=$(hs_age "$i")
  if [ "$age" -lt 20 ] && ping_ok "$i"; then
    log "$i: порт $old -> $new, поток ожил (хендшейк ${age} с)"; return 0
  fi
  log "$i: порт $old -> $new, не ожил (хендшейк ${age} с)"; return 1
}

check() {
  i=$1; now=$(date +%s)
  awg show "$i" >/dev/null 2>&1 || { put "$i" fails 0; return; }   # туннель выключен/перезапускается
  age=$(hs_age "$i")
  # решает пинг через туннель: хендшейк у здорового бывает до ~140 с, по нему смерть видна слишком поздно
  if ping_ok "$i"; then
    [ "$(get "$i" fails)" -gt 0 ] && log "$i: снова жив (хендшейк ${age} с)"
    put "$i" fails 0; return
  fi
  f=$(( $(get "$i" fails) + 1 )); put "$i" fails "$f"
  log "$i: плохо $f/$FAILS_NEEDED (хендшейк ${age} с, пинг нет)"
  [ "$f" -ge "$FAILS_NEEDED" ] || return
  [ "$now" -ge "$(get "$i" cooldown)" ] || { log "$i: пауза после неудач, не трогаю"; return; }
  n=1
  while [ "$n" -le "$TRIES" ]; do
    if [ "$(hops_last_hour "$i")" -ge "$HOUR_MAX" ]; then
      log "$i: лимит $HOUR_MAX смен в час, жду"; put "$i" cooldown $(( now + COOLDOWN )); return
    fi
    hop "$i" && { put "$i" fails 0; return; }
    [ "$DRY" = 1 ] && return
    [ "$STOP" = 1 ] && return
    n=$(( n + 1 ))
  done
  put "$i" cooldown $(( $(date +%s) + COOLDOWN ))
  log "$i: $TRIES смены не помогли, пауза $COOLDOWN с"
}

log "старт: $IFACES$( [ "$DRY" = 1 ] && echo ' [dry-run]')"
seen="-"
while :; do
  list=$(echo $(watched))
  if [ "$IFACES" = auto ] && [ "$list" != "$seen" ]; then
    log "сторожу: ${list:-никого (нет VPN-туннеля с маршрутом 0.0.0.0/0)}"; seen=$list
  fi
  for i in $list; do
    check "$i"
    [ "$STOP" = 1 ] && exit 0
  done
  trim_log
  [ "$ONCE" = 1 ] && break
  sleep "$INTERVAL" & wait $!
done
