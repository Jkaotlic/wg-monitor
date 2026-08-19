# Ответы живого awg-manager 2.17.2+r5

Сняты 2026-08-18 с домашнего роутера (`192.168.0.1:2222`), только чтение.
Это опорные фикстуры фазы B: тесты чтения маршрутизации, pingCheck и политик
доказываются на реальных формах, а не на выдуманных.

- `access-policies.json` — `GET /api/routing/access-policies`
- `policy-interfaces.json` — `GET /api/routing/policy-interfaces`
- `routing-tunnels.json` — `GET /api/routing/tunnels`
- `tunnels-all.json` — `GET /api/tunnels/all`
- `dns-routes-list.json` — `GET /api/dns-routes/list` (28 правил, все `hrRouteMode="policy"`)
- `pingcheck-status.json` — `GET /api/pingcheck/status` (без `successCount`, с `tunnelRunning`)
- `settings-get-trimmed.json` — `GET /api/settings/get`, урезанный до полей `Settings`

`settings-get-trimmed.json` урезан сознательно: полный ответ роутера содержит
приватные и preshared-ключи WireGuard в открытом виде, в репозитории им не место.
