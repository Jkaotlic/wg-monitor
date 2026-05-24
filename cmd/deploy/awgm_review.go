package main

import (
	"fmt"
	"os"
	"strings"
)

type awgmInstallInputs struct {
	APIKey           string
	Login            string
	Password         string
	TerminalUser     string
	TerminalPassword string
}

type awgmFailureAction string

const (
	awgmFailureEdit   awgmFailureAction = "edit"
	awgmFailureRetry  awgmFailureAction = "retry"
	awgmFailureDefer  awgmFailureAction = "defer"
	awgmFailureCancel awgmFailureAction = "cancel"
)

func confirmAWGMInstallInputs(state *State, secrets *SecretStore, ag *AgentState, in *awgmInstallInputs) bool {
	if os.Getenv("WG_YES_TO_ALL") == "1" || ag == nil || in == nil {
		return true
	}
	for {
		printAWGMInstallInputs(state, ag, in)
		choice := strings.ToLower(strings.TrimSpace(Ask("Проверь данные: Enter/ok — продолжить, url/auth/terminal/kind/arch — исправить, cancel — отмена", "ok")))
		switch choice {
		case "", "ok", "o", "y", "yes", "да", "д":
			return true
		case "url", "u":
			editAWGMURL(ag)
		case "auth", "a":
			editAWGMAuth(secrets, ag.Nickname, in)
		case "terminal", "term", "t":
			editAWGMTerminal(secrets, ag.Nickname, in)
		case "kind", "k":
			editAWGMKind(ag)
		case "arch":
			editAWGMArch(ag)
		case "cancel", "c", "q":
			return false
		default:
			PrintWarn("не понял выбор: " + choice)
		}
	}
}

func printAWGMInstallInputs(state *State, ag *AgentState, in *awgmInstallInputs) {
	fmt.Println()
	fmt.Println(Colorize("AWG Manager / deploy данные:", ColorBold))
	fmt.Printf("  Router      : %s\n", ag.Nickname)
	fmt.Printf("  Kind        : %s\n", nonEmpty(ag.Kind, "static"))
	fmt.Printf("  Arch        : %s\n", nonEmpty(ag.Arch, "auto from AWG Manager"))
	fmt.Printf("  AWG URL     : %s\n", nonEmpty(ag.AWGMURL, "(empty)"))
	if state != nil {
		fmt.Printf("  Backend     : %s\n", nonEmpty(state.Backend.Domain, state.Backend.Host))
	}
	auth := "login/password"
	if strings.TrimSpace(in.APIKey) != "" {
		auth = "api-key"
	}
	fmt.Printf("  AWG auth    : %s (api=%s, login=%s, password=%s)\n",
		auth, present(in.APIKey), present(in.Login), present(in.Password))
	fmt.Printf("  Terminal    : user=%s password=%s\n",
		nonEmpty(in.TerminalUser, "root"), present(in.TerminalPassword))
	fmt.Println()
}

func present(v string) string {
	if strings.TrimSpace(v) == "" {
		return "missing"
	}
	return "set"
}

func editAWGMURL(ag *AgentState) {
	next := normalizeAWGMURL(Ask("AWG Manager URL (KeenDNS, https://...)", ag.AWGMURL))
	if next == "" {
		PrintWarn("AWG Manager URL пустой — оставил прежнее значение")
		return
	}
	ag.AWGMURL = next
}

func editAWGMKind(ag *AgentState) {
	current := nonEmpty(ag.Kind, "static")
	next := strings.ToLower(strings.TrimSpace(Ask("Kind (static/mobile)", current)))
	if next != "static" && next != "mobile" {
		PrintWarn("kind должен быть static или mobile — оставил " + current)
		return
	}
	ag.Kind = next
}

func editAWGMArch(ag *AgentState) {
	next := strings.TrimSpace(Ask("Arch (arm64/mipsle; Enter — auto from AWG Manager)", ag.Arch))
	if next == "" {
		ag.Arch = ""
		return
	}
	normalized, err := normalizeKeeneticArch(next)
	if err != nil {
		PrintWarn(err.Error())
		return
	}
	ag.Arch = normalized
}

func editAWGMAuth(secrets *SecretStore, nickname string, in *awgmInstallInputs) {
	suffix := strings.ToUpper(nickname)
	apiKeyName := "WG_AWGM_API_KEY_" + suffix
	loginName := "WG_AWGM_LOGIN_" + suffix
	passName := "WG_AWGM_PASS_" + suffix
	current := "login"
	if strings.TrimSpace(in.APIKey) != "" {
		current = "api"
	}
	mode := strings.ToLower(strings.TrimSpace(Ask("AWG auth mode (api/login)", current)))
	switch mode {
	case "api", "api-key", "apikey":
		key := AskSecret("AWG Manager API key for " + nickname)
		if key == "" {
			PrintWarn("API key пустой — оставил прежний auth")
			return
		}
		in.APIKey = key
		in.Login = ""
		in.Password = ""
		setSecretValue(secrets, apiKeyName, key)
		setSecretValue(secrets, loginName, "")
		setSecretValue(secrets, passName, "")
	case "login", "password", "pass":
		in.APIKey = ""
		os.Unsetenv(apiKeyName)
		setSecretValue(secrets, apiKeyName, "")
		login := strings.TrimSpace(Ask("AWG Manager login for "+nickname, in.Login))
		if login != "" {
			in.Login = login
			setSecretValue(secrets, loginName, login)
		}
		pass := AskSecret("AWG Manager password for " + nickname + " (Enter — оставить прежний)")
		if pass != "" {
			in.Password = pass
			setSecretValue(secrets, passName, pass)
		}
	default:
		PrintWarn("auth mode должен быть api или login")
	}
}

func editAWGMTerminal(secrets *SecretStore, nickname string, in *awgmInstallInputs) {
	suffix := strings.ToUpper(nickname)
	user := strings.TrimSpace(Ask("Entware terminal user", userOrDefault(in.TerminalUser, "root")))
	if user != "" {
		in.TerminalUser = user
	}
	pass := AskSecret("Entware terminal root password for " + nickname + " (Enter — оставить прежний)")
	if pass != "" {
		in.TerminalPassword = pass
		setSecretValue(secrets, "WG_KEENETIC_PASS_"+suffix, pass)
	}
}

func setSecretValue(secrets *SecretStore, key, value string) {
	if key == "" {
		return
	}
	if value != "" {
		os.Setenv(key, value)
	} else {
		os.Unsetenv(key)
	}
	if secrets == nil {
		return
	}
	if err := secrets.Set(key, value); err != nil && !strings.Contains(err.Error(), "secret cache disabled") {
		PrintWarn("не смог сохранить " + key + ": " + err.Error())
	}
}

func chooseAWGMFailureAction(err error, canDefer bool) awgmFailureAction {
	if os.Getenv("WG_YES_TO_ALL") == "1" {
		if canDefer {
			return awgmFailureDefer
		}
		return awgmFailureCancel
	}
	PrintWarn("AWG Manager шаг не прошёл: " + err.Error())
	prompt := "Что сделать? [e] исправить данные / [r] повторить / [c] отмена"
	def := "e"
	if canDefer {
		prompt = "Что сделать? [e] исправить данные / [r] повторить / [d] отложить на VPS / [c] отмена"
	}
	for {
		choice := strings.ToLower(strings.TrimSpace(Ask(prompt, def)))
		switch choice {
		case "", "e", "edit", "исправить":
			return awgmFailureEdit
		case "r", "retry", "повторить":
			return awgmFailureRetry
		case "d", "defer", "отложить":
			if canDefer {
				return awgmFailureDefer
			}
		case "c", "cancel", "q", "отмена":
			return awgmFailureCancel
		}
		PrintWarn("не понял выбор: " + choice)
	}
}
