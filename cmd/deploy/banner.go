package main

import (
	"fmt"
	"os"
)

// PrintBanner renders the wizard splash: a big WG-MONITOR title in cyan
// alongside an oinking pig in pink. Shown once per session at menu launch
// (NOT on every redraw — that gets old fast).
//
// Suppressed when colours are off (-no-color or NO_COLOR env) or when the
// caller sets WG_NO_BANNER=1 (handy for narrow terminals / scripted runs).
func PrintBanner() {
	if !UseColor || os.Getenv("NO_COLOR") != "" || os.Getenv("WG_NO_BANNER") != "" {
		return
	}
	const colorPink = "\033[38;5;213m"

	title := []string{
		`██╗    ██╗ ██████╗       ███╗   ███╗ ██████╗ ███╗   ██╗██╗████████╗ ██████╗ ██████╗ `,
		`██║    ██║██╔════╝       ████╗ ████║██╔═══██╗████╗  ██║██║╚══██╔══╝██╔═══██╗██╔══██╗`,
		`██║ █╗ ██║██║  ███╗█████╗██╔████╔██║██║   ██║██╔██╗ ██║██║   ██║   ██║   ██║██████╔╝`,
		`██║███╗██║██║   ██║╚════╝██║╚██╔╝██║██║   ██║██║╚██╗██║██║   ██║   ██║   ██║██╔══██╗`,
		`╚███╔███╔╝╚██████╔╝      ██║ ╚═╝ ██║╚██████╔╝██║ ╚████║██║   ██║   ╚██████╔╝██║  ██║`,
		` ╚══╝╚══╝  ╚═════╝       ╚═╝     ╚═╝ ╚═════╝ ╚═╝  ╚═══╝╚═╝   ╚═╝    ╚═════╝ ╚═╝  ╚═╝`,
	}
	pig := []string{
		`                  .--~~,__              `,
		`     :-....,-------` + "`" + `~~'._.'             `,
		`      ` + "`" + `-,,,  ,_      ;'~U'   < oink! `,
		`       _,-' ,'` + "`" + `-__; '--.              `,
		`      (_/'~~      ''''(;                `,
	}

	fmt.Println()
	for _, line := range title {
		fmt.Println(colorPink + line + ColorReset)
	}
	fmt.Println()
	for _, line := range pig {
		fmt.Println(colorPink + line + ColorReset)
	}
	fmt.Println()
}
