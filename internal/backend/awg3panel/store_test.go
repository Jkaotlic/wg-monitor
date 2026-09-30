package awg3panel

import (
	"bytes"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func secretInstance() Instance {
	return Instance{
		ID: "main", Label: "Main", BaseURL: "https://panel.example.com", User: "admin",
		Password: "PANEL-PW-MUST-NOT-LEAK", CertPEM: "fake-cert",
		KeyPEM: "fake-key KEY-MUST-NOT-LEAK", Enabled: true,
	}
}

func TestSaveStoreIs0600AndRoundTrips(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state")
	path := filepath.Join(dir, DefaultStoreName)
	paused := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	inst := secretInstance()
	inst.Lock, inst.PausedUntil, inst.Readonly = LockBadPassword, paused, true
	if err := SaveStore(path, Store{Instances: []Instance{inst}}); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(path)
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("права файла: %v %v", fi.Mode(), err)
	}
	if di, _ := os.Stat(dir); di.Mode().Perm() != 0o700 {
		t.Fatalf("права каталога: %v", di.Mode())
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Fatalf("временные файлы остались: %v", entries)
	}
	st, err := LoadStore(path)
	if err != nil || st.Version != 1 || len(st.Instances) != 1 {
		t.Fatalf("чтение: %+v %v", st, err)
	}
	got := st.Instances[0]
	if got.Password != inst.Password || got.KeyPEM != inst.KeyPEM || got.Lock != LockBadPassword || !got.PausedUntil.Equal(paused) || !got.Readonly {
		t.Fatal("поля не пережили запись")
	}
}

func TestLoadStoreMissingIsEmpty(t *testing.T) {
	st, err := LoadStore(filepath.Join(t.TempDir(), "nope.json"))
	if err != nil || st.Version != 1 || len(st.Instances) != 0 {
		t.Fatalf("%+v %v", st, err)
	}
}

func TestLoadStoreCorruptDoesNotEchoContent(t *testing.T) {
	path := filepath.Join(t.TempDir(), DefaultStoreName)
	_ = os.WriteFile(path, []byte(`{"instances":[{"password":"hunter2-MUST-NOT-LEAK"`), 0o600)
	_, err := LoadStore(path)
	if err == nil || strings.Contains(err.Error(), "hunter2") {
		t.Fatalf("ошибка: %v", err)
	}
}

func TestInstancePrintsNothingSecret(t *testing.T) {
	inst := secretInstance()
	var logs bytes.Buffer
	slog.New(slog.NewTextHandler(&logs, nil)).Info("x", "inst", inst)
	out := fmt.Sprintf("%v %+v %#v %s", inst, inst, inst, logs.String())
	for _, bad := range []string{"PANEL-PW", "KEY-MUST-NOT-LEAK"} {
		if strings.Contains(out, bad) {
			t.Fatalf("печать выдала %q: %s", bad, out)
		}
	}
}

func TestNormalizeBaseURL(t *testing.T) {
	ok := map[string]string{
		"https://panel.example.com":       "https://panel.example.com",
		" https://panel.example.com/ ":    "https://panel.example.com",
		"https://203.0.113.5:8444":        "https://203.0.113.5:8444",
		"https://panel.example.com/awg3/": "https://panel.example.com/awg3",
	}
	for in, want := range ok {
		if got, err := NormalizeBaseURL(in); err != nil || got != want {
			t.Errorf("%q -> %q %v, ждали %q", in, got, err, want)
		}
	}
	for _, in := range []string{"", "panel.example.com", "http://panel.example.com", "https://", "https://admin:pw@example.com", "https://panel.example.com/?x=1", "https://panel.example.com/#a", "https://panel example.com"} {
		_, err := NormalizeBaseURL(in)
		var fe *FieldError
		if !errors.As(err, &fe) || fe.Field != "base_url" {
			t.Errorf("%q: ждали отказ по base_url, получили %v", in, err)
		}
	}
}

func TestValidateInstance(t *testing.T) {
	good := secretInstance()
	if err := validateInstance(good); err != nil {
		t.Fatalf("годный: %v", err)
	}
	cases := map[string]func(*Instance){
		"id":       func(i *Instance) { i.ID = "Main!" },
		"label":    func(i *Instance) { i.Label = strings.Repeat("я", 41) },
		"base_url": func(i *Instance) { i.BaseURL = "http://x" },
		"user":     func(i *Instance) { i.User = "ad:min" },
		"password": func(i *Instance) { i.Password = "" },
		"p12":      func(i *Instance) { i.CertPEM = "" },
	}
	for field, mut := range cases {
		inst := good
		mut(&inst)
		var fe *FieldError
		if err := validateInstance(inst); !errors.As(err, &fe) || fe.Field != field {
			t.Errorf("%s: %v", field, err)
		}
	}
	if !ValidInstanceID("nl2") || ValidInstanceID("2nl") || ValidInstanceID("n") {
		t.Fatal("правило id")
	}
}

func TestLoadStoreWithoutIssuers(t *testing.T) {
	path := filepath.Join(t.TempDir(), DefaultStoreName)
	if err := os.WriteFile(path, []byte(`{"version":1,"instances":[{"id":"main","label":"M","enabled":true}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	st, err := LoadStore(path)
	if err != nil || len(st.Instances) != 1 || st.Instances[0].Issuers != nil {
		t.Fatalf("%+v %v", st, err)
	}
}
