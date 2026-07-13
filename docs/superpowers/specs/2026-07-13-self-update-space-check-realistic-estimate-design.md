# self_update disk-space preflight — realistic size estimate

**Date:** 2026-07-13
**Status:** approved (operator)

## Problem

`self_update` refuses to run when a pending-update alert fired on `testkeen`:

```
self_update: insufficient /opt space: 28000 KB free, need 65536 KB for the
new binary plus 10585 KB headroom (105852 KB total)
```

`checkSelfUpdateFreeSpace` ([internal/agent/actions/self_update.go](../../../internal/agent/actions/self_update.go))
computes `neededKB` from `maxSelfUpdateArtifactSize` (64 MB) — but that
constant is the HTTP download safety cap (`httpGetToFileWithFallback`'s
`maxBytes`, guarding against a corrupted/oversized CDN response), not an
estimate of the actual binary size. The real agent binaries are tiny:
`wg-monitor-agent-linux-arm64` = 2079 KB, `wg-monitor-agent-linux-mipsle` =
2114 KB (measured from the v0.14.3 release assets). The check was demanding
~32x the real requirement, refusing updates on routers with modest but
adequate free space — and testkeen is not unusual; Entware `/opt` partitions
on Keenetic routers are commonly 100-250 MB total.

## Fix

Decouple the disk-space estimate from the HTTP download cap. Add a new
constant, `selfUpdateEstimatedBinaryKB = 8192` (8 MB — roughly 4x the real
~2.1 MB binary, generous slack for the binary growing over future releases)
used **only** by `checkSelfUpdateFreeSpace`. `maxSelfUpdateArtifactSize` (64
MB) is untouched and keeps its existing role as the hard download-size
ceiling.

The 10% headroom floor (`selfUpdateMinFreeRatioPct`, matching
`OpkgRunner.SmartUpgrade`'s own `df -k /opt` convention) is left as-is —
operator decision: the needed-size correction alone restores a comfortable
margin (testkeen: `28000 − 8192 = 19808` KB free vs `10585` KB headroom, ~9
MB of slack), so the headroom ratio doesn't need loosening too.

## Testing

- `TestCheckSelfUpdateFreeSpaceSufficient`: unaffected numerically (900,000
  KB free clears either constant); update its comment to reference the new
  8,192 KB figure instead of the old 65,536 KB one.
- `TestCheckSelfUpdateFreeSpaceInsufficient` and
  `selfUpdateTestHarness`'s `freeSpaceOK=false` fixture (duplicated `df`
  output, both currently `total=100000 free=70000`): under the new smaller
  `neededKB` this fixture would flip to *sufficient* (70000-8192=61808 ≥
  10000), silently breaking both tests' intent. Both must move to a fixture
  that is still genuinely insufficient after the fix, e.g. `total=100000
  free=15000` (`15000-8192=6808 < 10000` headroom) — kept identical between
  the two call sites since they're intentionally testing the same threshold.
- New regression test using the exact real-world numbers from the incident
  (`total=105852 free=28000`) asserting `checkSelfUpdateFreeSpace` now
  returns nil — locks in the actual bug fixed.

## Out of scope

- Changing `selfUpdateMinFreeRatioPct` (10%) — operator declined.
- Dynamic/real asset-size probing (e.g. a `Content-Length` HEAD request
  before the disk check) — operator picked the fixed-constant approach.
- Any change to `maxSelfUpdateArtifactSize` or the HTTP download path.
