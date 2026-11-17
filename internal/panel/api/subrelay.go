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
func SubscriptionRelay(d Deps, sub http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		ip, _, _ := net.SplitHostPort(r.RemoteAddr)
		now := d.Now()
		limit := "sub-key:" + auth.ClientKey(ip)
		if ok, _ := d.IPLimit.Allowed(limit, now); !ok {
			http.Error(w, "rate_limited", 429)
			return
		}
		token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !ok || len(token) != len(apiKeyPrefix)+apiKeyLen {
			http.Error(w, "unauthorized", 401)
			return
		}
		key, err := d.Store.Q.GetAPIKeyByHash(r.Context(), hashAPIKey(token))
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			d.Log.Error("subscription key lookup", "err", err)
			http.Error(w, "subscription service unavailable", 500)
			return
		}
		if err != nil || (key.ExpiresAt.Valid && key.ExpiresAt.Int64 <= now.Unix()) {
			d.IPLimit.Fail(limit, now)
			http.Error(w, "unauthorized", 401)
			return
		}
		if key.Scope != "subscription" {
			http.Error(w, "subscription_only_key", 403)
			return
		}
		p := r.URL.Path
		if !subrelay.Allowed(r.Method, p) {
			http.NotFound(w, r)
			return
		}
		r = r.Clone(r.Context())
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
			http.Error(w, "internal error", 500)
			return
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
		sub.ServeHTTP(w, r)
	})
}
