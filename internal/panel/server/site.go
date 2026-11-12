package server

import (
	"io/fs"
	"net/http"
	"os"
	"path"
	"strings"

	"mikan/internal/frontpage"
)

// OwnSite serves dir, a site of the admin's own (/opt/mikan/data/panel/www on the host), on
// every path the panel does not own. In front of a REALITY self-steal the panel is what a
// stranger sees, and the same bare 404 on "/", robots.txt and favicon.ico is a site with no
// page at all (GitHub issue #67). A missing file gets dir/404.html with status 404 when
// there is one. Without a page of dir at "/", the template the admin chose (template(), ""
// for none) is that page. Without either, the panel answers as before: NotFound.
func OwnSite(dir string, template func() string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
		root, err := os.OpenRoot(dir) // no way out of dir, symlinks included
		if err != nil {
			builtIn(w, r, name, template)
			return
		}
		defer root.Close()
		fsys := root.FS()
		if !servable(fsys, name) && !servable(fsys, ".") && builtIn(w, r, name, template) {
			return
		}
		if !servable(fsys, name) {
			if !servable(fsys, "404.html") {
				NotFound(w)
				return
			}
			plain(w.Header())
			page, err := fs.ReadFile(fsys, "404.html")
			if err != nil {
				NotFound(w)
				return
			}
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write(page)
			return
		}
		plain(w.Header())
		http.FileServerFS(fsys).ServeHTTP(w, r)
	})
}

// builtIn answers "/" with the chosen template and says whether it did; anything else, or
// no template, is NotFound.
func builtIn(w http.ResponseWriter, r *http.Request, name string, template func() string) bool {
	// The admin's own site has a file for more than one address.
	if template != nil && path.Base(template()) == frontpage.Custom && frontpage.HasSite() {
		plain(w.Header())
		return frontpage.ServeSite(w, r)
	}
	var page []byte
	kind := ""
	if template != nil && (name == "" || name == "index.html") {
		if t := template(); t != "" {
			kind = path.Base(t)
			page = frontpage.Page(kind)
		}
	}
	if page == nil {
		NotFound(w)
		return true
	}
	plain(w.Header())
	if kind == frontpage.Custom {
		// The admin's own page stands at the panel's address, where the admin's session
		// lives: it runs no script and loads nothing from another address.
		w.Header().Set("Content-Security-Policy", frontpage.SitePolicy)
	}
	frontpage.Write(w, r, page)
	return true
}

// HasOwnSite says whether dir has a page at "/" of its own: the templates do not apply then.
func HasOwnSite(dir string) bool {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return false
	}
	defer root.Close()
	return servable(root.FS(), ".")
}

// servable: a file, or a directory with index.html; never a hidden one (.git, .env).
func servable(fsys fs.FS, name string) bool {
	if name == "" {
		name = "."
	}
	for seg := range strings.SplitSeq(name, "/") {
		if strings.HasPrefix(seg, ".") && seg != "." {
			return false
		}
	}
	st, err := fs.Stat(fsys, name)
	if err != nil {
		return false
	}
	if st.IsDir() {
		st, err = fs.Stat(fsys, path.Join(name, "index.html"))
		return err == nil && !st.IsDir()
	}
	return true
}

// plain drops the panel's own policy headers: an ordinary site does not send them, and
// they would tell the panel apart.
func plain(h http.Header) {
	for _, k := range []string{"Content-Security-Policy", "Cross-Origin-Opener-Policy", "Cross-Origin-Resource-Policy", "Permissions-Policy", "X-Frame-Options", "Referrer-Policy"} {
		h.Del(k)
	}
}
