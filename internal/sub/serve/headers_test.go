package serve

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"mikan/internal/textutil"
)

func TestOperatorHeadersWithoutBranding(t *testing.T) {
	h := http.Header{}
	OperatorHeaders(h, User{Name: "a"}, Config{Brand: "Mikan"}, "https://vpn.example.com/s/tok", "https://t.me/bot", 1)
	if h.Get("Profile-Web-Page-Url") != "https://vpn.example.com/s/tok" {
		t.Errorf("page: %q", h.Get("Profile-Web-Page-Url"))
	}
	for k := range h {
		if strings.HasPrefix(k, "X-Brand") || k == "Announce" || k == "X-Hwid-Active" {
			t.Errorf("%s sent with branding, the announcement and binding off", k)
		}
	}
}

func TestOperatorHeadersAnnounce(t *testing.T) {
	h := http.Header{}
	OperatorHeaders(h, User{}, Config{Announce: "Работы 22:00–23:00", AnnounceURL: "https://t.me/news"}, "", "", -1)
	raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(h.Get("Announce"), "base64:"))
	if !strings.HasPrefix(h.Get("Announce"), "base64:") || err != nil || string(raw) != "Работы 22:00–23:00" {
		t.Errorf("announce %q: %v", h.Get("Announce"), err)
	}
	if h.Get("Announce-Url") != "https://t.me/news" {
		t.Errorf("announce url: %q", h.Get("Announce-Url"))
	}
	h = http.Header{}
	OperatorHeaders(h, User{}, Config{AnnounceURL: "https://t.me/news"}, "", "", -1)
	if h.Get("Announce-Url") != "" {
		t.Error("a link without an announcement leads nowhere")
	}
}

func TestOperatorHeadersBranding(t *testing.T) {
	u := User{Name: "Иван Петров", Devices: ptr(3), Places: true}
	cfg := Config{Brand: "Мандарин VPN", SupportURL: "https://t.me/support", Binding: true,
		App: AppBrand{Enabled: true, Accent: "#F07A2E", LogoURL: "https://cdn.example.com/logo.png"}}
	h := http.Header{}
	OperatorHeaders(h, u, cfg, "https://vpn.example.com/s/tok", "https://t.me/mikan_bot", 2)

	want := map[string]string{
		"X-Hwid-Active": "true",
		// ClashFest
		"X-Branding-Enabled":        "true",
		"X-Brand-Name":              "base64:" + base64.StdEncoding.EncodeToString([]byte("Мандарин VPN")),
		"X-Brand-User-Display-Name": "base64:" + base64.StdEncoding.EncodeToString([]byte("Иван Петров")),
		"X-Brand-Accent-Color":      "#F07A2E",
		"X-Brand-Logo-URL":          "https://cdn.example.com/logo.png",
		"X-Brand-Support-URL":       "https://t.me/support",
		"X-Brand-Bot-URL":           "https://t.me/mikan_bot",
		"X-Brand-Renew-URL":         "https://vpn.example.com/s/tok",
		"X-Brand-Max-Devices":       "3",
		"X-Brand-Current-Devices":   "2",
		// SlothClash: the same, UTF-8 as is
		"X-Brand-Desktop-Enabled":           "true",
		"X-Brand-Desktop-Name":              "Мандарин VPN",
		"X-Brand-Desktop-User-Display-Name": "Иван Петров",
		"X-Brand-Desktop-Accent-Color":      "#F07A2E",
		"X-Brand-Desktop-Logo-URL":          "https://cdn.example.com/logo.png",
		"X-Brand-Desktop-Support-URL":       "https://t.me/support",
		"X-Brand-Desktop-Bot-URL":           "https://t.me/mikan_bot",
		"X-Brand-Desktop-Renew-URL":         "https://vpn.example.com/s/tok",
		"X-Brand-Desktop-Devices-Limit":     "3",
		"X-Brand-Desktop-Devices-Used":      "2",
	}
	for k, v := range want {
		if got := h.Get(k); got != v {
			t.Errorf("%s = %q, want %q", k, got, v)
		}
	}

	// Without binding there are no places to count; an ASCII name goes as is.
	cfg.Binding, cfg.Brand, cfg.App.Accent = false, "Mikan", ""
	h = http.Header{}
	OperatorHeaders(h, u, cfg, "", "", 2)
	if h.Get("X-Brand-Name") != "Mikan" || h.Get("X-Brand-Max-Devices") != "" || h.Get("X-Brand-Desktop-Devices-Used") != "" || h.Get("X-Brand-Accent-Color") != "" {
		t.Errorf("without binding: %v", h)
	}
	// The online mode has no places to count either.
	cfg.Binding, u.Places = true, false
	h = http.Header{}
	OperatorHeaders(h, u, cfg, "", "", 2)
	if h.Get("X-Brand-Max-Devices") != "" {
		t.Errorf("online mode: %v", h)
	}
}

func TestBrandNameIsCutByLetters(t *testing.T) {
	long := strings.Repeat("Я", 40)
	h := http.Header{}
	OperatorHeaders(h, User{}, Config{Brand: long + "\x07", App: AppBrand{Enabled: true}}, "", "", -1)
	if got := h.Get("X-Brand-Desktop-Name"); got != strings.Repeat("Я", 32) {
		t.Errorf("name %q", got)
	}
}

func TestBotURLDropsTheStartCode(t *testing.T) {
	for in, want := range map[string]string{
		"https://t.me/mikan_bot?start=abc": "https://t.me/mikan_bot",
		"":                                 "",
		"tg://resolve?domain=x":            "",
	} {
		if got := BotURL(in); got != want {
			t.Errorf("BotURL(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestPageURL(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "https://vpn.example.com:2053/s/tok?format=clash", nil)
	if got := PageURL(r); got != "https://vpn.example.com:2053/s/tok" {
		t.Errorf("page %q", got)
	}
}

// The links the apps open: https with a host, and tg:// for the links that allow it.
func TestAppLinks(t *testing.T) {
	for _, c := range []struct {
		s      string
		tg, ok bool
	}{
		{"https://t.me/news", true, true},
		{"tg://resolve?domain=news", true, true},
		{"tg://resolve?domain=news", false, false},
		{"https://", true, false},
		{"https://t.me/a b", true, false},
		{"https://t.me/a\x00", true, false},
		{"http://t.me/news", true, false},
		{"javascript:alert(1)", true, false},
	} {
		rules := LogoLinks
		if c.tg {
			rules = AppLinks
		}
		if textutil.ValidLink(c.s, rules) != c.ok {
			t.Errorf("%q with tg=%v: %v", c.s, c.tg, !c.ok)
		}
	}
}

func TestValidAccent(t *testing.T) {
	for s, want := range map[string]bool{"#F07A2E": true, "#f07a2e": true, "F07A2E": false, "#F07A2": false, "#F07A2EE": false, "": false} {
		if ValidAccent(s) != want {
			t.Errorf("%q", s)
		}
	}
}

// The traffic headers: with packages left the total is what the user can reach, a term
// and the support link when there are some.
func TestUserInfoHeaders(t *testing.T) {
	hd := http.Header{}
	exp := time.Unix(1793491200, 0)
	userInfoHeaders(hd, User{UsedUp: 1, UsedDown: 9, Limit: ptr(100), Grants: 50, Expires: exp}, Config{Brand: "Mikan", SupportURL: "https://t.me/support"}, nil)
	if got := hd.Get("Subscription-Userinfo"); got != "upload=1; download=9; total=150; expire=1793491200" {
		t.Errorf("userinfo: %q", got)
	}
	if hd.Get("Profile-Title") != "base64:"+base64.StdEncoding.EncodeToString([]byte("Mikan")) || hd.Get("Profile-Update-Interval") != "1" || hd.Get("Support-Url") != "https://t.me/support" {
		t.Errorf("headers: %v", hd)
	}
	hd = http.Header{}
	userInfoHeaders(hd, User{UsedUp: 1, UsedDown: 9}, Config{}, nil)
	if got := hd.Get("Subscription-Userinfo"); got != "upload=1; download=9; total=0; expire=0" || hd.Get("Support-Url") != "" {
		t.Errorf("no limit, no term: %q %v", got, hd)
	}
}

// What the apps of the device binding read when a device gets no keys.
func TestStubHeaders(t *testing.T) {
	for reason, want := range map[error][]string{
		ErrNoHWID:       {"X-Hwid-Not-Supported"},
		ErrDeviceLimit:  {"X-Hwid-Max-Devices-Reached", "X-Hwid-Limit"},
		ErrDeviceBanned: nil,
	} {
		hd := http.Header{}
		stubHeaders(hd, reason)
		var got []string
		for k := range hd {
			got = append(got, k)
		}
		if len(got) != len(want) {
			t.Errorf("%v: %v, want %v", reason, got, want)
		}
		for _, k := range want {
			if hd.Get(k) != "true" {
				t.Errorf("%v: no %s", reason, k)
			}
		}
	}
}

func TestHappHeaders(t *testing.T) {
	h := http.Header{}
	happHeaders(h, Happ{Routing: "happ://routing/off", HideSettings: true})
	if h.Get("routing") != "happ://routing/off" || h.Get("providerid") != "" || h.Get("hide-settings") != "" {
		t.Fatalf("without a provider id Happ takes no provider headers: %v", h)
	}
	h = http.Header{}
	happHeaders(h, Happ{ProviderID: "p1", HideSettings: true})
	if h.Get("providerid") != "p1" || h.Get("hide-settings") != "1" || h.Get("routing") != "" {
		t.Fatalf("provider headers: %v", h)
	}
	for s, want := range map[string]bool{"abc-123_X": true, "": false, "a b": false, "a\nb": false, strings.Repeat("a", 65): false} {
		if got := ValidHappProviderID(s); got != want {
			t.Errorf("ValidHappProviderID(%q) = %v", s, got)
		}
	}
	// INCY takes the routing in its own scheme and none of Happ's provider headers.
	h = http.Header{}
	appHeaders(h, "INCY/2.0.8/iOS", Config{Happ: Happ{Routing: "happ://routing/off", ProviderID: "p1"}}, "")
	if h.Get("routing") != "incy://routing/off" || h.Get("providerid") != "" {
		t.Fatalf("INCY: %v", h)
	}
	h = http.Header{}
	appHeaders(h, "v2RayTun/5.1", Config{Happ: Happ{Routing: "happ://routing/off", ProviderID: "p1"}}, "")
	if len(h) != 0 {
		t.Fatalf("another app: %v", h)
	}
}

const gib = int64(1) << 30

func decodeGroups(t *testing.T, v string) AppGroups {
	t.Helper()
	raw, ok := strings.CutPrefix(v, "base64:")
	if !ok {
		t.Fatalf("no base64: prefix: %q", v)
	}
	b, err := base64.StdEncoding.DecodeString(raw)
	if err != nil {
		t.Fatal(err)
	}
	var g AppGroups
	if err := json.Unmarshal(b, &g); err != nil {
		t.Fatal(err)
	}
	return g
}

func TestAppGroupsHeader(t *testing.T) {
	if v := (AppGroups{V: 1}).header(); v != "" {
		t.Fatalf("no groups, yet a header: %q", v)
	}
	g := AppGroups{V: 1, ResetsAt: 1793491200, Groups: []AppGroup{
		{ID: 3, Name: "Премиум +", Mode: "own", Used: 3 * gib, Limit: ptr(15 * gib), Left: ptr(12 * gib), State: "active", Proxies: []string{"🇺🇸 VLESS Reality"}},
	}}
	v := g.header()
	if back := decodeGroups(t, v); !reflect.DeepEqual(back, g) {
		t.Fatalf("round trip: %+v", back)
	}
	// The JSON the app reads, field by field: a renamed field breaks every app out there.
	raw, _ := base64.StdEncoding.DecodeString(strings.TrimPrefix(v, "base64:"))
	if want := `{"v":1,"resets_at":1793491200,"groups":[{"id":3,"name":"Премиум +","mode":"own","used":3221225472,"limit":16106127360,"left":12884901888,"state":"active","proxies":["🇺🇸 VLESS Reality"]}]}`; string(raw) != want {
		t.Fatalf("JSON:\n got %s\nwant %s", raw, want)
	}
}

// A subscription with a great many servers in its groups still gets a header that fits
// behind a reverse proxy: the server names go first (and with them the groups that are only
// names), the traffic of the groups of their own stays.
func TestAppGroupsHeaderFits(t *testing.T) {
	g := AppGroups{V: 1}
	for i := range 6 {
		var names []string
		for j := range 20 {
			names = append(names, "🇺🇸 Сервер "+strconv.Itoa(i)+"-"+strconv.Itoa(j)+" VLESS Reality")
		}
		g.Groups = append(g.Groups, AppGroup{ID: int64(i + 1), Name: "Группа " + strconv.Itoa(i), Mode: "own", Used: gib, Limit: ptr(5 * gib), Left: ptr(4 * gib), State: "active", Proxies: names})
	}
	g.Groups = append(g.Groups, AppGroup{ID: 99, Name: "Только метка", Mode: "main", State: "active", Proxies: []string{"🇩🇪 Trojan"}})
	v := g.header()
	if len(v) > appGroupsMax {
		t.Fatalf("header of %d bytes, the most is %d", len(v), appGroupsMax)
	}
	back := decodeGroups(t, v)
	if len(back.Groups) != 6 {
		t.Fatalf("want the 6 groups of their own and no label-only group, got %d", len(back.Groups))
	}
	for _, x := range back.Groups {
		if x.Proxies != nil || x.Limit == nil || *x.Limit != 5*gib {
			t.Fatalf("names must be null (did not fit), the traffic kept: %+v", x)
		}
	}
	if len(g.Groups[0].Proxies) != 20 {
		t.Fatal("header() changed the groups it was given")
	}
}

func TestWantsFeature(t *testing.T) {
	for _, c := range []struct {
		values []string
		want   bool
	}{
		{nil, false},
		{[]string{"groups"}, true},
		{[]string{"Groups"}, true},
		{[]string{"news, groups"}, true},
		{[]string{"news", " groups "}, true},
		{[]string{"groupsx"}, false},
		{[]string{"news"}, false},
	} {
		h := http.Header{}
		for _, v := range c.values {
			h.Add(FeaturesHeader, v)
		}
		if got := wantsFeature(h, featureGroups); got != c.want {
			t.Errorf("%q: got %v, want %v", c.values, got, c.want)
		}
	}
}
