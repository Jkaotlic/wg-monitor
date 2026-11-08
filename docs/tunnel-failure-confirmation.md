# Tunnel failure confirmation

The agent independently checks suspected tunnel failures using HTTPS to two
different networks. Requests bind both the tunnel device and its IPv4 source
address. They do not use router DNS or an environment HTTP proxy. A successful
independent request disproves a control-target-only outage; a fresh WireGuard
handshake alone does not prove that user traffic works.

Confirmation runs for failed matrix observations, including single-tunnel
routers and simultaneous failures across all tunnels. Work is limited to four
seconds per report and three tunnels at once. Missing interface addresses,
binding failures, cancellation and TLS certificate validation problems are
unknown evidence, not proof of an outage. Certificate validation stays enabled.
Stopped interfaces, address conflicts and configured ping-check failures are
not cleared by a successful independent request.

Reports retain the raw matrix/AWG Manager results separately from confirmation
results and recent RX/TX counter deltas. Keepalive traffic can increment those
counters, so movement alone never proves working internet access.

Automatic repairs retain the three-attempts-per-six-hours ceiling and add a
persisted ten-minute pause between starts. A working exit check can avoid a
restart. Recurrent failures leave traffic on an independently verified backup
instead of immediately moving it back. The repair history stores up to eight
sanitized pre-mutation incidents per tunnel using the existing backend database;
it excludes credentials, full configurations and free-form command output.

## Add a secondary DNS resolver without resetting policy

On agents supporting this mode, the existing `dns_reset` command accepts:

```json
{"mode":"add_secondary","dry_run":true}
```

The preview must explicitly describe an addition, not replacement. With
`dry_run:false`, the agent probes the fixed secondary `1.0.0.1` using DoT with
SNI `cloudflare-dns.com`, writes a configuration snapshot, adds the missing
upstream, verifies that every existing upstream remains, and saves the
configuration. It never removes upstreams or changes regional DNS scopes.

Verify the installed agent version and additive preview before applying this
mode: older agents do not understand `mode` and would run the full DNS reset.
Adding resolver redundancy does not by itself prove that a boot-order problem
has been fixed; postboot endpoint resolution must be checked separately.
