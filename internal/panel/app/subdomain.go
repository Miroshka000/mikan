package app

import (
	"net"
	"net/http"
	"strings"

	"mikan/internal/panel/tlscert"
)

// subDomainRouter answers requests to the subscription domain, the one it is now or one it
// was before it changed, with the subscriptions alone: the admin panel, its API and the site
// stay on the panel's address, so the domain handed to every client leads to nothing else.
type subDomainRouter struct {
	names    *tlscert.BySNI
	sub, all http.Handler
}

func (h subDomainRouter) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if h.names.IsSub(requestHost(r.Host)) {
		h.sub.ServeHTTP(w, r)
		return
	}
	h.all.ServeHTTP(w, r)
}

// requestHost is the name of a Host header, without its port and final dot.
func requestHost(hostport string) string {
	host := hostport
	if h, _, err := net.SplitHostPort(hostport); err == nil {
		host = h
	}
	return strings.TrimSuffix(host, ".")
}
