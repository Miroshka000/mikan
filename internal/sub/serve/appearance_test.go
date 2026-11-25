package serve

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"mikan/internal/sub/apps"
)

// A config may set how often the apps fetch again and more headers of every answer; a
// response rule's headers go over them, and none of them keeps the answer in a cache.
func TestConfigIntervalAndHeaders(t *testing.T) {
	sub := newSub(t, apps.Policy{NoBuiltin: true, Rules: []apps.Rule{rule("koala", "koala", apps.Action{Headers: []apps.Header{{Name: "X-Who", Value: "rule"}}})}})
	if w := fetch(t, sub, "mihomo/1.19.31"); w.Header().Get("Profile-Update-Interval") != "1" || w.Header().Get("X-Who") != "" {
		t.Fatalf("by default: %v", w.Header())
	}
	sub.cfg.UpdateHours = 6
	sub.cfg.Headers = []apps.Header{{Name: "X-Who", Value: "group"}, {Name: "X-Group", Value: "vip"}, {Name: "Cache-Control", Value: "max-age=600"}}
	w := fetch(t, sub, "mihomo/1.19.31")
	if hd := w.Header(); hd.Get("Profile-Update-Interval") != "6" || hd.Get("X-Who") != "group" || hd.Get("X-Group") != "vip" || hd.Get("Cache-Control") != "no-store" {
		t.Fatalf("the config's: %v", hd)
	}
	if hd := fetch(t, sub, "Koala-Clash/1.1.0").Header(); hd.Get("X-Who") != "rule" || hd.Get("X-Group") != "vip" {
		t.Fatalf("a rule's over the config's: %v", hd)
	}
}

// The subscription page is told which of the source's pages it is.
func TestConfigNamesThePage(t *testing.T) {
	sub := newSub(t, apps.Policy{NoBuiltin: true})
	sub.cfg.Page = 7
	h := handlerOf(sub)
	var page int64 = -1
	h.SetPage(func(w http.ResponseWriter, _ *http.Request, cfg Config) {
		page = cfg.Page
		w.WriteHeader(http.StatusOK)
	})
	r := httptest.NewRequest(http.MethodGet, "/"+testToken+"?format=html", nil)
	h.Serve(httptest.NewRecorder(), r, testToken)
	if page != 7 {
		t.Fatalf("the page was told %d", page)
	}
}
