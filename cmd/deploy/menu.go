package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

const menuBoxInnerWidth = 59

type menuItem struct {
	Key   string
	Title string
	Help  string
}

func RunMenu(state *State, statePath string, secrets *SecretStore, dl *Downloader) {
	PrintBanner()

	// Best-effort: pull fresh fleet picture at startup so the menu reflects
	// what's actually on VPS. Silent on first-run / offline / missing token.
	if state.Backend.Domain != "" {
		if tok := secrets.GetNonInteractive("WIZARD_TOKEN"); tok != "" {
			if remote, err := startupListAgents(state.Backend.Domain, state.Backend.Host, tok); err == nil {
				merged, added, _, _ := MergeAgents(state.Agents, remote)
				state.Agents = merged
				if len(added) > 0 {
					PrintInfo(fmt.Sprintf("VPS sync на старте: добавлено %d новых роутеров (%s)", len(added), strings.Join(added, ", ")))
					_ = SaveState(statePath, state)
				}
			} else if shouldWarnStartupSync(err) {
				PrintWarn("VPS sync на старте не прошёл — проверь WIZARD_TOKEN (" + err.Error() + ")")
			}
		}
	}

	for {
		printMenuHeader(state)
		printMenuItems(state)
		line := readMenuChoice()

		switch line {
		case "1":
			runActionAndSave(state, statePath, secrets, func() error {
				return actionInstallBackend(state, secrets, dl)
			})
		case "2":
			runActionAndSave(state, statePath, secrets, func() error {
				return actionUpdateComponents(state, secrets, dl)
			})
		case "3":
			runRouterMenu(state, statePath, secrets, dl)
			continue
		case "4":
			runActionAndSave(state, statePath, secrets, func() error {
				return actionMigrateBackend(state, secrets, "")
			})
		case "5":
			actionDoctor(state, secrets, doctorOptions{}) //nolint:errcheck
		case "6":
			runActionAndSave(state, statePath, secrets, func() error {
				return actionSyncVPS(state, secrets)
			})
		case "7":
			runActionAndSave(state, statePath, secrets, func() error {
				return actionRestoreBackup(state, secrets, dl, RestoreBackupOptions{})
			})
		case "8":
			runServiceMenu(state, statePath, secrets)
			continue
		case "Q", "":
			return
		default:
			PrintFail("Не понял. Введи 1-8 или Q.")
		}
		fmt.Println()
		Ask("[Enter] чтобы вернуться в меню", "")
	}
}

func startupListAgents(domain, host, token string) ([]RemoteAgent, error) {
	c := NewVPSClientWithTimeoutAndDialHost(domain, token, 5*time.Second, host)
	if c == nil {
		return nil, fmt.Errorf("domain/token not configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	return c.ListAgents(ctx)
}

func shouldWarnStartupSync(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "HTTP 401") || strings.Contains(msg, "HTTP 403")
}

func printMenuHeader(state *State) {
	fmt.Println()
	fmt.Println(Colorize(boxTop(), ColorCyan))
	fmt.Println(Colorize(boxSplit("WG MONITOR // Deploy Console", Version), ColorCyan))
	fmt.Println(Colorize(boxMid(), ColorCyan))
	for _, row := range menuStatusRows(state) {
		if isInstalledStatusRow(row) {
			fmt.Println(boxLineStyled(row, ColorYellow))
		} else {
			fmt.Println(Colorize(boxLine(row), ColorCyan))
		}
	}
	fmt.Println(Colorize(boxBottom(), ColorCyan))
}

func isInstalledStatusRow(row string) bool {
	return strings.Contains(row, "installed ")
}

func menuStatusRows(state *State) []string {
	var rows []string
	if state.Backend.Host != "" {
		rows = append(rows,
			fmt.Sprintf("VPS    %s  %s",
				state.Backend.Host,
				emptyDash(state.Backend.Domain),
			),
			fmt.Sprintf("       installed %s at %s",
				emptyDash(state.Backend.LastDeployedVersion),
				formatDeployTime(state.Backend.LastDeploy),
			),
		)
	}
	for _, a := range state.Agents {
		rows = append(rows,
			fmt.Sprintf("Router %s:%d  %s",
				emptyDash(a.Host),
				a.Port,
				a.Nickname,
			),
			fmt.Sprintf("       installed %s at %s",
				emptyDash(a.LastDeployedVersion),
				formatDeployTime(a.LastDeploy),
			),
		)
	}
	return rows
}

func formatDeployTime(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "-"
	}
	if ts, err := time.Parse(time.RFC3339, raw); err == nil {
		return ts.UTC().Format("2006-01-02 15:04")
	}
	return raw
}

func boxTop() string {
	return "╔" + strings.Repeat("═", menuBoxInnerWidth) + "╗"
}

func boxMid() string {
	return "╠" + strings.Repeat("═", menuBoxInnerWidth) + "╣"
}

func boxBottom() string {
	return "╚" + strings.Repeat("═", menuBoxInnerWidth) + "╝"
}

func boxLine(text string) string {
	return "║" + fitRunes(" "+strings.TrimRight(text, " \t\r\n"), menuBoxInnerWidth) + "║"
}

func boxLineStyled(text, contentColor string) string {
	content := fitRunes(" "+strings.TrimRight(text, " \t\r\n"), menuBoxInnerWidth)
	return Colorize("║", ColorCyan) + Colorize(content, contentColor) + Colorize("║", ColorCyan)
}

func boxSplit(left, right string) string {
	left = " " + strings.TrimSpace(left)
	right = strings.TrimSpace(right) + " "
	leftLen := terminalCells(left)
	rightLen := terminalCells(right)
	if leftLen+rightLen+1 > menuBoxInnerWidth {
		keepLeft := menuBoxInnerWidth - rightLen - 4
		if keepLeft < 8 {
			return boxLine(left)
		}
		left = trimCells(left, keepLeft) + "..."
		leftLen = terminalCells(left)
	}
	gap := menuBoxInnerWidth - leftLen - rightLen
	return "║" + left + strings.Repeat(" ", gap) + right + "║"
}

func fitRunes(s string, width int) string {
	if terminalCells(s) > width {
		return trimCells(s, width-3) + "..."
	}
	return s + strings.Repeat(" ", width-terminalCells(s))
}

func trimCells(s string, width int) string {
	if width <= 0 {
		return ""
	}
	if terminalCells(s) <= width {
		return s
	}
	var b strings.Builder
	used := 0
	for _, r := range s {
		w := runeCells(r)
		if used+w > width {
			break
		}
		b.WriteRune(r)
		used += w
	}
	return b.String()
}

func terminalCells(s string) int {
	width := 0
	for _, r := range s {
		width += runeCells(r)
	}
	return width
}

func runeCells(r rune) int {
	switch {
	case r == 0:
		return 0
	case r < 32 || (r >= 0x7f && r < 0xa0):
		return 0
	case r >= 0x1100 && (r <= 0x115f ||
		r == 0x2329 || r == 0x232a ||
		(r >= 0x2e80 && r <= 0xa4cf) ||
		(r >= 0xac00 && r <= 0xd7a3) ||
		(r >= 0xf900 && r <= 0xfaff) ||
		(r >= 0xfe10 && r <= 0xfe19) ||
		(r >= 0xfe30 && r <= 0xfe6f) ||
		(r >= 0xff00 && r <= 0xff60) ||
		(r >= 0xffe0 && r <= 0xffe6)):
		return 2
	default:
		return 1
	}
}

func printMenuItems(state *State) {
	fmt.Print(renderMenuItems(mainMenuItems(os.Getenv("WG_LEGACY_ROUTER_SSH") == "1", state), "Q", "Выход"))
}

func mainMenuItems(_ bool, state *State) []menuItem {
	updateHelp := "когда нажимать: проверить актуальность backend/агентов и обновить только то, что реально устарело"
	if state.Backend.Host == "" && len(state.Agents) == 0 {
		updateHelp = "когда нажимать: позже; сначала поставь VPS и добавь хотя бы один роутер"
	}
	items := []menuItem{
		{Key: "1", Title: "VPS / backend", Help: "когда нажимать: первый запуск или полная переустановка backend на VPS"},
		{Key: "2", Title: "Проверить / обновить", Help: updateHelp},
		{Key: "3", Title: "Роутеры", Help: "когда нажимать: добавить, переустановить, re-enroll или удалить агент на роутере"},
		{Key: "4", Title: "Переезд на новый VPS", Help: "когда нажимать: backend уже новый, надо перепривязать старые роутеры через AWG Manager"},
		{Key: "5", Title: "Doctor", Help: "когда нажимать: что-то не работает или нужно быстро проверить VPS и всех агентов"},
		{Key: "6", Title: "Синхронизация с VPS", Help: "когда нажимать: wizard.toml пустой/старый, надо подтянуть список роутеров с backend"},
		{Key: "7", Title: "Сервис", Help: "когда нажимать: ручная правка wizard.toml, known_hosts, legacy recovery"},
	}
	items = append(items[:6], append([]menuItem{{
		Key:   "7",
		Title: "Restore / Disaster Recovery",
		Help:  "when to use: TG backup archive restore to current VPS or bootstrap a new VPS",
	}}, items[6:]...)...)
	items[7].Key = "8"
	return items
}

func routerMenuItems() []menuItem {
	return []menuItem{
		{Key: "1", Title: "Добавить новый", Help: "когда нажимать: новый роутер с Entware и AWG Manager/KeenDNS; wizard создаст enrollment и поставит агент"},
		{Key: "2", Title: "Re-enroll / переустановить", Help: "когда нажимать: потерян token, переезд, сломан агент; выдаст новый token и заново выполнит bootstrap"},
		{Key: "3", Title: "Удалить агента", Help: "когда нажимать: нужно снести wg-monitor с роутера или убрать ошибочную установку"},
		{Key: "4", Title: "Показать роутеры", Help: "когда нажимать: быстро посмотреть, кого wizard знает локально"},
	}
}

func serviceMenuItems(legacy bool) []menuItem {
	items := []menuItem{
		{Key: "1", Title: "Открыть wizard.toml", Help: "когда нажимать: ручная правка локального кэша wizard на этом ПК"},
		{Key: "2", Title: "Забыть known_hosts alias", Help: "когда нажимать: роутер физически заменили и SSH host key поменялся"},
	}
	if legacy {
		items = append(items, menuItem{Key: "3", Title: "Legacy Netfix route", Help: "когда нажимать: только старый SSH-деплой и recovery маршрута"})
	}
	return withCurrentVPSAdoption(items, legacy)
}

func withCurrentVPSAdoption(items []menuItem, legacy bool) []menuItem {
	if legacy && len(items) > 0 {
		items[len(items)-1].Key = "4"
	}
	return append(items[:2], append([]menuItem{{
		Key:   "3",
		Title: "Привязать текущий VPS",
		Help:  "когда нажимать: на этом ПК старый wizard.toml, а текущий VPS уже работает",
	}}, items[2:]...)...)
}

func renderMenuItems(items []menuItem, exitKey, exitTitle string) string {
	var b strings.Builder
	b.WriteString("\n")
	for _, item := range items {
		fmt.Fprintf(&b, "  [%s] %-28s %s\n", item.Key, item.Title, Colorize("(?)", ColorDim))
		if item.Help != "" {
			fmt.Fprintf(&b, "      %s\n", Colorize(item.Help, ColorDim))
		}
	}
	fmt.Fprintf(&b, "  [%s] %s\n\n", exitKey, exitTitle)
	return b.String()
}

func readMenuChoice() string {
	fmt.Print("> ")
	r := bufio.NewReader(os.Stdin)
	line, _ := r.ReadString('\n')
	return strings.TrimSpace(strings.ToUpper(line))
}

func runRouterMenu(state *State, statePath string, secrets *SecretStore, dl *Downloader) {
	for {
		fmt.Println()
		fmt.Println(Colorize("Роутеры", ColorBold))
		fmt.Print(renderMenuItems(routerMenuItems(), "B", "Назад"))
		switch readMenuChoice() {
		case "1":
			runActionAndSave(state, statePath, secrets, func() error {
				return actionAddRouter(state, secrets, dl)
			})
		case "2":
			runActionAndSave(state, statePath, secrets, func() error {
				return actionRepairAgentToken(state, secrets, dl, "")
			})
		case "3":
			runActionAndSave(state, statePath, secrets, func() error {
				return actionUninstallAgent(state, secrets, askUninstallTarget(state))
			})
		case "4":
			printKnownRouters(state)
		case "B", "Q", "":
			return
		default:
			PrintFail("Не понял. Введи 1-4 или B.")
		}
		fmt.Println()
		Ask("[Enter] чтобы вернуться в раздел Роутеры", "")
	}
}

func runServiceMenu(state *State, statePath string, secrets *SecretStore) {
	legacy := os.Getenv("WG_LEGACY_ROUTER_SSH") == "1"
	for {
		fmt.Println()
		fmt.Println(Colorize("Сервис", ColorBold))
		fmt.Print(renderMenuItems(serviceMenuItems(legacy), "B", "Назад"))
		switch readMenuChoice() {
		case "1":
			openInEditor(statePath)
			if reloaded, err := LoadState(statePath); err == nil {
				*state = *reloaded
			}
		case "2":
			ForgetKnownHostInteractive(state) //nolint:errcheck
		case "3":
			runActionAndSave(state, statePath, secrets, func() error {
				return actionAdoptBackend(state, secrets)
			})
		case "4":
			if !legacy {
				PrintFail("Не понял. Введи 1-3 или B.")
				break
			}
			if err := actionNetfix(state, netfixOptions{}); err != nil {
				PrintFail("netfix failed: " + err.Error())
			}
		case "B", "Q", "":
			return
		default:
			limit := "3"
			if legacy {
				limit = "4"
			}
			PrintFail("Не понял. Введи 1-" + limit + " или B.")
		}
		fmt.Println()
		Ask("[Enter] чтобы вернуться в раздел Сервис", "")
	}
}

func printKnownRouters(state *State) {
	if len(state.Agents) == 0 {
		PrintInfo("wizard пока не знает ни одного роутера")
		return
	}
	fmt.Println(Colorize("Роутеры в локальном wizard.toml:", ColorBold))
	for i, a := range state.Agents {
		mode := a.DeployMode
		if mode == "" {
			mode = "legacy-ssh"
		}
		fmt.Printf("  [%d] %s  mode=%s  awgm=%s  ssh=%s:%d  version=%s\n",
			i+1,
			a.Nickname,
			mode,
			a.AWGMURL,
			emptyDash(a.Host),
			a.Port,
			emptyDash(a.LastDeployedVersion),
		)
	}
}

func emptyDash(s string) string {
	if strings.TrimSpace(s) == "" {
		return "-"
	}
	return s
}

func runActionAndSave(state *State, statePath string, secrets *SecretStore, fn func() error) {
	if err := fn(); err != nil {
		PrintFail("action failed: " + err.Error())
	}
	if err := SaveState(statePath, state); err != nil {
		PrintFail("save state: " + err.Error())
	}
	PrintSecretsSaveAdvice(secrets)
}

func openInEditor(path string) {
	editor := os.Getenv("EDITOR")
	if editor == "" {
		if runtime.GOOS == "windows" {
			editor = "notepad"
		} else {
			editor = "nano"
		}
	}
	cmd := exec.Command(editor, path)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Run() //nolint:errcheck
}

// askUninstallTarget builds the UninstallTarget the operator wants to
// clean. Two paths:
//  1. They pick a nickname from state.Agents — we use its known SSH coords.
//  2. They pick "другой" → manual host/port/user — typical "I accidentally
//     installed on a router not in wizard.toml" scenario.
func askUninstallTarget(state *State) UninstallTarget {
	if len(state.Agents) == 0 {
		PrintInfo("в wizard.toml нет агентов — введи параметры роутера руками")
		return UninstallTarget{}
	}
	fmt.Println("Выбери роутер для удаления агента:")
	for i, a := range state.Agents {
		fmt.Printf("  [%d] %s (%s:%d)\n", i+1, a.Nickname, a.Host, a.Port)
	}
	fmt.Printf("  [%d] другой (ввести host/port/user руками)\n", len(state.Agents)+1)
	idx := parseIntOr(Ask("номер", "1"), 1)
	if idx < 1 || idx > len(state.Agents)+1 {
		idx = 1
	}
	if idx == len(state.Agents)+1 {
		return UninstallTarget{}
	}
	a := state.Agents[idx-1]
	return UninstallTarget{Nickname: a.Nickname, Host: a.Host, Port: a.Port, User: a.User}
}
