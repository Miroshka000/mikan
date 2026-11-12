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

// The admin's own front page (frontpage.Custom): a site made in the panel's builder, kept
// whole in the settings and sent to the nodes with their state. The builder works in the
// browser: the panel keeps what it made the site of (the document), the site's files and its
// first page in one file, and gives the builder a seed of this installation, so that what it
// offers by default differs from what any other installation offers.

type sitePageOutput struct {
	Body struct {
		Doc   string `json:"doc" doc:"Документ конструктора (JSON), как он был сохранён; пусто: своей страницы ещё нет"`
		Size  int    `json:"size" doc:"Размер сохранённого сайта в байтах (все файлы); 0: его нет"`
		Max   int    `json:"max" doc:"Наибольший размер всех файлов сайта в байтах"`
		Pages int    `json:"pages" doc:"Сколько страниц у сохранённого сайта"`
		Seed  string `json:"seed" doc:"Зерно установки для конструктора: от него зависят сайты и блоки по умолчанию"`
	}
}

// SiteFileBody is a file of the site as the builder sends it.
type SiteFileBody struct {
	Path string `json:"path" maxLength:"96" doc:"Путь внутри сайта: index.html, about.html, fonts/inter-latin.woff2"`
	Type string `json:"type" enum:"text/html,font/woff2,image/jpeg,image/png,image/webp"`
	Data []byte `json:"data" doc:"Содержимое файла (base64)"`
}

type putSitePageInput struct {
	Body struct {
		Doc   string         `json:"doc" maxLength:"4194304" doc:"Документ конструктора (JSON)"`
		HTML  string         `json:"html" minLength:"64" maxLength:"1048576" doc:"Первая страница одним файлом без внешних запросов: её показывают протоколы нод и ноды старых версий"`
		Files []SiteFileBody `json:"files,omitempty" maxItems:"64" doc:"Файлы сайта: страницы, шрифты, картинки"`
	}
}

func (h *handlers) registerSitePage() {
	huma.Register(h.api, huma.Operation{OperationID: "get-site-page", Method: http.MethodGet, Path: "/api/v1/settings/site-page", Summary: "Своя заглушка: документ конструктора", Tags: []string{"settings"}}, h.getSitePage)
	huma.Register(h.api, huma.Operation{OperationID: "put-site-page", Method: http.MethodPut, Path: "/api/v1/settings/site-page", Summary: "Сохранить свою заглушку", Tags: []string{"settings"}, MaxBodyBytes: 12 << 20}, h.putSitePage)
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
	size, pages := len(frontpage.Page(frontpage.Custom)), 0
	if files := frontpage.Site(); len(files) > 0 {
		size = 0
		for _, f := range files {
			size += len(f.Data)
			if f.Type == frontpage.SitePageType && f.Path != "404.html" {
				pages++
			}
		}
	} else if size > 0 {
		pages = 1
	}
	// The builder's seed is made of the installation's and is not it: the built-in pages
	// keep theirs to the panel.
	sum := sha256.Sum256([]byte("builder\x00" + seed))
	out.Body.Doc, out.Body.Size, out.Body.Max, out.Body.Pages, out.Body.Seed = doc, size, frontpage.MaxSite, pages, hex.EncodeToString(sum[:16])
	return out, nil
}

func (h *handlers) getSitePage(ctx context.Context, _ *struct{}) (*sitePageOutput, error) {
	return h.sitePage(ctx)
}

// isPage says whether text reads as an HTML document.
func isPage(text string) bool {
	text = strings.TrimSpace(text)
	return strings.HasPrefix(strings.ToLower(text[:min(len(text), 15)]), "<!doctype html")
}

// siteFiles checks the files of a site: paths a site may have, each once, pages that are
// pages, a first page among them, and all of it within the size a node's state carries.
func siteFiles(in []SiteFileBody) ([]frontpage.SiteFile, string) {
	if len(in) == 0 {
		return nil, ""
	}
	out := make([]frontpage.SiteFile, 0, len(in))
	seen := map[string]bool{}
	total, first := 0, false
	for _, f := range in {
		switch {
		case !frontpage.SitePathOK(f.Path) || seen[f.Path]:
			return nil, "page_bad_path"
		case (f.Type == frontpage.SitePageType) != strings.HasSuffix(f.Path, ".html"):
			return nil, "page_bad_path"
		case f.Type == frontpage.SitePageType && !isPage(string(f.Data)):
			return nil, "page_not_html"
		case len(f.Data) == 0:
			return nil, "page_bad_path"
		}
		seen[f.Path] = true
		first = first || f.Path == "index.html"
		if total += len(f.Data); total > frontpage.MaxSite {
			return nil, "page_too_large"
		}
		out = append(out, frontpage.SiteFile{Path: f.Path, Type: f.Type, Data: f.Data})
	}
	if !first {
		return nil, "page_bad_path"
	}
	return out, ""
}

// putSitePage saves the site. It is what strangers get at the panel's address, where the
// admin's own session lives, so it is the admin's alone to write (no API key, no operator),
// and the panel serves its pages with a policy that runs no script (frontpage.SitePolicy).
func (h *handlers) putSitePage(ctx context.Context, in *putSitePageInput) (*sitePageOutput, error) {
	if err := requireSession(ctx, "html"); err != nil {
		return nil, err
	}
	page := strings.TrimSpace(in.Body.HTML)
	if len(page) > frontpage.MaxCustom {
		return nil, fieldErr("body.html", "page_too_large")
	}
	if !isPage(page) {
		return nil, fieldErr("body.html", "page_not_html")
	}
	if in.Body.Doc != "" && !json.Valid([]byte(in.Body.Doc)) {
		return nil, fieldErr("body.doc", "page_bad_doc")
	}
	files, code := siteFiles(in.Body.Files)
	if code != "" {
		return nil, fieldErr("body.files", code)
	}
	if err := settings.Set(ctx, h.d.Settings, settings.KeySiteDoc, in.Body.Doc); err != nil {
		return nil, err
	}
	if err := settings.Set(ctx, h.d.Settings, settings.KeySiteHTML, page); err != nil {
		return nil, err
	}
	raw := ""
	if len(files) > 0 {
		b, err := json.Marshal(files)
		if err != nil {
			return nil, err
		}
		raw = string(b)
	}
	if err := settings.Set(ctx, h.d.Settings, settings.KeySiteFiles, raw); err != nil {
		return nil, err
	}
	frontpage.SetCustom([]byte(page))
	frontpage.SetSite(files)
	// The nodes that show the site get the new one.
	h.nodesChanged()
	h.d.Changes.SlotsChanged()
	h.audit(ctx, sessionOf(ctx).AdminID, "settings.site_page", "settings", "site_page", map[string]any{"size": len(page), "files": len(files)})
	return h.sitePage(ctx)
}
