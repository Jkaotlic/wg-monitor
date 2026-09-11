package dnswatch

import "time"

// Config is the watchdog's runtime configuration. cmd/agent builds it from
// agent.DNSWatchdogConfig after LoadConfig has validated the block and filled
// its defaults.
type Config struct {
	Endpoint     string // own DoH resolver, https://host/<secret>
	CanaryDomain string // probed at the own resolver and at foreign candidates
	RUCanary     string // probed at RU candidates
	BootstrapIP  string // address of the endpoint host; empty → plain DNS

	Interval      time.Duration
	FailThreshold int
	OKThreshold   int
	Cooldown      time.Duration

	MaxForeign        int
	RUZones           []string
	RUCandidates      []string // in order of preference
	ForeignCandidates []string // in order of preference
	PinnedZones       []string
	PinnedCandidate   string

	StatePath string // dns-watchdog-state.json
}

// defaultMaxForeign applies when Config.MaxForeign is not positive.
const defaultMaxForeign = 3
