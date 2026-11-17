package api

import (
	cryptohmac "crypto/hmac"
	"database/sql"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"strings"

	"mikan/internal/panel/auth"
	"mikan/internal/panel/store/db"
	"mikan/internal/subrelay"
)

// SubscriptionRelay exposes only the subscription surface to a revocable, dedicated
// key. It accepts no admin session and no general API key.
func SubscriptionRelay(d Deps, sub http.Handler) func(http.ResponseWriter, *http.Request) bool {
	signer := &relayProofSigner{db: d.Store.DB}
	return func(w http.ResponseWriter, r *http.Request) (served bool) {
		token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !ok || len(token) != len(apiKeyPrefix)+apiKeyLen {
			return false
		}
		diagnostic := r.Method == http.MethodHead && r.URL.Path == subrelay.AuthPath && r.URL.RawQuery == ""
		nonce := r.Header.Get(subrelay.RelayNonceHeader)
		diagnostic = diagnostic && nonce != "" && len(nonce) <= 128
		// Only a previously issued, token-bound ticket authenticates a revocation signal.
		mayDiagnose := false
		if proof := r.Header.Get(subrelay.RelayProofHeader); diagnostic && len(proof) == 43 {
			expected, err := signer.proof(r.Context(), token, false)
			if err != nil {
				http.Error(w, "subscription service unavailable", 503)
				return true
			}
			mayDiagnose = expected != "" && cryptohmac.Equal([]byte(expected), []byte(proof))
		}
		refused := func() bool {
			if !mayDiagnose {
				return false
			}
			w.Header().Set("Cache-Control", "no-store")
			w.Header().Set(subrelay.RelayAuthHeader, "revoked:"+nonce)
			w.WriteHeader(http.StatusNoContent)
			return true
		}
		// Without configured keys, strangers still see only the ordinary fallback.
		var enabled bool
		err := d.Store.DB.QueryRowContext(r.Context(), "SELECT EXISTS (SELECT 1 FROM api_keys WHERE scope = 'subscription' AND (expires_at IS NULL OR expires_at > $1))", d.Now().Unix()).Scan(&enabled)
		if err != nil {
			http.Error(w, "subscription service unavailable", 503)
			return true
		}
		if !enabled {
			return refused()
		}
		ip, _, _ := net.SplitHostPort(r.RemoteAddr)
		now := d.Now()
		limit := "sub-key:" + auth.ClientKey(ip)
		if ok, _ := d.IPLimit.Allowed(limit, now); !ok {
			return false
		}
		key, err := d.Store.Q.GetAPIKeyByHash(r.Context(), hashAPIKey(token))
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			d.Log.Error("subscription key lookup", "err", err)
			http.Error(w, "subscription service unavailable", 503)
			return true
		}
		if err != nil || (key.ExpiresAt.Valid && key.ExpiresAt.Int64 <= now.Unix()) {
			d.IPLimit.Fail(limit, now)
			return refused()
		}
		if key.Scope != "subscription" {
			return refused()
		}
		proof, err := signer.proof(r.Context(), token, true)
		if err != nil {
			d.Log.Error("subscription relay proof", "err", err)
			http.Error(w, "subscription service unavailable", 503)
			return true
		}
		w.Header().Set(subrelay.RelayProofHeader, proof)
		w.Header().Set("Cache-Control", "no-store")
		// Success metadata is visible only after authenticating a subscription key.
		w.Header().Set(subrelay.RelayAuthHeader, "1")
		if diagnostic {
			w.Header().Set(subrelay.RelayAuthHeader, nonce)
			w.WriteHeader(http.StatusNoContent)
			return true
		}
		p := r.URL.Path
		if !subrelay.Allowed(r.Method, p) {
			http.NotFound(w, r)
			return true
		}
		r = r.Clone(subrelay.WithRelay(r.Context()))
		r.URL.Path, r.URL.RawPath = p, ""
		r.Header.Del("Authorization")
		r.Header.Del("Cookie")
		r.Header.Del("Forwarded")
		r.Header.Del("X-Forwarded-For")
		r.Header.Del("X-Real-IP")
		if client, err := netip.ParseAddr(r.Header.Get("X-Mikan-Client-IP")); err == nil {
			r.RemoteAddr = net.JoinHostPort(client.String(), "0")
			r.Header.Set("X-Forwarded-For", client.String())
		}
		r.Header.Del("X-Mikan-Client-IP")
		ep, err := d.Settings.SubEndpoint(r.Context())
		if err != nil {
			http.Error(w, "subscription service unavailable", 503)
			return true
		}
		if ep.Host != "" {
			r.Host = ep.Host
			if ep.Port != 443 {
				r.Host = net.JoinHostPort(ep.Host, fmt.Sprint(ep.Port))
			}
		}
		_ = d.Store.Q.TouchAPIKey(r.Context(), db.TouchAPIKeyParams{
			ID: key.ID, LastUsedAt: sql.NullInt64{Int64: now.Unix(), Valid: true}, LastUsedAt_2: sql.NullInt64{Int64: now.Unix() - apiKeyTouchSecs, Valid: true}, LastIp: ip,
		})
		// Hide Hijacker so intentional response-rule drops reach ErrAbortHandler.
		// Translate that intent across the relay instead of treating it as an outage.
		defer func() {
			if value := recover(); value != nil {
				if value != http.ErrAbortHandler {
					panic(value)
				}
				served = true
				w.Header().Set(subrelay.RelayActionHeader, "drop")
				w.WriteHeader(http.StatusNoContent)
			}
		}()
		sub.ServeHTTP(relayWriter{w}, r)
		return true
	}
}

// No Unwrap or Hijack: the relay transports an intentional drop as metadata.
type relayWriter struct{ http.ResponseWriter }
