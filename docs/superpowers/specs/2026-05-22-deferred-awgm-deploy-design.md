# Deferred AWG Manager Deploy Design

## Goal

Allow the deploy wizard to onboard a router that is currently offline by placing a persistent job on the VPS. The VPS checks the public AWG Manager URL and installs the agent when the router wakes.

## Design

The deploy wizard keeps the normal live AWG Manager path first. If the public AWG Manager URL fails with a transient network error from the VPS relay, the wizard offers to queue a deferred deploy.

The queued job is a root-only JSON file in `/var/lib/wg-monitor/deferred-awgm`. A systemd timer runs `/usr/local/bin/wg-monitor-deferred-awgm-runner` every two minutes. The runner executes `/usr/local/lib/wg-monitor/awgm-relay.py` for each queued job.

The job does not create the agent enrollment at schedule time. When AWG Manager becomes reachable, the relay reads `/api/system/info`, creates the enrollment through the local backend at `127.0.0.1:8080`, builds the bootstrap script with the detected architecture, and runs it through the AWG Manager terminal. After success it updates wizard deploy metadata, stores the raw token in a root-only `.token` file, writes a `.done` marker, and removes the queued JSON.

## Error Handling

Transient AWG Manager failures leave the job queued for the next timer tick. Expired jobs are removed after the configured TTL. Terminal conflicts or bootstrap failures are logged by systemd and retried on the next tick unless the job completes.

## Security

AWG Manager credentials and the wizard token are stored only in the root-owned job JSON on the VPS. The agent raw token is not generated or stored until the router is reachable and the install actually starts.
