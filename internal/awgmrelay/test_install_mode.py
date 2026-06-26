import importlib.util, os, sys, types, unittest
from unittest.mock import MagicMock

HERE = os.path.dirname(os.path.abspath(__file__))

def load_relay():
    spec = importlib.util.spec_from_file_location("awgm_relay", os.path.join(HERE, "awgm-relay.py"))
    mod = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(mod)
    return mod

class InstallModeTest(unittest.TestCase):
    def test_run_install_bootstrap_builds_full_install_script(self):
        relay = load_relay()
        captured = {}

        # Stub network/terminal so only orchestration + script-build runs.
        relay.opener = lambda: (object(), object())
        relay.login_if_needed = lambda op, cfg: None

        # Track which api_paths were requested (for cleanup assertions).
        requested_paths = []

        def fake_request(op, cfg, method, api_path, body=None):
            requested_paths.append(api_path)
            if api_path == "/api/system/info":
                return {"data": {"goArch": "arm64"}}
            return {"data": {}}
        relay.request = fake_request

        relay.ensure_terminal = lambda op, cfg: None

        # Use a MagicMock so ws_connect returns a socket whose .close() is trackable.
        fake_sock = MagicMock()
        relay.ws_connect = lambda cfg, jar: fake_sock

        relay.ws_send = lambda sock, opcode, payload: None
        relay.send_resize = lambda sock, cols=120, rows=40: None
        relay.login_terminal = lambda sock, cfg: None

        def fake_run_bootstrap(sock, cfg):
            captured["script"] = cfg.get("bootstrap_script") or ""
        relay.run_bootstrap = fake_run_bootstrap

        cfg = {
            "mode": "bootstrap_install",
            "base_url": "https://awg.example",
            "nickname": "client-g",
            "target_version": "v0.13.8",
            "backend_url": "https://wgmon.example",
            "raw_token": "tok-deadbeef",
            "release_base": "https://wgmon.example/v1/releases/download",
            "init_script": "#!/bin/sh\necho init",
            "terminal_user": "root",
            "terminal_password": "rootpw",
            "checksums": {
                "wg-monitor-agent-linux-arm64": "aa11bb22",
                "wg-monitor-agent-linux-mipsle": "cc33dd44",
            },
        }
        relay.run_install_bootstrap(cfg)

        s = captured["script"]
        self.assertIn("NICKNAME='client-g'", s)
        self.assertIn("v0.13.8/wg-monitor-agent-linux-arm64", s)
        self.assertIn("EXPECTED_SHA='aa11bb22'", s)
        self.assertIn('token: "tok-deadbeef"', s)
        self.assertIn('url: "https://wgmon.example"', s)

        # Cleanup assertions: socket must be closed and terminal must be stopped.
        fake_sock.close.assert_called()
        self.assertIn("/api/terminal/stop", requested_paths,
                      "expected /api/terminal/stop to be called in finally-cleanup")

if __name__ == "__main__":
    unittest.main()
