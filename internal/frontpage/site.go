package frontpage

import (
	"bytes"
	"net/http"
	"regexp"
	"strings"
	"sync/atomic"
)

// The admin's own front page may be a site: a few pages, each at an address of its own,
// with the fonts and the pictures they use beside them. The panel keeps the files and sends
// them to its nodes; whoever shows the site answers each address with its file (ServeSite).
// The one-file copy of its first page (SetCustom) stays for where only one file fits: a
// protocol of a node that answers a stranger with its fallback page, and a node from before
// the sites.

// SiteFile is a file of the site: Path inside it ("index.html", "about.html",
// "fonts/inter-latin.woff2"), its content type and its bytes.
type SiteFile struct {
	Path string `json:"path"`
	Type string `json:"type"`
	Data []byte `json:"data"`
}

// MaxSite bounds the files of a site together, and MaxSiteFiles their number: the site
// travels to every node with the node's state.
const (
	MaxSite      = 2 << 20
	MaxSiteFiles = 64
)

// SitePageType is the content type of a page; SiteTypes are the types a file may have.
const SitePageType = "text/html"

var SiteTypes = []string{SitePageType, "font/woff2", "image/jpeg", "image/png", "image/webp"}

// sitePath is what a file's path may look like: lower-case names and folders, no dots at a
// start, nothing that climbs.
var sitePath = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]*(\.[a-z0-9]+)*(/[a-z0-9][a-z0-9_-]*(\.[a-z0-9]+)*){0,3}$`)

// SitePathOK says whether path may be a file of a site.
func SitePathOK(path string) bool {
	return len(path) <= 96 && sitePath.MatchString(path)
}

// SitePolicy is the Content-Security-Policy of a page of the admin's own site: no script at
// all and nothing from another address, so a page at the panel's address cannot reach the
// admin's session that lives there too.
const SitePolicy = "default-src 'none'; style-src 'unsafe-inline'; img-src 'self' data:; font-src 'self'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'"

// site is the files by path.
var site atomic.Pointer[map[string]SiteFile]

// SetSite sets the admin's own site: the panel at start and when it is saved, a node from
// its state. No files: there is none.
func SetSite(files []SiteFile) {
	if len(files) == 0 {
		site.Store(nil)
		return
	}
	m := make(map[string]SiteFile, len(files))
	for _, f := range files {
		f.Data = bytes.Clone(f.Data)
		m[f.Path] = f
	}
	site.Store(&m)
}

// Site is the files of the admin's own site, nil for none. The bytes are shared: a caller
// does not change them.
func Site() []SiteFile {
	m := site.Load()
	if m == nil {
		return nil
	}
	out := make([]SiteFile, 0, len(*m))
	for _, f := range *m {
		out = append(out, f)
	}
	return out
}

// HasSite says whether there are files to answer addresses with.
func HasSite() bool { return site.Load() != nil }

// siteFile is the file for the address a visitor asked for: "/" is the first page,
// "/about" (or "/about/") the page about.html, anything else the file at that path. A page
// is not answered at its file's own name, as a site without ".html" in its addresses does.
func siteFile(m map[string]SiteFile, urlPath string) (SiteFile, bool) {
	p := strings.Trim(urlPath, "/")
	if p == "" {
		f, ok := m["index.html"]
		return f, ok
	}
	if !SitePathOK(p) {
		return SiteFile{}, false
	}
	if f, ok := m[p+".html"]; ok && p != "index" && p != "404" {
		return f, true
	}
	if f, ok := m[p]; ok && f.Type != SitePageType {
		return f, true
	}
	return SiteFile{}, false
}

// ServeSite answers r with the admin's own site and says whether there is one. An address
// the site does not have gets its own "not found" page with 404, or the plain 404 when it
// has none; GET and HEAD only, as Write.
func ServeSite(w http.ResponseWriter, r *http.Request) bool {
	mp := site.Load()
	if mp == nil {
		return false
	}
	m := *mp
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		NotFound(w)
		return true
	}
	status := http.StatusOK
	f, ok := siteFile(m, r.URL.Path)
	if !ok {
		if f, ok = m["404.html"]; !ok {
			NotFound(w)
			return true
		}
		status = http.StatusNotFound
	}
	h := w.Header()
	if f.Type == SitePageType {
		h.Set("Content-Type", "text/html; charset=utf-8")
		h.Set("Content-Security-Policy", SitePolicy)
	} else {
		h.Set("Content-Type", f.Type)
		h.Set("Cache-Control", "public, max-age=86400")
	}
	h.Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	if r.Method != http.MethodHead {
		_, _ = w.Write(f.Data)
	}
	return true
}
