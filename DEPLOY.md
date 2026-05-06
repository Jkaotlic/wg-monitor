# Деплой wg-monitor

Полный деплой делает один интерактивный wizard — `wg-monitor-deploy`. Никаких ручных команд по SSH, никакого Python.

## Что нужно заранее (вручную)

| Компонент | Как получить |
| --- | --- |
| **VPS** | Linux amd64, минимум 256 МБ RAM, открытый порт 443. Любой провайдер. |
| **Домен** | Свой домен или DuckDNS, A-запись на IP VPS |
| **Telegram-бот** | Создать через [@BotFather](https://t.me/BotFather), сохранить токен |
| **Telegram-группа** | Супергруппа с топиками, бот добавлен админом, для каждого роутера — свой топик |
| **Telegram IDs** | chat_id, message_thread_id (через `getUpdates`), ваш user_id (через [@userinfobot](https://t.me/userinfobot)) |
| **Роутер Keenetic** | OS 4/5, [Entware](https://docs.keenetic.com/...) и [awg-manager](https://github.com/hoaxisr/awg-manager) 2.8+ установлены |

## Шаги

1. Скачай `wg-monitor-deploy` под свою OS из [Releases](https://github.com/anex/wg-monitor/releases/latest):
   - Windows: `wg-monitor-deploy-windows-amd64.exe`
   - macOS Apple Silicon: `wg-monitor-deploy-darwin-arm64`
   - macOS Intel: `wg-monitor-deploy-darwin-amd64`
   - Linux: `wg-monitor-deploy-linux-amd64`

2. **macOS** — снять Gatekeeper-карантин:

   ```bash
   xattr -d com.apple.quarantine wg-monitor-deploy-darwin-arm64
   chmod +x wg-monitor-deploy-darwin-arm64
   ```

   **Linux:** `chmod +x wg-monitor-deploy-linux-amd64`

3. Запусти:

   ```bash
   ./wg-monitor-deploy
   ```

   (или двойной клик на Windows)

4. Выбери в меню `[1] Первичная установка бэкенда`. Wizard проведёт через 12 шагов: спросит домен, токен бота, IDs, пароль root для VPS — и всё развернёт.

5. После бэкенда — `[3] Первичная установка агента`. Введи host роутера, его никнейм, awg-iface (`awg0` обычно). Wizard сам определит архитектуру (arm64/mipsle), скачает нужный бинарь и установит как Entware-сервис.

## Обновление

```bash
./wg-monitor-deploy update-backend     # без интерактива, по wizard.toml
./wg-monitor-deploy update-agent
```

Wizard скачивает свежие бинари из последнего GitHub Release.

## Файлы

- `wizard.toml` — конфиг wizard'а (хосты, домен, никнеймы). Не содержит секретов. По умолчанию: `~/.config/wg-monitor-deploy/wizard.toml` (Linux/macOS) или `%APPDATA%\wg-monitor-deploy\wizard.toml` (Windows).
- Секреты — через env vars: `WG_VPS_PASS`, `WG_KEENETIC_PASS_<NICKNAME>`, `WG_BOT_TOKEN`. Wizard напомнит после первого ввода.

## Подкоманды

```
wg-monitor-deploy                    # меню
wg-monitor-deploy install-backend    # без меню
wg-monitor-deploy update-backend
wg-monitor-deploy install-agent [--agent <nickname>]
wg-monitor-deploy update-agent  [--agent <nickname>]
wg-monitor-deploy add-router
wg-monitor-deploy status
wg-monitor-deploy --version
wg-monitor-deploy --no-color
wg-monitor-deploy --config <path>
```
