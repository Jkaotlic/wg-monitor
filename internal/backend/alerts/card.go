// Package alerts owns all user-facing Telegram message rendering. Card is
// the canonical template used by every action result and error path.
package alerts

import (
	"fmt"
	"strings"
)

// Card is the canonical shape of one bot-to-user message.
//
//	<Badge> <Label>: <Summary>
//	<Meta (optional; one compact context line)>
//
//	<Section title (optional)>:
//	  • <section line>
//
//	<Details (optional; may be code-fenced via CardOpts.CodeFenceDetails)>
//
//	💡 <Hint (optional)>
//
// Blank lines separate the three blocks. Any field may be empty; empty
// fields collapse their separator.
type Card struct {
	Badge    string // "✅" / "🔴" / "🟡" / "⚪" / "❌" / "⏳" / "ℹ"
	Label    string // "📊 Диагностика" — typically emoji + Russian noun
	Summary  string // one-line continuation after the label colon
	Meta     []string
	Sections []CardSection
	Details  string // optional multi-line body
	Hint     string // optional next-step explanation; rendered with 💡 prefix
}

// CardSection is a titled block rendered with indented bullets.
type CardSection struct {
	Title string
	Lines []string
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

	if len(c.Meta) > 0 {
		meta := compactNonEmpty(c.Meta)
		if len(meta) > 0 {
			b.WriteByte('\n')
			b.WriteString(strings.Join(meta, " · "))
		}
	}

	blocks := renderSections(c.Sections)
	if c.Details != "" {
		var d strings.Builder
		if opts.CodeFenceDetails {
			d.WriteString("```\n")
			d.WriteString(c.Details)
			d.WriteString("\n```")
		} else {
			d.WriteString(c.Details)
		}
		blocks = append(blocks, d.String())
	}
	for _, block := range blocks {
		if strings.TrimSpace(block) == "" {
			continue
		}
		b.WriteString("\n\n")
		b.WriteString(block)
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

// KV renders one compact key/value fragment for Card.Meta.
func KV(label string, value any) string {
	v := strings.TrimSpace(fmt.Sprint(value))
	if strings.TrimSpace(label) == "" {
		return v
	}
	if v == "" {
		return strings.TrimSpace(label)
	}
	return strings.TrimSpace(label) + ": " + v
}

func compactNonEmpty(in []string) []string {
	out := make([]string, 0, len(in))
	for _, s := range in {
		s = strings.TrimSpace(s)
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}

func renderSections(sections []CardSection) []string {
	var blocks []string
	for _, s := range sections {
		lines := compactNonEmpty(s.Lines)
		title := strings.TrimSpace(s.Title)
		if title == "" && len(lines) == 0 {
			continue
		}
		var b strings.Builder
		if title != "" {
			b.WriteString(title)
			b.WriteString(":")
		}
		for _, line := range lines {
			if b.Len() > 0 {
				b.WriteByte('\n')
			}
			b.WriteString("  • ")
			b.WriteString(line)
		}
		blocks = append(blocks, b.String())
	}
	return blocks
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
