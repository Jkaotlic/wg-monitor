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
