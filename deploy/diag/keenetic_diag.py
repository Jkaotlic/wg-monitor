#!/usr/bin/env python3
"""Read-only diagnostic for Keenetic routing setup vs SO_BINDTODEVICE behavior.

Reads root password from `host_keenetic.md` in the user's auto-memory (the file
that already stores it in plaintext for this user's setup). Never echoes the
password to stdout. All remote commands are read-only — no state on the router
is modified.

Usage: python deploy/diag/keenetic_diag.py [section]
where section is optional and limits output to one tagged group.
"""
import os, re, sys, pathlib
import paramiko

HOST = '192.168.0.1'
PORT = 222
USER = 'root'

MEMORY_FILE = pathlib.Path.home() / '.claude/projects/C--Users-user/memory/host_keenetic.md'


def load_password() -> str:
    txt = MEMORY_FILE.read_text(encoding='utf-8')
    m = re.search(r'pass\s+([A-Za-z0-9!@#$%^&*_+=\-]+)', txt)
    if not m:
        sys.exit(f'no password found in {MEMORY_FILE}')
    return m.group(1)


CMDS = [
    ('uname -a', 'kernel'),
    ('echo "--- ip rule list ---"; ip rule list', 'ip-rule'),
    ('echo "--- ip route show (main) ---"; ip route show', 'route-main'),
    ('echo "--- ip route show table all (head 100) ---"; ip route show table all 2>&1 | head -100', 'route-all'),
    ('echo "--- ip route show table 4096 ---"; ip route show table 4096 2>&1 | head -20', 'route-4096'),
    ('echo "--- ip route show table 16396 ---"; ip route show table 16396 2>&1 | head -20', 'route-16396'),
    ('echo "--- ip -d link show nwg0 ---"; ip -d link show nwg0 2>&1', 'link-nwg0'),
    ('echo "--- ip addr show nwg0 ---"; ip addr show nwg0 2>&1', 'addr-nwg0'),
    ('echo "--- ip route get 1.1.1.1 ---"; ip route get 1.1.1.1 2>&1', 'rget-default'),
    ('echo "--- ip route get 1.1.1.1 oif nwg0 ---"; ip route get 1.1.1.1 oif nwg0 2>&1', 'rget-oif-nwg0'),
    ('echo "--- ip route get 198.51.100.21 ---"; ip route get 198.51.100.21 2>&1', 'rget-amnezia'),
    ('echo "--- ip route get 198.51.100.21 oif nwg0 ---"; ip route get 198.51.100.21 oif nwg0 2>&1', 'rget-amnezia-oif'),
    ('echo "--- ip route get 8.8.8.8 oif nwg0 ---"; ip route get 8.8.8.8 oif nwg0 2>&1', 'rget-google-oif'),
    ('echo "--- ls /opt/share/awg-manager/ ---"; ls /opt/share/awg-manager/ 2>&1 | head -50', 'awgm-share'),
    ('echo "--- ls /opt/etc/awg-manager/ ---"; ls -la /opt/etc/awg-manager/ 2>&1 | head -50', 'awgm-etc'),
    ('echo "--- /opt/etc/init.d/S99awg-manager (head 80) ---"; head -80 /opt/etc/init.d/S99awg-manager 2>&1', 'awgm-init'),
    # Round 2 — actual reproduction of failing checks
    ('echo "--- agent config (no token) ---"; grep -vE "^\\s*token:" /opt/etc/wg-monitor/config.yaml 2>&1', 'agent-cfg'),
    ('echo "--- awg-manager tunnels mapping ---"; curl -s -H "X-Requested-With: XMLHttpRequest" http://127.0.0.1:2222/api/tunnels/all 2>&1 | head -200', 'awgm-tunnels'),
    ('echo "--- which curl + version ---"; which curl; curl --version 2>&1 | head -3', 'curl-ver'),
    ('echo "--- which dig ---"; which dig 2>&1; ls -la /opt/bin/dig /opt/sbin/dig /usr/bin/dig 2>&1', 'which-dig'),
    ('echo "--- curl baseline (no bind) ---"; curl -sS --max-time 8 https://api.ipify.org 2>&1; echo; echo rc=$?', 'curl-base'),
    ('echo "--- curl --interface nwg0 ipify ---"; curl -sS --max-time 8 --interface nwg0 https://api.ipify.org 2>&1; echo; echo rc=$?', 'curl-nwg0'),
    ('echo "--- curl --interface nwg1 ipify ---"; curl -sS --max-time 8 --interface nwg1 https://api.ipify.org 2>&1; echo; echo rc=$?', 'curl-nwg1'),
    ('echo "--- curl -v --interface nwg0 ---"; curl -v --max-time 8 --interface nwg0 https://api.ipify.org 2>&1 | head -30', 'curl-nwg0-v'),
    ('echo "--- dig +tls test cloudflare ---"; (which dig >/dev/null && dig +tls +short +timeout=3 @1.1.1.1 example.com 2>&1; echo rc=$?) || echo "dig not installed"', 'dig-cf'),
    ('echo "--- dig +tls test google ---"; (which dig >/dev/null && dig +tls +short +timeout=3 @8.8.8.8 example.com 2>&1; echo rc=$?) || echo "dig not installed"', 'dig-google'),
    ('echo "--- dig +tls test quad9 ---"; (which dig >/dev/null && dig +tls +short +timeout=3 @9.9.9.9 example.com 2>&1; echo rc=$?) || echo "dig not installed"', 'dig-quad9'),
    ('echo "--- iptables mangle OUTPUT (head 30) ---"; iptables -t mangle -L OUTPUT -n -v 2>&1 | head -30', 'ipt-mangle-out'),
    ('echo "--- iptables nat POSTROUTING (head 30) ---"; iptables -t nat -L POSTROUTING -n -v 2>&1 | head -30', 'ipt-nat-postr'),
    ('echo "--- last 40 lines of agent log via journalctl/syslog ---"; tail -40 /opt/var/log/wg-monitor.log 2>&1 | head -50; echo "---"; ls -la /opt/var/log/ 2>&1 | head -20', 'agent-log'),
    # Round 3 — DNS resolution and ICMP/TCP reachability
    ('echo "--- /etc/resolv.conf ---"; cat /etc/resolv.conf 2>&1', 'resolvconf'),
    ('echo "--- nslookup api.ipify.org ---"; nslookup api.ipify.org 2>&1 | head -20; echo rc=$?', 'ns-ipify'),
    ('echo "--- nslookup wgmonitor.example.com ---"; nslookup wgmonitor.example.com 2>&1 | head -10; echo rc=$?', 'ns-backend'),
    ('echo "--- nslookup www.youtube.com ---"; nslookup www.youtube.com 2>&1 | head -10; echo rc=$?', 'ns-youtube'),
    ('echo "--- ping 198.51.100.21 (no -I) 3 pkts ---"; ping -c 3 -W 2 198.51.100.21 2>&1', 'ping-amnezia'),
    ('echo "--- ping -I nwg0 198.51.100.21 3 pkts ---"; ping -c 3 -W 2 -I nwg0 198.51.100.21 2>&1', 'ping-amnezia-nwg0'),
    ('echo "--- ping -I nwg0 1.1.1.1 3 pkts ---"; ping -c 3 -W 2 -I nwg0 1.1.1.1 2>&1', 'ping-cf-nwg0'),
    ('echo "--- ping -I nwg0 8.8.8.8 3 pkts ---"; ping -c 3 -W 2 -I nwg0 8.8.8.8 2>&1', 'ping-google-nwg0'),
    ('echo "--- curl by IP --resolve nwg0 ---"; curl -sS --max-time 8 --interface nwg0 --resolve api.ipify.org:443:104.26.12.205 https://api.ipify.org 2>&1; echo rc=$?', 'curl-resolve-nwg0'),
    ('echo "--- curl baseline by IP (no DNS) ---"; curl -sS --max-time 8 --resolve api.ipify.org:443:104.26.12.205 https://api.ipify.org 2>&1; echo rc=$?', 'curl-resolve-base'),
    ('echo "--- curl direct to 1.1.1.1 (https) ---"; curl -sS --max-time 8 --interface nwg0 -k https://1.1.1.1/ 2>&1 | head -5; echo rc=$?', 'curl-cf-nwg0'),
    ('echo "--- curl direct to 198.51.100.21:8443 (Amnezia AGH if up) ---"; curl -sS --max-time 5 --interface nwg0 -k https://198.51.100.21/ 2>&1 | head -5; echo rc=$?', 'curl-amnezia-https'),
    ('echo "--- nc -vz 198.51.100.21 443 (no bind) ---"; nc -vz -w 5 198.51.100.21 443 2>&1; echo rc=$?', 'nc-amnezia'),
    ('echo "--- agent process + recent logs ---"; ps | grep wg-monitor | grep -v grep; echo "---"; logread 2>&1 | tail -20', 'agent-state'),
    ('echo "--- opkg list-installed | grep -iE \\"dig|bind\\" ---"; opkg list-installed 2>&1 | grep -iE "dig|bind|knot" | head -20', 'pkg-dig'),
    # Round 4 — verify proposed replacement URLs actually work via nwg0
    ('echo "--- cf trace via nwg0 ---"; curl -sS --max-time 8 --interface nwg0 https://1.1.1.1/cdn-cgi/trace 2>&1; echo rc=$?', 'cf-trace-nwg0'),
    ('echo "--- gstatic generate_204 via nwg0 (-w status) ---"; curl -sS -o /dev/null -w "code=%{http_code}\\n" --max-time 8 --interface nwg0 http://www.gstatic.com/generate_204 2>&1; echo rc=$?', 'gstatic-nwg0'),
    ('echo "--- detectportal firefox via nwg0 ---"; curl -sS --max-time 8 --interface nwg0 http://detectportal.firefox.com/success.txt 2>&1; echo rc=$?', 'firefox-nwg0'),
    ('echo "--- youtube manifest via nwg0 (-w status) ---"; curl -sS -o /dev/null -w "code=%{http_code}\\n" --max-time 8 --interface nwg0 https://www.youtube.com/-/manifest 2>&1; echo rc=$?', 'yt-manif-nwg0'),
    ('echo "--- DoH cloudflare JSON via main ---"; curl -sS --max-time 5 -H "accept: application/dns-json" "https://1.1.1.1/dns-query?name=example.com&type=A" 2>&1 | head -3; echo rc=$?', 'doh-cf-main'),
    ('echo "--- DoH cloudflare JSON via nwg0 ---"; curl -sS --max-time 5 --interface nwg0 -H "accept: application/dns-json" "https://1.1.1.1/dns-query?name=example.com&type=A" 2>&1 | head -3; echo rc=$?', 'doh-cf-nwg0'),
    ('echo "--- DoH google JSON via main ---"; curl -sS --max-time 5 "https://8.8.8.8/resolve?name=example.com&type=A" 2>&1 | head -3; echo rc=$?', 'doh-google-main'),
    # Round 5 — auto-discovery feasibility for DNS config
    ('echo "--- ndmc binary ---"; ls -la /bin/ndmc 2>&1', 'ndmc-bin'),
    ('echo "--- ndmc show running-config | grep -iE name-server|domain ---"; /bin/ndmc -c "show running-config" </dev/null 2>&1 | grep -iE "name-server|domain|dns-proxy|name domain" | head -40', 'ndmc-running-dns'),
    ('echo "--- ndmc show ip name-server ---"; /bin/ndmc -c "show ip name-server" </dev/null 2>&1 | head -40', 'ndmc-show-ip-ns'),
    ('echo "--- ndmc show ip dns ---"; /bin/ndmc -c "show ip dns" </dev/null 2>&1 | head -40', 'ndmc-show-ip-dns'),
    ('echo "--- ndmc show name-server ---"; /bin/ndmc -c "show name-server" </dev/null 2>&1 | head -40', 'ndmc-show-ns'),
    ('echo "--- ndmc show running-config | head 200 ---"; /bin/ndmc -c "show running-config" </dev/null 2>&1 | head -200', 'ndmc-running-head'),
    ('echo "--- ndmc show interface (head 60) ---"; /bin/ndmc -c "show interface" </dev/null 2>&1 | head -60', 'ndmc-show-iface'),
    ('echo "--- ndmc running-config | grep -iE doh|dot|tls|https-dns|service.*dns ---"; /bin/ndmc -c "show running-config" </dev/null 2>&1 | grep -iE "doh|dot |tls|https-dns|service +.*dns|dns-proxy" | head -40', 'ndmc-doh'),
    ('echo "--- running-config grep -A10 service ip-name-server-doh|tls ---"; /bin/ndmc -c "show running-config" </dev/null 2>&1 | grep -A4 -iE "name-server +https|name-server +tls|ip name-server.*[a-z]" | head -30', 'ndmc-name-server-doh'),
    ('echo "--- running-config raw 200-400 ---"; /bin/ndmc -c "show running-config" </dev/null 2>&1 | sed -n "200,400p"', 'ndmc-running-200-400'),
    ('echo "--- rc total lines + grep https/8443/dns-query ---"; cfg=$(/bin/ndmc -c "show running-config" </dev/null 2>&1); echo "$cfg" | wc -l; echo "$cfg" | grep -iE "https://|8443|dns-query|jkaotlic|name-server.+host" | head -20', 'ndmc-rc-https'),
    ('echo "--- ndmc show dns-proxy server ---"; /bin/ndmc -c "show dns-proxy server" </dev/null 2>&1 | head -40', 'ndmc-dns-proxy-server'),
    ('echo "--- ndmc show ip ---"; /bin/ndmc -c "show ip" </dev/null 2>&1 | head -40', 'ndmc-show-ip'),
    ('echo "--- dump full running-config to /tmp/keen-rc.txt then grep ---"; /bin/ndmc -c "show running-config" </dev/null 2>&1 > /tmp/keen-rc.txt; wc -l /tmp/keen-rc.txt; grep -nE "name-server|dns-proxy|https-dns|http-dns|tls-dns|secure-dns|dns-server" /tmp/keen-rc.txt | head -40', 'ndmc-rc-dump'),
    ('echo "--- context around https upstream (lines before+after) ---"; grep -n "https upstream" /tmp/keen-rc.txt; n=$(grep -n "https upstream" /tmp/keen-rc.txt | head -1 | cut -d: -f1); start=$((n-15)); end=$((n+5)); sed -n "${start},${end}p" /tmp/keen-rc.txt', 'ndmc-rc-doh-ctx'),
    ('echo "--- full dns-proxy section ---"; sed -n "880,914p" /tmp/keen-rc.txt', 'ndmc-rc-dnsproxy-end'),
    ('echo "--- listening ports ---"; netstat -tlpn 2>&1 | head -40', 'netstat'),
    ('echo "--- RCI port 79 ---"; curl -sS --max-time 3 http://127.0.0.1:79/rci/show/dns/server 2>&1 | head -10; echo "rc=$?"', 'rci-79'),
    ('echo "--- RCI port 80 ---"; curl -sS --max-time 3 -i http://127.0.0.1:80/rci/show/dns/server 2>&1 | head -15; echo "rc=$?"', 'rci-80'),
    ('echo "--- /tmp/system-configuration ---"; ls -la /tmp/system-configuration.txt /tmp/running-config /tmp/.ndmcfg 2>&1; head -10 /tmp/system-configuration.txt 2>&1', 'sys-config'),
    ('echo "--- ndmc show ipv6 ---"; timeout 5 /bin/ndmc -c "show ipv6 dns" 2>&1 | head -20', 'ndmc-show-ipv6-dns'),
]


def main():
    only = sys.argv[1] if len(sys.argv) > 1 else None
    pw = load_password()
    c = paramiko.SSHClient()
    c.set_missing_host_key_policy(paramiko.AutoAddPolicy())
    c.connect(HOST, port=PORT, username=USER, password=pw, timeout=10,
              allow_agent=False, look_for_keys=False)
    try:
        for cmd, tag in CMDS:
            if only and only != tag:
                continue
            print(f'\n========== [{tag}] ==========')
            _, out, err = c.exec_command(cmd, timeout=20)
            rc = out.channel.recv_exit_status()
            o = out.read().decode(errors='replace')
            e = err.read().decode(errors='replace')
            print(o.rstrip())
            if e.strip():
                print(f'[stderr] {e.rstrip()}')
            if rc != 0:
                print(f'[rc={rc}]')
    finally:
        c.close()


if __name__ == '__main__':
    main()
