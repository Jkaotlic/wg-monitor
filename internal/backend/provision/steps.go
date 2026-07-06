package provision

import "strings"

// StepMarker prefixes every structured progress line the relay writes to
// stdout, e.g. "__WG_STEP__ arch_detected arm64". It is the wire contract
// between the Python relay (which prints these lines), this package's
// ParseStepLine (which reads them off the streaming RelayFunc), and the
// frontend's STEP_LABELS map (which renders Step.Name for a human) — the
// exact string values here are shared across all three, so changing one
// without the others silently breaks the relay->backend->frontend chain.
const StepMarker = "__WG_STEP__"

// Step name constants (stable ids shared with the frontend label map).
const (
	StepTerminalConnected = "terminal_connected"
	StepArchDetected      = "arch_detected"
	StepDownloading       = "downloading"
	StepChecksumOK        = "checksum_ok"
	StepConfigWritten     = "config_written"
	StepInitInstalled     = "init_installed"
	StepServiceStarted    = "service_started"
	StepBackendURLRewrite = "backend_url_rewritten"
	StepServiceRestarted  = "service_restarted"
	StepVerifyOnline      = "verify_online"
)

// redactedMarker replaces a raw secret token wherever RedactToken finds one.
const redactedMarker = "«redacted»"

// installSequence is the full 8-step relay-driven install checklist shared
// by KindProvision (fresh router) and KindRepairReinstall ("same as
// provision-install", per the provisioning-rework plan: re-mint token,
// verify checksums, bootstrap_install against an existing nickname).
//
// installSequence itself is never returned directly — Template always
// builds a fresh []Step from it (see stepsOf) so two callers can never
// observe each other's mutations through a shared backing array. Store.
// Create (job.go) already deep-copies whatever slice it's handed, but that
// only protects the store from the caller; it does nothing to stop two
// sibling calls to Template from aliasing each other's result before either
// one ever reaches a Store.
var installSequence = [...]string{
	StepTerminalConnected,
	StepArchDetected,
	StepDownloading,
	StepChecksumOK,
	StepConfigWritten,
	StepInitInstalled,
	StepServiceStarted,
	StepVerifyOnline,
}

// repointSequence is the 4-step checklist for KindRepairRepoint (rewrite
// backend.url + restart): no reinstall, just reconnect, repoint, restart,
// verify.
var repointSequence = [...]string{
	StepTerminalConnected,
	StepBackendURLRewrite,
	StepServiceRestarted,
	StepVerifyOnline,
}

// Template returns the pending step list for a job kind. Every call
// allocates a brand-new []Step — never a package-level slice handed back
// as-is — so no two callers (and no two Jobs built from two separate
// Template calls) can ever share a backing array.
//
// KindRegister ("mint token only") completes instantly with no relay call
// (per the provisioning-rework plan's HTTP-handler task), so it has no
// install checklist to render; it and any other/unknown kind get an empty,
// non-nil slice.
func Template(kind JobKind) []Step {
	switch kind {
	case KindProvision, KindRepairReinstall:
		return stepsOf(installSequence[:])
	case KindRepairRepoint:
		return stepsOf(repointSequence[:])
	default:
		return []Step{}
	}
}

// stepsOf builds a fresh []Step, all StepPending, from a sequence of step
// names. Called with a fresh backing array by every Template invocation.
func stepsOf(names []string) []Step {
	out := make([]Step, len(names))
	for i, name := range names {
		out[i] = Step{Name: name, Status: StepPending}
	}
	return out
}

// ParseStepLine extracts a step marker from one relay stdout line.
// "__WG_STEP__ arch_detected arm64" -> ("arch_detected","arm64",true).
//
// line must have StepMarker as a literal prefix (the relay always emits the
// marker as the first token of its own dedicated stdout line); anything
// else — including StepMarker appearing later in an unrelated line, or the
// marker alone with no step name following it — is not a step line, so ok
// is false and name/detail are both "".
func ParseStepLine(line string) (name, detail string, ok bool) {
	rest, hasMarker := strings.CutPrefix(line, StepMarker)
	if !hasMarker {
		return "", "", false
	}

	fields := strings.Fields(rest)
	if len(fields) == 0 {
		return "", "", false
	}

	name = fields[0]
	if len(fields) > 1 {
		detail = strings.Join(fields[1:], " ")
	}
	return name, detail, true
}

// RedactToken removes the raw token from any text before it leaves the
// backend (e.g. a Job's failure Tail, which echoes recent relay stdout/
// stderr lines that may otherwise contain the auth/enrollment token this
// job's relay run was invoked with). A blank rawToken is a no-op — Steps
// with no credential in play (e.g. KindRepairRepoint's non-token repoint
// flow) must not have this turn every occurrence of "" in s into noise.
func RedactToken(s, rawToken string) string {
	if rawToken == "" {
		return s
	}
	return strings.ReplaceAll(s, rawToken, redactedMarker)
}
