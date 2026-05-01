package main

import (
	"fmt"
	"io"
	"os"
)

// initMenuDeprecationMessage is the user-facing notice printed when the
// removed `init-menu` subcommand is invoked. v0.6.0 replaced the pinned
// control panel with a topic-bound ReplyKeyboard installed automatically
// when the bot first sends into the topic, so init-menu has no role.
const initMenuDeprecationMessage = "command removed in v0.6.0; ReplyKeyboard installs automatically — see docs/superpowers/specs/2026-04-30-ui-replykeyboard-hybrid-design.md"

// initMenuDeprecationStatus is the exit code used after printing the notice.
// Stays 0 so wrapper scripts and onboarding docs that still call init-menu
// don't fail their pipelines during the transition window.
const initMenuDeprecationStatus = 0

func cmdInitMenu(args []string) {
	_ = args
	writeInitMenuDeprecation(os.Stderr)
	os.Exit(initMenuDeprecationStatus)
}

func writeInitMenuDeprecation(w io.Writer) {
	fmt.Fprintln(w, initMenuDeprecationMessage)
}
