package panelimport

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"strconv"
	"strings"

	"mikan/internal/panel/secure"
)

// Verifier checks a subscription token Marzban or PasarGuard signed, with the old panel's
// secret (the jwt table of its database), and says whose it is. It takes what the old
// panel took, no more:
//
//	Marzban     b64url("username,ts") + the first 10 chars of b64url(sha256(b64 + secret)),
//	            or a JWT (HS256) with access "subscription"
//	PasarGuard  b64url("v3,<id>,<ts>") + "." + b64url(HMAC-SHA256(secret, b64)), the
//	            Marzban format (signature in base64 or hex, for users moved from Marzban),
//	            "v2,<id>,<ts>" in either, or the JWT
type Verifier struct {
	Kind   Kind
	Secret string
}

const jwtHS256Header = "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9."

// Who returns the key the importer filed the user under ("name:<username>" or
// "id:<PasarGuard id>") and when the token was made (unix seconds); ok is false for a
// token whose signature does not match.
func (v Verifier) Who(token string) (key string, issued int64, ok bool) {
	if v.Secret == "" || len(token) < 15 || len(token) > 1024 {
		return "", 0, false
	}
	if strings.HasPrefix(token, jwtHS256Header) {
		return v.jwt(token)
	}
	if v.Kind == PasarGuard && strings.Contains(token, ".") {
		i := strings.LastIndexByte(token, '.')
		data, sig := token[:i], token[i+1:]
		mac := hmac.New(sha256.New, []byte(v.Secret))
		mac.Write([]byte(data))
		if !secure.Equal(sig, base64.RawURLEncoding.EncodeToString(mac.Sum(nil))) {
			return "", 0, false
		}
		return payloadKey(data, true)
	}
	data, sig := token[:len(token)-10], token[len(token)-10:]
	sum := sha256.Sum256([]byte(data + v.Secret))
	good := secure.Equal(sig, base64.URLEncoding.EncodeToString(sum[:])[:10])
	if v.Kind == PasarGuard && !good {
		good = secure.Equal(sig, hex.EncodeToString(sum[:])[:10])
	}
	if !good {
		return "", 0, false
	}
	return payloadKey(data, v.Kind == PasarGuard)
}

// payloadKey reads "username,ts" (both panels) or "v2|v3,<id>,<ts>" (PasarGuard only).
func payloadKey(b64 string, ids bool) (string, int64, bool) {
	raw, err := base64.URLEncoding.DecodeString(b64 + strings.Repeat("=", (4-len(b64)%4)%4))
	if err != nil {
		return "", 0, false
	}
	parts := strings.Split(string(raw), ",")
	switch {
	case len(parts) == 2 && parts[0] != "":
		ts, err := strconv.ParseInt(parts[1], 10, 64)
		if err != nil {
			return "", 0, false
		}
		return "name:" + parts[0], ts, true
	case ids && len(parts) == 3 && (parts[0] == "v2" || parts[0] == "v3"):
		id, err := strconv.ParseInt(parts[1], 10, 64)
		if err != nil || id <= 0 {
			return "", 0, false
		}
		ts, err := strconv.ParseInt(parts[2], 10, 64)
		if err != nil {
			return "", 0, false
		}
		return "id:" + strconv.FormatInt(id, 10), ts, true
	}
	return "", 0, false
}

func (v Verifier) jwt(token string) (string, int64, bool) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return "", 0, false
	}
	mac := hmac.New(sha256.New, []byte(v.Secret))
	mac.Write([]byte(parts[0] + "." + parts[1]))
	if !secure.Equal(strings.TrimRight(parts[2], "="), base64.RawURLEncoding.EncodeToString(mac.Sum(nil))) {
		return "", 0, false
	}
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(parts[1], "="))
	if err != nil {
		return "", 0, false
	}
	var claims struct {
		Sub    string  `json:"sub"`
		Access string  `json:"access"`
		Iat    float64 `json:"iat"`
	}
	if json.Unmarshal(raw, &claims) != nil || claims.Access != "subscription" || claims.Sub == "" {
		return "", 0, false
	}
	return "name:" + claims.Sub, int64(claims.Iat), true
}
