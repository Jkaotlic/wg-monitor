# Скрипты роутера

Шелл-скрипты, которые агент wg-monitor ставит на роутер (Keenetic/Netcraze + Entware). Файлы здесь —
единственный источник: агент встраивает их в себя (`go:embed`) и пишет на роутер как есть, подставляя
только значения из строк `ИМЯ=...` сверху. Те же файлы можно поставить руками.

## awg-porthop.sh — смена порта при блокировке

Лечит VPN-туннель AmneziaWG, чей UDP-поток убила блокировка: смерть потока привязана к паре портов, и
смена исходящего порта даёт новый поток. Каждые 20 с пинг через туннель (8.8.8.8, 1.1.1.1); три плохих
прохода подряд — снять пира, сменить порт на случайный 20000–59999, вернуть пира (порядок важен: первым
пакетом нового потока уходит хендшейк). Три неудачи подряд — пауза 10 минут; не больше 6 смен в час на
туннель. Журнал — `/opt/var/log/wg-monitor/porthop.log` (не больше 64 КБ: при переполнении остаётся половина; даты — со смещением пояса, `date '+%F %T %z'`), состояние —
`/tmp/wg-monitor-porthop/` (tmpfs: `/opt` — флешка, а счётчики меняются каждый проход; после перезагрузки
счёт начинается заново).

Что сторожить — `/opt/etc/wg-monitor/porthop.conf`:

Одна строка `IFACES=…`, без комментария в той же строке — хвост `# …` попал бы в значение.
Все интерфейсы с маршрутом 0.0.0.0/0, список перечитывается на каждом проходе:

```
IFACES=auto
```

Или явный список:

```
IFACES="opkgtun10 opkgtun12"
```

Проверить без изменений: `sh /opt/etc/wg-monitor/awg-porthop.sh --once --dry-run`.

### Поставить руками

```
mkdir -p /opt/etc/wg-monitor
cp awg-porthop.sh /opt/etc/wg-monitor/awg-porthop.sh
cp S99wg-monitor-porthop /opt/etc/init.d/S99wg-monitor-porthop
chmod 755 /opt/etc/wg-monitor/awg-porthop.sh /opt/etc/init.d/S99wg-monitor-porthop
echo 'IFACES=auto' > /opt/etc/wg-monitor/porthop.conf
/opt/etc/init.d/S99wg-monitor-porthop start
```

Снять: `/opt/etc/init.d/S99wg-monitor-porthop stop`, затем удалить три файла выше (журнал можно оставить).

### Ручная копия (до v0.57)

Ручная копия — `/opt/bin/awg-porthop.sh` под `/opt/etc/init.d/S99awg-porthop`. Две копии дрались бы за
один туннель, поэтому агент без явного согласия («Заменить ручную копию») ничего не ставит. С согласием он
останавливает её её же init и переносит init в `/opt/etc/wg-monitor/legacy/S99awg-porthop` (в init.d файл
на `S` запустился бы при загрузке снова). `/opt/bin/awg-porthop.sh` остаётся на месте.

Вернуть ручную копию:

```
/opt/etc/init.d/S99wg-monitor-porthop stop
mv /opt/etc/wg-monitor/legacy/S99awg-porthop /opt/etc/init.d/S99awg-porthop
/opt/etc/init.d/S99awg-porthop start
```

## S99wg-monitor-porthop — init

`start | stop | restart | status` по pid-файлу `/tmp/wg-monitor-porthop.pid`, а не по имени
процесса: ручная копия называется так же. Pid, доставшийся чужому процессу, своим не считается
(сверка `/proc/<pid>/cmdline` с путём скрипта), файл убирается. Остановка посреди смены порта доводит смену до конца.

## entware-cleanup.sh — очистка места

Удаляет файлы старше суток из `/opt/tmp`, `/opt/var/tmp`, кеша opkg и списков пакетов; при нехватке
памяти сбрасывает кеш страниц. Пакеты, конфиги и `/opt/etc` не трогает. Журнал —
`/opt/var/log/wg-monitor/entware-cleanup.log`. Агент ставит его по расписанию (cron) или запускает разово
(«Почистить сейчас»). Руками: `sh entware-cleanup.sh`.
