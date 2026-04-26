#!/usr/bin/env python3
"""Deploy wg-monitor agent to a Keenetic router via Paramiko (dropbear has no SFTP).

Binary upload uses raw stdin pipe (`cat > file`) since dropbear's busybox argv
is too small for chunked base64 + heredoc would also choke; raw binary over
stdin via paramiko channel works fine.
"""
import argparse, hashlib, sys, time
import paramiko


def run(client, cmd, check=False):
    _, out, err = client.exec_command(cmd)
    rc = out.channel.recv_exit_status()
    o = out.read().decode(errors="replace")
    e = err.read().decode(errors="replace")
    if check and rc != 0:
        sys.exit(f"FAIL ({rc}): {cmd}\nstdout: {o}\nstderr: {e}")
    return rc, o, e


def upload_via_stdin(transport, remote_path, data):
    """Stream raw bytes to a remote file using `cat > path` over the channel's stdin."""
    chan = transport.open_session()
    chan.exec_command(f"cat > {remote_path}")
    # paramiko send is non-blocking; loop until all bytes are written
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
    ap.add_argument("--host", default="192.168.31.1")
    ap.add_argument("--port", type=int, default=222)
    ap.add_argument("--user", default="root")
    ap.add_argument("--password", required=True)
    ap.add_argument("--bin", default="bin/linux-arm64/wg-monitor")
    ap.add_argument("--init-d", default="deploy/agent/S99wg-monitor")
    ap.add_argument("--config", required=True, help="local config.yaml path")
    args = ap.parse_args()

    with open(args.bin, "rb") as f:
        data = f.read()
    local_sha = hashlib.sha256(data).hexdigest()
    print(f"local: {args.bin} {len(data)} bytes sha256={local_sha}")

    with open(args.init_d) as f:
        init_text = f.read()
    with open(args.config) as f:
        config_text = f.read()

    c = paramiko.SSHClient()
    c.set_missing_host_key_policy(paramiko.AutoAddPolicy())
    c.connect(args.host, port=args.port, username=args.user, password=args.password,
              look_for_keys=False, allow_agent=False, timeout=10)
    print(f"connected to {args.user}@{args.host}:{args.port}")

    print("--- stop any running agent ---")
    run(c, "/opt/etc/init.d/S99wg-monitor stop 2>/dev/null; killall -9 wg-monitor 2>/dev/null; true")

    print("--- mkdir target dirs ---")
    run(c, "mkdir -p /opt/bin /opt/etc/wg-monitor /opt/var/wg-monitor /opt/etc/init.d", check=True)

    print(f"--- upload binary (raw bytes via stdin pipe) ---")
    sent = upload_via_stdin(c.get_transport(), "/opt/bin/wg-monitor", data)
    print(f"  sent {sent} bytes")
    run(c, "chmod 755 /opt/bin/wg-monitor", check=True)

    print("--- sha256-verify on remote ---")
    rc, o, _ = run(c, "sha256sum /opt/bin/wg-monitor | awk '{print $1}'", check=True)
    remote_sha = o.strip().split()[0] if o else ""
    print(f"  remote sha256={remote_sha}")
    if remote_sha != local_sha:
        sys.exit(f"sha256 MISMATCH local={local_sha} remote={remote_sha}")
    print("  sha256 OK")

    print("--- smoke binary --help ---")
    _, o, e = run(c, "/opt/bin/wg-monitor --help 2>&1 | head -10")
    print(f"  {o or e}")

    print("--- write config ---")
    payload = config_text.replace("'", "'\\''")
    run(c, f"cat > /opt/etc/wg-monitor/config.yaml <<'WGMONEOF'\n{config_text}\nWGMONEOF\nchmod 600 /opt/etc/wg-monitor/config.yaml", check=True)

    print("--- write init.d script ---")
    run(c, f"cat > /opt/etc/init.d/S99wg-monitor <<'WGMONEOF'\n{init_text}\nWGMONEOF\nchmod +x /opt/etc/init.d/S99wg-monitor", check=True)

    print("--- start agent ---")
    rc, o, e = run(c, "/opt/etc/init.d/S99wg-monitor start")
    print(f"  rc={rc} stdout={o!r} stderr={e!r}")

    time.sleep(2)
    print("--- verify pidof + ps ---")
    rc, o, e = run(c, "pidof wg-monitor; ps w 2>/dev/null | grep -v grep | grep wg-monitor || ps | grep -v grep | grep wg-monitor")
    print(f"  {o}{e}")
    if not o.strip():
        sys.exit("agent process not found after start")

    print("\nDEPLOY OK")
    c.close()


if __name__ == "__main__":
    main()
