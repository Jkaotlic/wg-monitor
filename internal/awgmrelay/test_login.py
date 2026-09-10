import importlib.util, os, unittest

HERE = os.path.dirname(os.path.abspath(__file__))


def load_relay():
    spec = importlib.util.spec_from_file_location("awgm_relay", os.path.join(HERE, "awgm-relay.py"))
    mod = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(mod)
    return mod


class LoginIfNeededTest(unittest.TestCase):
    """На awg-manager с выключенным входом (authEnabled=false) ручка
    /api/auth/login отвечает 405. Установка агента на такой роутер падала на
    первом шаге, не дойдя до терминала: релей принимал «входа нет» за отказ.
    Неверный пароль (401) — по-прежнему отказ."""

    CFG = {"base_url": "https://awg.example", "login": "admin", "password": "secret"}

    def login_answering(self, relay, status):
        def fake_request(op, cfg, method, api_path, body=None):
            raise relay.RelayError("awgm %s %s: HTTP %d: {\"code\":\"X\"}" % (method, api_path, status))
        relay.request = fake_request

    def test_login_disabled_405_is_not_an_error(self):
        relay = load_relay()
        self.login_answering(relay, 405)
        relay.login_if_needed(object(), dict(self.CFG))  # не должно бросать

    def test_login_missing_404_is_not_an_error(self):
        relay = load_relay()
        self.login_answering(relay, 404)
        relay.login_if_needed(object(), dict(self.CFG))

    def test_wrong_password_401_still_fails(self):
        relay = load_relay()
        self.login_answering(relay, 401)
        with self.assertRaises(relay.RelayError):
            relay.login_if_needed(object(), dict(self.CFG))


if __name__ == "__main__":
    unittest.main()
