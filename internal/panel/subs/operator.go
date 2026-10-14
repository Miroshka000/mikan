package subs

import (
	"encoding/base64"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"mikan/internal/panel/domain"
	"mikan/internal/panel/store/db"
)

// AppBrand is the brand the apps that read operator headers show in place of their own:
// ClashFest reads X-Brand-*, SlothClash X-Brand-Desktop-*. Both ignore every field unless
// the subscription turns branding on, so nothing changes for anyone with it off.
type AppBrand struct {
	Enabled bool
	Accent  string // #RRGGBB, empty: the app's own
	LogoURL string // https, empty: the app's own
}

var accentColor = regexp.MustCompile(`^#[0-9A-Fa-f]{6}$`)

// ValidAccent says whether s is a colour the apps take: #RRGGBB.
func ValidAccent(s string) bool { return accentColor.MatchString(s) }

// Lengths the apps cut to; longer values are cut here so a name never breaks mid-letter.
const (
	brandNameMax = 32
	userNameMax  = 64
)

// OperatorHeaders are the headers apps read besides the traffic ones: the subscription
// page, the announcement, whether device ids count, and the app branding.
//
//	page     where the subscription page is, for Happ's and v2RayTun's "open in browser"
//	devices  devices bound to the user, -1 when not counted
func OperatorHeaders(h http.Header, u db.User, cfg Config, page, botURL string, devices int64) {
	if page != "" {
		h.Set("Profile-Web-Page-Url", page)
	}
	if cfg.Announce != "" {
		h.Set("Announce", "base64:"+base64.StdEncoding.EncodeToString([]byte(cfg.Announce)))
		if cfg.AnnounceURL != "" {
			h.Set("Announce-Url", cfg.AnnounceURL)
		}
	}
	if cfg.Binding {
		h.Set("X-Hwid-Active", "true")
	}
	if !cfg.App.Enabled {
		return
	}
	name := cut(cfg.Brand, brandNameMax)
	user := cut(u.Name, userNameMax)
	limit := int64(0)
	// Branding shows the limit as places taken; the online mode has no places.
	if cfg.Binding && u.DeviceLimit.Valid && domain.BindsDevices(u.DeviceMode) {
		limit = u.DeviceLimit.Int64
	}
	// ClashFest decodes "base64:" values; SlothClash takes UTF-8 as sent.
	for _, ns := range []struct {
		prefix, on, limit, used string
		text                    func(string) string
	}{
		{"X-Brand-", "X-Branding-Enabled", "X-Brand-Max-Devices", "X-Brand-Current-Devices", asciiOrBase64},
		{"X-Brand-Desktop-", "X-Brand-Desktop-Enabled", "X-Brand-Desktop-Devices-Limit", "X-Brand-Desktop-Devices-Used", func(s string) string { return s }},
	} {
		h.Set(ns.on, "true")
		set := func(key, v string) {
			if v != "" {
				h.Set(ns.prefix+key, v)
			}
		}
		set("Name", ns.text(name))
		set("User-Display-Name", ns.text(user))
		set("Accent-Color", cfg.App.Accent)
		set("Logo-URL", cfg.App.LogoURL)
		set("Support-URL", cfg.SupportURL)
		set("Bot-URL", botURL)
		set("Renew-URL", page)
		if limit > 0 {
			h.Set(ns.limit, strconv.FormatInt(limit, 10))
			if devices >= 0 {
				h.Set(ns.used, strconv.FormatInt(devices, 10))
			}
		}
	}
}

// ValidLink says whether s is a link the apps open: https with a host, or tg://, without
// spaces or control characters.
func ValidLink(s string, tg bool) bool {
	if strings.ContainsFunc(s, func(r rune) bool { return r <= ' ' || r == 0x7f }) {
		return false
	}
	u, err := url.Parse(s)
	if err != nil {
		return false
	}
	switch u.Scheme {
	case "https":
		return u.Host != ""
	case "tg":
		return tg && (u.Host != "" || u.Opaque != "")
	}
	return false
}

// BotURL is the bot's address without a start code: the apps keep headers for days, and
// a code ties a subscription to whoever opens it first.
func BotURL(link string) string {
	u, err := url.Parse(link)
	if err != nil || u.Scheme != "https" || u.Host == "" {
		return ""
	}
	return "https://" + u.Host + u.Path
}

// PageURL is the address the request came to, which is also the subscription page: for a
// panel without an address of its own (Config.SubBase). The path is the one the app asked
// for: the handler sits behind a prefix that is cut off.
func PageURL(r *http.Request) string {
	if r.Host == "" {
		return ""
	}
	path := r.URL.EscapedPath()
	if u, err := url.ParseRequestURI(r.RequestURI); err == nil && u.EscapedPath() != "" {
		path = u.EscapedPath()
	}
	return "https://" + r.Host + path
}

func asciiOrBase64(s string) string {
	for i := 0; i < len(s); i++ {
		if s[i] >= utf8.RuneSelf {
			return "base64:" + base64.StdEncoding.EncodeToString([]byte(s))
		}
	}
	return s
}

// cut keeps at most n letters of s, without control characters.
func cut(s string, n int) string {
	s = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, strings.TrimSpace(s))
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n])
}
