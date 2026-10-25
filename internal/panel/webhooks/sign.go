package webhooks

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"strconv"
	"time"

	"mikan/internal/panel/events"
	"mikan/internal/panel/secure"
)

// The headers of a delivery (site/src/content/docs/operations/webhooks.mdx tells a receiver
// how to check them). The signature covers the timestamp and the body together, so a captured
// delivery can neither be given another body nor be replayed under a later time; each attempt
// is signed anew, so a retry hours later still carries a timestamp that is fresh.
const (
	headerEvent     = "X-Mikan-Event"
	headerEventID   = "X-Mikan-Event-Id"
	headerTimestamp = "X-Mikan-Timestamp"
	headerSignature = "X-Mikan-Signature"
)

// signatureScheme leads the signature in its header ("v1=<hex>"), so a stronger scheme can
// one day come beside it without breaking a receiver that checks this one.
const signatureScheme = "v1"

const (
	secretPrefix = "whsec_"
	secretLen    = 40 // base62 characters after the prefix: ~238 bits
	hintLen      = 4  // characters of the secret's end told back, to tell secrets apart
)

// NewSecret is a fresh signing secret, whsec_ and 40 base62 characters. The admin is shown it
// once; the receiver keys its check with the whole string.
func NewSecret() string { return secretPrefix + secure.Token(secretLen) }

// SecretHint is the end of a secret, enough to tell which one a receiver holds after a
// rotation and nothing to guess the rest from.
func SecretHint(secret string) string {
	return secret[max(0, len(secret)-hintLen):]
}

// sign is the signature of a body sent at time at: the hex HMAC-SHA256, keyed by the secret,
// of the timestamp in unix seconds, a dot and the body.
func sign(secret string, at time.Time, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(strconv.FormatInt(at.Unix(), 10)))
	mac.Write([]byte{'.'})
	mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil))
}

// setHeaders puts on a request what a receiver reads of a delivery: which event, and the
// timestamp and signature of this attempt.
func setHeaders(h http.Header, secret string, ev events.Event, at time.Time, body []byte) {
	h.Set("Content-Type", "application/json")
	h.Set(headerEvent, ev.Type)
	h.Set(headerEventID, strconv.FormatInt(ev.ID, 10))
	h.Set(headerTimestamp, strconv.FormatInt(at.Unix(), 10))
	h.Set(headerSignature, signatureScheme+"="+sign(secret, at, body))
}
