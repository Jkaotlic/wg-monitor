package main

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

// Проводка stepUploadFile: какие команды и в каком порядке уходят на сервер.
// Сами команды (umask 077, chmod-с-удалением) проверяются в ssh_test.go.
func TestStepUploadFileWiring(t *testing.T) {
	const p = "/etc/wg-monitor/bot-token.txt"
	cases := []struct {
		name   string
		mode   string
		testRC int
		testE  error
		want   []string
	}{
		{"секретный, файла не было", "600", 1, nil, []string{
			"run: test -e '" + p + "'",
			"upload: " + p + " mode=600",
			"run: chmod 600 '" + p + "' || { rm -f '" + p + "'; exit 1; }",
		}},
		{"секретный, файл был", "640", 0, nil, []string{
			"run: test -e '" + p + "'",
			"upload: " + p + " mode=640",
			"run: chmod 640 '" + p + "' || exit 1",
		}},
		{"секретный, проверка не дошла -- считаем, что был", "600", -1, errors.New("transport"), []string{
			"run: test -e '" + p + "'",
			"upload: " + p + " mode=600",
			"run: chmod 600 '" + p + "' || exit 1",
		}},
		{"открытый режим -- без test -e и без rm", "644", 0, nil, []string{
			"upload: " + p + " mode=644",
			"run: chmod 644 '" + p + "'",
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fake := newFakeRemote()
			fake.reply = func(cmd string) (string, string, int, error) {
				if strings.HasPrefix(cmd, "test -e ") {
					return "", "", tc.testRC, tc.testE
				}
				return "", "", 0, nil
			}
			if err := stepUploadFile(fake, p, []byte("secret\n"), tc.mode); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(fake.calls, tc.want) {
				t.Fatalf("calls:\n%s\nwant:\n%s", strings.Join(fake.calls, "\n"), strings.Join(tc.want, "\n"))
			}
			if string(fake.uploads[p]) != "secret\n" {
				t.Fatalf("upload body=%q", fake.uploads[p])
			}
		})
	}
}

func TestStepUploadFileChmodFailureIsError(t *testing.T) {
	fake := newFakeRemote()
	fake.reply = func(cmd string) (string, string, int, error) {
		if strings.HasPrefix(cmd, "chmod ") {
			return "", "chmod: denied", 1, nil
		}
		return "", "", 1, nil // test -e: файла не было
	}
	if err := stepUploadFile(fake, "/etc/wg-monitor/wizard-token.txt", []byte("x"), "600"); err == nil {
		t.Fatal("провал chmod должен вернуть ошибку")
	}
}
