package acme

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/mail"
	"net/url"
	"strings"
	"time"

	"mikan/internal/panel/settings"
)

// The certificate authorities the panel orders from. Let's Encrypt needs nothing; ZeroSSL
// binds the account to an e-mail (its API hands out the EAB for it); Google Trust
// Services needs an EAB key the admin creates in Google Cloud.
const (
	CALetsEncrypt = "letsencrypt"
	CAZeroSSL     = "zerossl"
	CAGoogle      = "google"
)

// directories are the CAs' ACME directories.
var directories = map[string]string{
	CALetsEncrypt: "https://acme-v02.api.letsencrypt.org/directory",
	CAZeroSSL:     "https://acme.zerossl.com/v2/DV90",
	CAGoogle:      "https://dv.acme-v02.api.pki.goog/directory",
}

// ValidCA says whether s names a CA of the panel.
func ValidCA(s string) bool {
	_, ok := directories[s]
	return ok
}

// EffectiveCA is the CA a certificate for id is ordered from: the chosen one, except for an
// IP address, which only Let's Encrypt certifies (the shortlived profile).
func EffectiveCA(chosen, id string) string {
	if !ValidCA(chosen) || net.ParseIP(id) != nil {
		return CALetsEncrypt
	}
	return chosen
}

// ChosenCA is the CA the admin chose: Let's Encrypt unless set.
func ChosenCA(ctx context.Context, set *settings.Settings) (string, error) {
	v, err := set.String(ctx, settings.KeyACMECA)
	if err != nil || !ValidCA(v) {
		return CALetsEncrypt, err
	}
	return v, nil
}

// EAB is an external account binding: the key id and the HMAC key (base64url).
type EAB struct {
	KID  string `json:"eab_kid"`
	HMAC string `json:"eab_hmac_key"`
}

// ValidEABKey says whether s looks like an EAB HMAC key: base64url (padded or not), at
// least 32 bytes once decoded, as HS256 needs.
func ValidEABKey(s string) bool {
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(s, "="))
	return err == nil && len(raw) >= 32 && len(s) <= 512
}

// ValidEABKID: the key id the CA gave, printable, no spaces.
func ValidEABKID(s string) bool {
	if s == "" || len(s) > 256 {
		return false
	}
	for _, c := range s {
		if c <= ' ' || c > '~' {
			return false
		}
	}
	return true
}

// ValidEmail: a plain address, no display name.
func ValidEmail(s string) bool {
	a, err := mail.ParseAddress(s)
	return err == nil && a.Address == s && a.Name == "" && len(s) <= 254
}

// zeroSSLAPI hands out EAB credentials for an e-mail; a test points it at its own server.
var zeroSSLAPI = "https://api.zerossl.com/acme/eab-credentials-email"

// The ways getting an EAB fails, as codes the admin panel translates.
var (
	ErrEmailRequired = errors.New("zerossl_email_required")
	ErrZeroSSLEAB    = errors.New("zerossl_eab_failed")
	ErrGoogleEAB     = errors.New("google_eab_required")
)

// zeroSSLEAB asks ZeroSSL for EAB credentials bound to email (as acme.sh does): POST, form
// field email; the answer is {"success": true, "eab_kid": "...", "eab_hmac_key": "..."}, or
// {"success": false, "error": {"code", "type"}}.
func zeroSSLEAB(ctx context.Context, client *http.Client, email string) (EAB, error) {
	if email == "" {
		return EAB{}, ErrEmailRequired
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, zeroSSLAPI, strings.NewReader(url.Values{"email": {email}}.Encode()))
	if err != nil {
		return EAB{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := client.Do(req)
	if err != nil {
		return EAB{}, fmt.Errorf("%w: %v", ErrZeroSSLEAB, err)
	}
	defer resp.Body.Close()
	var out struct {
		EAB
		Error struct {
			Code int    `json:"code"`
			Type string `json:"type"`
		} `json:"error"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&out); err != nil {
		return EAB{}, fmt.Errorf("%w: HTTP %d, %v", ErrZeroSSLEAB, resp.StatusCode, err)
	}
	// acme.sh, which has used this API for years, reads only the two fields: they are what
	// counts, "success" may be missing.
	if out.KID == "" || out.HMAC == "" {
		return EAB{}, fmt.Errorf("%w: HTTP %d, %s (%d)", ErrZeroSSLEAB, resp.StatusCode, out.Error.Type, out.Error.Code)
	}
	return out.EAB, nil
}
