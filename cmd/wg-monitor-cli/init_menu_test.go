package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestInitMenuDeprecationMessageContent(t *testing.T) {
	if !strings.Contains(initMenuDeprecationMessage, "removed in v0.6.0") {
		t.Errorf("deprecation message missing version marker: %q", initMenuDeprecationMessage)
	}
	if !strings.Contains(initMenuDeprecationMessage, "ReplyKeyboard") {
		t.Errorf("deprecation message must point to ReplyKeyboard replacement: %q", initMenuDeprecationMessage)
	}
}

func TestWriteInitMenuDeprecation(t *testing.T) {
	var buf bytes.Buffer
	writeInitMenuDeprecation(&buf)
	if !strings.Contains(buf.String(), "removed in v0.6.0") {
		t.Errorf("writer output missing version marker: %s", buf.String())
	}
	if !strings.HasSuffix(buf.String(), "\n") {
		t.Errorf("writer output must terminate with newline so it lands on its own console line")
	}
}

func TestInitMenuDeprecationStatusIsZero(t *testing.T) {
	// Wrapper scripts (router self-onboarding docs) still pipe init-menu
	// through `set -e`. Keep status 0 during the transition window.
	if initMenuDeprecationStatus != 0 {
		t.Errorf("deprecation exit status must be 0, got %d", initMenuDeprecationStatus)
	}
}
