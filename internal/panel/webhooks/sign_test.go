package webhooks

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"mikan/internal/panel/events"
)

// tolerance is how far a receiver lets a timestamp be from its clock: the five minutes the
// documentation recommends.
const tolerance = 5 * time.Minute

// verify is a receiver written from the documentation and from nothing else: it reads the two
// headers, refuses a timestamp outside the window, recomputes the signature over "<timestamp>.<body>"
// and compares it in constant time. Every delivery the tests catch goes through it.
func verify(secret string, h http.Header, body []byte, now time.Time) error {
	ts, err := strconv.ParseInt(h.Get(headerTimestamp), 10, 64)
	if err != nil {
		return errors.New("no timestamp")
	}
	if d := now.Sub(time.Unix(ts, 0)); d > tolerance || d < -tolerance {
		return errors.New("timestamp outside the window")
	}
	given, ok := strings.CutPrefix(h.Get(headerSignature), "v1=")
	if !ok {
		return errors.New("no v1 signature")
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(h.Get(headerTimestamp) + "."))
	mac.Write(body)
	want := hex.EncodeToString(mac.Sum(nil))
	if !hmac.Equal([]byte(given), []byte(want)) {
		return errors.New("signature does not match")
	}
	return nil
}

// The signature of a fixed delivery, worked out by another implementation (Node's crypto):
// the same bytes are in the documentation for a receiver to check its own code against.
func TestSignKnownVector(t *testing.T) {
	const (
		secret = "whsec_Tq3nJw8Lz0KcVxY5bR7mHd2PfGaE9uNs4XoIe6Ct"
		body   = `{"id":42,"type":"payment.paid","at":"2026-01-01T00:00:00Z","user_id":7,"tg_id":555,"data":{"payment_id":9,"kind":"new","provider":"stars","amount":100,"currency":"XTR"}}`
		want   = "0b3ec8289ce0e77bc3c06beeaed636fb367c8cd4251e95418f3c8b6a9a429fe9"
	)
	at := time.Unix(1767225600, 0)
	if got := sign(secret, at, []byte(body)); got != want {
		t.Fatalf("sign = %s, want %s", got, want)
	}
	// The sub-second part of the clock is not in the signature: the header says seconds.
	if got := sign(secret, at.Add(999*time.Millisecond), []byte(body)); got != want {
		t.Fatalf("sign with a fraction of a second = %s", got)
	}
}

// What goes on the wire is what the documentation says: the type, the event's id, the
// timestamp of this attempt and "v1=" and the signature, and a receiver written from the
// documentation accepts it.
func TestSetHeaders(t *testing.T) {
	const secret = "whsec_abc"
	now := time.Unix(1_800_000_000, 0)
	ev := events.Event{ID: 17, Type: events.PaymentPaid}
	body := []byte(`{"id":17}`)
	h := http.Header{}
	setHeaders(h, secret, ev, now, body)
	for name, want := range map[string]string{
		"Content-Type":      "application/json",
		"X-Mikan-Event":     "payment.paid",
		"X-Mikan-Event-Id":  "17",
		"X-Mikan-Timestamp": "1800000000",
		"X-Mikan-Signature": "v1=" + sign(secret, now, body),
	} {
		if got := h.Get(name); got != want {
			t.Errorf("%s = %q, want %q", name, got, want)
		}
	}
	if err := verify(secret, h, body, now); err != nil {
		t.Fatalf("a receiver refuses what was sent: %v", err)
	}
}

// A receiver refuses a delivery that was altered or is replayed: another body, another
// timestamp, another secret, another scheme, a timestamp outside the window on either side.
func TestVerifyRefuses(t *testing.T) {
	const secret = "whsec_abc"
	now := time.Unix(1_800_000_000, 0)
	body := []byte(`{"id":17,"type":"payment.paid"}`)
	signed := func(at time.Time) http.Header {
		h := http.Header{}
		setHeaders(h, secret, events.Event{ID: 17, Type: events.PaymentPaid}, at, body)
		return h
	}
	if err := verify(secret, signed(now), body, now); err != nil {
		t.Fatalf("a fresh delivery: %v", err)
	}
	// At the edge of the window on both sides it is still accepted.
	for _, at := range []time.Time{now.Add(-tolerance), now.Add(tolerance)} {
		if err := verify(secret, signed(at), body, now); err != nil {
			t.Errorf("timestamp %v off: %v", at.Sub(now), err)
		}
	}
	for name, c := range map[string]struct {
		h    http.Header
		body []byte
		key  string
	}{
		"another body":        {signed(now), []byte(`{"id":18,"type":"payment.paid"}`), secret},
		"a body with a space": {signed(now), append(append([]byte{}, body...), ' '), secret},
		"another secret":      {signed(now), body, "whsec_abd"},
		"an empty secret":     {signed(now), body, ""},
		"too old":             {signed(now.Add(-tolerance - time.Second)), body, secret},
		"from the future":     {signed(now.Add(tolerance + time.Second)), body, secret},
		"a day old":           {signed(now.Add(-24 * time.Hour)), body, secret},
	} {
		if err := verify(c.key, c.h, c.body, now); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	// A timestamp moved to look fresh breaks the signature, which covers it.
	moved := signed(now.Add(-time.Hour))
	moved.Set(headerTimestamp, strconv.FormatInt(now.Unix(), 10))
	if err := verify(secret, moved, body, now); err == nil {
		t.Error("an old delivery with a new timestamp accepted")
	}
	// Another scheme, a bare signature and a missing one.
	for _, value := range []string{"v2=" + sign(secret, now, body), sign(secret, now, body), ""} {
		h := signed(now)
		h.Set(headerSignature, value)
		if err := verify(secret, h, body, now); err == nil {
			t.Errorf("signature header %q accepted", value)
		}
	}
}

// A secret is whsec_ and 40 characters of base62, a new one each time, and its hint is its
// last four characters.
func TestNewSecret(t *testing.T) {
	seen := map[string]bool{}
	for range 50 {
		s := NewSecret()
		rest, ok := strings.CutPrefix(s, "whsec_")
		if !ok || len(rest) != 40 || strings.Trim(rest, "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz") != "" {
			t.Fatalf("secret %q", s)
		}
		if seen[s] {
			t.Fatalf("%q made twice", s)
		}
		seen[s] = true
		if SecretHint(s) != s[len(s)-4:] {
			t.Fatalf("hint %q of %q", SecretHint(s), s)
		}
	}
	if SecretHint("abc") != "abc" || SecretHint("") != "" {
		t.Error("the hint of a short secret")
	}
}
