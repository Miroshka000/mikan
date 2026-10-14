package subs

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"mikan/internal/panel/server"
	"mikan/internal/panel/subpage"
)

// headPage is a page that takes extra markup for its <head> (server.SPA).
type headPage interface {
	ServeHead(w http.ResponseWriter, r *http.Request, head []byte)
}

// servePage sends the subscription page with what the admin made of it (subpage): its data,
// the link preview and the admin's CSS. When that cannot be read the page goes as built,
// in its default look, rather than not at all.
func (h *Handler) servePage(w http.ResponseWriter, r *http.Request, cfg Config) {
	hp, ok := h.page.(headPage)
	if !ok || h.pages == nil {
		h.page.ServeHTTP(w, r)
		return
	}
	pub, err := h.pages.Public(r.Context(), cfg.Brand, cfg.App.Accent)
	if err != nil {
		h.warn("page", "subscription page: its settings were not read, the default look is shown", "err", err)
		h.page.ServeHTTP(w, r)
		return
	}
	if pub.RemoteImages() {
		// An instruction may show images from other sites, by https only.
		hd := w.Header()
		hd.Set("Content-Security-Policy", strings.Replace(hd.Get("Content-Security-Policy"), "img-src 'self' data: blob:", "img-src 'self' data: blob: https:", 1))
	}
	hp.ServeHead(w, r, pub.Head(subRootURL(r, cfg)))
}

// subRootURL is the subscription path's absolute address: the panel's own when it has
// one, or the page's address without its last part (the token or "tg").
func subRootURL(r *http.Request, cfg Config) string {
	if cfg.SubBase != "" {
		return strings.TrimSuffix(cfg.SubBase, "/")
	}
	page := PageURL(r)
	if i := strings.LastIndex(page, "/"); i > len("https://") {
		return page[:i]
	}
	return page
}

// image sends an image the admin uploaded for the page. The type is the one sniffed when
// it was uploaded; a link with its current hash is kept by the browser for good.
func (h *Handler) image(w http.ResponseWriter, r *http.Request, name string) {
	if subpage.AssetLimit(name) == 0 {
		server.NotFound(w)
		return
	}
	a, ok, err := h.pages.Image(r.Context(), name)
	switch {
	case err != nil:
		internalError(w)
		return
	case !ok:
		server.NotFound(w)
		return
	}
	hd := w.Header()
	hd.Set("Content-Type", a.ContentType)
	hd.Set("X-Content-Type-Options", "nosniff")
	// The apps that show the logo (app branding) fetch it from elsewhere than this page.
	hd.Set("Cross-Origin-Resource-Policy", "cross-origin")
	hd.Set("Content-Security-Policy", "default-src 'none'; sandbox")
	hd.Set("ETag", `"`+a.Hash+`"`)
	if r.URL.Query().Get("v") == a.Hash {
		hd.Set("Cache-Control", "public, max-age=31536000, immutable")
	} else {
		hd.Set("Cache-Control", "no-cache")
	}
	http.ServeContent(w, r, "", time.Unix(a.UpdatedAt, 0), bytes.NewReader(a.Data))
}

// doc sends a published instruction for the page to open.
func (h *Handler) doc(w http.ResponseWriter, r *http.Request, rest string) {
	id, err := strconv.ParseInt(rest, 10, 64)
	if err != nil || id <= 0 {
		server.NotFound(w)
		return
	}
	d, ok, err := h.pages.Doc(r.Context(), id)
	switch {
	case err != nil:
		internalError(w)
		return
	case !ok:
		server.NotFound(w)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-cache")
	_ = json.NewEncoder(w).Encode(d)
}

// linkPreviewBots are the User-Agents of the messengers and sites that fetch a pasted link
// to show its preview.
var linkPreviewBots = []string{"telegrambot", "whatsapp", "facebookexternalhit", "twitterbot", "slackbot", "discordbot", "vkshare", "skypeuripreview", "linkedinbot", "viber"}

// LinkPreview says whether the request is a messenger building a link's preview.
func LinkPreview(userAgent string) bool {
	ua := strings.ToLower(userAgent)
	for _, b := range linkPreviewBots {
		if strings.Contains(ua, b) {
			return true
		}
	}
	return false
}
