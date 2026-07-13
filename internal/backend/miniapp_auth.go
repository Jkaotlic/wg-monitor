package backend

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

// miniappInitDataMaxAge bounds how old a Telegram-signed initData payload may
// be when a session is minted from it. This only gates the one-time session
// mint (POST /v1/miniapp/session); the resulting session cookie has its own,
// independent TTL (miniappSessionTTL).
const miniappInitDataMaxAge = 24 * time.Hour

type miniappInitDataUser struct {
	ID        int64  `json:"id"`
	FirstName string `json:"first_name"`
	Username  string `json:"username"`
}

// verifyInitData validates a Telegram Mini App initData string per
// https://core.telegram.org/bots/webapps#validating-data-received-via-the-mini-app
// and returns the embedded user. now is an injectable seam for deterministic tests.
func verifyInitData(raw, botToken string, now time.Time) (miniappInitDataUser, error) {
	values, err := url.ParseQuery(raw)
	if err != nil {
		return miniappInitDataUser{}, errors.New("init_data: malformed query string")
	}
	hash := values.Get("hash")
	if hash == "" {
		return miniappInitDataUser{}, errors.New("init_data: missing hash")
	}
	values.Del("hash")

	keys := make([]string, 0, len(values))
	for k := range values {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	pairs := make([]string, 0, len(keys))
	for _, k := range keys {
		pairs = append(pairs, k+"="+values.Get(k))
	}
	dataCheckString := strings.Join(pairs, "\n")

	secretMAC := hmac.New(sha256.New, []byte("WebAppData"))
	secretMAC.Write([]byte(botToken))
	secretKey := secretMAC.Sum(nil)

	checkMAC := hmac.New(sha256.New, secretKey)
	checkMAC.Write([]byte(dataCheckString))
	computed := hex.EncodeToString(checkMAC.Sum(nil))

	if subtle.ConstantTimeCompare([]byte(computed), []byte(hash)) != 1 {
		return miniappInitDataUser{}, errors.New("init_data: signature mismatch")
	}

	authDateUnix, err := strconv.ParseInt(values.Get("auth_date"), 10, 64)
	if err != nil {
		return miniappInitDataUser{}, errors.New("init_data: missing/invalid auth_date")
	}
	authDate := time.Unix(authDateUnix, 0).UTC()
	if now.Sub(authDate) > miniappInitDataMaxAge {
		return miniappInitDataUser{}, errors.New("init_data: stale auth_date")
	}
	if authDate.After(now.Add(1 * time.Minute)) {
		return miniappInitDataUser{}, errors.New("init_data: auth_date in the future")
	}

	userRaw := values.Get("user")
	if userRaw == "" {
		return miniappInitDataUser{}, errors.New("init_data: missing user")
	}
	var u miniappInitDataUser
	if err := json.Unmarshal([]byte(userRaw), &u); err != nil {
		return miniappInitDataUser{}, errors.New("init_data: malformed user json")
	}
	if u.ID == 0 {
		return miniappInitDataUser{}, errors.New("init_data: missing user id")
	}
	return u, nil
}
