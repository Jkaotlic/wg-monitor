package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"regexp"
	"strings"
	"time"
)

type awgmRelayConfig struct {
	BaseURL          string `json:"base_url"`
	APIKey           string `json:"api_key"`
	Login            string `json:"login"`
	Password         string `json:"password"`
	TerminalUser     string `json:"terminal_user"`
	TerminalPassword string `json:"terminal_password"`
	BootstrapScript  string `json:"bootstrap_script"`
	Mode             string `json:"mode"`
}

func runAWGMBootstrapDirect(awgm *AWGMClient, script, terminalUser, terminalPass string) (TerminalRunResult, error) {
	st, err := awgm.TerminalStatus(context.Background())
	if err != nil {
		return TerminalRunResult{}, err
	}
	if st.SessionActive {
		return TerminalRunResult{}, fmt.Errorf("AWG Manager terminal already has an active session; close it in the web UI and retry")
	}
	if !st.Installed {
		if err := awgm.TerminalInstall(context.Background()); err != nil {
			return TerminalRunResult{}, err
		}
	}
	if !st.Running {
		if err := awgm.TerminalStart(context.Background()); err != nil {
			return TerminalRunResult{}, err
		}
	}
	defer func() {
		if err := awgm.TerminalStop(context.Background()); err != nil {
			PrintWarn("AWG Manager terminal stop: " + err.Error())
		}
	}()
	return awgm.RunTerminalScriptWithLogin(context.Background(), script, terminalUser, terminalPass)
}

func runAWGMBootstrapViaVPS(state *State, secrets *SecretStore, ag *AgentState, apiKey, login, pass, terminalUser, terminalPass, script string) (TerminalRunResult, error) {
	if state == nil || strings.TrimSpace(state.Backend.Host) == "" {
		return TerminalRunResult{}, fmt.Errorf("backend SSH host is not configured; install backend first")
	}
	kh, err := NewKnownHosts(defaultCacheDir() + "/known_hosts")
	if err != nil {
		return TerminalRunResult{}, err
	}
	bs, err := connectBackendSSH(state, secrets, kh)
	if err != nil {
		return TerminalRunResult{}, err
	}
	defer bs.Close()

	nick := safeRelayName(ag.Nickname)
	stamp := time.Now().UTC().Format("20060102150405")
	base := "/tmp/wg-monitor-awgm-relay-" + nick + "-" + stamp
	scriptPath := base + ".py"
	configPath := base + ".json"
	cleanup := "rm -f " + shellSingleQuote(scriptPath) + " " + shellSingleQuote(configPath)
	defer func() {
		if _, _, rc, err := bs.Run(cleanup); err != nil || rc != 0 {
			PrintWarn("AWG Manager VPS relay cleanup failed")
		}
	}()

	cfg := awgmRelayConfig{
		BaseURL:          ag.AWGMURL,
		APIKey:           apiKey,
		Login:            login,
		Password:         pass,
		TerminalUser:     terminalUser,
		TerminalPassword: terminalPass,
		BootstrapScript:  script,
		Mode:             "bootstrap",
	}
	cfgBytes, err := json.Marshal(cfg)
	if err != nil {
		return TerminalRunResult{}, err
	}
	if err := bs.UploadStdin(scriptPath, []byte(awgmVPSRelayPython)); err != nil {
		return TerminalRunResult{}, fmt.Errorf("upload awgm relay script: %w", err)
	}
	if err := bs.UploadStdin(configPath, cfgBytes); err != nil {
		return TerminalRunResult{}, fmt.Errorf("upload awgm relay config: %w", err)
	}
	cmd := "chmod 700 " + shellSingleQuote(scriptPath) + " && chmod 600 " + shellSingleQuote(configPath) +
		" && python3 " + shellSingleQuote(scriptPath) + " " + shellSingleQuote(configPath)
	out, stderr, rc, err := bs.Run(cmd)
	if err != nil {
		return TerminalRunResult{Output: out + stderr}, fmt.Errorf("awgm vps relay ssh transport: %w", err)
	}
	if rc != 0 {
		return TerminalRunResult{Output: out + stderr}, fmt.Errorf("awgm vps relay failed rc=%d", rc)
	}
	return TerminalRunResult{Output: out}, nil
}

func fetchAWGMSystemInfoViaVPS(state *State, secrets *SecretStore, ag *AgentState, apiKey, login, pass string) (*AWGMSystemInfo, error) {
	if state == nil || strings.TrimSpace(state.Backend.Host) == "" {
		return nil, fmt.Errorf("backend SSH host is not configured; install backend first")
	}
	kh, err := NewKnownHosts(defaultCacheDir() + "/known_hosts")
	if err != nil {
		return nil, err
	}
	bs, err := connectBackendSSH(state, secrets, kh)
	if err != nil {
		return nil, err
	}
	defer bs.Close()

	nick := safeRelayName(ag.Nickname)
	stamp := time.Now().UTC().Format("20060102150405")
	base := "/tmp/wg-monitor-awgm-info-" + nick + "-" + stamp
	scriptPath := base + ".py"
	configPath := base + ".json"
	cleanup := "rm -f " + shellSingleQuote(scriptPath) + " " + shellSingleQuote(configPath)
	defer func() {
		if _, _, rc, err := bs.Run(cleanup); err != nil || rc != 0 {
			PrintWarn("AWG Manager VPS info cleanup failed")
		}
	}()

	cfg := awgmRelayConfig{
		BaseURL:  ag.AWGMURL,
		APIKey:   apiKey,
		Login:    login,
		Password: pass,
		Mode:     "system_info",
	}
	cfgBytes, err := json.Marshal(cfg)
	if err != nil {
		return nil, err
	}
	if err := bs.UploadStdin(scriptPath, []byte(awgmVPSRelayPython)); err != nil {
		return nil, fmt.Errorf("upload awgm relay script: %w", err)
	}
	if err := bs.UploadStdin(configPath, cfgBytes); err != nil {
		return nil, fmt.Errorf("upload awgm relay config: %w", err)
	}
	cmd := "chmod 700 " + shellSingleQuote(scriptPath) + " && chmod 600 " + shellSingleQuote(configPath) +
		" && python3 " + shellSingleQuote(scriptPath) + " " + shellSingleQuote(configPath)
	out, stderr, rc, err := bs.Run(cmd)
	if err != nil {
		return nil, fmt.Errorf("awgm vps relay ssh transport: %w", err)
	}
	if rc != 0 {
		msg := strings.TrimSpace(stderr)
		if msg == "" {
			msg = strings.TrimSpace(out)
		}
		return nil, fmt.Errorf("awgm vps system info failed rc=%d: %s", rc, msg)
	}
	return parseAWGMSystemInfoRelayOutput(out)
}

const awgmRelayJSONMarker = "__WG_MONITOR_JSON__"

func parseAWGMSystemInfoRelayOutput(out string) (*AWGMSystemInfo, error) {
	idx := strings.LastIndex(out, awgmRelayJSONMarker)
	if idx < 0 {
		return nil, fmt.Errorf("awgm vps relay returned no system info marker")
	}
	raw := strings.TrimSpace(out[idx+len(awgmRelayJSONMarker):])
	if nl := strings.IndexByte(raw, '\n'); nl >= 0 {
		raw = strings.TrimSpace(raw[:nl])
	}
	var env awgmEnvelope[AWGMSystemInfo]
	if err := json.Unmarshal([]byte(raw), &env); err != nil {
		return nil, fmt.Errorf("awgm vps relay system info decode: %w", err)
	}
	if !env.Success {
		return nil, fmt.Errorf("awgm vps relay system info: success=false")
	}
	return &env.Data, nil
}

func shouldRunAWGMBootstrapViaVPS(state *State, rawURL string) bool {
	return state != nil && strings.TrimSpace(state.Backend.Host) != "" && !isLocalAWGMURL(rawURL)
}

func isLocalAWGMURL(rawURL string) bool {
	u, err := url.Parse(normalizeAWGMURL(rawURL))
	if err != nil {
		return false
	}
	host := strings.TrimSpace(u.Hostname())
	if host == "" {
		return false
	}
	lower := strings.ToLower(host)
	if lower == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast()
}

var relayNameRe = regexp.MustCompile(`[^a-zA-Z0-9_-]+`)

func safeRelayName(name string) string {
	name = relayNameRe.ReplaceAllString(strings.TrimSpace(name), "_")
	name = strings.Trim(name, "_-")
	if name == "" {
		return "agent"
	}
	if len(name) > 32 {
		return name[:32]
	}
	return name
}

const awgmVPSRelayPython = `#!/usr/bin/env python3
import base64
import http.cookiejar
import json
import os
import re
import socket
import ssl
import struct
import sys
import time
import urllib.error
import urllib.parse
import urllib.request

MARKER = "__WG_MONITOR_DONE__"
JSON_MARKER = "__WG_MONITOR_JSON__"

class RelayError(Exception):
    pass

def load_config(path):
    with open(path, "r", encoding="utf-8") as f:
        return json.load(f)

def base_url(cfg):
    return cfg["base_url"].rstrip("/")

def auth_header(cfg):
    api_key = (cfg.get("api_key") or "").strip()
    if api_key:
        return "Bearer " + api_key
    login = cfg.get("login") or ""
    password = cfg.get("password") or ""
    if not login and not password:
        return ""
    token = base64.b64encode((login + ":" + password).encode("utf-8")).decode("ascii")
    return "Basic " + token

def opener():
    jar = http.cookiejar.CookieJar()
    ctx = ssl._create_unverified_context()
    return urllib.request.build_opener(urllib.request.HTTPCookieProcessor(jar), urllib.request.HTTPSHandler(context=ctx)), jar

def request(op, cfg, method, api_path, body=None):
    data = None
    headers = {
        "Accept": "application/json",
        "X-Requested-With": "XMLHttpRequest",
    }
    if body is not None:
        data = json.dumps(body).encode("utf-8")
        headers["Content-Type"] = "application/json"
    elif method == "POST":
        data = b""
    ah = auth_header(cfg)
    if ah:
        headers["Authorization"] = ah
    req = urllib.request.Request(base_url(cfg) + api_path, data=data, headers=headers, method=method)
    try:
        with op.open(req, timeout=20) as resp:
            raw = resp.read(1024 * 1024)
    except urllib.error.HTTPError as e:
        raw = e.read(65536)
        raise RelayError("awgm %s %s: HTTP %d: %s" % (method, api_path, e.code, raw.decode("utf-8", "replace")))
    if not raw:
        return {}
    try:
        return json.loads(raw.decode("utf-8"))
    except json.JSONDecodeError as e:
        raise RelayError("awgm %s %s decode: %s: %s" % (method, api_path, e, raw[:200].decode("utf-8", "replace")))

def login_if_needed(op, cfg):
    if (cfg.get("api_key") or "").strip():
        return
    if not ((cfg.get("login") or "") or (cfg.get("password") or "")):
        return
    env = request(op, cfg, "POST", "/api/auth/login", {
        "login": cfg.get("login") or "",
        "password": cfg.get("password") or "",
    })
    if env and env.get("success") is False:
        raise RelayError("awgm login: success=false")

def ensure_terminal(op, cfg):
    env = request(op, cfg, "GET", "/api/terminal/status")
    data = env.get("data") or {}
    if data.get("sessionActive"):
        raise RelayError("AWG Manager terminal already has an active session; close it in the web UI and retry")
    if not data.get("installed"):
        request(op, cfg, "POST", "/api/terminal/install")
    if not data.get("running"):
        request(op, cfg, "POST", "/api/terminal/start")

def ws_target(cfg):
    parsed = urllib.parse.urlparse(base_url(cfg))
    scheme = "wss" if parsed.scheme == "https" else "ws"
    ws_path = (parsed.path.rstrip("/") if parsed.path else "") + "/api/terminal/ws"
    return urllib.parse.urlunparse((scheme, parsed.netloc, ws_path, "", "", ""))

def cookie_header(jar):
    parts = []
    for c in jar:
        if c.name and c.value:
            parts.append("%s=%s" % (c.name, c.value))
    return "; ".join(parts)

def read_until(sock, needle):
    data = b""
    while needle not in data:
        chunk = sock.recv(1)
        if not chunk:
            break
        data += chunk
        if len(data) > 65536:
            break
    return data

def ws_connect(cfg, jar):
    target = urllib.parse.urlparse(ws_target(cfg))
    host = target.hostname
    port = target.port or (443 if target.scheme == "wss" else 80)
    raw = socket.create_connection((host, port), timeout=20)
    if target.scheme == "wss":
        ctx = ssl._create_unverified_context()
        raw = ctx.wrap_socket(raw, server_hostname=host)
    raw.settimeout(20)
    key = base64.b64encode(os.urandom(16)).decode("ascii")
    path_q = target.path or "/"
    if target.query:
        path_q += "?" + target.query
    headers = [
        "GET %s HTTP/1.1" % path_q,
        "Host: %s" % target.netloc,
        "Upgrade: websocket",
        "Connection: Upgrade",
        "Sec-WebSocket-Key: %s" % key,
        "Sec-WebSocket-Version: 13",
        "Origin: %s" % base_url(cfg),
        "X-Requested-With: XMLHttpRequest",
    ]
    ah = auth_header(cfg)
    if ah:
        headers.append("Authorization: " + ah)
    ch = cookie_header(jar)
    if ch:
        headers.append("Cookie: " + ch)
    raw.sendall(("\r\n".join(headers) + "\r\n\r\n").encode("ascii"))
    response = read_until(raw, b"\r\n\r\n")
    first = response.split(b"\r\n", 1)[0].decode("ascii", "replace")
    if " 101 " not in first:
        raise RelayError("awgm terminal ws handshake failed: " + response[:500].decode("utf-8", "replace"))
    raw.settimeout(1.0)
    return raw

def ws_send(sock, opcode, payload):
    if isinstance(payload, str):
        payload = payload.encode("utf-8")
    first = 0x80 | opcode
    length = len(payload)
    if length < 126:
        header = struct.pack("!BB", first, 0x80 | length)
    elif length < 65536:
        header = struct.pack("!BBH", first, 0x80 | 126, length)
    else:
        header = struct.pack("!BBQ", first, 0x80 | 127, length)
    mask = os.urandom(4)
    masked = bytes(b ^ mask[i % 4] for i, b in enumerate(payload))
    sock.sendall(header + mask + masked)

def recv_exact(sock, n):
    chunks = []
    remaining = n
    while remaining:
        chunk = sock.recv(remaining)
        if not chunk:
            raise RelayError("websocket closed")
        chunks.append(chunk)
        remaining -= len(chunk)
    return b"".join(chunks)

def ws_recv(sock):
    while True:
        head = recv_exact(sock, 2)
        b1, b2 = head[0], head[1]
        opcode = b1 & 0x0f
        masked = (b2 & 0x80) != 0
        length = b2 & 0x7f
        if length == 126:
            length = struct.unpack("!H", recv_exact(sock, 2))[0]
        elif length == 127:
            length = struct.unpack("!Q", recv_exact(sock, 8))[0]
        mask = recv_exact(sock, 4) if masked else b""
        payload = recv_exact(sock, length) if length else b""
        if masked:
            payload = bytes(b ^ mask[i % 4] for i, b in enumerate(payload))
        if opcode == 0x8:
            raise RelayError("websocket closed by server")
        if opcode == 0x9:
            ws_send(sock, 0xA, payload)
            continue
        if opcode in (0x1, 0x2):
            text = payload.decode("utf-8", "replace")
            if text and text[0].isdigit():
                text = text[1:]
            return text

def send_input(sock, text):
    ws_send(sock, 0x2, b"0" + text.encode("utf-8"))

def send_resize(sock, cols=120, rows=40):
    ws_send(sock, 0x2, b"1" + json.dumps({"columns": cols, "rows": rows}, separators=(",", ":")).encode("ascii"))

def prompt_action(text):
    lower = text.lower()
    if "password:" in lower:
        return "password"
    if "login:" in lower:
        return "login"
    if re.search(r"(^|\n)[^ \n\r]+@[^\n\r]+:[^\n\r]*[#$] ?", text):
        return "shell"
    if re.search(r"(^|\n)[#$] ?", text):
        return "shell"
    return ""

def login_terminal(sock, cfg):
    out = ""
    sent_user = False
    sent_password = False
    deadline = time.time() + 8
    while time.time() < deadline:
        try:
            text = ws_recv(sock)
        except socket.timeout:
            if not sent_user and not sent_password:
                return out
            continue
        out += text
        action = prompt_action(out)
        if action == "login" and not sent_user:
            user = (cfg.get("terminal_user") or "").strip()
            if not user:
                raise RelayError("awgm terminal asks for login but terminal user is empty")
            send_input(sock, user + "\n")
            sent_user = True
        elif action == "password" and not sent_password:
            password = cfg.get("terminal_password") or ""
            if not password:
                raise RelayError("awgm terminal asks for password but router root password is empty")
            send_input(sock, password + "\n")
            sent_password = True
        elif action == "shell":
            return out
    return out

def run_bootstrap(sock, cfg):
    script = cfg.get("bootstrap_script") or ""
    payload = (
        "cat >/tmp/wg-monitor-bootstrap.sh <<'WG_MONITOR_BOOTSTRAP'\n"
        + script
        + "\nWG_MONITOR_BOOTSTRAP\n"
        + "sh /tmp/wg-monitor-bootstrap.sh\n"
        + "rc=$?\n"
        + "echo " + MARKER + "$rc\n"
    )
    send_input(sock, payload)
    out = ""
    deadline = time.time() + 900
    while time.time() < deadline:
        try:
            text = ws_recv(sock)
        except socket.timeout:
            continue
        out += text
        sys.stdout.write(text)
        sys.stdout.flush()
        pos = out.rfind(MARKER)
        if pos >= 0:
            tail = out[pos + len(MARKER):]
            m = re.search(r"(\d+)", tail)
            if m:
                rc = int(m.group(1))
                if rc == 0:
                    return
                raise RelayError("bootstrap script failed: %d" % rc)
    raise RelayError("bootstrap script timeout waiting for marker")

def main():
    cfg = load_config(sys.argv[1])
    op, jar = opener()
    sock = None
    try:
        login_if_needed(op, cfg)
        if cfg.get("mode") == "system_info":
            env = request(op, cfg, "GET", "/api/system/info")
            print(JSON_MARKER + json.dumps(env, separators=(",", ":")))
            return
        ensure_terminal(op, cfg)
        sock = ws_connect(cfg, jar)
        ws_send(sock, 0x1, '{"AuthToken":""}')
        send_resize(sock)
        login_terminal(sock, cfg)
        run_bootstrap(sock, cfg)
    finally:
        if sock is not None:
            try:
                sock.close()
            except Exception:
                pass
        try:
            request(op, cfg, "POST", "/api/terminal/stop")
        except Exception as e:
            print("WARN terminal stop failed: %s" % e, file=sys.stderr)

if __name__ == "__main__":
    try:
        main()
    except Exception as e:
        print(str(e), file=sys.stderr)
        sys.exit(1)
`
