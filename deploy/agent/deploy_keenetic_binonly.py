#!/usr/bin/env python3
"""Replace wg-monitor agent binary on a Keenetic router (no config touch).

Use when config is already provisioned and you just want to update the binary
in-place. Stops agent, swaps /opt/bin/wg-monitor, sha256-verifies, restarts,
verifies process running.
"""
import argparse
import hashlib
import pathlib
import re
import sys
import time

import paramiko

MEMORY_FILE = pathlib.Path.home() / '.claude/projects/C--Users-user/memory/host_keenetic.md'


def password_from_memory() -> str | None:
    if not MEMORY_FILE.exists():
        return None
    txt = MEMORY_FILE.read_text(encoding='utf-8')
    m = re.search(r'pass\s+([A-Za-z0-9!@#$%^&*_+=\-]+)', txt)
    return m.group(1) if m else None


def run(client, cmd, check=False):
    _, out, err = client.exec_command(cmd)
    rc = out.channel.recv_exit_status()
    o = out.read().decode(errors="replace")
    e = err.read().decode(errors="replace")
    if check and rc != 0:
        sys.exit(f"FAIL ({rc}): {cmd}\nstdout: {o}\nstderr: {e}")
    return rc, o, e


def upload_via_stdin(transport, remote_path, data):
    chan = transport.open_session()
    chan.exec_command(f"cat > {remote_path}")
    view = memoryview(data)
    sent = 0
    while sent < len(view):
        n = chan.send(view[sent:sent + 32768])
        if n <= 0:
            chan.close()
            raise IOError(f"send returned {n} after {sent}/{len(view)} bytes")
        sent += n
    chan.shutdown_write()
    rc = chan.recv_exit_status()
    chan.close()
    if rc != 0:
        raise IOError(f"`cat > {remote_path}` exited {rc}")
    return sent


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--host", default="192.168.0.1")
    ap.add_argument("--port", type=int, default=222)
    ap.add_argument("--user", default="root")
    ap.add_argument("--password", default=None)
    ap.add_argument("--bin", required=True)
    args = ap.parse_args()

    pw = args.password or password_from_memory()
    if not pw:
        sys.exit("password not provided and not found in host_keenetic.md")

    bin_path = pathlib.Path(args.bin)
    data = bin_path.read_bytes()
    local_sha = hashlib.sha256(data).hexdigest()
    print(f"local: {bin_path} {len(data)} bytes sha256={local_sha[:16]}...")

    c = paramiko.SSHClient()
    c.set_missing_host_key_policy(paramiko.AutoAddPolicy())
    c.connect(args.host, port=args.port, username=args.user, password=pw,
              look_for_keys=False, allow_agent=False, timeout=10)
    print(f"connected to {args.user}@{args.host}:{args.port}")

    print("--- ensure /opt/var/wg-monitor exists (for opkg.lock) ---")
    run(c, "mkdir -p /opt/var/wg-monitor", check=True)

    print("--- stop running agent ---")
    run(c, "/opt/etc/init.d/S99wg-monitor stop 2>/dev/null; killall -9 wg-monitor 2>/dev/null; sleep 1; true")

    print("--- upload new binary -> /opt/bin/wg-monitor.new ---")
    sent = upload_via_stdin(c.get_transport(), "/opt/bin/wg-monitor.new", data)
    print(f"  sent {sent} bytes")
    run(c, "chmod 755 /opt/bin/wg-monitor.new", check=True)

    print("--- sha256-verify on remote ---")
    rc, o, _ = run(c, "sha256sum /opt/bin/wg-monitor.new | awk '{print $1}'", check=True)
    remote_sha = o.strip().split()[0] if o else ""
    if remote_sha != local_sha:
        sys.exit(f"sha256 MISMATCH local={local_sha} remote={remote_sha}")
    print(f"  remote sha256 OK: {remote_sha[:16]}...")

    print("--- atomic swap ---")
    run(c, "mv /opt/bin/wg-monitor.new /opt/bin/wg-monitor", check=True)

    print("--- start agent ---")
    rc, o, e = run(c, "/opt/etc/init.d/S99wg-monitor start")
    print(f"  rc={rc} stdout={o!r} stderr={e!r}")

    time.sleep(3)
    print("--- verify process ---")
    rc, o, e = run(c, "pidof wg-monitor; ps w 2>/dev/null | grep -v grep | grep wg-monitor || ps | grep -v grep | grep wg-monitor")
    print(f"  {o}{e}")
    if not o.strip():
        sys.exit("agent process not found after start")

    print("--- agent --version ---")
    rc, o, e = run(c, "/opt/bin/wg-monitor --version 2>&1 | head -3")
    print(f"  {o or e}")

    print("\nDEPLOY OK")
    c.close()


if __name__ == "__main__":
    main()
