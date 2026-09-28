package actions

import (
	"context"
	"encoding/json"
	"testing"
)

func TestUpdateAgentConfigSetsWakeHooksOff(t *testing.T) {
	path := writeSampleConfig(t)
	stubRestart(t)
	if _, err := UpdateAgentConfig(context.Background(), map[string]any{"wake_hooks_off": true}, path, ""); err != nil {
		t.Fatal(err)
	}
	out, err := GetAgentConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	var view AgentConfigView
	if err := json.Unmarshal([]byte(out), &view); err != nil {
		t.Fatal(err)
	}
	if !view.WakeHooksOff {
		t.Fatalf("view = %s", out)
	}
}
