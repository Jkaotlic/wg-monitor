# Deploy Wizard — Design

**Date:** 2026-05-06
**Status:** Approved (brainstorming)
**Author:** Anex + Claude

---

## 1. Цель

Заменить текущий «зоопарк» из 4 Python-скриптов и ручной DEPLOY.md инструкции **одним самодостаточным бинарём** `wg-monitor-deploy`, который:

1. Скачивается с GitHub Releases как готовый бинарь под Windows / macOS / Linux.
2. На любой машине запускается без установки Python, paramiko, Go, git — **ничего**.
3. Интерактивно проводит оператора по всем шагам деплоя: первичная установка бэкенда, обновление бэкенда, первичная установка агента, обновление агента, добавление роутера, проверка статуса.
4. Идемпотентен: каждый шаг сначала проверяет своё состояние и скипает если уже сделано; если шаг сломан — говорит точно что и как починить.
5. Сам качает свежие бинари **агента** и **бэкенда** из того же GitHub Release — оператор не собирает Go-проект руками.

**Anti-цели:**
- НЕ автоматизирует то что технически нельзя автоматизировать (создание Telegram-бота через BotFather, аренда VPS, установка Entware/awg-manager на роутер). На таких этапах wizard работает как чеклист с ссылками и ждёт `Enter`.
- НЕ заменяет CLI-инструмент `wg-monitor-cli` (это другой бинарь для оперативной работы с бэкендом).
- НЕ ставит сам себя как сервис — это утилита запуска, не демон.

---

## 2. Контекст и обоснование

Сейчас (v0.8.0) деплой выглядит так:

| Действие | Команда | Зависимости |
|---|---|---|
| Обновить бэкенд | `GOOS=linux GOARCH=amd64 go build ... && VPS_MAIN_PASS=... python deploy/backend/deploy_vps_main.py --binary ...` | Go, Python 3.10+, paramiko, доступ к sources |
| Обновить агента | `GOOS=linux GOARCH=arm64 go build ... && python deploy/agent/deploy_keenetic_binonly.py --bin ...` | то же |
| Первичная установка | Ручные шаги по `DEPLOY.md`: создать юзера, поставить Caddy, написать backend.yaml, запустить systemd | Шеллбог |

Боли:
- 4 отдельных Python-скрипта с дублированным кодом (`upload_via_stdin`, `run`, `password_from_memory`).
- Build и deploy раздельны — две команды вместо одной.
- Пароли разнесены: `WG_VPS_PASS` env var против `host_keenetic.md` файла памяти Claude.
- VPS-IP, домен, пути, тайминги захардкожены в коде Python-скриптов.
- Первичная установка — простыня из 30+ ручных шагов в `DEPLOY.md`, легко промахнуться.
- Чтобы кто-то (не разработчик) развернул копию проекта, ему нужно поставить Go, Python, paramiko, склонить репо.

---

## 3. Архитектура

### 3.1 Языки и стек

- **Реализация:** Go 1.22+ (тот же что и весь проект). Никаких новых языков/тулинга в стеке.
- **SSH:** `golang.org/x/crypto/ssh` (уже indirect-dep через wgctrl).
- **TOML:** `github.com/BurntSushi/toml` (новая зависимость, чистый Go, маленькая).
- **HTTP-клиент для GitHub API:** `net/http` (stdlib).
- **TUI:** голый `bufio.Scanner` + ANSI escape codes. Без `huh`/`survey`/`bubbletea` — лишний вес.
- **Embed шаблонов:** `embed.FS` (stdlib).

### 3.2 Точка входа в репозитории

```
cmd/deploy/                     # новый Go-пакет, весь wizard здесь
  main.go                       # CLI parsing, меню, dispatch
  ui.go                         # цвета, prompts, ask, askSecret, askChoice
  ssh.go                        # обёртка SSH: Run, RunSudo, UploadStdin, UploadSFTP
  state.go                      # load/save wizard.toml + lookup секретов из env vars
  github.go                     # GitHub Releases: latest release, download asset, sha256
  templates.go                  # //go:embed templates/* + рендеринг через text/template
  steps.go                      # маленькие функции-шаги: stepInstallCaddy, stepUploadBinary...
  actions.go                    # action_install_backend, action_update_agent — последовательности шагов
  templates/                    # embed-источник
    S99wg-monitor               # как сейчас, без изменений
    backend.yaml.tmpl           # как сейчас + Go-template плейсхолдеры
    agent.yaml.tmpl             # как сейчас + плейсхолдеры
    Caddyfile.tmpl              # как сейчас + {{.Domain}}
    wg-monitor-backend.service  # как сейчас, без изменений
```

### 3.3 Что удаляется

| Файл | Заменяется на |
|---|---|
| `deploy/agent/deploy_keenetic.py` | `wg-monitor-deploy install-agent` |
| `deploy/agent/deploy_keenetic_binonly.py` | `wg-monitor-deploy update-agent` |
| `deploy/backend/deploy_vps_main.py` | `wg-monitor-deploy update-backend` |
| `deploy/backend/deploy_cli.py` | `wg-monitor-deploy update-cli` (опционально, см. п. 8) |
| `deploy/agent/requirements.txt` | (удалить) |

`DEPLOY.md` сильно укорачивается: 90% разделов «выполни команду на VPS» удаляются (теперь это делает wizard), остаётся только нерасстаганаемый минимум — арендовать VPS, создать бота, установить awg-manager.

### 3.4 Что остаётся как есть

- `Makefile` — без изменений (он для разработки, не для деплоя). Добавляется одна цель `build-deploy`.
- Шаблоны (`S99wg-monitor`, `*.service`, `*.yaml.example`, `Caddyfile`) — переезжают в `cmd/deploy/templates/`. Старые копии в `deploy/agent/`, `deploy/backend/` удаляются.
- `deploy/diag/keenetic_diag.py` — диагностический скрипт, не часть деплоя, остаётся.

---

## 4. Дистрибуция

### 4.1 GitHub Releases

При push тега `v*`:

1. CI matrix собирает 7 артефактов:
   - `wg-monitor-deploy-windows-amd64.exe` (4–5 MB)
   - `wg-monitor-deploy-darwin-arm64` (5 MB)
   - `wg-monitor-deploy-darwin-amd64` (5 MB)
   - `wg-monitor-deploy-linux-amd64` (5 MB)
   - `wg-monitor-agent-linux-arm64` (UPX-pack, 1.5–3 MB) — для современных Keenetic
   - `wg-monitor-agent-linux-mipsle` (UPX-pack, 1.5–3 MB) — для старых Keenetic
   - `wg-monitor-backend-linux-amd64` (8–12 MB) — для VPS
2. Все артефакты + `checksums.txt` (sha256 каждого) аттачатся к Release.
3. CI: GitHub Actions, runners — `windows-latest`, `macos-latest`, `ubuntu-latest`. Cross-compile агента/бэкенда — на `ubuntu-latest`. Wizard — каждый бинарь на своём рунере (родной билд проще для будущей подписи, если когда-то понадобится).

### 4.2 Wizard скачивает бинари

Wizard на старте, прежде чем что-либо делать:
1. `GET https://api.github.com/repos/<OWNER>/<REPO>/releases/latest` → парсит `tag_name` и `assets[]`.
2. Кэширует имя релиза и список ассетов в памяти.
3. Качает нужные бинари в `~/.cache/wg-monitor-deploy/<tag>/` (Windows: `%LOCALAPPDATA%\wg-monitor-deploy\<tag>\`).
4. Перед использованием бинаря — sha256-чек против `checksums.txt`.
5. Если бинарь уже в кэше с правильной hash — не качает повторно.

**Версия wizard'а зашита в его собственный билд** (`-X main.Version=v0.9.0`). Wizard НЕ обновляет себя сам — оператор вручную качает новый. На старте wizard показывает свою версию и последнюю на GitHub. Если разница больше 1 минора — предупреждает «`update yourself: https://github.com/.../releases/latest`».

### 4.3 GitHub API rate limits

Анонимные запросы: 60 req/h на IP. Wizard делает 1 запрос на запуск (latest release) + N запросов на download (но это уже `releases/download/<tag>/<asset>`, не API, без лимитов). 60/h хватает с большим запасом.

### 4.4 Где брать `<OWNER>/<REPO>`

Захардкожено в коде wizard через ldflags:
```
-X github.com/anex/wg-monitor/cmd/deploy/github.RepoOwner=anex
-X github.com/anex/wg-monitor/cmd/deploy/github.RepoName=wg-monitor
```

Это позволяет форкам пересобрать wizard для своего форка одной командой.

---

## 5. UX / Wizard Flow

### 5.1 Точка входа

```
wg-monitor-deploy                    # интерактивное меню
wg-monitor-deploy install-backend    # сразу первичная установка бэкенда
wg-monitor-deploy update-backend     # сразу обновление бэкенда
wg-monitor-deploy install-agent      # сразу первичная установка агента
wg-monitor-deploy update-agent       # сразу обновление агента
wg-monitor-deploy add-router         # добавить новый роутер
wg-monitor-deploy status             # проверить статус всего
wg-monitor-deploy --help
wg-monitor-deploy --version
wg-monitor-deploy --no-color         # отключить ANSI
wg-monitor-deploy --config <path>    # альтернативный путь к wizard.toml
```

### 5.2 Главное меню

```
┌──────────────────────────────────────────────────┐
│ wg-monitor deploy wizard         v0.9.0          │
│ Latest on GitHub: v0.9.0  ✓                      │
├──────────────────────────────────────────────────┤
│ VPS:    103.106.1.253   wgmonitor.jkaotlic.duckdns.org │
│ Router: 192.168.31.1:222 (testkeen)              │
│ Last backend deploy:  2026-04-12 15:23           │
│ Last agent deploy:    2026-04-15 11:08           │
└──────────────────────────────────────────────────┘

  [1] Первичная установка бэкенда на VPS
  [2] Обновить бэкенд на VPS
  [3] Первичная установка агента на роутер
  [4] Обновить агента на роутере
  [5] Добавить новый роутер
  [6] Проверить статус
  [7] Открыть wizard.toml в редакторе
  [Q] Выход

>
```

Если `wizard.toml` не найден — поля `VPS:`, `Router:`, `Last deploy:` не показываются, доступны только пункты `[1]`, `[3]`, `[Q]`.

### 5.3 Структура шага

Каждый шаг — функция Go с сигнатурой:

```go
type StepResult int
const (
    StepOK   StepResult = iota
    StepSkip            // уже сделано
    StepFail
    StepAbort           // пользователь отменил
)

type Step func(ctx *WizardCtx) StepResult
```

Шаг печатает свой заголовок `[N/M] Что делаю`, проверяет состояние, делает или скипает, печатает результат.

```
[3/8] Установка Caddy
  Проверяю...  ✗ не установлен

  Caddy ставится так:
    apt install -y debian-keyring debian-archive-keyring apt-transport-https
    curl -1sLf 'https://dl.cloudsmith.io/public/caddy/stable/gpg.key' | ...
    apt update && apt install caddy

  Что делаем?
    [A] Сделай за меня по SSH
    [M] Я сам — нажму Enter когда готов
    [S] Скипнуть (если ставлю не Caddy, а что-то своё)
  > A
  → apt install caddy ...
  ✓ caddy v2.7.6 установлен
```

Если шаг уже выполнен:
```
[3/8] Установка Caddy
  Проверяю...  ✓ caddy v2.7.6  →  скипаю
```

Если шаг сломан и пользователь не хочет автоматики:
```
[3/8] Установка Caddy
  Проверяю...  ✗ не установлен
  Что делаем? [A/M/S] > M
  Жду пока поставишь. Нажми Enter когда готово...
  ↵
  Перепроверяю...  ✓ caddy v2.7.6
```

### 5.4 Action: «Первичная установка бэкенда» — последовательность шагов

| # | Шаг | Что проверяет | Что делает если нет |
|---|---|---|---|
| 1 | Запросить параметры | wizard.toml уже содержит host/domain | Спрашивает у пользователя: VPS host, root пароль (с warning), домен, Telegram bot token (warning), chat_id, admin_user_id |
| 2 | SSH-доступ | пингуется ssh-порт, аутентификация | Если auth fail → перепросить пароль |
| 3 | User wgmonitor | `id wgmonitor` exit 0 | `useradd --system --no-create-home --shell /usr/sbin/nologin wgmonitor` |
| 4 | Директории | `/etc/wg-monitor`, `/var/lib/wg-monitor` существуют | mkdir + chown |
| 5 | backend.yaml | существует и hash совпадает с ожидаемым | Сгенерить из `backend.yaml.tmpl` (домен/токены/IDs) → залить в `/etc/wg-monitor/backend.yaml`, chmod 600 |
| 6 | systemd unit | `/etc/systemd/system/wg-monitor-backend.service` существует и hash совпадает | Залить из embed → `systemctl daemon-reload` + `enable` |
| 7 | Caddy | `which caddy` exit 0 | A/M/S choice; auto-install через `apt` (Debian/Ubuntu). Для других дистрибутивов (RHEL/Alpine) `[A]` недоступен, доступны только `[M]/[S]` с инструкцией |
| 8 | Caddyfile | `/etc/caddy/Caddyfile` содержит наш домен | Отрендерить из `Caddyfile.tmpl` → залить → `systemctl reload caddy` |
| 9 | Скачать backend бинарь | бинарь в кэше + hash совпадает | `GET https://github.com/.../releases/download/<tag>/wg-monitor-backend-linux-amd64` |
| 10 | Загрузить + swap | бинарь на VPS совпадает с локальным | SFTP → `/tmp/wg-monitor-backend.new` → sha256 → `mv` → `chmod` |
| 11 | Стартануть сервис | `systemctl is-active` = active | `systemctl start` |
| 12 | Verify | `curl https://<domain>/health` → 200 | Если не 200 → показать `journalctl -u wg-monitor-backend -n 50` и предложить ретрай |

В конце: записать `last_deploy = <timestamp>` в `wizard.toml`.

### 5.5 Action: «Обновить агента» — последовательность шагов

| # | Шаг | Что проверяет | Что делает если нет |
|---|---|---|---|
| 1 | Выбрать роутер | в wizard.toml есть `[[agents]]` | Если несколько — спросить какой; если один — взять его; если нет — направить в `add-router` |
| 2 | SSH-доступ | подключение | Запросить пароль из env / `host_keenetic.md` / prompt |
| 3 | Определить архитектуру | `uname -m` → `aarch64` (→ arm64) или `mips`/`mipsel` (→ mipsle) | Выбрать соответствующий артефакт. Если `uname -m` возвращает что-то другое (`armv7l`, `x86_64`, `mips64` и т.п.) — wizard abort с сообщением «архитектура $ARCH не поддерживается, открой issue». В будущем добавить arm32/x86_64 если кто-то попросит |
| 4 | Скачать агент бинарь | в кэше + hash | GitHub Releases |
| 5 | Stop + upload + sha + swap + start | `pidof wg-monitor` отсутствует после stop, hash совпадает после upload, `pidof wg-monitor` присутствует после start | Поэтапно делать |
| 6 | Verify version | `/opt/bin/wg-monitor --version` совпадает с тегом | Если нет → показать last 30 строк `logread \| grep wg-monitor` |

### 5.6 Прочие actions

- **«Первичная установка агента»** = «Обновить агента» + дополнительно шаги: создать `/opt/bin`, `/opt/etc/wg-monitor/`, `/opt/var/wg-monitor/`, сгенерить `config.yaml` из `agent.yaml.tmpl` (запросив nickname/token/awg_iface/expected_exit_ip/backend URL), залить `S99wg-monitor` в `/opt/etc/init.d/`. Также обновляет `wizard.toml` — добавляет агента в массив `[[agents]]`.
- **«Добавить новый роутер»** = «Первичная установка агента» с дополнительным шагом в начале: запросить новый nickname, проверить уникальность, сгенерить новый агентский токен через `crypto/rand`, добавить в `wizard.toml`, и (важно!) обновить `backend.yaml` на VPS (добавить запись `agents:` + перезапустить бэкенд). Это требует SSH к VPS, который тоже из wizard.toml.
- **«Проверить статус»** — read-only действие: SSH к VPS → `systemctl status wg-monitor-backend`, `curl https://<domain>/health`, `journalctl -u wg-monitor-backend -n 5`. SSH к каждому роутеру → `pidof wg-monitor`, `/opt/bin/wg-monitor --version`. Сводная таблица.

### 5.7 Цвета и форматирование

ANSI:
- `\033[32m✓\033[0m` зелёная галочка для OK/skip
- `\033[31m✗\033[0m` красный крестик для fail
- `\033[33m⚠\033[0m` жёлтый треугольник для warn
- `\033[36m→\033[0m` голубая стрелка для running operation
- `\033[1m...\033[0m` жирный для заголовков шагов
- `\033[2m...\033[0m` тусклый для команд которые wizard будет выполнять

Если `--no-color` или `os.Getenv("NO_COLOR") != ""` или stdout не TTY (`!isatty.IsTerminal(os.Stdout.Fd())`) — все escape-коды глотаются.

---

## 6. Состояние и секреты

### 6.1 wizard.toml (non-secrets)

Расположение по умолчанию (priority order):
1. `--config <path>` флаг
2. `./wizard.toml` (если запущен из репо)
3. `~/.config/wg-monitor-deploy/wizard.toml` (Linux/macOS) или `%APPDATA%\wg-monitor-deploy\wizard.toml` (Windows)

Формат:
```toml
schema_version = 1

[backend]
host = "103.106.1.253"
port = 22
user = "root"
domain = "wgmonitor.jkaotlic.duckdns.org"
last_deploy = "2026-04-12T15:23:00Z"
last_deployed_version = "v0.8.0-tunnel-import"

[telegram]
chat_id = -1001234567890
admin_user_id = 123456789
# bot_token: NOT STORED — see [secrets] below

[[agents]]
nickname = "testkeen"
host = "192.168.31.1"
port = 222
user = "root"
arch = "arm64"  # arm64 или mipsle, заполняется при первой установке
thread_id = 42
awg_iface = "awg0"
expected_exit_ip = "1.2.3.4"
last_deploy = "2026-04-15T11:08:00Z"
last_deployed_version = "v0.8.0-tunnel-import"

[secrets]
# Эта секция — чисто документация (комментарии для пользователя).
# Wizard НЕ сохраняет секреты в этом файле и не читает из него.
# Источники, в порядке приоритета:
#   1. environment variables (см. ниже)
#   2. файлы памяти Claude (для совместимости): ~/.claude/projects/*/memory/host_keenetic.md
#   3. prompt у пользователя (с warning)
#
# Переменные окружения, которые wizard понимает:
#   WG_VPS_PASS                       — пароль root на VPS
#   WG_KEENETIC_PASS                  — пароль root на роутере (если один на все)
#   WG_KEENETIC_PASS_<NICKNAME>       — пароль на конкретный роутер (для нескольких разных)
#   WG_BOT_TOKEN                      — Telegram bot token
#   WG_AGENT_TOKEN_<NICKNAME>         — токен агента (если уже сгенерирован вне wizard)
```

`schema_version = 1` — для будущих миграций. Wizard валидирует поле и refuse запуск если schema_version > 1 (для forward-compat между разными версиями wizard).

### 6.2 Секреты

Wizard **никогда не пишет секреты на диск**. Когда секрет нужен:
1. Чек env var
2. Чек файлов памяти (только `host_keenetic.md` пока) — для совместимости с текущей разработкой
3. Если ничего не нашлось — `askSecret()` (ввод без эха, как `ssh-askpass`)

Если оператор ввёл секрет через prompt — wizard в конце действия выводит warning:
```
⚠  Несколько секретов введено вручную в этой сессии:
     WG_VPS_PASS
     WG_BOT_TOKEN
   Чтобы не вводить заново при следующих запусках, сохрани их в env vars:

   PowerShell (постоянно):
     [Environment]::SetEnvironmentVariable("WG_VPS_PASS", "<пароль>", "User")
     [Environment]::SetEnvironmentVariable("WG_BOT_TOKEN", "<токен>", "User")

   Bash/Zsh (~/.zshrc или ~/.bashrc):
     export WG_VPS_PASS="<пароль>"
     export WG_BOT_TOKEN="<токен>"

   Или сохрани в KeePass / 1Password / Bitwarden.
```

### 6.3 Где хранятся скачанные бинари

```
Linux/macOS:  ~/.cache/wg-monitor-deploy/<tag>/wg-monitor-{backend,agent-arm64,agent-mipsle}
Windows:      %LOCALAPPDATA%\wg-monitor-deploy\<tag>\<binary>
```

При запуске wizard очищает кэш: оставляет только последние 3 версии. Команда `wg-monitor-deploy --clean-cache` стирает всё.

---

## 7. Шаблоны

`cmd/deploy/templates/` содержит:

- `S99wg-monitor` — Entware init.d. Пере- кладывается из `deploy/agent/S99wg-monitor` без изменений.
- `wg-monitor-backend.service` — systemd unit. Перекладывается из `deploy/backend/wg-monitor-backend.service` без изменений.
- `Caddyfile.tmpl` — было `deploy/backend/Caddyfile`. Хардкоженный домен (`wgmonitor.jkaotlic.duckdns.org`) и email (`asnekhaev@gmail.com`) заменяются на `{{.Domain}}` и `{{.Email}}`.
- `backend.yaml.tmpl` — было `deploy/backend/backend.yaml.example`. Все поля (chat_id, admin_user_id, bot_token, agents) — Go-template плейсхолдеры.
- `agent.yaml.tmpl` — было `deploy/agent/config.yaml.example`. То же.

Embed:
```go
//go:embed templates/*
var Templates embed.FS

func RenderBackendYAML(p BackendParams) ([]byte, error) {
    raw, _ := Templates.ReadFile("templates/backend.yaml.tmpl")
    t := template.Must(template.New("").Parse(string(raw)))
    var buf bytes.Buffer
    err := t.Execute(&buf, p)
    return buf.Bytes(), err
}
```

Структуры параметров шаблонов:
```go
type BackendParams struct {
    BotToken     string
    ChatID       int64
    AdminUserID  int64
    Agents       []AgentEntry  // nickname, token, thread_id
}

type AgentParams struct {
    BackendURL       string  // https://<domain>
    Token            string
    Nickname         string
    AWGIface         string
    ExpectedExitIP   string
}

type CaddyParams struct {
    Domain string
    Email  string
}
```

---

## 8. Прочие детали

### 8.1 Логирование

Wizard в каждом запуске пишет лог в `~/.cache/wg-monitor-deploy/log/<timestamp>.log` (полный transcript SSH-команд + stderr с удалённой стороны). Полезно для постмортема если что-то сломалось. Хранится 14 дней.

### 8.2 Обработка ошибок

- Любой шаг при ошибке предлагает `[R]etry / [S]kip / [A]bort`. Retry перезапускает только этот шаг. Skip помечает шаг как failed но идёт дальше (потом в конце action — сводка). Abort выходит из action в меню.
- Ctrl+C — graceful: текущий шаг доводится до атомарной точки (например, после `mv tmp file`), потом exit. SSH-сессии закрываются.

### 8.3 wg-monitor-cli

Текущий `deploy_cli.py` — отдельный скрипт для деплоя CLI-бинаря на VPS. Он редко нужен (CLI используется на VPS вручную для миграций/инспекций). В wizard это станет:

- Отдельный пункт меню? **Нет** — слишком редкий use case, мусорит меню.
- Подкоманда `wg-monitor-deploy update-cli`? **Да** — без пункта в интерактивном меню, но команда есть в `--help`.

### 8.4 Версионирование релизов

Wizard и агент/бэкенд **версионируются совместно** — один тег = один Release = один набор всех 7 артефактов. Это упрощает совместимость: wizard версии `v0.9.0` качает агент `v0.9.0` и бэкенд `v0.9.0`. Никаких matrix совместимости нет.

Если хочется задеплоить **более старую** версию (rollback) — `wg-monitor-deploy --pin v0.8.0 update-backend`. По умолчанию используется `latest`.

### 8.5 Безопасность

- HTTPS для GitHub API/Releases — стандартный Go `net/http`, проверка TLS-сертификатов включена.
- sha256 каждого скачанного бинаря сверяется с `checksums.txt` из того же Release (который тоже скачан по HTTPS).
- SSH host keys — `golang.org/x/crypto/ssh`. Первый коннект к новому хосту: `TOFU` (trust on first use), записываем fingerprint в `~/.cache/wg-monitor-deploy/known_hosts`. Последующие — verify. Если fingerprint изменился (MITM или ребилд хоста) — abort с явным сообщением и инструкцией как починить.
- Все секреты: ввод без эха, не логируются в лог-файл, не передаются в командной строке (всегда через stdin).

### 8.6 Тестирование

Unit:
- `templates_test.go` — рендеринг шаблонов с типовыми параметрами не падает и содержит ожидаемые подстроки.
- `state_test.go` — load/save wizard.toml round-trip.
- `github_test.go` — парсер GitHub Releases JSON на фикстурах (httptest server).

Integration (опционально, может прийти позже):
- `e2e_test.go` — wizard запускает себя в режиме `install-agent`, целевой хост — `localhost` через docker (Alpine + dropbear + busybox), проверяет что после installation бинарь стоит и pidof работает.

Manual checklist для пре-релиз тестов:
1. Чистый Windows VM → скачать `.exe` → запустить → пройти меню `update-agent` → проверить что агент обновился.
2. Чистый Mac → `xattr -d com.apple.quarantine` → запустить → пройти `update-backend` → проверить.
3. Linux → запустить `install-backend` против тестового VPS → /health отвечает 200.

---

## 9. Миграция

### 9.1 Этапы

1. Реализуем wizard в `cmd/deploy/`. Старые Python-скрипты остаются нетронутыми.
2. Добавляем CI workflow на GitHub Actions с релизной матрицей.
3. Тегаем `v0.9.0` (или какой будет следующий минор), убеждаемся что Release собрался корректно.
4. Прогоняем wizard вручную: `update-backend` против реального VPS, `update-agent` против реального роутера.
5. Если всё работает — удаляем 4 Python-скрипта одним коммитом.
6. Переписываем `DEPLOY.md` — оставляем только prereqs (VPS/бот/awg-manager) и ссылку на wizard.
7. Обновляем `README.md`.

### 9.2 Backward-compatibility

Нет. Переход разовый. Старые Python-скрипты после удаления не возвращаются, ссылок на них в новой документации нет. Кому понадобятся — git history.

### 9.3 wizard.toml миграция

Первый запуск wizard на машине разработчика (где уже всё развёрнуто) — wizard НЕ найдёт wizard.toml и предложит:
```
wizard.toml не найден.
  [I] Импортировать из текущего DEPLOY.md (распарсить значения)
  [N] Заполнить заново (проведу через шаги)
  [Q] Выйти
```
`[I]` опция — wizard читает `DEPLOY.md` regex'ами и достаёт VPS IP, домен, никнейм роутера. Это nice-to-have, можно реализовать позже.

---

## 10. Высокоуровневый план работ

(Детализируется в плане реализации после этого spec.)

1. Skeleton `cmd/deploy/main.go` с меню и пустыми actions, без сетевых вызовов. Smoke run: `go run ./cmd/deploy` показывает меню.
2. SSH-обёртка с unit-тестами (mock-сервер на dropbear в Docker).
3. Шаблоны и embed.
4. State (wizard.toml).
5. GitHub Releases клиент.
6. Шаги (steps.go).
7. Actions (actions.go) — `update-backend` первым (он простейший: один SSH, один бинарь, systemctl).
8. `update-agent` (немного хитрее из-за dropbear stdin pipe и architecture detection).
9. `install-backend` (12 шагов).
10. `install-agent` + `add-router`.
11. `status` (read-only).
12. CI workflow.
13. Удаление старых скриптов + апдейт DEPLOY.md и README.

Каждый этап — отдельный коммит, отдельная проверка.

---

## 11. Риски

| Риск | Митигация |
|---|---|
| GitHub API rate-limit при тестировании | Кэш ответов на 5 минут в памяти + флаг `--no-update-check` |
| Анти-вирусы Windows ругаются на неподписанный `.exe` | Документировать в README; в будущем Authenticode-подпись (~$200/year) |
| macOS Gatekeeper блокирует первый запуск | Документировать `xattr -d com.apple.quarantine` в README |
| dropbear stdin pipe ломается на новом Keenetic | Уже работает в текущем Python-варианте; в Go тот же механизм через `session.StdinPipe()` |
| Сломанный wizard.toml после ручной правки | Валидация при load + clear error message; флаг `--config-init` для пересоздания с дефолтами |
| Wizard падает посреди установки backend | Каждый шаг идемпотентен → повторный запуск довершит. last_deploy в wizard.toml не выставляется до полного успеха action. |

---

## 12. Open questions (на момент написания спека закрыты, но фиксирую для контекста)

- **Q:** Builds локально или из Releases? **A:** Только из Releases (выбран вариант 1).
- **Q:** Подпись бинарей? **A:** Не сейчас. macOS — инструкция `xattr`. Windows — никак.
- **Q:** Auto-update wizard'а? **A:** Нет. Wizard сообщает что есть новая версия, оператор качает руками.
- **Q:** Поддержка нескольких VPS (кластер)? **A:** Не сейчас. Одна VPS на инсталляцию. Можно расширить позже введением `[[backends]]` массива (как `[[agents]]`).
- **Q:** Encrypted wizard.toml? **A:** Нет — там нет секретов.
- **Q:** GUI? **A:** Нет. TUI достаточен. Если когда-то понадобится — отдельный проект на Wails/Fyne.

---

**Конец дизайна.**
