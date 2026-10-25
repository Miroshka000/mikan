package serve

import (
	"database/sql"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"mikan/internal/panel/store/db"
	"mikan/internal/textutil"
)

func TestOperatorHeadersWithoutBranding(t *testing.T) {
	h := http.Header{}
	OperatorHeaders(h, db.User{Name: "a"}, Config{Brand: "Mikan"}, "https://vpn.example.com/s/tok", "https://t.me/bot", 1)
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
	OperatorHeaders(h, db.User{}, Config{Announce: "Работы 22:00–23:00", AnnounceURL: "https://t.me/news"}, "", "", -1)
	raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(h.Get("Announce"), "base64:"))
	if !strings.HasPrefix(h.Get("Announce"), "base64:") || err != nil || string(raw) != "Работы 22:00–23:00" {
		t.Errorf("announce %q: %v", h.Get("Announce"), err)
	}
	if h.Get("Announce-Url") != "https://t.me/news" {
		t.Errorf("announce url: %q", h.Get("Announce-Url"))
	}
	h = http.Header{}
	OperatorHeaders(h, db.User{}, Config{AnnounceURL: "https://t.me/news"}, "", "", -1)
	if h.Get("Announce-Url") != "" {
		t.Error("a link without an announcement leads nowhere")
	}
}

func TestOperatorHeadersBranding(t *testing.T) {
	u := db.User{Name: "Иван Петров", DeviceLimit: sql.NullInt64{Int64: 3, Valid: true}}
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
}

func TestBrandNameIsCutByLetters(t *testing.T) {
	long := strings.Repeat("Я", 40)
	h := http.Header{}
	OperatorHeaders(h, db.User{}, Config{Brand: long + "\x07", App: AppBrand{Enabled: true}}, "", "", -1)
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
