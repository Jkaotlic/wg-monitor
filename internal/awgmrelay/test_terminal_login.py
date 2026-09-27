import importlib.util, os, socket, types, unittest

HERE = os.path.dirname(os.path.abspath(__file__))


def load_relay():
    spec = importlib.util.spec_from_file_location("awgm_relay", os.path.join(HERE, "awgm-relay.py"))
    mod = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(mod)
    return mod


class TerminalLoginTest(unittest.TestCase):
    """AGENT-07: неверный root-пароль. Раньше login_terminal по истечении
    8 с молча возвращался, run_bootstrap набирал heredoc со скриптом (а в нём
    токен агента) прямо в приглашение login: (оно уходит в syslog) и ждал
    маркер 15 минут. Отказ входа должен обрываться сразу и не слать скрипт."""

    CFG = {"terminal_user": "root", "terminal_password": "wrong"}

    def drive(self, relay, frames):
        sent = []
        queue = list(frames)

        def fake_recv(sock):
            if queue:
                return queue.pop(0)
            raise socket.timeout()

        def fake_send(sock, text):
            sent.append(text)
            if shell_answers_probe and "WG_SHELL" in text:
                queue.append("__WG_SHELL_OK__\n~ > ")

        shell_answers_probe = self.shell_answers_probe
        relay.ws_recv = fake_recv
        relay.send_input = fake_send
        self.fake_clock(relay)
        return sent

    shell_answers_probe = False

    def fake_clock(self, relay):
        clock = [0.0]

        def tick():
            clock[0] += 0.5
            return clock[0]

        relay.time = types.SimpleNamespace(time=tick)

    def test_wrong_password_raises_root_login_refused(self):
        relay = load_relay()
        sent = self.drive(relay, ["Keenetic login: ", "Password: ", "\nLogin incorrect\n", "Keenetic login: "])
        with self.assertRaises(relay.RelayError) as ctx:
            relay.login_terminal(object(), dict(self.CFG))
        msg = str(ctx.exception)
        self.assertIn("root_auth_failed", msg)  # контракт с бэкендом (HintRootAuthFailed)
        self.assertIn("auth_failed", msg)  # и прежняя метка отказа авторизации
        self.assertEqual(sent, ["root\n", "wrong\n"])  # скрипт и проба не ушли

    def test_second_login_prompt_after_password_is_refusal(self):
        relay = load_relay()
        self.drive(relay, ["login: ", "Password: ", "\n\nlogin: "])
        with self.assertRaises(relay.RelayError):
            relay.login_terminal(object(), dict(self.CFG))

    def test_no_shell_after_credentials_is_error_not_silent_return(self):
        relay = load_relay()
        self.drive(relay, ["login: ", "Password: "])
        with self.assertRaises(relay.RelayError):
            relay.login_terminal(object(), dict(self.CFG))

    def test_good_password_reaches_shell(self):
        relay = load_relay()
        self.drive(relay, ["login: ", "Password: ", "\nroot@Keenetic:~# "])
        relay.login_terminal(object(), {"terminal_user": "root", "terminal_password": "right"})


    def test_last_login_banner_is_not_refusal(self):
        relay = load_relay()
        self.drive(relay, ["login: ", "Password: ", "\nLast login:", " Sun Sep 27 on pts/0\nroot@Keenetic:~# "])
        relay.login_terminal(object(), {"terminal_user": "root", "terminal_password": "right"})

    def test_unrecognised_prompt_confirmed_by_probe(self):
        relay = load_relay()
        self.shell_answers_probe = True
        sent = self.drive(relay, ["login: ", "Password: ", "\n~ > "])
        relay.login_terminal(object(), {"terminal_user": "root", "terminal_password": "right"})
        self.assertEqual(len(sent), 3)
        self.assertNotIn("__WG_SHELL_OK__", sent[2])  # эхо команды -- не ответ


if __name__ == "__main__":
    unittest.main()
