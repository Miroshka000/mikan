package api

import (
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
	return func(w http.ResponseWriter, r *http.Request) (served bool) {
		// With no configured keys, the relay has no visible HTTP surface.
		var enabled bool
		err := d.Store.DB.QueryRowContext(r.Context(), "SELECT EXISTS (SELECT 1 FROM api_keys WHERE scope = 'subscription' AND (expires_at IS NULL OR expires_at > $1))", d.Now().Unix()).Scan(&enabled)
		if err != nil {
			http.Error(w, "subscription service unavailable", 503)
			return true
		}
		if !enabled {
			return false
		}
		ip, _, _ := net.SplitHostPort(r.RemoteAddr)
		now := d.Now()
		limit := "sub-key:" + auth.ClientKey(ip)
		if ok, _ := d.IPLimit.Allowed(limit, now); !ok {
			return false
		}
		token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !ok || len(token) != len(apiKeyPrefix)+apiKeyLen {
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
			w.Header().Set(subrelay.RelayErrorHeader, "credential")
			return false
		}
		if key.Scope != "subscription" {
			w.Header().Set(subrelay.RelayErrorHeader, "credential")
			return false
		}
		w.Header().Set("Cache-Control", "no-store")
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
