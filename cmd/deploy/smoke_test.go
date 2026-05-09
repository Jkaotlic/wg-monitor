package main

import (
	"reflect"
	"sort"
	"testing"
)

func TestSmokeCheckAgentReconcile(t *testing.T) {
	yaml := `
agents:
  - nickname: alpha
    token: "deadbeef"
    thread_id: 12
  - nickname: beta-router_2
    token: "cafebabe"
    thread_id: 34
`
	tests := []struct {
		name    string
		agents  []AgentState
		missing []string
	}{
		{
			name:    "all present",
			agents:  []AgentState{{Nickname: "alpha"}, {Nickname: "beta-router_2"}},
			missing: nil,
		},
		{
			name:    "one missing",
			agents:  []AgentState{{Nickname: "alpha"}, {Nickname: "gamma"}},
			missing: []string{"gamma"},
		},
		{
			name:    "all missing",
			agents:  []AgentState{{Nickname: "x"}, {Nickname: "y"}},
			missing: []string{"x", "y"},
		},
		{
			name:    "no agents",
			agents:  nil,
			missing: nil,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := smokeCheckAgentReconcile(yaml, tc.agents)
			sort.Strings(got)
			sort.Strings(tc.missing)
			if !reflect.DeepEqual(got, tc.missing) {
				t.Fatalf("missing mismatch: got=%v want=%v", got, tc.missing)
			}
		})
	}
}
