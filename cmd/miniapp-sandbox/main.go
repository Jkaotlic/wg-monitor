// Package main -- локальная песочница мини-аппа: настоящий backend, поддельный
// Telegram, поддельный парк роутеров.
//
// Мини-апп нельзя открыть в браузере просто так: клиент берёт initData из
// window.Telegram.WebApp, а backend проверяет её подпись ботовым токеном.
// Внутри Telegram это правильно, но означает, что ни один экран нельзя открыть
// без телефона -- ни глазами, ни автоматом. Песочница подписывает initData
// собственным фальшивым токеном (тем же, с которым запущен backend), подсовывает
// странице заглушку Telegram-SDK и отвечает на любую команду роутера успехом.
//
// Никакого обхода защиты здесь нет: подпись настоящая, просто ключ -- локальный
// и одноразовый. Продовый токен песочница не читает и прочитать не может.
//
//	go run ./cmd/miniapp-sandbox
//	→ http://127.0.0.1:8099/miniapp/#tgWebAppData=...
package main

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Jkaotlic/wg-monitor/internal/backend"
	cmdpkg "github.com/Jkaotlic/wg-monitor/internal/backend/cmd"
	"github.com/Jkaotlic/wg-monitor/internal/backend/db"
	"github.com/Jkaotlic/wg-monitor/internal/backend/heartbeat"
	"github.com/Jkaotlic/wg-monitor/internal/backend/provision"
	"github.com/Jkaotlic/wg-monitor/internal/backend/replace"
	"github.com/Jkaotlic/wg-monitor/internal/backend/state"
	"github.com/Jkaotlic/wg-monitor/pkg/wire"
)

// Токен песочницы намеренно нерабочий как ботовый: он нужен только как ключ
// HMAC, одинаковый у подписывающей и проверяющей стороны.
const sandboxBotToken = "sandbox:local-only-not-a-real-bot-token"

const sandboxDashboardToken = "sandbox-dashboard-token"

var telegramSDKTag = regexp.MustCompile(`<script[^>]*telegram-web-app\.js[^>]*>\s*</script>`)

func main() {
	addr := flag.String("addr", "127.0.0.1:8099", "адрес песочницы")
	dbPath := flag.String("db", "", "путь к базе (по умолчанию временный файл)")
	tgUser := flag.Int64("tg-user", 4242, "telegram user id, от чьего имени открыт мини-апп")
	keep := flag.Bool("keep", false, "не удалять временную базу при выходе")
	flag.Parse()

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)

	path := *dbPath
	if path == "" {
		dir, err := os.MkdirTemp("", "wgm-sandbox-")
		if err != nil {
			fatal(err)
		}
		path = filepath.Join(dir, "sandbox.db")
		if !*keep {
			defer os.RemoveAll(dir)
		}
	}
	d, err := db.Open(path)
	if err != nil {
		fatal(err)
	}
	defer d.Close()

	if err := seed(d, *tgUser); err != nil {
		fatal(err)
	}
	sink := &fakeAgent{}

	// Настоящий сторож heartbeat: без него строка о нём в панели пуста, и
	// проверить её было бы нечем. Уведомления никуда не уходят -- в песочнице
	// некому.
	watcher := heartbeat.NewWatcher(d, offlineToLog{}, heartbeat.Config{
		StaleAfterStatic: 3 * time.Minute,
		MobileLifecycle:  true,
		MobileSleepAfter: 5 * time.Minute,
		ScanEvery:        15 * time.Second,
	})
	go watcher.Run(context.Background())

	cabinet := &fakeCabinet{}
	// Мастер замены -- настоящий движок: песочница подменяет только кабинет,
	// очередь команд и запись происхождения. Проверять экран мастера на
	// выдуманном движке не имело бы смысла: шаги, откат и блокировка -- это
	// как раз то, что хочется увидеть глазами.
	replaceEngine := &replace.Deps{
		Store:    provision.NewStore(),
		Commands: sink,
		Cabinet:  backend.ReplaceCabinet(cabinet),
		Origin:   backend.ReplaceOrigin(d),
		Notify: func(_ context.Context, routerID int64, text string) {
			slog.Info("песочница: уведомление в топик", "router_id", routerID, "text", text)
		},
		BaseCtx:        context.Background(),
		AwaitStep:      20 * time.Second,
		HandshakeTries: 2,
		HandshakeWait:  time.Second,
	}

	deps := backend.Deps{
		Logger:                logger,
		DB:                    d,
		CommandSink:           sink,
		VPNCabinet:            cabinet,
		Replace:               replaceEngine,
		Thresholds:            state.Thresholds{Fail: 2, Recovery: 2},
		MuteCutoffHour:        23,
		TelegramBotToken:      sandboxBotToken,
		TelegramAdminUserID:   *tgUser,
		TelegramPrimaryChatID: -100500,
		// Дашборд поднимается тем же токеном, что напечатан при старте:
		// песочница -- единственное место, где его можно писать в открытую.
		DashboardToken: sandboxDashboardToken,
		HeartbeatStats: watcher.Snapshot,
		PublicBaseURL:  "http://" + *addr,
	}
	mux := backend.NewMux(deps)
	initData := signInitData(sandboxBotToken, *tgUser, time.Now())

	fmt.Printf("\nпесочница мини-аппа\n")
	fmt.Printf("  база:   %s\n", path)
	fmt.Printf("  адрес:  http://%s/miniapp/\n", *addr)
	fmt.Printf("  открыть: http://%s/miniapp/\n", *addr)
	fmt.Printf("  дашборд: http://%s/dashboard/ (токен %s)\n\n", *addr, sandboxDashboardToken)

	if err := http.ListenAndServe(*addr, withTelegramStub(mux, initData)); err != nil {
		fatal(err)
	}
}

// offlineToLog -- «отправка» тревоги в песочнице: строка в консоли вместо
// сообщения в Telegram.
type offlineToLog struct{}

func (offlineToLog) SendOffline(_ context.Context, userID int64, nickname string, since time.Duration) error {
	slog.Info("песочница: роутер молчит", "nickname", nickname, "user_id", userID, "молчит", since.Round(time.Second))
	return nil
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "песочница:", err)
	os.Exit(1)
}

// withTelegramStub подменяет оболочку приложения: та же страница из бандла, но
// с заглушкой window.Telegram.WebApp перед скриптом приложения. Инжектить в
// собранный HTML, а не держать вторую копию страницы, -- единственный способ
// не разъехаться с настоящей вёрсткой.
func withTelegramStub(next http.Handler, initData string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || (r.URL.Path != "/miniapp/" && r.URL.Path != "/miniapp/index.html") {
			next.ServeHTTP(w, r)
			return
		}
		rec := httptest.NewRecorder()
		next.ServeHTTP(rec, r)
		body := rec.Body.String()
		if rec.Code != http.StatusOK || !strings.Contains(body, "<head>") {
			for k, v := range rec.Header() {
				w.Header()[k] = v
			}
			w.WriteHeader(rec.Code)
			_, _ = w.Write([]byte(body))
			return
		}
		// Настоящий telegram-web-app.js грузится со стороннего хоста и
		// затирает нашу заглушку своим объектом WebApp -- с пустой initData и
		// предупреждениями «не поддерживается в версии 6.0». В песочнице
		// Telegram не нужен вовсе, поэтому его скрипт из страницы вырезается.
		body = telegramSDKTag.ReplaceAllString(body, "")
		patched := strings.Replace(body, "<head>", "<head>\n"+telegramStubScript(initData), 1)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(patched))
	})
}

// Заглушка повторяет ровно ту часть Telegram-SDK, которой пользуется
// приложение (см. miniapp/src/telegram.js): initData, ready/expand, покраску
// хрома и системную кнопку "назад". Кнопка настоящая: без неё половина
// экранов не закрывается, и клик по ней проверить было бы нечем.
func telegramStubScript(initData string) string {
	return `<script>
(function () {
  var back = { visible: false, handlers: [] };
  window.Telegram = { WebApp: {
    initData: ` + strconv.Quote(initData) + `,
    initDataUnsafe: {},
    platform: 'sandbox',
    colorScheme: 'dark',
    themeParams: {},
    ready: function () {},
    expand: function () {},
    close: function () { console.log('[sandbox] WebApp.close()'); },
    setHeaderColor: function () {},
    setBackgroundColor: function () {},
    HapticFeedback: { impactOccurred: function () {}, notificationOccurred: function () {}, selectionChanged: function () {} },
    BackButton: {
      show: function () { back.visible = true; render(); },
      hide: function () { back.visible = false; render(); },
      onClick: function (h) { back.handlers.push(h); },
      offClick: function (h) { back.handlers = back.handlers.filter(function (x) { return x !== h; }); },
    },
  }};
  // Системная кнопка Telegram живёт вне вебвью, поэтому в песочнице её
  // приходится рисовать самим -- иначе экраны, которые закрываются только
  // ею, останутся непроверяемыми.
  var btn;
  function render() {
    if (!btn) {
      btn = document.createElement('button');
      btn.id = 'sandbox-back';
      btn.textContent = '← назад (Telegram)';
      btn.setAttribute('style', 'position:fixed;left:8px;bottom:8px;z-index:9999;padding:8px 12px;border-radius:8px;border:1px solid #666;background:#222;color:#eee;font:12px system-ui');
      btn.onclick = function () { back.handlers.forEach(function (h) { h(); }); };
      document.body.appendChild(btn);
    }
    btn.style.display = back.visible ? 'block' : 'none';
  }
  document.addEventListener('DOMContentLoaded', render);
})();
</script>`
}

// signInitData подписывает initData так же, как это делает Telegram, но
// локальным ключом. Формат -- строго по документации Mini Apps: пары
// key=value, отсортированные по ключу, склеенные \n, HMAC на ключе
// HMAC("WebAppData", token).
func signInitData(token string, userID int64, now time.Time) string {
	user, _ := json.Marshal(map[string]any{
		"id":            userID,
		"first_name":    "Песочница",
		"username":      "sandbox",
		"language_code": "ru",
	})
	fields := map[string]string{
		"auth_date": strconv.FormatInt(now.Unix(), 10),
		"query_id":  "sandbox",
		"user":      string(user),
	}
	keys := make([]string, 0, len(fields))
	for k := range fields {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	pairs := make([]string, 0, len(keys))
	for _, k := range keys {
		pairs = append(pairs, k+"="+fields[k])
	}
	secretMAC := hmac.New(sha256.New, []byte("WebAppData"))
	secretMAC.Write([]byte(token))
	checkMAC := hmac.New(sha256.New, secretMAC.Sum(nil))
	checkMAC.Write([]byte(strings.Join(pairs, "\n")))

	values := url.Values{}
	for k, v := range fields {
		values.Set(k, v)
	}
	values.Set("hash", hex.EncodeToString(checkMAC.Sum(nil)))
	return values.Encode()
}

// fakeAgent -- роутер, которого нет. Любая команда мгновенно отвечает успехом:
// песочница проверяет экраны и переходы, а не поведение железа. Ответы с
// ошибкой имитируются отдельным флагом, когда понадобится.
type fakeAgent struct {
	mu      sync.Mutex
	results map[string]wire.CommandResult
	cmds    map[string]wire.Command
}

func (f *fakeAgent) ensure() {
	if f.results == nil {
		f.results = map[string]wire.CommandResult{}
		f.cmds = map[string]wire.Command{}
	}
}

func (f *fakeAgent) Enqueue(userID int64, cmd wire.Command) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ensure()
	f.cmds[key(userID, cmd.ID)] = cmd
	f.results[key(userID, cmd.ID)] = wire.CommandResult{
		ID:         cmd.ID,
		Status:     "ok",
		Output:     sandboxOutput(cmd.Action, cmd.Args),
		DurationMs: 42,
	}
	slog.Info("песочница: команда принята", "action", cmd.Action, "args", argsLine(cmd.Args))
	return nil
}

func (f *fakeAgent) AwaitResult(_ context.Context, userID int64, id string, _ time.Duration) (*wire.CommandResult, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ensure()
	res, ok := f.results[key(userID, id)]
	if !ok {
		return nil, false
	}
	return &res, true
}

func (f *fakeAgent) CommandByID(userID int64, id string) (wire.Command, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ensure()
	c, ok := f.cmds[key(userID, id)]
	return c, ok
}

func (f *fakeAgent) Dequeue(_ context.Context, _ int64, _ time.Duration) (*wire.Command, bool) {
	return nil, false
}
func (f *fakeAgent) RecordResult(_ int64, _ wire.CommandResult) error { return nil }

// Ссылку на исходное сообщение в Telegram песочница не хранит: связывать
// ответ не с чем, бота здесь нет.
func (f *fakeAgent) ConsumeOriginRef(_ int64, _ string) (cmdpkg.MessageRef, bool) {
	return cmdpkg.MessageRef{}, false
}
func (f *fakeAgent) DropPending(_ int64, _ string) []wire.Command { return nil }

func key(userID int64, id string) string { return strconv.FormatInt(userID, 10) + "/" + id }
