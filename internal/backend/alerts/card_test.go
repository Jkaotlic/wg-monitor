package alerts

import (
	"strings"
	"testing"
)

func TestCard_BadgeLabelSummaryOnly(t *testing.T) {
	c := Card{Badge: "✅", Label: "📊 Диагностика", Summary: "всё ок"}
	got := c.Render(CardOpts{})
	want := "✅ 📊 Диагностика: всё ок"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestCard_NoSummaryDropsTrailingColon(t *testing.T) {
	c := Card{Badge: "ℹ", Label: "Помощь"}
	got := c.Render(CardOpts{})
	want := "ℹ Помощь"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestCard_DetailsPlainHaveBlankLineSeparator(t *testing.T) {
	c := Card{Badge: "❌", Label: "📊 Диагностика", Summary: "не получилось",
		Details: "deeper info"}
	got := c.Render(CardOpts{})
	if !strings.Contains(got, "не получилось\n\ndeeper info") {
		t.Errorf("expected blank-line separator between summary and details:\n%s", got)
	}
}

func TestCard_DetailsCodeFenceWhenRequested(t *testing.T) {
	c := Card{Badge: "📊", Label: "Диагностика", Summary: "raw report",
		Details: "{\"foo\":1}"}
	got := c.Render(CardOpts{CodeFenceDetails: true})
	if !strings.Contains(got, "```\n{\"foo\":1}\n```") {
		t.Errorf("expected code fence around details:\n%s", got)
	}
}

func TestCard_HintRendersBelowDetailsWithEmoji(t *testing.T) {
	c := Card{Badge: "❌", Label: "📊 Диагностика", Summary: "no report",
		Hint: "Запусти ещё раз."}
	got := c.Render(CardOpts{})
	if !strings.Contains(got, "\n\n💡 Запусти ещё раз.") {
		t.Errorf("expected hint block with 💡 prefix:\n%s", got)
	}
}

func TestCard_HintWithoutDetailsStillHasBlankLine(t *testing.T) {
	c := Card{Badge: "❌", Label: "Команда", Summary: "не сработало",
		Hint: "проверь связь"}
	got := c.Render(CardOpts{})
	if !strings.Contains(got, "не сработало\n\n💡 проверь связь") {
		t.Errorf("hint must follow summary with blank line if no details:\n%s", got)
	}
}

func TestCard_FullQuadOrdering(t *testing.T) {
	c := Card{Badge: "❌", Label: "Тест", Summary: "summary line",
		Details: "details body", Hint: "hint line"}
	got := c.Render(CardOpts{})
	want := "❌ Тест: summary line\n\ndetails body\n\n💡 hint line"
	if got != want {
		t.Errorf("got:\n%s\n\nwant:\n%s", got, want)
	}
}

func TestCard_MaxBytesTruncatesWithEllipsis(t *testing.T) {
	long := strings.Repeat("Я", 5000)
	c := Card{Badge: "📊", Label: "Диагностика", Summary: "raw", Details: long}
	got := c.Render(CardOpts{MaxBytes: 200})
	if len(got) > 200 {
		t.Errorf("rendered length %d exceeds MaxBytes=200", len(got))
	}
	if !strings.HasSuffix(got, "…") {
		t.Errorf("expected ellipsis suffix on truncation, got: ...%q", got[len(got)-20:])
	}
}
