import http.server, importlib.util, io, json, os, socket, subprocess, sys, tempfile, threading, unittest

HERE = os.path.dirname(os.path.abspath(__file__))
RELAY = os.path.join(HERE, "awgm-relay.py")


def load_relay():
    spec = importlib.util.spec_from_file_location("awgm_relay", RELAY)
    mod = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(mod)
    return mod


class ErrCodeTest(unittest.TestCase):
    """BACK-01: релей сообщает класс отказа меткой __WG_ERR__ <code> в stdout,
    backend по ней выбирает подсказку. Без метки до backend доходил только
    «exit status 1», и «root-пароль не подошёл» не выдавался никогда."""

    def setUp(self):
        self.relay = load_relay()

    def code(self, msg):
        return self.relay.err_code(self.relay.RelayError(msg))

    def test_root_login_refused(self):
        self.assertEqual(self.relay.err_code(self.relay.RelayError(self.relay.ROOT_LOGIN_REFUSED)), "root_auth_failed")

    def test_login_success_false(self):
        self.assertEqual(self.code("awgm login: success=false"), "auth_failed")

    def test_awgm_http_401_403(self):
        self.assertEqual(self.code('awgm POST /api/auth/login: HTTP 401: {"code":"X"}'), "auth_failed")
        self.assertEqual(self.code("awgm GET /api/terminal/status: HTTP 403: forbidden"), "auth_failed")

    def test_other_http_codes_have_no_code(self):
        self.assertIsNone(self.code("awgm GET /api/terminal/status: HTTP 500: boom"))
        self.assertIsNone(self.code("awgm GET /api/terminal/status: HTTP 404: nope"))
        # 401 не от API awg-manager (локальный мастер) -- не отказ входа в панель.
        self.assertIsNone(self.code("local wizard POST /x: HTTP 401: no"))
        # «401» в теле ответа другой ошибки -- не код.
        self.assertIsNone(self.code("awgm GET /api/x: HTTP 500: upstream said 401"))

    def test_active_session(self):
        self.assertEqual(self.code("AWG Manager terminal already has an active session; close it in the web UI and retry"), "session_active")

    def test_other_errors_have_no_code(self):
        self.assertIsNone(self.code("websocket closed"))
        self.assertIsNone(self.code("bootstrap script timeout waiting for marker"))
        self.assertIsNone(self.relay.err_code(ValueError("boom")))


class HandshakeAuthTest(unittest.TestCase):
    """v0.62 T10: 401/403 на рукопожатии websocket терминала -- это отказ
    входа в панель awg-manager (auth_failed), а не безымянный сбой."""

    def handshake_with(self, status_line):
        srv = socket.socket()
        srv.bind(("127.0.0.1", 0))
        srv.listen(1)
        port = srv.getsockname()[1]

        def serve():
            conn, _ = srv.accept()
            data = b""
            while b"\r\n\r\n" not in data:
                chunk = conn.recv(1024)
                if not chunk:
                    break
                data += chunk
            conn.sendall(status_line.encode("ascii") + b"\r\nContent-Length: 0\r\n\r\n")
            conn.close()
            srv.close()

        t = threading.Thread(target=serve, daemon=True)
        t.start()
        relay = load_relay()
        cfg = {"base_url": "http://127.0.0.1:%d" % port, "login": "admin", "password": "secret"}
        with self.assertRaises(relay.RelayError) as ctx:
            relay.ws_connect(cfg, [])
        t.join(5)
        return relay, ctx.exception

    def test_401_is_auth_failed(self):
        relay, e = self.handshake_with("HTTP/1.1 401 Unauthorized")
        self.assertEqual(relay.err_code(e), "auth_failed")

    def test_403_is_auth_failed(self):
        relay, e = self.handshake_with("HTTP/1.1 403 Forbidden")
        self.assertEqual(relay.err_code(e), "auth_failed")

    def test_other_status_has_no_code(self):
        relay, e = self.handshake_with("HTTP/1.1 502 Bad Gateway")
        self.assertIsNone(relay.err_code(e))
        self.assertIn("handshake failed", str(e))


class MainMarkerTest(unittest.TestCase):
    """Метка печатается в stdout из обработчика __main__, до текста в stderr."""

    def run_relay(self, cfg_text):
        with tempfile.TemporaryDirectory() as d:
            job = os.path.join(d, "job.json")
            with open(job, "w") as f:
                f.write(cfg_text)
            return subprocess.run([sys.executable, RELAY, job], capture_output=True, text=True, timeout=30)

    NONCE = "0123456789abcdef"

    def test_marker_in_stdout_on_classified_error(self):
        relay = load_relay()
        out, err = io.StringIO(), io.StringIO()
        relay.report_error(relay.RelayError(relay.ROOT_LOGIN_REFUSED), out, err, nonce=self.NONCE)
        self.assertEqual(out.getvalue().strip(), "__WG_ERR__ 0123456789abcdef root_auth_failed")
        self.assertIn("root_auth_failed", err.getvalue())

    def test_marker_survives_unterminated_router_line(self):
        # Роутер оставил строку без перевода строки -- метка не должна
        # приклеиться к ней и потеряться для backend.
        relay = load_relay()
        out, err = io.StringIO(), io.StringIO()
        out.write("router# partial output")
        relay.report_error(relay.RelayError(relay.ROOT_LOGIN_REFUSED), out, err, nonce=self.NONCE)
        lines = [l.strip() for l in out.getvalue().splitlines()]
        self.assertIn("__WG_ERR__ 0123456789abcdef root_auth_failed", lines)

    def test_no_marker_without_nonce(self):
        # v0.62 T10: в задании нет err_nonce (старый backend) -- метку не
        # печатаем: без нонса backend её не отличит от вывода роутера.
        relay = load_relay()
        for nonce in (None, ""):
            out, err = io.StringIO(), io.StringIO()
            relay.report_error(relay.RelayError(relay.ROOT_LOGIN_REFUSED), out, err, nonce=nonce)
            self.assertEqual(out.getvalue(), "")
            self.assertIn("root_auth_failed", err.getvalue())

    def test_nonce_taken_from_job(self):
        relay = load_relay()
        self.assertEqual(relay.job_err_nonce({"err_nonce": self.NONCE}), self.NONCE)
        self.assertEqual(relay.job_err_nonce({}), "")
        self.assertEqual(relay.job_err_nonce({"err_nonce": 5}), "")
        # Нонс с пробелом/переводом строки сломал бы разбор строки метки.
        self.assertEqual(relay.job_err_nonce({"err_nonce": "ab cd"}), "")
        self.assertEqual(relay.job_err_nonce({"err_nonce": "ab\ncd"}), "")

    def test_no_marker_on_unclassified_error(self):
        relay = load_relay()
        out, err = io.StringIO(), io.StringIO()
        relay.report_error(relay.RelayError("websocket closed"), out, err, nonce=self.NONCE)
        self.assertEqual(out.getvalue(), "")
        self.assertIn("websocket closed", err.getvalue())

    def run_relay_against_panel(self, job_extra):
        """Подпроцесс relay против локальной «панели», которая отвечает 401 на
        вход: main падает классифицированной ошибкой auth_failed по
        настоящему пути __main__ -> report_error."""

        class Panel(http.server.BaseHTTPRequestHandler):
            def do_POST(self):
                n = int(self.headers.get("Content-Length") or 0)
                if n:
                    self.rfile.read(n)
                code = 401 if self.path == "/api/auth/login" else 200
                self.send_response(code)
                self.send_header("Content-Type", "application/json")
                self.send_header("Content-Length", "2")
                self.end_headers()
                self.wfile.write(b"{}")

            def log_message(self, *a):
                pass

        srv = http.server.HTTPServer(("127.0.0.1", 0), Panel)
        t = threading.Thread(target=srv.serve_forever, daemon=True)
        t.start()
        try:
            job = {"base_url": "http://127.0.0.1:%d" % srv.server_address[1], "login": "admin", "password": "secret"}
            job.update(job_extra)
            env = {k: v for k, v in os.environ.items() if "proxy" not in k.lower()}
            with tempfile.TemporaryDirectory() as d:
                path = os.path.join(d, "job.json")
                with open(path, "w") as f:
                    json.dump(job, f)
                return subprocess.run([sys.executable, RELAY, path], capture_output=True, text=True, timeout=30, env=env)
        finally:
            srv.shutdown()
            srv.server_close()

    def test_main_prints_marker_with_job_nonce(self):
        res = self.run_relay_against_panel({"err_nonce": self.NONCE})
        self.assertEqual(res.returncode, 1, res.stderr)
        self.assertIn("__WG_ERR__ 0123456789abcdef auth_failed", [l.strip() for l in res.stdout.splitlines()])
        self.assertIn("HTTP 401", res.stderr)

    def test_main_no_marker_without_job_nonce(self):
        res = self.run_relay_against_panel({})
        self.assertEqual(res.returncode, 1, res.stderr)
        self.assertNotIn("__WG_ERR__", res.stdout)
        self.assertIn("HTTP 401", res.stderr)

    def test_process_exits_1_without_marker_on_bad_job(self):
        res = self.run_relay("not json")
        self.assertEqual(res.returncode, 1)
        self.assertNotIn("__WG_ERR__", res.stdout)


if __name__ == "__main__":
    unittest.main()
