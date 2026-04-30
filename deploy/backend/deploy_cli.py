#!/usr/bin/env python3
"""Upload wg-monitor-cli binary to VPS Main /usr/local/bin/wg-monitor-cli."""
import os
import sys
from pathlib import Path

import paramiko

VPS_HOST = "103.106.1.253"
TARGET = "/usr/local/bin/wg-monitor-cli"


def main() -> int:
    if len(sys.argv) < 2:
        print("usage: deploy_cli.py <local-binary-path>", file=sys.stderr)
        return 2
    binary = Path(sys.argv[1]).resolve()
    pw = os.environ.get("VPS_MAIN_PASS")
    if not pw:
        print("VPS_MAIN_PASS env var required", file=sys.stderr)
        return 2

    ssh = paramiko.SSHClient()
    ssh.set_missing_host_key_policy(paramiko.AutoAddPolicy())
    ssh.connect(VPS_HOST, port=22, username="root", password=pw,
                timeout=15, allow_agent=False, look_for_keys=False)
    sftp = ssh.open_sftp()
    sftp.put(str(binary), TARGET)
    sftp.chmod(TARGET, 0o755)
    sftp.close()
    print(f"uploaded {binary} -> {TARGET}")
    ssh.close()
    return 0


if __name__ == "__main__":
    sys.exit(main())
