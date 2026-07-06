package provision

import (
	"reflect"
	"testing"
)

// --- (a) Template ---------------------------------------------------------

func TestTemplate_ProvisionReturnsEightStepInstallSequence(t *testing.T) {
	want := []Step{
		{Name: StepTerminalConnected, Status: StepPending},
		{Name: StepArchDetected, Status: StepPending},
		{Name: StepDownloading, Status: StepPending},
		{Name: StepChecksumOK, Status: StepPending},
		{Name: StepConfigWritten, Status: StepPending},
		{Name: StepInitInstalled, Status: StepPending},
		{Name: StepServiceStarted, Status: StepPending},
		{Name: StepVerifyOnline, Status: StepPending},
	}

	got := Template(KindProvision)

	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Template(KindProvision) = %+v, want %+v", got, want)
	}
}

func TestTemplate_RepairRepointReturnsFourStepSequence(t *testing.T) {
	want := []Step{
		{Name: StepTerminalConnected, Status: StepPending},
		{Name: StepBackendURLRewrite, Status: StepPending},
		{Name: StepServiceRestarted, Status: StepPending},
		{Name: StepVerifyOnline, Status: StepPending},
	}

	got := Template(KindRepairRepoint)

	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Template(KindRepairRepoint) = %+v, want %+v", got, want)
	}
}

// RepairReinstall is documented in the provisioning-rework plan (Task 8) as
// "same as provision-install" (re-mint token, verify checksums,
// bootstrap_install) — it drives the identical relay install flow as a
// fresh KindProvision run, just against an existing nickname, so it must
// use the same 8-step template.
func TestTemplate_RepairReinstallMatchesProvisionSequence(t *testing.T) {
	got := Template(KindRepairReinstall)
	want := Template(KindProvision)

	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Template(KindRepairReinstall) = %+v, want (same as KindProvision) %+v", got, want)
	}
}

// KindRegister ("mint token only") never touches the relay — the handler
// task (Task 8) describes it as completing instantly with no relay call, so
// there is no install checklist to render for it.
func TestTemplate_RegisterReturnsNoSteps(t *testing.T) {
	got := Template(KindRegister)

	if len(got) != 0 {
		t.Fatalf("Template(KindRegister) = %+v, want empty", got)
	}
}

// Template must build a fresh slice on every call: two callers (e.g. two
// concurrent Store.Create calls for two different jobs) must never observe
// each other's mutations through a shared package-level backing array. This
// mirrors job.go's own copySteps discipline, just one layer further out —
// Store.Create deep-copies whatever it is handed, but only Template itself
// can guarantee two *separate calls* never alias in the first place.
func TestTemplate_ReturnsIndependentSlicesAcrossCalls(t *testing.T) {
	first := Template(KindProvision)
	second := Template(KindProvision)

	first[0].Status = StepDone
	first[0].Detail = "mutated"

	if second[0].Status != StepPending || second[0].Detail != "" {
		t.Fatalf("Template shares a backing array across calls: second call observed %+v after mutating the first call's result", second[0])
	}

	firstRepoint := Template(KindRepairRepoint)
	secondRepoint := Template(KindRepairRepoint)
	firstRepoint[0].Status = StepFailed
	if secondRepoint[0].Status != StepPending {
		t.Fatalf("Template(KindRepairRepoint) shares a backing array across calls: got %+v", secondRepoint[0])
	}
}

// --- (b) ParseStepLine -----------------------------------------------------

func TestParseStepLine(t *testing.T) {
	tests := []struct {
		name       string
		line       string
		wantName   string
		wantDetail string
		wantOK     bool
	}{
		{
			name:     "marker with name only",
			line:     "__WG_STEP__ downloading",
			wantName: "downloading", wantDetail: "", wantOK: true,
		},
		{
			name:     "marker with name and detail",
			line:     "__WG_STEP__ arch_detected arm64",
			wantName: "arch_detected", wantDetail: "arm64", wantOK: true,
		},
		{
			name:     "unrelated line",
			line:     "random noise",
			wantName: "", wantDetail: "", wantOK: false,
		},
		{
			name:     "empty line",
			line:     "",
			wantName: "", wantDetail: "", wantOK: false,
		},
		{
			name:     "marker alone with no name",
			line:     "__WG_STEP__",
			wantName: "", wantDetail: "", wantOK: false,
		},
		{
			name:     "marker not at start of line is not a match",
			line:     "prefix __WG_STEP__ downloading",
			wantName: "", wantDetail: "", wantOK: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			name, detail, ok := ParseStepLine(tt.line)
			if name != tt.wantName || detail != tt.wantDetail || ok != tt.wantOK {
				t.Errorf("ParseStepLine(%q) = (%q, %q, %v), want (%q, %q, %v)",
					tt.line, name, detail, ok, tt.wantName, tt.wantDetail, tt.wantOK)
			}
		})
	}
}

// --- (c) RedactToken --------------------------------------------------------

func TestRedactToken_ReplacesRawTokenWithMarker(t *testing.T) {
	got := RedactToken("token: abc123 rest", "abc123")
	want := "token: «redacted» rest"
	if got != want {
		t.Errorf("RedactToken() = %q, want %q", got, want)
	}
}

func TestRedactToken_EmptyRawTokenIsNoOp(t *testing.T) {
	s := "token: abc123 rest"
	got := RedactToken(s, "")
	if got != s {
		t.Errorf("RedactToken with empty rawToken = %q, want unchanged %q", got, s)
	}
}

func TestRedactToken_ReplacesEveryOccurrence(t *testing.T) {
	got := RedactToken("abc123 abc123 abc123", "abc123")
	want := "«redacted» «redacted» «redacted»"
	if got != want {
		t.Errorf("RedactToken() = %q, want %q", got, want)
	}
}

func TestRedactToken_NoMatchLeavesStringUnchanged(t *testing.T) {
	s := "nothing sensitive here"
	got := RedactToken(s, "abc123")
	if got != s {
		t.Errorf("RedactToken() = %q, want unchanged %q", got, s)
	}
}
