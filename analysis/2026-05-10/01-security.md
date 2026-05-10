# Анализ Security wg-monitor v0.11.0-rc18

Аудит покрывает backend (cmd/backend, internal/backend), agent (cmd/agent, internal/agent),
deploy wizard (cmd/deploy), CLI (cmd/wg-monitor-cli) и обмен по wire-протоколу. rc12-rc18
(cross-fingerprint, existing-agent guard, rollback, PID-lock, auth-probe,
shellSingleQuote, install-backend preflight, bot-token drift) НЕ ре-флажу — они
уже закрыты.

## Критические

(критических находок нет)

## Средние

### SEC-01: Bot-token утекает в логи через ошибки HTTP-клиента Telegram
- **Файл**: `internal/backend/tg/client.go:264-266` (метод `callWith`), а также `client.go:228-237` (`DownloadFile`)
- **Проблема**: URL формируется как `BaseURL + Token + "/" + method` (строка 258). При сбое `httpc.Do(req)` Go возвращает `*url.Error`, чей `Error()` встраивает **полный URL** с токеном. Этот err оборачивается через `%w`:
  ```go
  return fmt.Errorf("tg %s: %w", method, err)
  ```
  И затем уплывает в `slog.Warn` / `slog.Error` в `callbacks/router.go` (`smart reply send failed`, `tunnels-panel send failed`, `routes panel send failed` и др., всего ~30 точек). На любом транзиентном таймауте/DNS-фейле запись лога содержит:
  `Post "https://api.telegram.org/bot<TOKEN>/sendMessage": dial tcp ...`
  Логи backend'а попадают в systemd-journal на VPS — оператор-сосед, читавший journalctl с `wgmonitor` group, получает токен бота → полный контроль над ботом.
- **Риск**: Средний. Эксплуатация требует network failure ИЛИ доступа к journald. Но WG-Mon специально проектируется для нестабильных провайдеров (mobile router) — TG getUpdates/sendMessage стабильно фейлятся при ребилде VPN, и токен будет в логах с большой вероятностью.
- **Решение**: В `callWith`/`DownloadFile` после `httpc.Do` маскировать URL-Error: `if ue, ok := err.(*url.Error); ok { ue.URL = strings.Replace(ue.URL, c.Token, "<TOKEN>", 1) }` — или строить URL через `req.URL.RequestURI()` + ручной log-redact, или поместить токен в Authorization header (TG поддерживает только URL-path token, но можно сделать кастомный wrapping err: `return fmt.Errorf("tg %s: <hidden>: %v", method, redactURLErr(err))`).

### SEC-02: `ndms_name` из Telegram-callback не валидируется перед передачей в `ndmc -c "interface NAME up"`
- **Файл**: `internal/backend/callbacks/parse.go:136-141`, `internal/agent/actions/runner.go:122-134`
- **Проблема**: callback-data вида `tunnel_enable:<uid>:<check>:<ndms_name>` попадает в parse.go, где `ndms_name` — это `parts[3]` без regex-валидации (в отличие от tunnel-name в `actions.go:15` и nickname в config.go:13). Дальше backend кладёт значение в `wire.Command.Args["ndms_name"]`, агент читает строку и формирует:
  ```go
  r.Exec(ctx, "ndmc", "-c", fmt.Sprintf("interface %s %s", ndms, state))
  ```
  `os/exec.CommandContext` шелл не вызывает, но `ndmc -c` сам парсит свой аргумент: пробельные/специальные символы в `ndms_name` могут сместить токенизацию команды и вызвать другую операцию (`ndmc -c "interface X system reboot"`). Источник `ndms_name` — поле details JSON в events (которое в свою очередь приходит от агента на основе ответа awg-manager). Скомпрометированный awg-manager или агент может прислать значение с пробелами и эскалировать через бэкенд.
- **Риск**: Средний. Защита-в-глубину: backend и so доверяет агенту, но если callback-buttons формируются по чужим event-данным, mod chain agent → awg-mgr может пробросить подмену.
- **Решение**: В `parse.go` для `tunnel_enable`/`tunnel_disable` добавить `if !regexp.MustCompile("^[A-Za-z0-9]{1,32}$").MatchString(parts[3]) { return Args{}, fmt.Errorf(...) }`. Аналогично — валидировать `Args["ndms_name"]` на стороне runner до `Exec`.

## Низкие

### SEC-03: tunnel-ID не URL-кодируется в путях awgmgr API
- **Файл**: `internal/agent/awgmgr/client.go:218-230`, `internal/agent/actions/route_rebind.go:63`
- **Проблема**:
  ```go
  c.post(ctx, "/api/control/start?id="+tunnelID, ...)
  c.GetEnv(ctx, "/api/tunnels/get?id="+id, &env)
  ```
  `tunnelID/id` склеивается без `url.QueryEscape`. Сегодня источники — `RoutesCache` snapshot или ответ awg-manager (alphanumeric), но если awg-manager изменит формат tunnel-id (например, добавит спец. символы), запрос станет malformed и/или попадёт мимо нужного эндпоинта. Это не эксплуатируемый SSRF (BaseURL фиксирован localhost:2222), но defense-in-depth слабая.
- **Риск**: Низкий.
- **Решение**: использовать `url.Values{"id":{tunnelID}}.Encode()` или `url.QueryEscape(tunnelID)`.

### SEC-04: backend HTTP-сервер не выставляет ReadTimeout/WriteTimeout/IdleTimeout
- **Файл**: `cmd/backend/main.go:124-128`
- **Проблема**: установлен только `ReadHeaderTimeout: 5*time.Second`. Slowloris-style атака на BODY всё ещё возможна (агент-impostor, не зная токена, может перебивать TCP-сокеты). Перед backend'ом стоит Caddy с собственными таймаутами, но `localhost:8080` тоже доступен любому процессу на VPS.
- **Риск**: Низкий (Caddy-front, малый surface).
- **Решение**: добавить `ReadTimeout: 30s`, `WriteTimeout: 90s` (≥ maxCmdWait=60s + grace), `IdleTimeout: 120s`.

### SEC-05: Path-traversal в `ImportSecrets` ловится только substring-чеком
- **Файл**: `cmd/deploy/secrets_io.go:166`
- **Проблема**: `strings.Contains(hdr.Name, "..")` — substring, не path-aware. Имя `..foo.bak` (без `/`) прошло бы. Также не отбрасывается абсолютный путь типа `/etc/passwd`. Но дальше `filepath.Base(hdr.Name)` нормализует, и пишется только в `secPath`/`statePath`, поэтому реального чтения/записи вне whitelist нет. Текущая логика безопасна, но проверка вводит ложное чувство уверенности.
- **Риск**: Низкий (не эксплуатируемо в текущем коде).
- **Решение**: заменить на `if filepath.IsAbs(hdr.Name) || strings.Contains(filepath.ToSlash(hdr.Name), "../") { return ... }` — либо удалить проверку и положиться на `filepath.Base` + whitelist (с комментарием).

### SEC-06: GitHub asset download не лимитирован по размеру
- **Файл**: `cmd/deploy/github.go:198-205` (`downloadTo`)
- **Проблема**: `io.Copy(f, resp.Body)` без `LimitReader`. Скомпрометированный CDN/GitHub-релиз с 10 ГБ-боди забьёт диск пользователя до того, как сработает `sha256` mismatch. SHA verify случается ПОСЛЕ полного скачивания.
- **Риск**: Низкий (нужно хакнуть CDN GitHub).
- **Решение**: `io.Copy(f, io.LimitReader(resp.Body, 200<<20))` — текущие бинари ~10-15MB, 200MB — щедрый запас.

### SEC-07: `existingInstallDetected` и cache-чек игнорируют permission-denied как "exists"
- **Файл**: `cmd/deploy/steps.go:424-427` (комментарий явно говорит про это)
- **Проблема**: разница между "файл есть, но не читаем" и "файла нет" не различается, и в обоих случаях SSH-юзер ловит false-negative или false-positive в зависимости от логики. Это не уязвимость, а UX-нюанс. Упоминаю потому что rc-bug-chain содержал близкие issues.
- **Риск**: Низкий.
- **Решение**: при `rc != 0 && err != nil` логировать `PrintWarn` чтобы оператор понимал, что чек не доказал отсутствие.

### SEC-08: `secretsCachePath` на Windows получает только NTFS-ACL inheritance
- **Файл**: `cmd/deploy/secrets.go:239-247`
- **Проблема**: уже warning'ится коду, но стоит держать в виду: `0o600` на Windows — no-op. Любой процесс под `BUILTIN\Administrators` локальной машины читает `secrets.env` (включая токены агентов и WG_BOT_TOKEN). Это user-error по сути, но cache содержит самый чувствительный материал.
- **Риск**: Низкий (single-admin laptop scenario).
- **Решение**: рассмотреть DPAPI-шифрование (`golang.org/x/sys/windows.CryptProtectData`) для Windows-варианта; либо хотя бы документировать в README.

### SEC-09: Callback-policy "любой member chat'а может тапать" + opkg_upgrade в меню
- **Файл**: `internal/backend/callbacks/router.go:236-242`
- **Проблема**: явно задокументированное архитектурное решение. Любой пользователь, добавленный в group chat, может тапать `opkg_upgrade`, `restart_tunnel`, `tunnel_disable`, `route_rebind`, `firmware_install` (последние два — есть подтверждение, но `opkg_upgrade` исполняется без). Уровень доступа кнопок на роутерах = full root. Если в чате окажется злоумышленник — он мутит фронт.
- **Риск**: Низкий ↔ Средний (зависит от того, насколько строго оператор регулирует chat membership).
- **Решение**: добавить ALLOWED_USER_IDS allowlist для destructive действий в callbacks-config; либо требовать confirm-token для всех command-кнопок (а не только rebind/firmware/restart router).

### SEC-10: Token попадает в `User-Agent`-вывод смотрящего CLI на VPS
- **Файл**: `cmd/wg-monitor-cli/main.go:113`
- **Проблема**: `wg-monitor-cli add-user` печатает raw-token в stdout. Это by design (one-shot), и wizard захватывает stdout по regex — но если кто-то запускает CLI вручную через SSH с tee в файл / в TTY, токен ложится в bash history (`history -c` не делается). По текущему wizard-флоу токен дополнительно сохраняется в disk-cache, а админ обычно вручную CLI не запускает.
- **Риск**: Низкий (operational).
- **Решение**: добавить флаг `--print-token=stderr` чтобы оператор мог разделить command-output (stdout, в YAML hint) и сам токен (stderr, redirect куда нужно).

## Suggested fix priorities

1. **SEC-01** (medium, easy fix, real exposure surface): redact bot token из URL-Error в `tg/client.go`.
2. **SEC-02** (medium, defense-in-depth): regex для `ndms_name` в `parse.go`.
3. **SEC-03**, **SEC-06**, **SEC-04** (low, hygiene): URL-encode, LimitReader, server timeouts.
4. **SEC-09** (operational/policy): обсудить с оператором, нужен ли расширенный allowlist для destructive callbacks.
