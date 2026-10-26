package serve

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"mikan/internal/sub/apps"
	"mikan/internal/sub/xray"
	"mikan/internal/textutil"
)

// Every header the answer to a subscription carries is set here: the traffic and term the
// apps show, the profile's name and file, the operator's page, announcement and branding,
// what Happ and INCY take beside the profile, the device binding's notices and the Mikan
// app's server groups.

// userInfoHeaders: the traffic and term apps show. With traffic packages left the total
// is what the user can reach: what is used plus what is left.
func userInfoHeaders(hd http.Header, u User, cfg Config, vars map[string]string) {
	var expire int64
	if !u.Expires.IsZero() {
		expire = u.Expires.Unix()
	}
	hd.Set("Subscription-Userinfo", "upload="+strconv.FormatInt(u.UsedUp, 10)+"; download="+strconv.FormatInt(u.UsedDown, 10)+
		"; total="+strconv.FormatInt(total(u), 10)+"; expire="+strconv.FormatInt(expire, 10))
	// Hourly: a port or target the panel changed on its own reaches clients soon.
	hd.Set("Profile-Update-Interval", "1")
	hd.Set("Profile-Title", "base64:"+base64.StdEncoding.EncodeToString([]byte(profileTitle(cfg, vars))))
	if cfg.SupportURL != "" {
		hd.Set("Support-Url", cfg.SupportURL)
	}
}

// total is the traffic the user can reach: the limit, and with packages left what is used
// plus what is left; 0 without a limit.
func total(u User) int64 {
	if u.Limit == nil {
		return 0
	}
	t := *u.Limit
	if u.Grants > 0 {
		t = max(t, u.UsedUp+u.UsedDown) + u.Grants
	}
	return t
}

// The profile's content, and for a whole profile its file: Clash apps name the profile
// after the file, so the file takes the name of Profile-Title.
const (
	contentYAML  = "text/yaml; charset=utf-8"
	contentJSON  = "application/json; charset=utf-8"
	contentLinks = "text/plain; charset=utf-8"
)

// contentType is the type of a body in format.
func contentType(hd http.Header, format string) {
	switch format {
	case FormatClash:
		hd.Set("Content-Type", contentYAML)
	case FormatSingBox, FormatXray:
		hd.Set("Content-Type", contentJSON)
	default:
		hd.Set("Content-Type", contentLinks)
	}
}

// profileHeaders: the type of a profile in format and, for the formats of a file, its
// name made of the profile's title.
func profileHeaders(hd http.Header, format, title string) {
	contentType(hd, format)
	switch format {
	case FormatClash:
		hd.Set("Content-Disposition", "attachment; filename*=UTF-8''"+url.PathEscape(fileName(title))+".yaml")
	case FormatSingBox:
		hd.Set("Content-Disposition", "attachment; filename*=UTF-8''"+url.PathEscape(fileName(title))+".json")
	}
}

// stubHeaders are what the apps that read the device binding's headers show for reason.
func stubHeaders(hd http.Header, reason error) {
	switch {
	case errors.Is(reason, ErrNoHWID):
		hd.Set("X-Hwid-Not-Supported", "true")
	case errors.Is(reason, ErrDeviceLimit):
		hd.Set("X-Hwid-Max-Devices-Reached", "true")
		hd.Set("X-Hwid-Limit", "true") // v2RayTun shows the notice only with it
	}
}

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
func OperatorHeaders(h http.Header, u User, cfg Config, page, botURL string, devices int64) {
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
	if cfg.Binding && u.Devices != nil && u.Places {
		limit = *u.Devices
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

// The links the apps open from a subscription's headers: https with a host, or tg:// for a
// support or announcement link; a logo is a picture, https alone.
var (
	AppLinks  = textutil.LinkRules{TG: true, Opaque: true}
	LogoLinks = textutil.LinkRules{}
)

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

// Happ is what the panel tells Happ beyond the profile (Settings → Subscription → Happ),
// per its provider docs (happ.su/main/dev-docs).
type Happ struct {
	// Routing is a happ://routing/... link: Happ adds the routing profile with the
	// subscription ("onadd" also turns it on). Sent to Happ and INCY only.
	// xray.HappRoutingAuto: the panel makes it of the Clash profile's rules.
	Routing string
	// ProviderID ties the subscription to the admin's account at happ-proxy.com. Happ takes
	// the provider headers (HideSettings) only with it.
	ProviderID   string
	HideSettings bool
}

var happProvider = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// ValidHappProviderID: the ids happ-proxy.com gives out are short words of letters, digits,
// "-" and "_"; anything else would not survive a header.
func ValidHappProviderID(s string) bool { return happProvider.MatchString(s) }

// appHeaders are what Happ and INCY take beside the profile: the routing profile, of the
// own Clash profile clash when there is one, and Happ's provider headers. Other apps never
// get them.
func appHeaders(hd http.Header, ua string, cfg Config, clash string) {
	switch {
	case apps.IsINCY(ua):
		// INCY reads Happ's routing profile from the same header (docs.incy.cc/en/routing),
		// and none of Happ's provider headers.
		if routing := cfg.Happ.Routing; routing == xray.HappRoutingAuto {
			hd.Set("routing", xray.IncyLink(happAutoRouting(cfg, clash)))
		} else if routing != "" {
			hd.Set("routing", xray.IncyLink(routing))
		}
	case apps.IsHapp(ua):
		happ := cfg.Happ
		if happ.Routing == xray.HappRoutingAuto {
			happ.Routing = happAutoRouting(cfg, clash)
		}
		happHeaders(hd, happ)
	}
}

// happHeaders are the provider headers Happ reads.
func happHeaders(h http.Header, c Happ) {
	if c.Routing != "" {
		h.Set("routing", c.Routing)
	}
	if c.ProviderID == "" {
		return
	}
	h.Set("providerid", c.ProviderID)
	if c.HideSettings {
		h.Set("hide-settings", "1")
	}
}

// The server groups as the Mikan app (getmikan/MikanApp) shows them: the app asks with
// "groups" in X-Mikan-Features, and the answer carries X-Mikan-Groups beside the profile.
// An app that does not ask gets the same subscription as before, header for header: the
// groups never become fake servers or proxy groups that other apps would show.
const (
	// FeaturesHeader lists, comma-separated, the extensions the Mikan app shows.
	FeaturesHeader = "X-Mikan-Features"
	// GroupsHeader is "base64:" and the JSON of AppGroups.
	GroupsHeader = "X-Mikan-Groups"

	featureGroups = "groups"
	// appGroupsMax is the most X-Mikan-Groups takes. A reverse proxy in front of the panel
	// fails an answer whose headers outgrow its buffer (nginx: 4 or 8 KB for all of them by
	// default), so past this the server names go and the groups' traffic stays.
	appGroupsMax = 1536
)

// wantsFeature says whether the app lists f in X-Mikan-Features.
func wantsFeature(h http.Header, f string) bool {
	for _, v := range h.Values(FeaturesHeader) {
		for _, x := range strings.Split(v, ",") {
			if strings.EqualFold(strings.TrimSpace(x), f) {
				return true
			}
		}
	}
	return false
}

// header is the X-Mikan-Groups value of g; "" without groups. Past appGroupsMax the server
// names go, and the groups that are names alone (on the main traffic) with them; then the
// last groups, should a great many still not fit.
func (g AppGroups) header() string {
	if len(g.Groups) == 0 {
		return ""
	}
	encode := func() string {
		b, _ := json.Marshal(g)
		return "base64:" + base64.StdEncoding.EncodeToString(b)
	}
	v := encode()
	if len(v) <= appGroupsMax {
		return v
	}
	var kept []AppGroup
	for _, x := range g.Groups {
		if x.Mode != GroupMain {
			x.Proxies = nil
			kept = append(kept, x)
		}
	}
	g.Groups = kept
	for v = encode(); len(v) > appGroupsMax && len(g.Groups) > 0; v = encode() {
		g.Groups = g.Groups[:len(g.Groups)-1]
	}
	if len(g.Groups) == 0 {
		return ""
	}
	return v
}

// ruleHeaders are the headers a response rule adds, over the panel's own: the admin may
// tell a client something the panel does not (Check keeps out the ones that would break
// the answer).
func ruleHeaders(hd http.Header, headers []apps.Header) {
	for _, h := range headers {
		hd.Set(h.Name, h.Value)
	}
}

// htmlPolicy keeps the admin's page what it is, a page to read: no script runs, nothing is
// embedded or sent anywhere, so it cannot be turned against the subscriber who opens it on
// the panel's own address.
const htmlPolicy = "default-src 'none'; img-src https: data:; style-src 'unsafe-inline'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'"

// htmlHeaders are what the admin's page of a response rule is served with. They are set
// after the rule's own headers, which must not weaken the policy.
func htmlHeaders(hd http.Header) {
	hd.Set("Content-Type", "text/html; charset=utf-8")
	hd.Set("Content-Security-Policy", htmlPolicy)
	hd.Set("X-Content-Type-Options", "nosniff")
	hd.Set("Referrer-Policy", "no-referrer")
}
