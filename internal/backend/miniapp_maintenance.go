package backend

import (
	"strings"
	"sync"
	"time"

	cmdpkg "github.com/Jkaotlic/wg-monitor/internal/backend/cmd"
	"github.com/Jkaotlic/wg-monitor/internal/backend/db"
)

// miniappRebootCooldown -- окно «роутер уже перезагружается»: столько роутер и
// перезагружается, и повтор внутри окна ничего, кроме второй перезагрузки, не даст.
const miniappRebootCooldown = 5 * time.Minute

// normalizeConfirmPhrase -- то же правило, что confirmReady в miniapp/src/sheet.js:
// виды дефиса (U+2010, U+2011) -- обычный дефис, пробелы по краям -- прочь,
// регистр не важен. Правило общее намеренно: разойдясь, экран показал бы
// активную кнопку, а сервер отказал бы. Общие случаи --
// testdata/confirm_phrase_cases.json, их прогоняют оба набора тестов.
func normalizeConfirmPhrase(s string) string {
	s = strings.NewReplacer("‐", "-", "‑", "-").Replace(s)
	return strings.ToLower(strings.TrimSpace(s))
}

// confirmPhraseMatches -- совпал ли набор с именем роутера. Пустое имя не
// совпадает ни с чем: подтверждать пустоту пустотой нельзя.
func confirmPhraseMatches(typed, routerName string) bool {
	want := normalizeConfirmPhrase(routerName)
	return want != "" && normalizeConfirmPhrase(typed) == want
}

func miniappIsRouterReboot(action string, args map[string]any) bool {
	name, _ := args["name"].(string)
	return action == "service_restart" && name == "router"
}

// miniappConfirmRequired -- необратимые действия, которые сервер не поставит
// в очередь без набранного имени роутера.
func miniappConfirmRequired(action string, args map[string]any) bool {
	return action == "firmware_install" || miniappIsRouterReboot(action, args)
}

// routerCooldown -- окно на роутер. В памяти процесса: рестарт бэкенда окно
// сбрасывает, и это приемлемо -- перезагрузка роутера длится столько же.
type routerCooldown struct {
	mu     sync.Mutex
	window time.Duration
	now    func() time.Time
	until  map[int64]time.Time
}

func newRouterCooldown(window time.Duration, now func() time.Time) *routerCooldown {
	return &routerCooldown{window: window, now: now, until: make(map[int64]time.Time)}
}

// tryStart атомарно открывает окно; false -- окно уже открыто. Проверка и
// запись под одним замком: два одновременных нажатия не пройдут оба.
func (c *routerCooldown) tryStart(routerID int64) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.now()
	if until, ok := c.until[routerID]; ok && now.Before(until) {
		return false
	}
	c.until[routerID] = now.Add(c.window)
	return true
}

// release снимает окно, когда команда в очередь не встала: роутер не
// перезагружается, и запрещать повтор незачем.
func (c *routerCooldown) release(routerID int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.until, routerID)
}

// miniappWakeWindow -- спит ли роутер, каким именно статусом и сколько минут
// команда его подождёт. Статус тот же, по которому экран флота пишет
// «спит»/«не на связи» (dashboardAgentFromUser; App.jsx считает спящим оба),
// и отдаётся ОТДЕЛЬНО от булева asleep (M7, fix round 1): «спит» и «не на
// связи» -- разные тексты для владельца («проснётся» против «появится»), и
// экран выбирает между ними по router_status, а не по одному сплющенному
// признаку.
func miniappWakeWindow(d Deps, u *db.User, action string, now time.Time) (asleep bool, status string, waitMin int) {
	st := dashboardAgentFromUser(*u, nil, now, dashboardStatusPolicyFromDeps(d)).Status
	if st != "sleeping" && st != "offline" {
		return false, "", 0
	}
	return true, st, int(cmdpkg.CommandTTL(action) / time.Minute)
}
