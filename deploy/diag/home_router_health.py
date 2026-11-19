#!/usr/bin/env python3
"""Read awg-manager health from a known Linux host on the router's LAN over VPN.

Requires a previously trusted SSH host key and the LAN MAC of the intended router.
Use --help for arguments. No local HTTP fallback when the VPN is unavailable.
"""
import argparse
import ipaddress
import re
import subprocess


# Executed on the trusted LAN host. Connect first to populate its neighbour
# table; verify the router before making any API request over that connection.
REMOTE = """
import collections, http.client, json, sys

address, port, expected = sys.argv[1:]
def read(path):
    conn = http.client.HTTPConnection(address, int(port), timeout=8)
    try:
        conn.connect()
        with open('/proc/net/arp') as table:
            rows = [line.split() for line in table if line.split()[0] == address]
        if len(rows) != 1 or not int(rows[0][2], 16) & 2 or rows[0][3].lower() != expected:
            sys.exit('Router identity mismatch or no verified LAN neighbour')
        conn.request('GET', '/api/' + path)
        resp = conn.getresponse()
        if resp.status != 200:
            sys.exit(f'Router returned HTTP {resp.status}')
        packet = json.load(resp)
        if not packet.get('success'):
            sys.exit('Router did not return a successful result')
        return packet['data']
    finally:
        conn.close()

logs = read('logs?bucket=app&limit=500')
matrix = read('monitoring/matrix')
print(json.dumps({
    'log_enabled': logs['enabled'], 'oldest_log': logs.get('oldestTimestamp'),
    'total_logs': logs['total'], 'levels_in_sample': dict(collections.Counter(x['level'] for x in logs['logs'])),
    'checks': [{k: cell.get(k) for k in ('tunnelId','targetId','ok','ts','latencyMs')} for cell in matrix['cells']]
}, ensure_ascii=False, indent=2))
"""


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--via', required=True, help='SSH alias of the trusted LAN host')
    parser.add_argument('--interface', required=True, help='VPN interface on this Mac (utunN)')
    parser.add_argument('--router', type=ipaddress.IPv4Address, required=True)
    parser.add_argument('--port', type=int, default=2222)
    parser.add_argument('--expected-mac', required=True, help='previously verified LAN MAC')
    args = parser.parse_args()
    if not re.fullmatch(r'utun[0-9]+', args.interface):
        parser.error('expected a VPN interface (utunN)')
    mac = args.expected_mac.lower()
    if not re.fullmatch(r'(?:[0-9a-f]{2}:){5}[0-9a-f]{2}', mac) or not 1 <= args.port <= 65535:
        parser.error('invalid MAC or port')
    cmd = ['ssh', '-B', args.interface, '-o', 'StrictHostKeyChecking=yes', '-o', 'BatchMode=yes',
           '-o', 'ConnectTimeout=8', '--', args.via, 'python3', '-', str(args.router), str(args.port), mac]
    return subprocess.run(cmd, input=REMOTE, text=True).returncode


if __name__ == '__main__':
    raise SystemExit(main())
