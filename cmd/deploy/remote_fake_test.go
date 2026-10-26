package main

import (
	"fmt"
	"strings"
)

// fakeRemote -- remoteExec без сети: пишет каждую команду и загрузку по
// порядку, ответы даёт reply (nil -- всё успешно, пустой вывод).
type fakeRemote struct {
	calls   []string
	uploads map[string][]byte
	reply   func(cmd string) (stdout, stderr string, rc int, err error)
	closed  bool
}

func newFakeRemote() *fakeRemote {
	return &fakeRemote{uploads: map[string][]byte{}}
}

func (f *fakeRemote) Run(cmd string) (string, string, int, error) {
	f.calls = append(f.calls, "run: "+cmd)
	if f.reply == nil {
		return "", "", 0, nil
	}
	return f.reply(cmd)
}

func (f *fakeRemote) MustRun(cmd string) (string, error) {
	out, errS, rc, err := f.Run(cmd)
	if err != nil {
		return out, fmt.Errorf("ssh transport: %w", err)
	}
	if rc != 0 {
		return out, fmt.Errorf("cmd %q exit %d: %s", cmd, rc, errS)
	}
	return out, nil
}

func (f *fakeRemote) UploadSFTPMode(remotePath string, data []byte, mode string) error {
	f.calls = append(f.calls, "upload: "+remotePath+" mode="+mode)
	f.uploads[remotePath] = append([]byte(nil), data...)
	return nil
}

func (f *fakeRemote) Close() error {
	f.closed = true
	return nil
}

// indexOfCall -- номер первого вызова, содержащего needle, или -1.
func (f *fakeRemote) indexOfCall(needle string) int {
	for i, c := range f.calls {
		if strings.Contains(c, needle) {
			return i
		}
	}
	return -1
}

// stubRestoreConnect подменяет подключение восстановления фейком и
// считает вызовы.
func stubRestoreConnect(t interface{ Cleanup(func()) }, fake *fakeRemote) *int {
	n := 0
	prev := connectRestoreBackend
	connectRestoreBackend = func(*State, *SecretStore) (remoteSession, error) {
		n++
		return fake, nil
	}
	t.Cleanup(func() { connectRestoreBackend = prev })
	return &n
}
