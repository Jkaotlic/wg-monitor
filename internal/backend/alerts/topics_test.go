package alerts

import (
	"context"
	"strings"
	"testing"
)

type fakeWelcomeSender struct {
	calls []welcomeCall
	err   error
}

type welcomeCall struct {
	chatID   int64
	threadID *int64
	text     string
	markup   any
}

func (f *fakeWelcomeSender) SendMessageWithReplyKeyboard(_ context.Context, chatID int64, threadID *int64, text, _ string, _ *int64, markup any) (int64, error) {
	f.calls = append(f.calls, welcomeCall{chatID, threadID, text, markup})
	return 1, f.err
}

func TestSendWelcome_IncludesNickname(t *testing.T) {
	f := &fakeWelcomeSender{}
	if err := SendWelcome(context.Background(), f, -100, 555, "testkeen", "stub-kb"); err != nil {
		t.Fatalf("SendWelcome: %v", err)
	}
	if len(f.calls) != 1 {
		t.Fatalf("want 1 send, got %d", len(f.calls))
	}
	got := f.calls[0]
	if got.chatID != -100 {
		t.Errorf("chatID: %d", got.chatID)
	}
	if got.threadID == nil || *got.threadID != 555 {
		t.Errorf("threadID: %v", got.threadID)
	}
	if !strings.Contains(got.text, "testkeen") {
		t.Errorf("text missing nickname: %s", got.text)
	}
}

func TestSendWelcome_PresentsVisibleOperatorMenu(t *testing.T) {
	f := &fakeWelcomeSender{}
	if err := SendWelcome(context.Background(), f, -100, 555, "testkeen", "stub-kb"); err != nil {
		t.Fatalf("SendWelcome: %v", err)
	}
	got := f.calls[0].text
	for _, want := range []string{"Меню роутера", "кнопки под этим сообщением", "без slash-команд"} {
		if !strings.Contains(got, want) {
			t.Fatalf("welcome text should explain visible menu %q, got: %s", want, got)
		}
	}
}

func TestSendWelcome_AttachesProvidedMarkup(t *testing.T) {
	f := &fakeWelcomeSender{}
	mark := "my-keyboard"
	if err := SendWelcome(context.Background(), f, -100, 555, "x", mark); err != nil {
		t.Fatal(err)
	}
	if f.calls[0].markup != mark {
		t.Errorf("markup not propagated: %v", f.calls[0].markup)
	}
}

func TestRepushKeyboard_PresentsMenuWithoutSlashFallback(t *testing.T) {
	f := &fakeWelcomeSender{}
	if err := RepushKeyboard(context.Background(), f, -100, 555, "del", "stub-kb"); err != nil {
		t.Fatalf("RepushKeyboard: %v", err)
	}
	got := f.calls[0].text
	for _, want := range []string{"Меню роутера", "del", "кнопки под этим сообщением"} {
		if !strings.Contains(got, want) {
			t.Fatalf("repush text should expose menu %q, got: %s", want, got)
		}
	}
	if strings.Contains(got, "/keyboard") || strings.Contains(got, "slash") {
		t.Fatalf("repush text must not rely on operators guessing slash commands: %s", got)
	}
}
