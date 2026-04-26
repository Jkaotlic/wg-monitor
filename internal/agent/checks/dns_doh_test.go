package checks

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestDNSDoH_AllOK(t *testing.T) {
	d := Deps{Runner: RunnerFunc(func(_ context.Context, name string, args ...string) (string, error) {
		if name != "dig" {
			t.Fatalf("expected dig, got %s", name)
		}
		return ";; ANSWER SECTION:\nexample.com. 60 IN A 93.184.216.34\n", nil
	})}
	chk := DNSDoH{
		Providers:     []DNSProvider{{Name: "cf", Host: "1.1.1.1"}, {Name: "g", Host: "8.8.8.8"}, {Name: "q9", Host: "9.9.9.9"}},
		TestDomain:    "example.com",
		FailThreshold: 2,
	}
	got := chk.Run(context.Background(), d)
	if got.Status != "ok" {
		t.Fatalf("got %+v", got)
	}
}

func TestDNSDoH_TwoFail_TriggersFail(t *testing.T) {
	calls := 0
	d := Deps{Runner: RunnerFunc(func(_ context.Context, _ string, _ ...string) (string, error) {
		calls++
		if calls <= 2 {
			return "", errors.New("connection timed out")
		}
		return ";; ANSWER SECTION:\nexample.com. 60 IN A 1.2.3.4\n", nil
	})}
	chk := DNSDoH{
		Providers:     []DNSProvider{{Name: "cf", Host: "1.1.1.1"}, {Name: "g", Host: "8.8.8.8"}, {Name: "q9", Host: "9.9.9.9"}},
		TestDomain:    "example.com",
		FailThreshold: 2,
	}
	got := chk.Run(context.Background(), d)
	if got.Status != "fail" {
		t.Fatalf("expected fail, got %+v", got)
	}
	failed, _ := got.Details["failed_providers"].([]string)
	if len(failed) != 2 {
		t.Fatalf("failed_providers=%v", failed)
	}
}

func TestDNSDoH_OneFail_StillOK(t *testing.T) {
	calls := 0
	d := Deps{Runner: RunnerFunc(func(_ context.Context, _ string, _ ...string) (string, error) {
		calls++
		if calls == 1 {
			return "", errors.New("timeout")
		}
		return "ANSWER SECTION", nil
	})}
	chk := DNSDoH{
		Providers:     []DNSProvider{{Name: "cf", Host: "1.1.1.1"}, {Name: "g", Host: "8.8.8.8"}, {Name: "q9", Host: "9.9.9.9"}},
		TestDomain:    "example.com",
		FailThreshold: 2,
	}
	got := chk.Run(context.Background(), d)
	if got.Status != "ok" {
		t.Fatalf("expected ok with 1/3 fail, got %+v", got)
	}
}

func TestDNSDoHRejectsEmptyAnswer(t *testing.T) {
	d := Deps{Runner: RunnerFunc(func(_ context.Context, _ string, _ ...string) (string, error) {
		return "no answer here", nil // no "ANSWER SECTION" → counted as fail
	})}
	chk := DNSDoH{
		Providers:     []DNSProvider{{Name: "cf", Host: "1.1.1.1"}, {Name: "g", Host: "8.8.8.8"}, {Name: "q9", Host: "9.9.9.9"}},
		TestDomain:    "example.com",
		FailThreshold: 2,
	}
	got := chk.Run(context.Background(), d)
	if got.Status != "fail" {
		t.Fatalf("got %+v", got)
	}
	if !strings.Contains(got.Details["error"].(string), "providers failed") {
		t.Fatalf("err msg: %v", got.Details["error"])
	}
}
