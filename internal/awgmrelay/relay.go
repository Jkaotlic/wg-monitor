// Package awgmrelay embeds the Python relay that drives a router's awg-manager
// terminal out-of-band (raw-socket websocket + the awg-manager HTTP API, stdlib
// only — no pip deps, just python3). It is the single source of truth shared by:
//
//   - the backend (dashboard "Revive via AWG Manager"), which self-provisions the
//     relay to a temp file and runs it, so revive needs no wizard step; and
//   - the wizard (deferred-awgm deploy), which uploads it to the VPS.
package awgmrelay

import _ "embed"

//go:embed awgm-relay.py
var Script []byte

// ScriptString is Script as a string, for callers that upload or write it as text.
var ScriptString = string(Script)
