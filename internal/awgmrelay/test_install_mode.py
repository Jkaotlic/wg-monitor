import contextlib, importlib.util, io, os, socket, sys, types, unittest
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
            "nickname": "bronya",
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
        stdout_buf = io.StringIO()
        with contextlib.redirect_stdout(stdout_buf):
            relay.run_install_bootstrap(cfg)

        s = captured["script"]
        self.assertIn("NICKNAME='bronya'", s)
        self.assertIn("v0.13.8/wg-monitor-agent-linux-arm64", s)
        self.assertIn("EXPECTED_SHA='aa11bb22'", s)
        self.assertIn('token: "tok-deadbeef"', s)
        self.assertIn('url: "https://wgmon.example"', s)

        # Cleanup assertions: socket must be closed and terminal must be stopped.
        fake_sock.close.assert_called()
        self.assertIn("/api/terminal/stop", requested_paths,
                      "expected /api/terminal/stop to be called in finally-cleanup")

        # --- __WG_STEP__ progress markers (Task 7 / B7) --------------------
        #
        # Router-side markers: the rendered bootstrap script must "echo" each
        # marker (never emit a bare "__WG_STEP__..." as the first token of a
        # command -- the Go parser strict-prefix-matches trimmed stdout
        # lines, and an echoed COMMAND line must not itself parse as a
        # marker), in checklist order.
        marker_lines = [
            "echo __WG_STEP__ downloading",
            "echo __WG_STEP__ checksum_ok",
            "echo __WG_STEP__ config_written",
            "echo __WG_STEP__ init_installed",
            "echo __WG_STEP__ service_started",
        ]
        for marker_line in marker_lines:
            self.assertIn(marker_line, s)
        positions = [s.index(line) for line in marker_lines]
        self.assertEqual(positions, sorted(positions),
                          "step markers must appear in checklist order in the rendered script")

        # Token safety: the raw token must appear ONLY inside the quoted
        # config heredoc, never on a __WG_STEP__ line -- the Go side stores a
        # marker's detail argument UNREDACTED in the visible job step.
        token = cfg["raw_token"]
        self.assertIn(token, s, "sanity: token should still be present in the config heredoc")
        for line in s.splitlines():
            if "__WG_STEP__" in line:
                self.assertNotIn(token, line,
                                  "raw token must never appear on a __WG_STEP__ line: %r" % line)

        # Python-side markers: terminal_connected/arch_detected are printed
        # directly by the relay process itself (not part of the router-side
        # script). login_terminal is stubbed to a no-op above, so only
        # arch_detected (printed right after normalize_arch in
        # run_install_bootstrap) is observable in this test.
        printed = stdout_buf.getvalue()
        self.assertIn("__WG_STEP__ arch_detected arm64", printed)
        self.assertNotIn(cfg["raw_token"], printed,
                          "raw token must never appear in relay stdout prints")


class LoginTerminalStepMarkerTest(unittest.TestCase):
    """login_terminal must print terminal_connected on BOTH success paths:
    the explicit shell-prompt match, and the "already at a shell, first read
    just timed out before any login/password exchange began" fast path."""

    def test_prints_terminal_connected_on_shell_prompt_match(self):
        relay = load_relay()
        relay.ws_recv = lambda sock: "root@Keenetic:~# "
        buf = io.StringIO()
        with contextlib.redirect_stdout(buf):
            out = relay.login_terminal(object(), {})
        self.assertIn("__WG_STEP__ terminal_connected", buf.getvalue())
        self.assertIn("root@Keenetic:~# ", out)

    def test_prints_terminal_connected_on_immediate_timeout_fast_path(self):
        relay = load_relay()

        def raise_timeout(sock):
            raise socket.timeout()
        relay.ws_recv = raise_timeout

        buf = io.StringIO()
        with contextlib.redirect_stdout(buf):
            relay.login_terminal(object(), {})
        self.assertIn("__WG_STEP__ terminal_connected", buf.getvalue())


if __name__ == "__main__":
    unittest.main()
