// Package subrelay is the database-free subscription edge. It deliberately has no
// dependency on the panel or its configuration and database packages.
package subrelay

import (
	"net/http"
	"path"
	"regexp"
	"strings"
)

var subscriber = regexp.MustCompile(`^/[A-Za-z0-9]{24}(/info|/docs/[0-9]+|/devices/[0-9]+/(name|unbind))?$`)

// Static identifies public assets which can be cached without skipping subscriber or
// device authorization. Profiles and personalized pages always reach the panel.
func Static(p string) bool {
	return strings.HasPrefix(p, "/assets/") || p == "/favicon.png" || p == "/apple-touch-icon.png"
}

// Allowed is shared by both ends: no arbitrary proxying or admin paths, including via
// escaped or non-canonical paths. User actions retain the panel's own authorization.
func Allowed(method, p string) bool {
	if path.Clean(p) != p || strings.ContainsAny(p, "\\%") {
		return false
	}
	if method == http.MethodGet || method == http.MethodHead {
		return p == "/tg" || p == "/shop" || Static(p) || subscriber.MatchString(p) && !strings.Contains(p, "/devices/") || strings.HasPrefix(p, "/brand/")
	}
	return method == http.MethodPost && (subscriber.MatchString(p) && strings.Contains(p, "/devices/") || p == "/tg/session" || p == "/tg/reissue" || p == "/tg/shop" || p == "/tg/pay" || p == "/tg/free" || p == "/tg/promo" || p == "/tg/promo-history" || p == "/shop/offers" || p == "/shop/pay" || p == "/shop/status")
}
