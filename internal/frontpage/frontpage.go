// Package frontpage holds the built-in front pages, "maintenance" and a cats clicker: what a
// stranger sees at the panel's address, or on a node's own domain, instead of a bare 404.
package frontpage

import (
	"embed"
	"net/http"
	"slices"
	"sync"
	"sync/atomic"
)

//go:embed *.html
var pages embed.FS

// Templates are the names of the built-in pages.
var Templates = []string{"maintenance", "cats"}

// seed is the installation's own (SetSeed); nil: the pages as written.
var seed atomic.Pointer[string]

// made are the pages rendered so far, by name and seed.
var made sync.Map

// SetSeed makes the pages and the plain 404 the installation's own (vary.go): the panel
// sets its seed at start and sends it to its nodes with their state. "" is the pages as
// written.
func SetSeed(s string) {
	if s == "" {
		seed.Store(nil)
		return
	}
	seed.Store(&s)
}

func currentSeed() string {
	if s := seed.Load(); s != nil {
		return *s
	}
	return ""
}

// Page is the built-in page called name as the installation shows it; nil for none (""
// included). The bytes are shared: a caller does not change them.
func Page(name string) []byte {
	file := File(name)
	if file == "" {
		return nil
	}
	s := currentSeed()
	key := name + "\x00" + s
	if b, ok := made.Load(key); ok {
		return b.([]byte)
	}
	src, err := pages.ReadFile(file)
	if err != nil {
		return nil
	}
	b, err := render(name, src, s)
	if err != nil {
		return nil
	}
	made.Store(key, b)
	return b
}

// File is the file of the built-in page called name ("cats.html"), named from Templates and
// not from name, so a path made of it holds no name a caller sent; "" for none.
func File(name string) string {
	if i := slices.Index(Templates, name); i >= 0 {
		return Templates[i] + ".html"
	}
	return ""
}

// Serve answers "/" (and /index.html) with page, any other path with a plain 404, as a
// small site of one page does (Write).
func Serve(w http.ResponseWriter, r *http.Request, page []byte) {
	if r.URL.Path != "/" && r.URL.Path != "/index.html" {
		NotFound(w)
		return
	}
	Write(w, r, page)
}

// Write answers r with page: GET and HEAD get it, any other method the plain 404, as
// Citrus' fallback-page answers on a node: a site of one page takes no posts, and the panel's
// address, a node's domain and its protocols' ports tell no stranger apart by how they answer.
func Write(w http.ResponseWriter, r *http.Request, page []byte) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		NotFound(w)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	if r.Method != http.MethodHead {
		_, _ = w.Write(page)
	}
}

// NotFound is a plain 404: the one of the panel's server, its sites and the nodes' pages,
// in the installation's own words (SetSeed).
func NotFound(w http.ResponseWriter) {
	h := w.Header()
	h.Set("Content-Type", "text/plain; charset=utf-8")
	h.Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusNotFound)
	_, _ = w.Write([]byte(notFoundBody(currentSeed())))
}
