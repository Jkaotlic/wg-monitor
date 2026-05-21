package main

import (
	"bufio"
	"fmt"
	"os"
	"strings"
)

// ANSI color codes.
const (
	ColorReset  = "\033[0m"
	ColorBold   = "\033[1m"
	ColorDim    = "\033[2m"
	ColorRed    = "\033[31m"
	ColorGreen  = "\033[32m"
	ColorYellow = "\033[33m"
	ColorBlue   = "\033[34m"
	ColorCyan   = "\033[36m"
)

// UseColor controls whether Colorize emits escape codes.
// Auto-detected at startup; can be overridden by --no-color flag.
var UseColor = isTerminal(os.Stdout) && os.Getenv("NO_COLOR") == ""

func isTerminal(f *os.File) bool {
	// Minimal isatty: check if stdout is a char device.
	// Avoids pulling in mattn/go-isatty for a single use.
	st, err := f.Stat()
	if err != nil {
		return false
	}
	return st.Mode()&os.ModeCharDevice != 0
}

func Colorize(s, color string) string {
	if !UseColor || os.Getenv("NO_COLOR") != "" {
		return s
	}
	return color + s + ColorReset
}

// Print helpers.

func PrintStep(n, total int, name string) {
	header := fmt.Sprintf("[%d/%d] %s", n, total, name)
	fmt.Println(Colorize(header, ColorBold))
}

func PrintOK(msg string) {
	fmt.Printf("  %s %s\n", Colorize("✓", ColorGreen), msg)
}

func PrintFail(msg string) {
	fmt.Printf("  %s %s\n", Colorize("✗", ColorRed), msg)
}

func PrintWarn(msg string) {
	fmt.Printf("  %s %s\n", Colorize("⚠", ColorYellow), msg)
}

func PrintInfo(msg string) {
	fmt.Printf("  %s %s\n", Colorize("→", ColorCyan), msg)
}

func PrintSkip(msg string) {
	fmt.Printf("  %s %s %s\n", Colorize("✓", ColorGreen), msg,
		Colorize("→ скипаю", ColorDim))
}

// Ask prompts for free-form input. If user enters empty string and defaultVal
// is non-empty, returns defaultVal.
func Ask(prompt, defaultVal string) string {
	if defaultVal != "" {
		fmt.Printf("%s [%s]: ", prompt, defaultVal)
	} else {
		fmt.Printf("%s: ", prompt)
	}
	r := bufio.NewReader(os.Stdin)
	line, _ := r.ReadString('\n')
	line = strings.TrimSpace(line)
	if line == "" {
		return defaultVal
	}
	return line
}

func cleanPromptDefaultLeak(s string) string {
	return strings.TrimRight(strings.TrimSpace(s), "]")
}

// AskSecret prompts without echoing input.
func AskSecret(prompt string) string {
	fmt.Printf("%s: ", prompt)
	b, err := readPasswordNoEcho(int(os.Stdin.Fd()))
	fmt.Println()
	if err != nil {
		return ""
	}
	return string(b)
}

// AskChoice presents [A]/[B]/[C]... options and returns the selected key (uppercase).
// Re-prompts until a valid key is entered.
func AskChoice(prompt string, options []ChoiceOption) string {
	for _, o := range options {
		fmt.Printf("  [%s] %s\n", o.Key, o.Label)
	}
	for {
		fmt.Print(prompt + " > ")
		r := bufio.NewReader(os.Stdin)
		line, _ := r.ReadString('\n')
		line = strings.TrimSpace(strings.ToUpper(line))
		for _, o := range options {
			if line == strings.ToUpper(o.Key) {
				return strings.ToUpper(o.Key)
			}
		}
		PrintFail("Не понял. Введи букву из списка.")
	}
}

// ChoiceOption represents a single choice in AskChoice.
type ChoiceOption struct {
	Key   string
	Label string
}

// Confirm asks a yes/no question. defaultYes makes Enter == yes.
func Confirm(prompt string, defaultYes bool) bool {
	suffix := "[y/N]"
	if defaultYes {
		suffix = "[Y/n]"
	}
	fmt.Printf("%s %s: ", prompt, suffix)
	r := bufio.NewReader(os.Stdin)
	line, _ := r.ReadString('\n')
	line = strings.TrimSpace(strings.ToLower(line))
	if line == "" {
		return defaultYes
	}
	return line == "y" || line == "yes" || line == "д" || line == "да"
}
