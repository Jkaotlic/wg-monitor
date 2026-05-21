package main

import (
	"fmt"
	"os"
)

// PrintBanner renders the wizard splash. Shown once per session at menu
// launch (NOT on every redraw — that gets old fast).
//
// Suppressed when colours are off (-no-color or NO_COLOR env) or when the
// caller sets WG_NO_BANNER=1 (handy for narrow terminals / scripted runs).
func PrintBanner() {
	if !UseColor || os.Getenv("NO_COLOR") != "" || os.Getenv("WG_NO_BANNER") != "" {
		return
	}
	const colorBlue = "\033[38;5;81m"
	title := []string{
		`██╗    ██╗ ██████╗     ███╗   ███╗ ██████╗ ███╗   ██╗`,
		`██║    ██║██╔════╝     ████╗ ████║██╔═══██╗████╗  ██║`,
		`██║ █╗ ██║██║  ███╗    ██╔████╔██║██║   ██║██╔██╗ ██║`,
		`██║███╗██║██║   ██║    ██║╚██╔╝██║██║   ██║██║╚██╗██║`,
		`╚███╔███╔╝╚██████╔╝    ██║ ╚═╝ ██║╚██████╔╝██║ ╚████║`,
		` ╚══╝╚══╝  ╚═════╝     ╚═╝     ╚═╝ ╚═════╝ ╚═╝  ╚═══╝`,
	}
	fmt.Println()
	for _, line := range title {
		fmt.Println(colorBlue + line + ColorReset)
	}
	fmt.Println()
}
