package main

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// stripPort returns the host part of a "host:port" key. For bare hosts
// (no colon) the input is returned unchanged. For knownhosts-style entries
// with multiple comma-separated names ("foo,bar:22"), only the first
// name is considered (this matches how the wizard writes new entries —
// one host per line).
func stripPort(hostKey string) string {
	// Take the first comma-separated name first (just in case).
	if i := strings.IndexByte(hostKey, ','); i >= 0 {
		hostKey = hostKey[:i]
	}
	// Strip an IPv6 bracket pair: [::1]:22 -> ::1
	if strings.HasPrefix(hostKey, "[") {
		if end := strings.Index(hostKey, "]"); end > 0 {
			return hostKey[1:end]
		}
	}
	if i := strings.LastIndexByte(hostKey, ':'); i >= 0 {
		// Avoid splitting bare IPv6 addresses (which contain multiple ':').
		// Heuristic: only strip the suffix when everything after the last
		// ':' parses as a port number — i.e. all-digits, length<=5.
		suffix := hostKey[i+1:]
		if isDigits(suffix) && len(suffix) > 0 && len(suffix) <= 5 {
			return hostKey[:i]
		}
	}
	return hostKey
}

func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// ListKnownHostAliases reads the known_hosts file and returns a sorted
// list of unique host keys (the first column of each line, with the
// trailing :port stripped for display).
func ListKnownHostAliases(path string) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()

	seen := map[string]struct{}{}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		alias := stripPort(fields[0])
		if alias == "" {
			continue
		}
		seen[alias] = struct{}{}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	out := make([]string, 0, len(seen))
	for k := range seen {
		out = append(out, k)
	}
	sort.Strings(out)
	return out, nil
}

// ForgetKnownHost rewrites the known_hosts file with all lines whose
// first-column host (port-stripped) does NOT equal the supplied alias.
// Returns the number of lines removed.
func ForgetKnownHost(path, alias string) (int, error) {
	if alias == "" {
		return 0, fmt.Errorf("empty alias")
	}
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return 0, nil
		}
		return 0, err
	}
	var kept []string
	removed := 0
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		raw := sc.Text()
		trim := strings.TrimSpace(raw)
		if trim == "" || strings.HasPrefix(trim, "#") {
			kept = append(kept, raw)
			continue
		}
		fields := strings.Fields(trim)
		if len(fields) == 0 {
			kept = append(kept, raw)
			continue
		}
		host := stripPort(fields[0])
		if host == alias {
			removed++
			continue
		}
		kept = append(kept, raw)
	}
	if err := sc.Err(); err != nil {
		f.Close()
		return 0, err
	}
	f.Close()

	if removed == 0 {
		return 0, nil
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return 0, err
	}
	tmp := path + ".tmp"
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return 0, err
	}
	w := bufio.NewWriter(out)
	for _, line := range kept {
		if _, err := w.WriteString(line + "\n"); err != nil {
			out.Close()
			os.Remove(tmp)
			return 0, err
		}
	}
	if err := w.Flush(); err != nil {
		out.Close()
		os.Remove(tmp)
		return 0, err
	}
	if err := out.Close(); err != nil {
		os.Remove(tmp)
		return 0, err
	}
	if err := os.Rename(tmp, path); err != nil {
		return 0, err
	}
	return removed, nil
}

// ForgetKnownHostInteractive is the CLI entry point: lists current
// aliases, asks which to forget (operator types alias name or "*" for
// all), and calls ForgetKnownHost. Returns nil on success / no-op.
//
// Если state передан, для alias-ов, совпадающих с nickname'ом живого
// агента в wizard.toml, выводится warning + требуется явный y/N. Это
// защита от: оператор forget'нет alias живого агента → следующий connect
// делает TOFU без сравнения с прежним fingerprint'ом, что эквивалентно
// принять любой ключ (и любой подменённый роутер). state=nil оставляет
// старое поведение (CLI usage где state не загружен).
func ForgetKnownHostInteractive(state *State) error {
	path := filepath.Join(defaultCacheDir(), "known_hosts")
	aliases, err := ListKnownHostAliases(path)
	if err != nil {
		PrintFail("чтение " + path + ": " + err.Error())
		return err
	}
	if len(aliases) == 0 {
		PrintWarn("known_hosts пуст или не существует — нечего забывать")
		return nil
	}

	// Map nickname → bool для быстрой проверки.
	live := map[string]bool{}
	if state != nil {
		for _, a := range state.Agents {
			live[a.Nickname] = true
		}
	}

	fmt.Println(Colorize("Текущие записи в known_hosts:", ColorBold))
	for _, a := range aliases {
		marker := ""
		if live[a] {
			marker = " " + Colorize("(живой агент в wizard.toml)", ColorYellow)
		}
		fmt.Println("  • " + a + marker)
	}
	fmt.Println()
	answer := strings.TrimSpace(Ask("Какой alias забыть? (имя из списка, '*' — все, Enter — отмена)", ""))
	if answer == "" {
		PrintInfo("отмена")
		return nil
	}

	if answer == "*" {
		// Проверяем, есть ли среди aliases живые агенты.
		var liveHits []string
		for _, a := range aliases {
			if live[a] {
				liveHits = append(liveHits, a)
			}
		}
		if len(liveHits) > 0 {
			PrintWarn("в known_hosts есть alias'ы живых агентов: " + strings.Join(liveHits, ", "))
			PrintWarn("после forget следующий connect к ним пойдёт через TOFU без сравнения fingerprint'а — подменённый роутер примется без проверки")
			confirm := strings.ToLower(strings.TrimSpace(Ask("точно забыть ВСЕ записи? [y/N]", "n")))
			if confirm != "y" && confirm != "yes" {
				PrintInfo("отмена")
				return nil
			}
		}
		total := 0
		for _, a := range aliases {
			n, err := ForgetKnownHost(path, a)
			if err != nil {
				PrintFail(a + ": " + err.Error())
				return err
			}
			total += n
		}
		PrintOK(fmt.Sprintf("удалено %d записей из %s", total, path))
		return nil
	}

	// Validate: alias must be in the listed set.
	found := false
	for _, a := range aliases {
		if a == answer {
			found = true
			break
		}
	}
	if !found {
		PrintFail("alias " + answer + " не найден в known_hosts")
		return fmt.Errorf("alias not found: %s", answer)
	}

	if live[answer] {
		PrintWarn(answer + " — alias живого агента в wizard.toml")
		PrintWarn("после forget следующий connect к нему пойдёт через TOFU без сравнения fingerprint'а — подменённый роутер примется без проверки")
		confirm := strings.ToLower(strings.TrimSpace(Ask(fmt.Sprintf("точно забыть %s? [y/N]", answer), "n")))
		if confirm != "y" && confirm != "yes" {
			PrintInfo("отмена")
			return nil
		}
	}

	n, err := ForgetKnownHost(path, answer)
	if err != nil {
		PrintFail(err.Error())
		return err
	}
	if n == 0 {
		PrintWarn("ничего не удалено")
		return nil
	}
	PrintOK(fmt.Sprintf("удалено %d записей для %s из %s", n, answer, path))
	return nil
}
