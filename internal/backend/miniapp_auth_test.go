package backend

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"
)

// signTestInitData builds a validly-signed Telegram initData query string per
// https://core.telegram.org/bots/webapps#validating-data-received-via-the-mini-app
// so tests can exercise verifyInitData against real signatures.
func signTestInitData(t *testing.T, botToken string, fields map[string]string) string {
	t.Helper()
	keys := make([]string, 0, len(fields))
	for k := range fields {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	pairs := make([]string, 0, len(keys))
	for _, k := range keys {
		pairs = append(pairs, k+"="+fields[k])
	}
	dataCheckString := strings.Join(pairs, "\n")

	secretMAC := hmac.New(sha256.New, []byte("WebAppData"))
	secretMAC.Write([]byte(botToken))
	secretKey := secretMAC.Sum(nil)

	checkMAC := hmac.New(sha256.New, secretKey)
	checkMAC.Write([]byte(dataCheckString))
	hash := hex.EncodeToString(checkMAC.Sum(nil))

	values := url.Values{}
	for k, v := range fields {
		values.Set(k, v)
	}
	values.Set("hash", hash)
	return values.Encode()
}

func TestVerifyInitData_Valid(t *testing.T) {
	now := time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC)
	raw := signTestInitData(t, "test-bot-token", map[string]string{
		"auth_date": strconv.FormatInt(now.Add(-1*time.Minute).Unix(), 10),
		"query_id":  "AA1234",
		"user":      `{"id":555,"first_name":"Op","username":"op_tg"}`,
	})

	u, err := verifyInitData(raw, "test-bot-token", now)
	if err != nil {
		t.Fatalf("verifyInitData: %v", err)
	}
	if u.ID != 555 {
		t.Errorf("ID = %d, want 555", u.ID)
	}
	if u.Username != "op_tg" {
		t.Errorf("Username = %q, want op_tg", u.Username)
	}
}

func TestVerifyInitData_WrongBotTokenRejected(t *testing.T) {
	now := time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC)
	raw := signTestInitData(t, "test-bot-token", map[string]string{
		"auth_date": strconv.FormatInt(now.Add(-1*time.Minute).Unix(), 10),
		"user":      `{"id":555,"first_name":"Op","username":"op_tg"}`,
	})

	if _, err := verifyInitData(raw, "different-bot-token", now); err == nil {
		t.Fatal("expected signature mismatch error, got nil")
	}
}

func TestVerifyInitData_TamperedRejected(t *testing.T) {
	now := time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC)
	raw := signTestInitData(t, "test-bot-token", map[string]string{
		"auth_date": strconv.FormatInt(now.Add(-1*time.Minute).Unix(), 10),
		"user":      `{"id":555,"first_name":"Op","username":"op_tg"}`,
	})
	tampered := strings.Replace(raw, "555", "999", 1)

	if _, err := verifyInitData(tampered, "test-bot-token", now); err == nil {
		t.Fatal("expected signature mismatch error, got nil")
	}
}

func TestVerifyInitData_StaleAuthDateRejected(t *testing.T) {
	now := time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC)
	raw := signTestInitData(t, "test-bot-token", map[string]string{
		"auth_date": strconv.FormatInt(now.Add(-48*time.Hour).Unix(), 10),
		"user":      `{"id":555,"first_name":"Op","username":"op_tg"}`,
	})

	if _, err := verifyInitData(raw, "test-bot-token", now); err == nil {
		t.Fatal("expected stale auth_date error, got nil")
	}
}

func TestVerifyInitData_FutureAuthDateRejected(t *testing.T) {
	now := time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC)
	raw := signTestInitData(t, "test-bot-token", map[string]string{
		"auth_date": strconv.FormatInt(now.Add(10*time.Minute).Unix(), 10),
		"user":      `{"id":555,"first_name":"Op","username":"op_tg"}`,
	})

	if _, err := verifyInitData(raw, "test-bot-token", now); err == nil {
		t.Fatal("expected future auth_date error, got nil")
	}
}

func TestVerifyInitData_MissingUserRejected(t *testing.T) {
	now := time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC)
	raw := signTestInitData(t, "test-bot-token", map[string]string{
		"auth_date": strconv.FormatInt(now.Add(-1*time.Minute).Unix(), 10),
	})

	if _, err := verifyInitData(raw, "test-bot-token", now); err == nil {
		t.Fatal("expected missing-user error, got nil")
	}
}
