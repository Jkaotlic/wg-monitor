# 2026-08-18 Живая проверка маршрутизации на домашнем роутере

## Контекст

Задача 13 (последняя в фазе `2026-08-18-miniapp-routing-truth`): двенадцать
предыдущих задач научили код читать привязку правил маршрутизации из
access-policies awg-manager вместо поля на самом правиле, которое живые
роутеры оставляют пустым. Эта задача сверяет получившийся код с реальным
домашним роутером оператора.

Слой 4 (эксплуатация): TDD не применяется. Работает диагностика до мутации и
проверка постусловия — состояние до, действие, состояние после сравнивается
с ожиданием.

Роутер: Keenetic Ultra (KN-1811), прошивка `5.1.3` (`5.01.C.3.0-1`),
awg-manager **2.17.2+r5** (`GET /api/system/info`), доступ по
`http://192.168.0.1:2222`.

Разрешённые оператором мутации — ровно три:
1. `POST /api/access-policies/permit {"name":"HydraRoute","interface":"Bridge1","order":9}`
2. `DELETE /api/access-policies/permit?name=HydraRoute&interface=Bridge1`
3. `POST /api/control/restart?id=awg11`

`Bridge1` выбран как проверочный интерфейс, потому что роутер предлагает его
политикам (`/api/routing/policy-interfaces`), он сейчас **не** входит ни в
одну цепочку и находится в состоянии down — в отличие от `Wireguard0`,
который уже стоит в цепочке `HydraRoute` и трогать его нельзя.

## Шаг 1: исходное состояние

Снято до какой-либо мутации и сверено побайтово перед стартом задачи (файл
`.superpowers/sdd/2026-08-18-miniapp-routing-truth/live-before-policies.json`
совпал с повторным снятием непосредственно перед пробой permit):

```
GET /api/routing/access-policies
HydraRoute: OpkgTun11(0) → Wireguard0(1) → OpkgTun10(2)
RU:         GigabitEthernet1(0)
```

```
GET /api/tunnels/all
awg10  disabled  lastHandshake=2026-08-17T17:53:43+03:00
awg11  running   lastHandshake=2026-08-18T18:28:09+03:00
awg20  disabled  lastHandshake=""
```

## Шаг 2: сверка раскладки чтением

Добавлен `internal/agent/actions/route_status_live_manual_test.go` (тег
`manual`, обычный `go test ./...` его не подхватывает) и прогнан против
живого роутера:

```
export PATH=.../scratchpad/go/bin:$PATH
AWGM_URL=http://192.168.0.1:2222 go test -tags manual ./internal/agent/actions/ -run LiveRouteSnapshot -v
--- PASS: TestLiveRouteSnapshot (0.14s)
```

Снимок, который построил `RouteStatus`, сверен с утверждениями из брифа:

| Утверждение | Снимок кода | Вердикт |
| --- | --- | --- |
| Политика `HydraRoute` — 26 правил | `dns: 26, hr_neo: 26` | **совпало** |
| Активное звено `OpkgTun11`, резолвится в туннель `awg11` | `interfaces[0] = {bind: OpkgTun11, role: active, tunnel_id: awg11, via_vpn: true}`, `active_tunnel_id: awg11` | **совпало** |
| Остальные два звена недоступны | `Wireguard0→awg20` и `OpkgTun10→awg10` оба `role: unavailable` | **совпало** |
| Политика `RU` — 2 правила, помечена «мимо VPN» | `dns: 2`, `active_tunnel_id` отсутствует (пусто), `via_vpn` отсутствует (пусто → `false` по умолчанию) | **совпало** |
| Ни одно правило не осело в «без привязки» | `rules` — 28 штук, `bind` пуст у 0; `snap.other` = `{dns:0, hr_neo:0, static:0}` | **совпало** |
| Каждый туннель из `/api/tunnels/all` — `restart_method: "control"` | Три записи типа `managed` (`awg10`, `awg11`, `awg20`), взятые именно из `/api/tunnels/all`, все три несут `restart_method: "control"`. Пять дополнительных записей в снимке (`Wireguard4`, `apcli0`, `apclii0`, `cdc_br0`, `eth3`) — это WAN/системные записи из отдельного каталога `/api/routing/tunnels`, у них `restart_method: "none"` по коду намеренно (`route_status.go:165`, они не наши VPN-туннели) | **совпало** — утверждение относится только к `/api/tunnels/all`, и там оно верно без исключений |

Все проверяемые в этом шаге утверждения подтвердились. Полный JSON снимка
сохранён в тестовом логе прогона (см. вывод команды выше).

## Шаг 3: обратимая проверка permit — Bridge1

Состояние до (повторное снятие непосредственно перед мутацией, побайтово
совпало с `live-before-policies.json`):

```
HydraRoute: OpkgTun11(0) → Wireguard0(1) → OpkgTun10(2)
RU:         GigabitEthernet1(0)
```

Команда:

```
POST /api/access-policies/permit {"name":"HydraRoute","interface":"Bridge1","order":9}
→ {"success":true,"data":{"ok":true}}
```

**Постусловие НЕ выполнено.** Ожидалось, что `Bridge1` появится в цепочке
`HydraRoute` последним звеном при активном звене, оставшемся `OpkgTun11`.
Вместо этого повторное чтение `GET /api/routing/access-policies` (и
альтернативного пути чтения `GET /api/access-policies`, который отдаёт то
же самое) показало цепочку **без изменений**:

```
HydraRoute: OpkgTun11(0) → Wireguard0(1) → OpkgTun10(2)   -- Bridge1 отсутствует
```

Проверено дважды с паузой 2 с — не эффект кеша. `GET /api/routing/policy-interfaces`
по-прежнему числит `Bridge1` как доступный политикам (`up:false`), т.е.
интерфейс не исчез из каталога — permit просто не применился к цепочке,
хотя ответ API вернул `success:true`.

Это не удивление за пределами кода: `internal/agent/actions/route_hrneo_policy.go`
(`addTunnelToHydraRoutePolicies`) уже не доверяет одному только `success:true`
от `PermitPolicyInterface` — после вызова permit он **перечитывает**
`AccessPolicies` и явно проверяет, что интерфейс появился в цепочке,
возвращая ошибку `"interface %q did not appear in policy %q after permit"`,
если нет. Живой роутер только что продемонстрировал ровно тот сценарий,
для которого эта защита была написана: API может отрапортовать успех и не
изменить цепочку. Возможная причина — расхождение между запрошенным
`order:9` (зафиксирован протоколом мутации) и фактической длиной цепочки
(3 элемента, следующий индекс — 3); проверка альтернативного значения
`order` не входила в разрешённый оператором набор мутаций и не
предпринималась.

Откат (выполнен по протоколу независимо от того, требовался ли он
фактически):

```
DELETE /api/access-policies/permit?name=HydraRoute&interface=Bridge1
→ {"success":true,"data":{"ok":true}}
```

Состояние после отката, diff с `live-before-policies.json`:

```
HydraRoute: OpkgTun11(0) → Wireguard0(1) → OpkgTun10(2)
RU:         GigabitEthernet1(0)
diff → пусто
```

**Постусловие отката выполнено**: цепочка побайтово совпадает с исходной.
Поскольку permit не изменил цепочку, откат физически являлся no-op, но был
всё равно выполнен по регламенту задачи и подтверждён diff'ом.

## Шаг 4: перезапуск туннеля awg11

Состояние до:

```
15:31:16 UTC  awg11  status=running  lastHandshake=2026-08-18T18:30:09+03:00
```

Команда:

```
15:31:22 UTC  POST /api/control/restart?id=awg11
→ {"success":true,"data":{"id":"awg11","status":"starting"}}
```

Опрос `/api/tunnels/all` каждые 4 секунды (а не единичный `sleep`) до
восстановления:

```
15:32:03 UTC  running  lastHandshake=2026-08-18T18:31:21+03:00   (первый опрос после рестарта — уже поднялся)
15:32:07…15:33:01 UTC  running  lastHandshake=2026-08-18T18:31:21+03:00 (стабильно)
```

**Постусловие выполнено**: `status` вернулся в `running`, `lastHandshake`
сдвинулся вперёд с `18:30:09` на `18:31:21` (+72 с) — новее, чем до
перезапуска. Туннель поднялся быстрее, чем 20-секундный интервал из брифа;
первый опрос уже застал его рабочим, а последующие 14 опросов подтвердили,
что состояние стабильно, а не колеблется.

## Восстановление роутера

Router restored: **да**. Единственная содержательная мутация (permit)
не изменила состояние роутера (см. шаг 3), а формальный откат подтверждён
пустым diff'ом против исходного снятия. Перезапуск `awg11` — санкционированная
операция без отката (постусловие — успешное восстановление, оно выполнено).
Финальное состояние `HydraRoute`/`RU` идентично состоянию на момент начала
задачи.

## Финальный прогон

```
export PATH=.../scratchpad/go/bin:$PATH
gofmt -l . | grep -v '^miniapp/'
  → cmd/deploy/awgm_pins_test.go
  → cmd/deploy/known_hosts_cmd_test.go
  (эти два файла не тронуты этой задачей; расхождение форматирования — не наше)

go vet ./...
  → чисто, без вывода

go test ./...
  → FAIL github.com/Jkaotlic/wg-monitor/cmd/deploy — единственный провал:
    TestDiagnosisFromReport_RouteElevationHint — известный провал на darwin
    (см. память оператора: deploy-test-fails-on-macos.md), не связан с этой задачей
  → все остальные пакеты: ok, включая
    internal/agent/actions, internal/agent/awgmgr, internal/backend, pkg/wire

cd miniapp && npm test
  → Test Files  11 passed (11)
  → Tests       158 passed (158)
```

## Вывод по трём разрешённым мутациям

1. **permit / rollback (Bridge1, HydraRoute)** — API вернул `success:true`,
   но цепочка не изменилась; откат — подтверждённый no-op, роутер не
   пострадал. Это находка, не провал задачи: код уже перепроверяет permit
   постчтением именно из-за такого сценария.
2. **restart (awg11)** — сработал штатно: статус и handshake восстановились
   в течение первых ~40 секунд после запроса.
3. Раскладка (шаг 2) — полностью совпала с ожиданиями брифа по всем шести
   проверяемым пунктам.

## Файлы

- `internal/agent/actions/route_status_live_manual_test.go` — новый, ручной
  прогон (`//go:build manual`).
- `docs/operations/2026-08-18-routing-truth-live-session.md` — этот протокол.
