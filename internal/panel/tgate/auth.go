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

	"mikan/internal/panel/model"
	"mikan/internal/tgapi"
)

// A link code opens the bot with /start <code> from a subscription page and ties that
// subscription to the Telegram account that follows it. Whoever holds the subscription
// link can get one, the same as with the link itself. It is signed, not stored: user id
// and expiry, sealed with the panel's secret.

// LinkTTL: how long a code from the subscription page stays good.
const LinkTTL = 30 * time.Minute

// The refusals of a link code are the model's, so the bot addon tells them apart.
var (
	ErrLinkInvalid = model.ErrLinkInvalid
	ErrLinkExpired = model.ErrLinkExpired
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
func CheckInitData(token, initData string, now time.Time) (tgapi.User, error) {
	vals, err := url.ParseQuery(initData)
	if err != nil {
		return tgapi.User{}, ErrInitData
	}
	hash := vals.Get("hash")
	if hash == "" {
		return tgapi.User{}, ErrInitData
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
		return tgapi.User{}, ErrInitData
	}
	at, err := strconv.ParseInt(vals.Get("auth_date"), 10, 64)
	if err != nil || now.Sub(time.Unix(at, 0)) > InitDataTTL || time.Unix(at, 0).Sub(now) > time.Minute {
		return tgapi.User{}, ErrInitData
	}
	var u tgapi.User
	if err := json.Unmarshal([]byte(vals.Get("user")), &u); err != nil || u.ID == 0 {
		return tgapi.User{}, ErrInitData
	}
	return u, nil
}

// The Telegram login (oauth.telegram.org, the login widget's own page) proves who signed in
// with the account's fields signed with a key derived from the bot token: unlike initData's,
// the SHA-256 of the token itself.

// LoginTTL: the login comes back at once, so an older signature is not accepted; within it
// the caller takes each signature once.
const LoginTTL = 10 * time.Minute

var ErrLogin = errors.New("telegram_login")

// LoginUser is the account a Telegram login is of.
type LoginUser struct {
	ID       int64
	Username string // "" without one
	Name     string // first and last name
	Hash     string // the signature, which the caller takes once
}

// CheckLogin verifies the result of a Telegram login: the base64 of its JSON fields, as
// oauth.telegram.org gives it back in the address (#tgAuthResult=…).
func CheckLogin(token, result string, now time.Time) (LoginUser, error) {
	var raw []byte
	var err error
	for _, enc := range []*base64.Encoding{base64.RawURLEncoding, base64.URLEncoding, base64.RawStdEncoding, base64.StdEncoding} {
		if raw, err = enc.DecodeString(strings.TrimSpace(result)); err == nil {
			break
		}
	}
	if err != nil {
		return LoginUser{}, ErrLogin
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		return LoginUser{}, ErrLogin
	}
	vals := map[string]string{}
	for k, v := range obj {
		var s string
		if json.Unmarshal(v, &s) == nil {
			vals[k] = s
			continue
		}
		// Numbers and the like are signed as written.
		vals[k] = string(v)
	}
	hash := vals["hash"]
	if hash == "" || token == "" {
		return LoginUser{}, ErrLogin
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
		lines[i] = k + "=" + vals[k]
	}
	key := sha256.Sum256([]byte(token))
	m := hmac.New(sha256.New, key[:])
	m.Write([]byte(strings.Join(lines, "\n")))
	want, err := hex.DecodeString(hash)
	if err != nil || !hmac.Equal(m.Sum(nil), want) {
		return LoginUser{}, ErrLogin
	}
	at, err := strconv.ParseInt(vals["auth_date"], 10, 64)
	if err != nil || now.Sub(time.Unix(at, 0)) > LoginTTL || time.Unix(at, 0).Sub(now) > time.Minute {
		return LoginUser{}, ErrLogin
	}
	id, err := strconv.ParseInt(vals["id"], 10, 64)
	if err != nil || id <= 0 {
		return LoginUser{}, ErrLogin
	}
	return LoginUser{ID: id, Username: vals["username"], Name: strings.TrimSpace(vals["first_name"] + " " + vals["last_name"]), Hash: hash}, nil
}
