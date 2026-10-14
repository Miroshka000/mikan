package subpage

import (
	"bytes"
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"testing"
)

// Nothing configured is the page as it always was: the zero document cleans to Default,
// whose blocks are the page's sections in their old order, the new ones hidden.
func TestZeroIsDefault(t *testing.T) {
	var c Page
	if ps := Normalize(&c); len(ps) != 0 {
		t.Fatalf("the zero document has problems: %+v", ps)
	}
	d := Default()
	// What zero means for numbers and a switch; a stored document is read over Default
	// (Service.Config), so these never come from a zero.
	d.Apps.QR, d.Look.Background.Angle, d.Look.Background.Dim = false, 0, 0
	if !reflect.DeepEqual(c, d) {
		t.Fatalf("zero cleans to\n%+v\nwant\n%+v", c, d)
	}
	var order []string
	for _, b := range Default().Blocks {
		if b.On {
			order = append(order, b.ID)
		}
	}
	if got := strings.Join(order, ","); got != "status,promo,shop,traffic,devices,apps,guide,instructions,link,reissue,telegram,support" {
		t.Fatalf("default sections: %s", got)
	}
	if def := Default(); def.Look != (PageLook{Palette: "mikan", Mode: "light", Accent: "theme", Font: "default", Radius: "medium", Cards: "glass", Background: PageBackground{Kind: "theme", Angle: 160, Dim: 30}}) || !def.Apps.QR || def.Apps.Platform != "auto" || def.CSS != "" {
		t.Fatalf("default look changed: %+v", def)
	}
}

func TestDefaultIsClean(t *testing.T) {
	c := Default()
	if ps := Normalize(&c); len(ps) != 0 || !reflect.DeepEqual(c, Default()) {
		t.Fatalf("Default is not clean: %+v", ps)
	}
}

func codes(ps []Problem) map[string]string {
	out := map[string]string{}
	for _, p := range ps {
		if _, ok := out[p.Field]; !ok { // the first: what follows from it comes after
			out[p.Field] = p.Code
		}
	}
	return out
}

func TestNormalizeRefuses(t *testing.T) {
	cases := []struct {
		name  string
		edit  func(*Page)
		field string
		code  string
	}{
		{"colour without #", func(c *Page) { c.Look.Background = PageBackground{Kind: "solid", From: "F07A2E"} }, "look.background.from", "color_invalid"},
		{"colour with a name", func(c *Page) { c.Look.Background = PageBackground{Kind: "gradient", From: "#F07A2E", To: "red"} }, "look.background.to", "color_invalid"},
		{"short colour", func(c *Page) { c.Look.Background.From = "#FFF" }, "look.background.from", "color_invalid"},
		{"solid without a colour", func(c *Page) { c.Look.Background = PageBackground{Kind: "solid"} }, "look.background.from", "color_required"},
		{"unknown palette", func(c *Page) { c.Look.Palette = "neon" }, "look.palette", "value_invalid"},
		{"dim out of range", func(c *Page) { c.Look.Background.Dim = 95 }, "look.background.dim", "value_invalid"},
		{"emoji logo without emoji", func(c *Page) { c.Brand.Logo = "emoji" }, "brand.emoji", "emoji_required"},
		{"markup for an emoji", func(c *Page) { c.Brand.Logo, c.Brand.Emoji = "emoji", "<b>" }, "brand.emoji", "emoji_invalid"},
		{"subtitle on two lines", func(c *Page) { c.Brand.Subtitle = "a\nb" }, "brand.subtitle", "one_line"},
		{"unknown block", func(c *Page) { c.Blocks = append(c.Blocks, PageBlock{ID: "ads", Type: "ads", On: true}) }, "blocks[14].type", "block_unknown"},
		{"built-in twice", func(c *Page) { c.Blocks = append(c.Blocks, PageBlock{ID: "status", Type: "status"}) }, "blocks[14].id", "block_duplicate"},
		{"built-in under another id", func(c *Page) { c.Blocks[0].ID = "news" }, "blocks[0].id", "block_id_invalid"},
		{"own block without its prefix", func(c *Page) { c.Blocks = append(c.Blocks, PageBlock{ID: "links-1", Type: TypeText}) }, "blocks[14].id", "block_id_invalid"},
		{"own text too long", func(c *Page) {
			c.Blocks = append(c.Blocks, PageBlock{ID: "text-a", Type: TypeText, Text: strings.Repeat("я", 8001)})
		}, "blocks[14].text", "too_long"},
		{"image over http", func(c *Page) {
			c.Blocks = append(c.Blocks, PageBlock{ID: "text-a", Type: TypeText, Text: "see ![x](http://example.com/a.png)"})
		}, "blocks[14].text", "image_https"},
		{"javascript link", func(c *Page) {
			c.Blocks = append(c.Blocks, PageBlock{ID: "links-a", Type: TypeLinks, Links: []PageLink{{Label: "Go", URL: "javascript:alert(1)"}}})
		}, "blocks[14].links[0].url", "link_invalid"},
		{"data link", func(c *Page) {
			c.Blocks = append(c.Blocks, PageBlock{ID: "links-a", Type: TypeLinks, Links: []PageLink{{Label: "Go", URL: "data:text/html,x"}}})
		}, "blocks[14].links[0].url", "link_invalid"},
		{"link with credentials", func(c *Page) {
			c.Blocks = append(c.Blocks, PageBlock{ID: "links-a", Type: TypeLinks, Links: []PageLink{{Label: "Go", URL: "https://user:pw@example.com"}}})
		}, "blocks[14].links[0].url", "link_invalid"},
		{"link without a label", func(c *Page) {
			c.Blocks = append(c.Blocks, PageBlock{ID: "links-a", Type: TypeLinks, Links: []PageLink{{URL: "https://example.com"}}})
		}, "blocks[14].links[0].label", "label_required"},
		{"app name with markup", func(c *Page) { c.Apps.IOS.Order = []string{"<Happ>"} }, "apps.ios.order", "app_invalid"},
		{"css too long", func(c *Page) { c.CSS = strings.Repeat("a", 20481) }, "css", "too_long"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := Default()
			tc.edit(&c)
			got := codes(Normalize(&c))
			if got[tc.field] != tc.code {
				t.Fatalf("problems %v, want %s at %s", got, tc.code, tc.field)
			}
		})
	}
}

func TestNormalizeKeepsWhatIsRight(t *testing.T) {
	c := Default()
	c.Look = PageLook{Palette: "ocean", Mode: "system", Accent: "brand", Font: "rounded", Radius: "large", Cards: "solid", Background: PageBackground{Kind: "gradient", From: "#0f6272", To: "#eaf5f8", Angle: 90}}
	c.Blocks = append([]PageBlock{
		{ID: "links-x1", Type: TypeLinks, On: true, Title: " Каналы ", Links: []PageLink{{Label: "Канал", URL: "https://t.me/mikan", Emoji: "📣"}, {Label: "Бот", URL: "tg://resolve?domain=mikan_bot"}, {Label: "Сайт", URL: "http://example.com"}}},
		{ID: "text-1", Type: TypeText, On: true, Text: "# Привет\r\n![](https://example.com/a.png)", Links: []PageLink{{Label: "dropped", URL: "https://x"}}},
	}, c.Blocks[:3]...) // the other built-ins are left out: they come back at the end
	c.Apps.Android = PageAppList{Order: []string{"INCY", "Happ", "INCY"}, Hidden: []string{"Hiddify"}}
	if ps := Normalize(&c); len(ps) != 0 {
		t.Fatalf("problems: %+v", ps)
	}
	if c.Look.Background.From != "#0F6272" || c.Blocks[0].Title != "Каналы" || len(c.Blocks[0].Links) != 3 || c.Blocks[1].Links != nil || c.Blocks[1].Text != "# Привет\n![](https://example.com/a.png)" {
		t.Fatalf("cleaned wrong: %+v", c.Blocks[:2])
	}
	if len(c.Blocks) != 16 || c.Blocks[5].ID != "shop" || c.Blocks[15].ID != "support" || c.Blocks[13].On || c.Blocks[12].ID != "reissue" {
		t.Fatalf("missing built-ins not put back in order: %+v", c.Blocks)
	}
	if strings.Join(c.Apps.Android.Order, ",") != "INCY,Happ" {
		t.Fatalf("app order not deduplicated: %v", c.Apps.Android.Order)
	}
}

func TestTooManyOwnBlocks(t *testing.T) {
	c := Default()
	for i := range MaxCustom + 1 {
		c.Blocks = append(c.Blocks, PageBlock{ID: "text-" + string(rune('a'+i)), Type: TypeText})
	}
	if got := codes(Normalize(&c)); got["blocks[26]"] != "too_many_blocks" {
		t.Fatalf("13 own blocks: %v", got)
	}
}

func TestCleanCSS(t *testing.T) {
	for in, want := range map[string]string{
		"body{color:red}":                              "body{color:red}",
		"a{}</style><script>alert(1)</script>":         "a{}/style>script>alert(1)/script>",
		"@import url(https://evil.example/x.css); a{}": "none; a{}",
		"@IMPORT 'x'; @imp@importort 'y';":             "'x';  'y';",
		`@\69mport url(x); b{}`:                        "@69mport url(x); b{}",
		// Images of other sites would let them count the subscribers; the panel's and
		// data: images stay, and an escape cannot spell url( past the check.
		"body{background:url(https://t.example/p.gif)}":         "body{background:none}",
		"a{background:URL( '//t.example/p.gif' )}":              "a{background:none}",
		"a{background:image-set('https://t.example/a.png' 1x)}": "a{background:none}",
		`a{background:u\72l(https://t.example/p.gif)}`:          "a{background:u72l(https://t.example/p.gif)}",
		"a{background:url(/sub/brand/logo)}":                    "a{background:url(/sub/brand/logo)}",
		"a{background:url(data:image/png;base64,AAAA)}":         "a{background:url(data:image/png;base64,AAAA)}",
		".x::after{content:'<'}":                                ".x::after{content:''}",
		"  @media (max-width: 400px){.a{color:red}}  \n\n":      "@media (max-width: 400px){.a{color:red}}",
	} {
		if got := CleanCSS(in); got != want {
			t.Errorf("CleanCSS(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestCheckImage(t *testing.T) {
	png := append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, 64)...)
	jpeg := append([]byte("\xff\xd8\xff\xe0"), make([]byte, 64)...)
	webp := append([]byte("RIFF\x00\x00\x00\x00WEBPVP8 "), make([]byte, 64)...)
	for name, c := range map[string]struct {
		asset string
		data  []byte
		want  string
		err   error
	}{
		"png":            {AssetLogo, png, "image/png", nil},
		"jpeg":           {AssetBackground, jpeg, "image/jpeg", nil},
		"webp":           {AssetLogo, webp, "image/webp", nil},
		"svg":            {AssetLogo, []byte(`<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`), "", ErrImageType},
		"svg with xml":   {AssetLogo, []byte(`<?xml version="1.0"?><svg xmlns="http://www.w3.org/2000/svg"/>`), "", ErrImageType},
		"html as png":    {AssetLogo, []byte("<!doctype html><script>alert(1)</script>"), "", ErrImageType},
		"gif":            {AssetLogo, []byte("GIF89a......"), "", ErrImageType},
		"empty":          {AssetLogo, nil, "", ErrImageType},
		"logo too big":   {AssetLogo, append(png, make([]byte, 512<<10)...), "", ErrImageTooBig},
		"background fit": {AssetBackground, append(png, make([]byte, 1<<20)...), "image/png", nil},
		"unknown name":   {"favicon", png, "", ErrImageTooBig},
	} {
		got, err := CheckImage(c.asset, c.data)
		if got != c.want || err != c.err {
			t.Errorf("%s: %q %v, want %q %v", name, got, err, c.want, c.err)
		}
	}
}

func TestNormalizeDoc(t *testing.T) {
	d := Doc{Title: "  Как подключить iPhone ", Emoji: "📱", Body: "Шаг 1\r\n![](https://example.com/1.png)", Platform: "ios"}
	if ps := NormalizeDoc(&d); len(ps) != 0 || d.Title != "Как подключить iPhone" || d.Body != "Шаг 1\n![](https://example.com/1.png)" {
		t.Fatalf("a good page: %+v %+v", ps, d)
	}
	for name, c := range map[string]struct {
		d     Doc
		field string
		code  string
	}{
		"no title":       {Doc{Title: "  "}, "title", "title_required"},
		"title on lines": {Doc{Title: "a\nb"}, "title", "title_required"},
		"bad platform":   {Doc{Title: "x", Platform: "beos"}, "platform", "value_invalid"},
		"http image":     {Doc{Title: "x", Body: "![a](http://example.com/x.png)"}, "body", "image_https"},
		"data image":     {Doc{Title: "x", Body: "![a](data:image/png;base64,AAAA)"}, "body", "image_https"},
		"too long":       {Doc{Title: "x", Body: strings.Repeat("a", 20001)}, "body", "too_long"},
	} {
		if got := codes(NormalizeDoc(&c.d)); got[c.field] != c.code {
			t.Errorf("%s: %v, want %s at %s", name, got, c.code, c.field)
		}
	}
}

// What the page's head gets cannot close its own tags, whatever the admin typed.
func TestHeadEscapes(t *testing.T) {
	c := Default()
	c.OG = PageOG{Title: `"><script>alert(1)</script>`, Description: "Быстрый VPN", Image: true}
	c.Blocks = append(c.Blocks, PageBlock{ID: "text-a", Type: TypeText, On: true, Text: "</script><script>alert(2)</script>"})
	p := Public{Config: c, Brand: "</script>", Logo: AssetPath(AssetLogo, "abc"), Docs: []DocItem{}, css: "a{}</style><b>"}
	head := p.Head("https://vpn.example.com/sub")
	if bytes.Count(head, []byte("<script")) != 1 || bytes.Count(head, []byte("</script>")) != 1 || bytes.Count(head, []byte("</style>")) != 1 {
		t.Fatalf("markup leaked into the head: %s", head)
	}
	for _, want := range []string{`<meta property="og:title" content="&#34;&gt;&lt;script&gt;`, `<meta property="og:image" content="https://vpn.example.com/sub/brand/logo?v=abc">`, `<meta property="og:description" content="Быстрый VPN">`, `<style id="mikan-css">a{}/style>b></style>`} {
		if !bytes.Contains(head, []byte(want)) {
			t.Errorf("head lacks %s:\n%s", want, head)
		}
	}
	start := bytes.Index(head, []byte(`id="mikan-page">`)) + len(`id="mikan-page">`)
	end := bytes.Index(head, []byte("</script>"))
	var back Public
	if err := json.Unmarshal(head[start:end], &back); err != nil || back.Brand != "</script>" || back.Config.CSS != "" {
		t.Fatalf("the page's data does not read back: %v %+v", err, back)
	}
}

// Nothing set: no preview tags, no style, no images from other sites.
func TestHeadOfDefault(t *testing.T) {
	p := Public{Config: Default(), Brand: "VPN", Docs: []DocItem{}}
	head := string(p.Head("https://vpn.example.com/sub"))
	if strings.Contains(head, "og:") || strings.Contains(head, "<style") || p.RemoteImages() {
		t.Fatalf("the default page got more than its data: %s", head)
	}
	p.Docs = []DocItem{{ID: 1, Title: "x"}}
	if !p.RemoteImages() {
		t.Fatal("instructions may show https images")
	}
}

// A page saved before the "new link" block existed gets it right behind the link block,
// wherever the admin put that; a page that has it keeps it where it is.
func TestReissueFollowsLink(t *testing.T) {
	index := func(c Page, id string) int {
		return slices.IndexFunc(c.Blocks, func(b PageBlock) bool { return b.ID == id })
	}
	if c := Default(); index(c, "reissue") != index(c, "link")+1 || !c.Blocks[index(c, "reissue")].On {
		t.Fatalf("default page: %+v", c.Blocks)
	}
	old := Page{Blocks: []PageBlock{{ID: "link", Type: "link", On: true}, {ID: "status", Type: "status", On: true}}}
	if ps := Normalize(&old); len(ps) != 0 || index(old, "reissue") != 1 || !old.Blocks[1].On {
		t.Fatalf("saved before the block: %v %+v", ps, old.Blocks)
	}
	moved := Page{Blocks: []PageBlock{{ID: "reissue", Type: "reissue"}, {ID: "status", Type: "status", On: true}, {ID: "link", Type: "link", On: true}}}
	if ps := Normalize(&moved); len(ps) != 0 || index(moved, "reissue") != 0 || moved.Blocks[0].On {
		t.Fatalf("the admin's place and choice kept: %v %+v", ps, moved.Blocks)
	}
	if index(moved, "reissue") == index(moved, "link")+1 {
		t.Fatal("moved block must stay where it is")
	}
}
