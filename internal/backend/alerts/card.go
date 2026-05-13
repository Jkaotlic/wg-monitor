// Package alerts owns all user-facing Telegram message rendering. Card is
// the canonical 4-block template (badge + label/summary + details + hint)
// used by every action result and error path.
package alerts

import (
	"strings"
)

// Card is the canonical shape of one bot-to-user message.
//
//	<Badge> <Label>: <Summary>
//
//	<Details (optional; may be code-fenced via CardOpts.CodeFenceDetails)>
//
//	💡 <Hint (optional)>
//
// Blank lines separate the three blocks. Any field may be empty; empty
// fields collapse their separator.
type Card struct {
	Badge   string // "✅" / "🔴" / "🟡" / "⚪" / "❌" / "⏳" / "ℹ"
	Label   string // "📊 Диагностика" — typically emoji + Russian noun
	Summary string // one-line continuation after the label colon
	Details string // optional multi-line body
	Hint    string // optional next-step explanation; rendered with 💡 prefix
}

// CardOpts tunes Card.Render output.
type CardOpts struct {
	// CodeFenceDetails wraps Details in triple-backtick. Use only for
	// monospaced machine output (JSON, route tables, opkg logs). Never
	// for natural-language errors.
	CodeFenceDetails bool
	// MaxBytes hard-caps the rendered length. 0 means no cap. When the
	// rendered string would exceed MaxBytes, it is truncated rune-aware
	// with a single trailing "…".
	MaxBytes int
}

// Render assembles the card into its final on-the-wire string.
func (c Card) Render(opts CardOpts) string {
	var b strings.Builder
	if c.Badge != "" {
		b.WriteString(c.Badge)
		if c.Label != "" || c.Summary != "" {
			b.WriteByte(' ')
		}
	}
	b.WriteString(c.Label)
	if c.Summary != "" {
		if c.Label != "" {
			b.WriteString(": ")
		}
		b.WriteString(c.Summary)
	}
	if c.Details != "" {
		b.WriteString("\n\n")
		if opts.CodeFenceDetails {
			b.WriteString("```\n")
			b.WriteString(c.Details)
			b.WriteString("\n```")
		} else {
			b.WriteString(c.Details)
		}
	}
	if c.Hint != "" {
		b.WriteString("\n\n💡 ")
		b.WriteString(c.Hint)
	}
	out := b.String()
	if opts.MaxBytes > 0 && len(out) > opts.MaxBytes {
		out = truncateWithEllipsis(out, opts.MaxBytes)
	}
	return out
}

// truncateWithEllipsis trims by runes until the candidate (s + "…")
// fits maxBytes.
func truncateWithEllipsis(s string, maxBytes int) string {
	const ellipsis = "…"
	if maxBytes <= len(ellipsis) {
		return ellipsis[:maxBytes]
	}
	runes := []rune(s)
	for len(runes) > 0 {
		candidate := string(runes) + ellipsis
		if len(candidate) <= maxBytes {
			return candidate
		}
		runes = runes[:len(runes)-1]
	}
	return ellipsis
}
