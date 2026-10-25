package subsrc

import (
	"net/http"
	"path"
	"strings"

	"mikan/internal/panel/panelimport"
	"mikan/internal/panel/server"
	"mikan/internal/panel/store/db"
	"mikan/internal/sub/serve"
)

// Legacy serves the subscription links of the panel users were imported from: the old
// token leads to the user (legacy_sub_tokens), and the request goes on as one to the
// user's own link. The router strips the old path, so the path here is /<token>[/<rest>].
//
// After the token Marzban and PasarGuard put a client type (/sub/<token>/clash), and
// Remnawave does the same (/api/sub/<token>/mihomo): the types mikan has a format for
// become ?format=, the rest is left to the User-Agent, as on mikan's own links.
func (h *Handler) Legacy() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			server.NotFound(w)
			return
		}
		token, rest, _ := strings.Cut(strings.TrimPrefix(path.Clean(r.URL.Path), "/"), "/")
		if token == "" || len(token) > panelimport.MaxToken {
			server.NotFound(w)
			return
		}
		u, ok := h.legacyUser(r, token)
		if !ok {
			server.NotFound(w)
			return
		}
		r2 := r.Clone(r.Context())
		r2.URL.Path = "/" + u.SubToken
		r2.URL.RawPath = ""
		switch f := legacyFormat(rest); {
		case rest == "info":
			r2.URL.Path += "/info"
		case f != "" && r.URL.Query().Get("format") == "":
			q := r2.URL.Query()
			q.Set("format", f)
			r2.URL.RawQuery = q.Encode()
		}
		h.ServeHTTP(w, r2)
	})
}

// legacyUser finds whose an old link is (panelimport.Verifier.User; the bot asks the same).
func (h *Handler) legacyUser(r *http.Request, token string) (db.User, bool) {
	// Without the settings the tokens kept as they are can still be told, the signed ones not.
	cfg, _ := h.cfg(r.Context())
	return cfg.Legacy.User(r.Context(), h.st.Q, token)
}

// legacyFormat is mikan's format for an old panel's client type, "" when mikan has none
// (the User-Agent decides then).
func legacyFormat(clientType string) string {
	switch strings.ToLower(clientType) {
	case "clash", "clash-meta", "clash_meta", "clashmeta", "mihomo", "stash":
		return serve.FormatClash
	case "v2ray", "links", "links-base64", "links_base64", "base64", "xray-base64", "xray_base64":
		return serve.FormatURI
	}
	return ""
}
