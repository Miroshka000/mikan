package api

import (
	"context"
	"net/url"
	"strings"

	"mikan/internal/panel/panelimport"
)

// LegacyLinks is what a user kept from the panel they were imported from: their old
// subscription links (settings.KeyLegacySubPath).
type LegacyLinks struct {
	Source string `json:"source" doc:"Из какой панели: marzban, pasarguard, remnawave или 3xui"`
	Active bool   `json:"active" doc:"Старые ссылки сейчас открываются: задан их путь, а для подписанных ещё и секрет старой панели"`
	URL    string `json:"url,omitempty" doc:"Ссылка целиком. Только у Remnawave и 3x-ui: токен Marzban и PasarGuard подписан, импорт его не видел и не хранит, а ссылки, что уже у людей, проверяются по подписи"`
}

// legacyLinks reads the old links of a user; nil when they had none. The address is the
// panel's own host with the old path: the old domain is not known here, and a link is
// given only where it is what the old panel handed out.
func (h *handlers) legacyLinks(ctx context.Context, userID int64) *LegacyLinks {
	rows, err := h.d.Store.Q.ListLegacySubTokensOf(ctx, userID)
	if err != nil || len(rows) == 0 {
		return nil
	}
	v := &LegacyLinks{Source: rows[0].Source}
	if h.d.Settings == nil {
		return v
	}
	path, verifier, err := panelimport.LoadLegacy(ctx, h.d.Settings)
	if err != nil || path == "" {
		return v
	}
	v.Active = true
	for _, r := range rows {
		// "name:…" and "id:…" are the keys a signed token is filed under, not tokens.
		if strings.Contains(r.Token, ":") {
			v.Active = v.Active && verifier.Secret != ""
			continue
		}
		if v.URL != "" || h.d.SubBase == nil {
			continue
		}
		if base, err := url.Parse(h.d.SubBase(ctx)); err == nil && base.Host != "" {
			base.Path, base.RawPath = "/"+path+"/"+r.Token, ""
			v.URL = base.String()
		}
	}
	return v
}
