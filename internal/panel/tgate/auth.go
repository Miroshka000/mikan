package tgate

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

// A link code opens the bot with /start <code> from a subscription page and ties that
// subscription to the Telegram account that follows it. Whoever holds the subscription
// link can get one, the same as with the link itself. It is signed, not stored: user id
// and expiry, sealed with the panel's secret.

// LinkTTL: how long a code from the subscription page stays good.
const LinkTTL = 30 * time.Minute

var (
	ErrLinkInvalid = errors.New("link_invalid")
	ErrLinkExpired = errors.New("link_expired")
)

const macSize = 12

// LinkCode returns a code for /start: 38 characters of [A-Za-z0-9_-], within Telegram's 64.
func LinkCode(secret []byte, userID int64, now time.Time) string {
	b := make([]byte, 16, 16+macSize)
	binary.BigEndian.PutUint64(b, uint64(userID))
	binary.BigEndian.PutUint64(b[8:], uint64(now.Add(LinkTTL).Unix()))
	return base64.RawURLEncoding.EncodeToString(append(b, sum(secret, b)...))
}

// ParseLinkCode returns the user a code was made for.
func ParseLinkCode(secret []byte, code string, now time.Time) (int64, error) {
	b, err := base64.RawURLEncoding.DecodeString(code)
	if err != nil || len(b) != 16+macSize || !hmac.Equal(b[16:], sum(secret, b[:16])) {
		return 0, ErrLinkInvalid
	}
	if now.Unix() > int64(binary.BigEndian.Uint64(b[8:16])) {
		return 0, ErrLinkExpired
	}
	return int64(binary.BigEndian.Uint64(b[:8])), nil
}

func sum(secret, b []byte) []byte {
	m := hmac.New(sha256.New, secret)
	m.Write([]byte("mikan-tg-link"))
	m.Write(b)
	return m.Sum(nil)[:macSize]
}

// The Mini App proves who opened it with Telegram's initData: the launch parameters
// signed with a key derived from the bot token.

// InitDataTTL: an older signature is not accepted (Telegram's own advice: check auth_date).
// initData travels in the Mini App's address and in every request it makes, so it ends up
// in histories and logs: what it opens must not stay open for a day. The Mini App asks for
// its session when it opens; one left open for longer has to be opened again.
const InitDataTTL = time.Hour

var ErrInitData = errors.New("init_data")

// CheckInitData verifies initData and returns the Telegram user.
func CheckInitData(token, initData string, now time.Time) (User, error) {
	vals, err := url.ParseQuery(initData)
	if err != nil {
		return User{}, ErrInitData
	}
	hash := vals.Get("hash")
	if hash == "" {
		return User{}, ErrInitData
	}
	keys := make([]string, 0, len(vals))
	for k := range vals {
		if k != "hash" {
			keys = append(keys, k)
		}
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
	want, err := hex.DecodeString(hash)
	if err != nil || !hmac.Equal(m.Sum(nil), want) {
		return User{}, ErrInitData
	}
	at, err := strconv.ParseInt(vals.Get("auth_date"), 10, 64)
	if err != nil || now.Sub(time.Unix(at, 0)) > InitDataTTL || time.Unix(at, 0).Sub(now) > time.Minute {
		return User{}, ErrInitData
	}
	var u User
	if err := json.Unmarshal([]byte(vals.Get("user")), &u); err != nil || u.ID == 0 {
		return User{}, ErrInitData
	}
	return u, nil
}
