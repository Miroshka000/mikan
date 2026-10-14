package tgate

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestLinkCode(t *testing.T) {
	secret := []byte("0123456789abcdef0123456789abcdef")
	now := time.Unix(1_800_000_000, 0)
	code := LinkCode(secret, 42, now)
	if len(code) > 64 || strings.Trim(code, "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789_-") != "" {
		t.Fatalf("a /start parameter is at most 64 of [A-Za-z0-9_-]: %q", code)
	}
	if id, err := ParseLinkCode(secret, code, now.Add(LinkTTL-time.Second)); err != nil || id != 42 {
		t.Fatalf("fresh code: %d %v", id, err)
	}
	if _, err := ParseLinkCode(secret, code, now.Add(LinkTTL+time.Second)); !errors.Is(err, ErrLinkExpired) {
		t.Fatalf("stale code: %v", err)
	}
	if _, err := ParseLinkCode([]byte("another secret, another panel..."), code, now); !errors.Is(err, ErrLinkInvalid) {
		t.Fatalf("another panel's code: %v", err)
	}
	forged := []byte(code)
	forged[3] ^= 1
	if _, err := ParseLinkCode(secret, string(forged), now); !errors.Is(err, ErrLinkInvalid) {
		t.Fatalf("a changed code: %v", err)
	}
}

// signed builds initData the way Telegram does.
func signed(token string, vals url.Values) string {
	keys := make([]string, 0, len(vals))
	for k := range vals {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	lines := make([]string, len(keys))
	for i, k := range keys {
		lines[i] = k + "=" + vals.Get(k)
	}
	secret := hmac.New(sha256.New, []byte("WebAppData"))
	secret.Write([]byte(token))
	m := hmac.New(sha256.New, secret.Sum(nil))
	m.Write([]byte(strings.Join(lines, "\n")))
	vals.Set("hash", hex.EncodeToString(m.Sum(nil)))
	return vals.Encode()
}

func TestCheckInitData(t *testing.T) {
	const token = "123456:ABC-test-token"
	now := time.Unix(1_800_000_000, 0)
	data := url.Values{"auth_date": {strconv.FormatInt(now.Add(-time.Minute).Unix(), 10)}, "query_id": {"AAH"},
		"user": {`{"id":777,"first_name":"Анна","username":"anna","language_code":"ru"}`}}
	good := signed(token, data)
	u, err := CheckInitData(token, good, now)
	if err != nil || u.ID != 777 || u.Username != "anna" {
		t.Fatalf("valid initData: %+v %v", u, err)
	}
	if _, err := CheckInitData("654321:other-bot", good, now); !errors.Is(err, ErrInitData) {
		t.Fatalf("another bot's signature: %v", err)
	}
	tampered := strings.Replace(good, "777", "778", 1)
	if _, err := CheckInitData(token, tampered, now); !errors.Is(err, ErrInitData) {
		t.Fatalf("a changed user id: %v", err)
	}
	if _, err := CheckInitData(token, good, now.Add(InitDataTTL+time.Hour)); !errors.Is(err, ErrInitData) {
		t.Fatalf("an old signature: %v", err)
	}
	// An hour, not a day: the data of a Mini App opened a minute ago is good for 59 more.
	if _, err := CheckInitData(token, good, now.Add(InitDataTTL-2*time.Minute)); err != nil {
		t.Fatalf("a signature within the hour: %v", err)
	}
	if _, err := CheckInitData(token, good, now.Add(InitDataTTL)); !errors.Is(err, ErrInitData) {
		t.Fatalf("a signature past the hour: %v", err)
	}
	if InitDataTTL > time.Hour {
		t.Fatalf("initData is good for %s", InitDataTTL)
	}
	if _, err := CheckInitData(token, "user=%7B%7D", now); !errors.Is(err, ErrInitData) {
		t.Fatalf("no hash: %v", err)
	}
}
