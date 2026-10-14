package app

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"mikan/internal/panel/domain"
	"mikan/internal/panel/server"
	"mikan/internal/panel/settings"
	"mikan/internal/panel/subpage"
)

// subPageUser makes the harness's panel addressable and gives it a user to open the page of.
func subPageUser(t *testing.T, h *harness) string {
	t.Helper()
	ctx := context.Background()
	if err := domain.Seed(ctx, h.st, h.now); err != nil {
		t.Fatal(err)
	}
	set := settings.New(h.st.Q)
	for k, v := range map[string]any{settings.KeyPublicHost: "203.0.113.10", settings.KeyPanelPort: 21355} {
		if err := settings.Set(ctx, set, k, v); err != nil {
			t.Fatal(err)
		}
	}
	clock := func() time.Time { return h.now }
	tariffs, _ := h.st.Q.ListTariffs(ctx)
	u, err := domain.NewUsers(h.st, domain.NewPool(h.st, clock), noChanges{}, clock).Create(ctx, domain.CreateInput{Name: "Аня", TariffID: tariffs[1].ID})
	if err != nil {
		t.Fatal(err)
	}
	return "/" + subPath + "/" + u.SubToken
}

var browser = map[string]string{"Accept": "text/html,application/xhtml+xml", "User-Agent": "Mozilla/5.0 (iPhone; CPU iPhone OS 18_0 like Mac OS X)"}

// pageData is the page's data block in the HTML.
func pageData(t *testing.T, body []byte) subpage.Public {
	t.Helper()
	start := bytes.Index(body, []byte(`<script type="application/json" id="mikan-page">`))
	if start < 0 {
		t.Fatalf("no page data in %s", body)
	}
	start += len(`<script type="application/json" id="mikan-page">`)
	end := start + bytes.Index(body[start:], []byte("</script>"))
	var p subpage.Public
	if err := json.Unmarshal(body[start:end], &p); err != nil {
		t.Fatal(err)
	}
	return p
}

// Nothing configured: the subscription page is the page it always was. Its data is the
// default document, nothing else goes into its head, and its CSP is the panel's own.
func TestSubPageDefaultIsUnchanged(t *testing.T) {
	h := newHarness(t)
	sub := subPageUser(t, h)
	resp, body := h.do(http.MethodGet, sub, nil, browser)
	if resp.StatusCode != http.StatusOK || !bytes.Contains(body, []byte("<body>sub</body>")) {
		t.Fatalf("page: %d %s", resp.StatusCode, body)
	}
	p := pageData(t, body)
	// The page gets the blocks that are on: the default's own, in its order.
	def := subpage.Default()
	def.Blocks = slices.DeleteFunc(def.Blocks, func(b subpage.PageBlock) bool { return !b.On })
	if !reflect.DeepEqual(p.Config, def) || p.Brand != "VPN" || p.Accent != "" || p.Logo != "" || p.Background != "" || len(p.Docs) != 0 {
		t.Fatalf("default page data: %+v", p)
	}
	for _, extra := range []string{"<style", "og:", "<link"} {
		if bytes.Contains(body, []byte(extra)) {
			t.Errorf("the default page got %s: %s", extra, body)
		}
	}
	want := http.Header{}
	server.SecurityHeaders(want)
	if got := resp.Header.Get("Content-Security-Policy"); got != want.Get("Content-Security-Policy") {
		t.Errorf("CSP changed: %s", got)
	}
	// The Mini App is the same page.
	if resp, body := h.do(http.MethodGet, "/"+subPath+"/tg", nil, nil); resp.StatusCode != http.StatusOK || !reflect.DeepEqual(pageData(t, body).Config, def) {
		t.Fatalf("mini app page: %d", resp.StatusCode)
	}
}

func pngBytes(n int) []byte { return append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, n)...) }

func TestSubPageEditorEndToEnd(t *testing.T) {
	h := newHarness(t)
	sub := subPageUser(t, h)
	if resp, _ := h.login(password, ""); resp.StatusCode != http.StatusOK {
		t.Fatal("login")
	}
	api := "/" + adminPath + "/api/v1"
	csrf := map[string]string{"X-CSRF-Token": h.csrf}

	// Images: PNG, JPEG, WebP only, by what the bytes are; within the limits.
	upload := func(name string, data []byte) (*http.Response, []byte) {
		return h.do(http.MethodPut, api+"/sub-page/images/"+name, map[string]string{"data": base64.StdEncoding.EncodeToString(data)}, csrf)
	}
	for what, c := range map[string]struct {
		name string
		data []byte
		code string
	}{
		"svg":       {"logo", []byte(`<svg xmlns="http://www.w3.org/2000/svg" onload="alert(1)"/>`), "image_type"},
		"html":      {"logo", []byte("<html><script>alert(1)</script></html>"), "image_type"},
		"too big":   {"logo", pngBytes(512 << 10), "image_too_big"},
		"bad name":  {"favicon", pngBytes(10), ""},
		"not b64":   {"logo", nil, "image_type"},
		"bg as svg": {"background", []byte(`<?xml version="1.0"?><svg/>`), "image_type"},
	} {
		var resp *http.Response
		var out []byte
		if what == "not b64" {
			resp, out = h.do(http.MethodPut, api+"/sub-page/images/logo", map[string]string{"data": "%%%"}, csrf)
		} else {
			resp, out = upload(c.name, c.data)
		}
		if resp.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(string(out), c.code) {
			t.Errorf("%s: %d %s", what, resp.StatusCode, out)
		}
	}
	logo := pngBytes(100)
	resp, out := upload("logo", logo)
	var asset subpage.PageAsset
	if resp.StatusCode != http.StatusOK || json.Unmarshal(out, &asset) != nil || asset.Type != "image/png" || asset.Size != int64(len(logo)) {
		t.Fatalf("logo: %d %s", resp.StatusCode, out)
	}

	// A save is refused whole when anything in it is wrong, and names the field.
	c := subpage.Default()
	c.Look.Background = subpage.PageBackground{Kind: "image"}
	c.Blocks = append(c.Blocks, subpage.PageBlock{ID: "links-a", Type: subpage.TypeLinks, On: true, Links: []subpage.PageLink{{Label: "x", URL: "javascript:alert(1)"}}})
	resp, out = h.do(http.MethodPut, api+"/sub-page", map[string]any{"config": c}, csrf)
	if resp.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(string(out), "background_missing") || !strings.Contains(string(out), "body.config.blocks[14].links[0].url") {
		t.Fatalf("bad save: %d %s", resp.StatusCode, out)
	}
	resp, out = h.do(http.MethodPut, api+"/sub-page", map[string]any{"config": subpage.Page{Look: subpage.PageLook{Palette: "neon"}}}, csrf)
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("an unknown palette: %d %s", resp.StatusCode, out)
	}
	c = subpage.Default()
	c.Look.Palette, c.Look.Mode, c.Look.Accent = "forest", "system", "brand"
	c.Brand = subpage.PageBrand{Logo: "image", Subtitle: "Быстро и тихо"}
	c.OG = subpage.PageOG{Title: "Mikan VPN", Description: "Подписка", Image: true}
	c.CSS = ".card{color:red}</style><script>alert(1)</script>@import url(https://evil.example/x.css);"
	c.Blocks = append(c.Blocks, subpage.PageBlock{ID: "text-a", Type: subpage.TypeText, On: true, Text: "**Привет**"})
	// A block turned off is a draft: it must not reach the page's source.
	c.Blocks = append(c.Blocks, subpage.PageBlock{ID: "text-b", Type: subpage.TypeText, On: false, Text: "черновик для своих"})
	resp, out = h.do(http.MethodPut, api+"/sub-page", map[string]any{"config": c, "brand": "Mikan", "brand_accent": "#2a813f"}, csrf)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("save: %d %s", resp.StatusCode, out)
	}
	var view struct {
		Config      subpage.Page       `json:"config"`
		BrandAccent string             `json:"brand_accent"`
		Logo        *subpage.PageAsset `json:"logo"`
	}
	if json.Unmarshal(out, &view) != nil || view.Config.CSS != ".card{color:red}/style>script>alert(1)/script> none;" || view.BrandAccent != "#2A813F" || view.Logo == nil {
		t.Fatalf("saved: %s", out)
	}

	// Instructions: drafts stay in the admin.
	mk := func(title string, published bool) int64 {
		resp, out := h.do(http.MethodPost, api+"/sub-docs", map[string]any{"title": title, "emoji": "📱", "body": "## Шаг 1\nОткройте Happ", "platform": "ios", "published": published}, csrf)
		var d struct {
			ID int64 `json:"id"`
		}
		if resp.StatusCode != http.StatusCreated || json.Unmarshal(out, &d) != nil {
			t.Fatalf("doc: %d %s", resp.StatusCode, out)
		}
		return d.ID
	}
	shown, draft := mk("iPhone", true), mk("Черновик", false)
	if resp, out := h.do(http.MethodPost, api+"/sub-docs", map[string]any{"title": "x", "emoji": "", "body": "![](http://example.com/a.png)", "platform": "", "published": true}, csrf); resp.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(string(out), "image_https") {
		t.Fatalf("an http image: %d %s", resp.StatusCode, out)
	}

	// The page: the admin's look, the preview tags, the CSS kept in its style, the list.
	resp, body := h.do(http.MethodGet, sub, nil, browser)
	p := pageData(t, body)
	if p.Brand != "Mikan" || p.Accent != "#2A813F" || p.Logo != "brand/logo?v="+asset.Hash || p.Config.Look.Palette != "forest" || len(p.Docs) != 1 || p.Docs[0].ID != shown || p.Config.CSS != "" {
		t.Fatalf("page data: %+v", p)
	}
	if bytes.Contains(body, []byte("черновик для своих")) {
		t.Fatal("a block turned off is in the page")
	}
	for _, want := range []string{`<meta property="og:title" content="Mikan VPN">`, `<meta property="og:image" content="https://203.0.113.10:21355/` + subPath + `/brand/logo?v=` + asset.Hash + `">`, `<style id="mikan-css">.card{color:red}/style>script>alert(1)/script> none;</style>`} {
		if !bytes.Contains(body, []byte(want)) {
			t.Errorf("page lacks %s:\n%s", want, body)
		}
	}
	if bytes.Count(body, []byte("<script")) != 1 || !bytes.Contains(body[bytes.Index(body, []byte("</style>")):], []byte("</head>")) {
		t.Fatalf("the admin's markup reached the page: %s", body)
	}
	if csp := resp.Header.Get("Content-Security-Policy"); !strings.Contains(csp, "img-src 'self' data: blob: https:;") || !strings.Contains(csp, "script-src 'self';") {
		t.Errorf("CSP: %s", csp)
	}
	// A messenger building a preview gets the page, not the keys.
	resp, body = h.do(http.MethodGet, sub, nil, map[string]string{"User-Agent": "TelegramBot (like TwitterBot)"})
	if resp.StatusCode != http.StatusOK || !bytes.Contains(body, []byte("og:title")) || bytes.Contains(body, []byte("vless://")) {
		t.Fatalf("link preview: %d %.200s", resp.StatusCode, body)
	}

	// The image goes as what it is, never sniffed as something else.
	resp, body = h.do(http.MethodGet, "/"+subPath+"/brand/logo?v="+asset.Hash, nil, nil)
	if resp.StatusCode != http.StatusOK || resp.Header.Get("Content-Type") != "image/png" || resp.Header.Get("X-Content-Type-Options") != "nosniff" ||
		!strings.Contains(resp.Header.Get("Cache-Control"), "immutable") || !bytes.Equal(body, logo) {
		t.Fatalf("logo: %d %v", resp.StatusCode, resp.Header)
	}
	if resp, _ := h.do(http.MethodGet, "/"+subPath+"/brand/logo?v=old", nil, nil); strings.Contains(resp.Header.Get("Cache-Control"), "immutable") {
		t.Fatal("an old link is kept for good")
	}
	if resp, _ := h.do(http.MethodGet, "/"+subPath+"/brand/background", nil, nil); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("no background: %d", resp.StatusCode)
	}
	if resp, _ := h.do(http.MethodGet, "/"+subPath+"/brand/secret", nil, nil); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown image: %d", resp.StatusCode)
	}

	// An instruction opens by its id, a draft does not.
	// Only a subscriber opens them: an id alone is not enough.
	if resp, _ := h.do(http.MethodGet, "/"+subPath+"/docs/"+strconv.FormatInt(shown, 10), nil, nil); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("an instruction without a token: %d", resp.StatusCode)
	}
	resp, body = h.do(http.MethodGet, sub+"/docs/"+strconv.FormatInt(shown, 10), nil, nil)
	var doc subpage.DocView
	if resp.StatusCode != http.StatusOK || json.Unmarshal(body, &doc) != nil || doc.Body != "## Шаг 1\nОткройте Happ" || doc.Platform != "ios" {
		t.Fatalf("doc: %d %s", resp.StatusCode, body)
	}
	for _, id := range []string{strconv.FormatInt(draft, 10), "999", "x", "-1"} {
		if resp, _ := h.do(http.MethodGet, sub+"/docs/"+id, nil, nil); resp.StatusCode != http.StatusNotFound {
			t.Errorf("doc %s: %d", id, resp.StatusCode)
		}
	}
	// Published, reordered, deleted.
	if resp, out := h.do(http.MethodPut, api+"/sub-docs/"+strconv.FormatInt(draft, 10), map[string]any{"title": "Android", "emoji": "🤖", "body": "текст", "platform": "android", "published": true}, csrf); resp.StatusCode != http.StatusOK {
		t.Fatalf("update: %d %s", resp.StatusCode, out)
	}
	resp, out = h.do(http.MethodPut, api+"/sub-docs/order", map[string]any{"ids": []int64{draft, shown}}, csrf)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("order: %d %s", resp.StatusCode, out)
	}
	_, body = h.do(http.MethodGet, sub, nil, browser)
	if p := pageData(t, body); len(p.Docs) != 2 || p.Docs[0].ID != draft || p.Docs[0].Title != "Android" {
		t.Fatalf("after reorder: %+v", p.Docs)
	}
	if resp, _ := h.do(http.MethodDelete, api+"/sub-docs/"+strconv.FormatInt(shown, 10), nil, csrf); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("delete: %d", resp.StatusCode)
	}
	if resp, _ := h.do(http.MethodGet, sub+"/docs/"+strconv.FormatInt(shown, 10), nil, nil); resp.StatusCode != http.StatusNotFound {
		t.Fatal("a deleted instruction still opens")
	}

	// The logo removed: the page falls back to the letter, the preview loses its image.
	if resp, _ := h.do(http.MethodDelete, api+"/sub-page/images/logo", nil, csrf); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("delete logo: %d", resp.StatusCode)
	}
	_, body = h.do(http.MethodGet, sub, nil, browser)
	if p := pageData(t, body); p.Logo != "" || p.Config.Brand.Logo != "letter" || bytes.Contains(body, []byte("og:image")) {
		t.Fatalf("without the logo: %+v", p.Config.Brand)
	}
}
