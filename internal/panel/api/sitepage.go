package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/danielgtaylor/huma/v2"

	"mikan/internal/frontpage"
	"mikan/internal/panel/settings"
)

// The admin's own front page (frontpage.Custom): made in the panel's builder, kept whole
// in the settings and sent to the nodes with their state. The builder works in the browser:
// the panel keeps what it made the page of (the document) and the page itself, and gives
// the builder a seed of this installation, so that what it offers by default differs from
// what any other installation offers.

type sitePageOutput struct {
	Body struct {
		Doc  string `json:"doc" doc:"Документ конструктора (JSON), как он был сохранён; пусто: своей страницы ещё нет"`
		Size int    `json:"size" doc:"Размер сохранённой страницы в байтах; 0: её нет"`
		Max  int    `json:"max" doc:"Наибольший размер страницы в байтах"`
		Seed string `json:"seed" doc:"Зерно установки для конструктора: от него зависят страницы и блоки по умолчанию"`
	}
}

type putSitePageInput struct {
	Body struct {
		Doc  string `json:"doc" maxLength:"204800" doc:"Документ конструктора (JSON)"`
		HTML string `json:"html" minLength:"64" maxLength:"204800" doc:"Готовая страница: один HTML-файл без внешних запросов"`
	}
}

func (h *handlers) registerSitePage() {
	huma.Register(h.api, huma.Operation{OperationID: "get-site-page", Method: http.MethodGet, Path: "/api/v1/settings/site-page", Summary: "Своя заглушка: документ конструктора", Tags: []string{"settings"}}, h.getSitePage)
	huma.Register(h.api, huma.Operation{OperationID: "put-site-page", Method: http.MethodPut, Path: "/api/v1/settings/site-page", Summary: "Сохранить свою заглушку", Tags: []string{"settings"}}, h.putSitePage)
}

func (h *handlers) sitePage(ctx context.Context) (*sitePageOutput, error) {
	out := &sitePageOutput{}
	doc, err := h.d.Settings.String(ctx, settings.KeySiteDoc)
	if err != nil {
		return nil, err
	}
	seed, err := h.d.Settings.String(ctx, settings.KeyFrontSeed)
	if err != nil {
		return nil, err
	}
	// The builder's seed is made of the installation's and is not it: the built-in pages
	// keep theirs to the panel.
	sum := sha256.Sum256([]byte("builder\x00" + seed))
	out.Body.Doc, out.Body.Size, out.Body.Max, out.Body.Seed = doc, len(frontpage.Page(frontpage.Custom)), frontpage.MaxCustom, hex.EncodeToString(sum[:16])
	return out, nil
}

func (h *handlers) getSitePage(ctx context.Context, _ *struct{}) (*sitePageOutput, error) {
	return h.sitePage(ctx)
}

// putSitePage saves the page. It is what strangers get at the panel's address, where the
// admin's own session lives, so it is the admin's alone to write (no API key, no operator),
// and the panel serves it with an origin of its own (server.CustomPageCSP).
func (h *handlers) putSitePage(ctx context.Context, in *putSitePageInput) (*sitePageOutput, error) {
	if err := requireSession(ctx, "html"); err != nil {
		return nil, err
	}
	page := strings.TrimSpace(in.Body.HTML)
	if len(page) > frontpage.MaxCustom {
		return nil, fieldErr("body.html", "page_too_large")
	}
	if head := strings.ToLower(page[:min(len(page), 15)]); !strings.HasPrefix(head, "<!doctype html") {
		return nil, fieldErr("body.html", "page_not_html")
	}
	if in.Body.Doc != "" && !json.Valid([]byte(in.Body.Doc)) {
		return nil, fieldErr("body.doc", "page_bad_doc")
	}
	if err := settings.Set(ctx, h.d.Settings, settings.KeySiteDoc, in.Body.Doc); err != nil {
		return nil, err
	}
	if err := settings.Set(ctx, h.d.Settings, settings.KeySiteHTML, page); err != nil {
		return nil, err
	}
	frontpage.SetCustom([]byte(page))
	// The nodes that show the page get the new one.
	h.nodesChanged()
	h.d.Changes.SlotsChanged()
	h.audit(ctx, sessionOf(ctx).AdminID, "settings.site_page", "settings", "site_page", map[string]any{"size": len(page)})
	return h.sitePage(ctx)
}
