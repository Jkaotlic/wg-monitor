#!/usr/bin/env python3
"""Deploy wg-monitor-backend to VPS Main via Paramiko + SFTP.

Usage:
    python deploy_vps_main.py --binary ../../dist/wg-monitor-backend [--password <pass>]

Idempotent: stop service, atomic swap binary, start, verify journal for errors.
"""
import argparse
import hashlib
import os
import sys
import time
from pathlib import Path

import paramiko

VPS_HOST = "103.106.1.253"
VPS_PORT = 22
VPS_USER = "root"
TARGET = "/usr/local/bin/wg-monitor-backend"
SERVICE = "wg-monitor-backend"


def sha256(path: Path) -> str:
    h = hashlib.sha256()
    with path.open("rb") as f:
        for chunk in iter(lambda: f.read(65536), b""):
            h.update(chunk)
    return h.hexdigest()


def run(ssh: paramiko.SSHClient, cmd: str, *, check: bool = True) -> tuple[int, str, str]:
    stdin, stdout, stderr = ssh.exec_command(cmd, timeout=30)
    out = stdout.read().decode("utf-8", errors="replace")
    err = stderr.read().decode("utf-8", errors="replace")
    rc = stdout.channel.recv_exit_status()
    if check and rc != 0:
        raise RuntimeError(f"cmd failed (rc={rc}): {cmd}\nSTDOUT:{out}\nSTDERR:{err}")
    return rc, out, err


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--binary", required=True, type=Path)
    ap.add_argument("--password", default=os.environ.get("VPS_MAIN_PASS"))
    ap.add_argument("--skip-restart", action="store_true")
    args = ap.parse_args()

    binary = args.binary.resolve()
    if not binary.is_file():
        print(f"ERROR: {binary} not found", file=sys.stderr)
        return 1

    local_hash = sha256(binary)
    print(f"Local: {binary} ({binary.stat().st_size} bytes) sha256={local_hash[:16]}...")

    if not args.password:
        print("ERROR: --password or VPS_MAIN_PASS env required", file=sys.stderr)
        return 2

    ssh = paramiko.SSHClient()
    ssh.set_missing_host_key_policy(paramiko.AutoAddPolicy())
    print(f"Connecting to {VPS_USER}@{VPS_HOST}:{VPS_PORT}...")
    ssh.connect(VPS_HOST, port=VPS_PORT, username=VPS_USER, password=args.password,
                timeout=15, allow_agent=False, look_for_keys=False)

    sftp = ssh.open_sftp()
    tmp_path = "/tmp/wg-monitor-backend.new"
    print(f"SFTP upload -> {tmp_path}")
    sftp.put(str(binary), tmp_path)
    sftp.chmod(tmp_path, 0o755)
    sftp.close()

    rc, out, _ = run(ssh, f"sha256sum {tmp_path} | awk '{{print $1}}'")
    remote_hash = out.strip()
    if remote_hash != local_hash:
        run(ssh, f"rm -f {tmp_path}", check=False)
        raise RuntimeError(f"sha256 mismatch! local={local_hash} remote={remote_hash}")
    print(f"Remote sha256 OK: {remote_hash[:16]}...")

    if args.skip_restart:
        print(f"Uploaded to {tmp_path}, --skip-restart set, exiting")
        return 0

    print(f"systemctl stop {SERVICE}")
    run(ssh, f"systemctl stop {SERVICE}")

    print(f"Atomic swap: mv {tmp_path} {TARGET}")
    run(ssh, f"mv {tmp_path} {TARGET} && chown root:root {TARGET} && chmod 755 {TARGET}")

    print(f"systemctl start {SERVICE}")
    run(ssh, f"systemctl start {SERVICE}")

    time.sleep(3)

    rc, out, _ = run(ssh, f"systemctl is-active {SERVICE}", check=False)
    state = out.strip()
    print(f"Service state: {state}")
    if state != "active":
        rc, out, _ = run(ssh, f"journalctl -u {SERVICE} -n 50 --no-pager", check=False)
        print("=== Last 50 journal lines ===")
        print(out)
        return 3

    rc, out, _ = run(ssh, f"journalctl -u {SERVICE} -n 20 --no-pager")
    print("=== Last 20 journal lines ===")
    print(out)

    rc, out, _ = run(ssh, f"{TARGET} --version 2>&1 || true", check=False)
    print(f"--version: {out.strip()}")

    rc, out, _ = run(ssh, "curl -sS -o /dev/null -w '%{http_code}' https://wgmonitor.jkaotlic.duckdns.org/health", check=False)
    print(f"https://wgmonitor.jkaotlic.duckdns.org/health -> HTTP {out.strip()}")

    ssh.close()
    print("DEPLOY OK")
    return 0


if __name__ == "__main__":
    sys.exit(main())
