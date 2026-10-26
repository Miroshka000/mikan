package serve

import (
	"net/http"
	"slices"
	"testing"

	"mikan/internal/sub/apps"
)

func TestFormat(t *testing.T) {
	cases := map[string]string{
		"clash-verge/v2.2.3":                          FormatClash,
		"FlClash/v0.8.80 clash-meta":                  FormatClash,
		"mihomo/1.19.31":                              FormatClash,
		"Stash/3.1.1 Clash/1.9.0":                     FormatClash,
		"Happ/3.4.1":                                  FormatURI,
		"SFA (sing-box 1.14.2; language en_US)":       FormatSingBox,
		"SFM (sing-box 1.12.20; language ru_RU)":      FormatSingBox,
		"SFI (sing-box 1.11.4; language en_US)":       FormatURI, // a config of 1.12 would not load
		"Karing/1.2.26 platform/tvos sing-box 1.14.0": FormatURI, // routes by its own settings
		"v2rayNG/1.10.2":                              FormatURI,
		"v2RayTun/Android":                            FormatURI,
		"Streisand/1.6":                               FormatURI,
		"HiddifyNext/2.5.7":                           FormatURI,
		"curl/8.9":                                    FormatURI,
	}
	for ua, want := range cases {
		if got := Format(ua, "*/*", ""); got != want {
			t.Errorf("%q → %s, want %s", ua, got, want)
		}
	}
	if Format("Mozilla/5.0 (iPhone)", "text/html,application/xhtml+xml", "") != FormatHTML {
		t.Error("browser must get the page")
	}
	if Format("Happ/3", "", "clash") != FormatClash || Format("HiddifyNext/4.1.1", "", "singbox") != FormatSingBox {
		t.Error("?format= must override the User-Agent")
	}
}

// Format and apps.DetectApp read the same User-Agent, and must agree about an app: Hiddify
// says "like ClashMeta" and used to be given a Clash profile; Prizrak and its kin are mihomo
// apps that were given a link list.
func TestFormatFollowsDetectApp(t *testing.T) {
	cases := []struct {
		ua     string
		format string
		family apps.Family
	}{
		{"HiddifyNext/2.5.7 (android) like ClashMeta v2ray sing-box", FormatURI, apps.FamilySingBox},
		{"Prizrak-Box/1.4.2 (windows)", FormatClash, apps.FamilyMihomo},
		{"Flowvy/0.9 mihomo/1.19.31", FormatClash, apps.FamilyMihomo},
		{"Murge/2.0", FormatClash, apps.FamilyMihomo},
		{"Rabbit/1.1 (macos)", FormatClash, apps.FamilyMihomo},
		{"clash-verge/v2.2.3", FormatClash, apps.FamilyMihomo},
		{"Koala-Clash/1.4.0", FormatClash, apps.FamilyMihomo},
		{"Stash/3.1.1 Clash/1.9.0", FormatClash, apps.FamilyStash},
		{"Happ/3.4.1", FormatURI, apps.FamilyXray},
		{"v2rayNG/1.10.2", FormatURI, apps.FamilyXray},
		{"Karing/1.2.0", FormatURI, apps.FamilySingBox},
		{"NekoBox/1.3.7", FormatURI, apps.FamilySingBox},
		{"sing-box 1.11", FormatURI, apps.FamilySingBox},
		{"Streisand/1.6", FormatURI, apps.FamilyOther},
		{"Shadowrocket/2.2.60", FormatURI, apps.FamilyOther},
		{"curl/8.9", FormatURI, apps.FamilyOther},
	}
	for _, c := range cases {
		if got := apps.DetectApp(c.ua).Family; got != c.family {
			t.Errorf("DetectApp(%q) = %s, want %s", c.ua, got, c.family)
		}
		if got := Format(c.ua, "*/*", ""); got != c.format {
			t.Errorf("Format(%q) = %s, want %s", c.ua, got, c.format)
		}
	}
	// Whatever DetectApp says is a core's family decides: never Clash for an Xray or sing-box app.
	for _, c := range cases {
		switch apps.DetectApp(c.ua).Family {
		case apps.FamilyXray, apps.FamilySingBox:
			if Format(c.ua, "*/*", "") == FormatClash {
				t.Errorf("%q: a Clash profile for a %s app", c.ua, apps.DetectApp(c.ua).Family)
			}
		case apps.FamilyMihomo, apps.FamilyStash:
			if Format(c.ua, "*/*", "") != FormatClash {
				t.Errorf("%q: no Clash profile for a mihomo app", c.ua)
			}
		}
	}
	if Format("Mozilla/5.0", "text/html", "") != FormatHTML || Format("Happ/3.4.1", "text/html", "") != FormatURI {
		t.Error("a browser gets the page, an app that names itself gets its format")
	}
}

// pinnedUAs is what the handler makes of a User-Agent today, beside apps' own detectors
// (apps.TestDetectionIsPinned has the same User-Agents): the format for an app's Accept
// and for a browser's, the one Resolve gives with the xray JSON switch on, the routing
// header appHeaders sets for Happ or INCY, and whether the announcement keeps its lines.
// It pins the answers, not what they should be: the rows marked "looks wrong" disagree
// with another detector or with the app.
var pinnedUAs = []struct {
	ua            string
	format, page  string // Format for "*/*" and for "text/html"
	resolved      string // Resolve with Config.XrayJSON on
	routing       string // the routing header for Happ{Routing: "happ://routing/off"}
	announceLines bool   // fillAppAnnounce keeps the lines
}{
	// Looks wrong: a sing-box app for DetectApp, yet Happ's xray configs and Happ's headers.
	{"Happ/3.0 sing-box", FormatURI, FormatURI, FormatXray, "happ://routing/off", false},
	{"sing-box 1.12 mihomo", FormatURI, FormatURI, FormatURI, "", false},
	{"Inhive/1.0 like ClashMeta sing-box", FormatURI, FormatURI, FormatURI, "", false},
	// Looks wrong: v2rayN, an xray app, is given a Clash profile ("mihomo" wins in DetectApp).
	{"v2rayN/7.0.0 mihomo", FormatClash, FormatClash, FormatClash, "", false},
	{"v2rayN/7 mihomo/1.19", FormatClash, FormatClash, FormatClash, "", false},
	// Looks wrong: INCY's headers and announcement, though apps.Identify says Happ.
	{"INCY/2.1 Happ", FormatURI, FormatURI, FormatXray, "incy://routing/off", true},
	{"Stash/2.8.0 Clash/1.11.0", FormatClash, FormatClash, FormatClash, "", false},
	// The Mikan app asks as "mihomo[/core] Mikan/x" and gets Clash; a bare "Mikan/x" comes
	// only from its update check or a user's own User-Agent, and is an unknown app here.
	{"Mikan/0.1.3", FormatURI, FormatHTML, FormatURI, "", false},
	{"mihomo/1.19.32 Mikan/0.1.3", FormatClash, FormatClash, FormatClash, "", false},
	{"mihomo Mikan/1.1.1", FormatClash, FormatClash, FormatClash, "", false},
	// Looks wrong: the discontinued Clash apps get mihomo's profile; the built-in rule turns
	// them away before Format is asked.
	{"ClashforWindows/0.20.39", FormatClash, FormatClash, FormatClash, "", false},
	{"Clash for Windows/0.20.39", FormatClash, FormatClash, FormatClash, "", false},
	{"ClashForAndroid/2.5.12", FormatClash, FormatClash, FormatClash, "", false},
	{"ClashX/1.118.0 (com.west2online.ClashX; build:1.118.0; macOS 14.0.0) Alamofire/5.8.0", FormatClash, FormatClash, FormatClash, "", false},
	{"ClashX Meta/1.3.0 (com.metacubex.ClashX.meta; build:1.3.0; macOS 14.0.0) Alamofire/5.8.0", FormatClash, FormatClash, FormatClash, "", false},
	{"ShadowsocksR/2.5.6", FormatURI, FormatHTML, FormatURI, "", false},
	{"ssr/1.0", FormatURI, FormatHTML, FormatURI, "", false},
	{"", FormatURI, FormatHTML, FormatURI, "", false},
	{"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/130.0.0.0 Safari/537.36", FormatURI, FormatHTML, FormatURI, "", false},
	{"curl/8.9", FormatURI, FormatHTML, FormatURI, "", false},
	{"HAPP/3.4.1", FormatURI, FormatURI, FormatXray, "happ://routing/off", false},
	{"Happ/3.6.0/Android/abc", FormatURI, FormatURI, FormatXray, "happ://routing/off", false},
	{"INCY/2.0.8/iOS", FormatURI, FormatURI, FormatXray, "incy://routing/off", true},
	{"v2rayNG/1.10.20", FormatURI, FormatURI, FormatURI, "", false},
	{"v2RayTun/5.1", FormatURI, FormatURI, FormatURI, "", false},
	{"koala-clash/1.4.1", FormatClash, FormatClash, FormatClash, "", false},
	{"clash-verge/v2.2.3", FormatClash, FormatClash, FormatClash, "", false},
	{"HiddifyNext/2.5.7 (android) like ClashMeta v2ray sing-box", FormatURI, FormatURI, FormatURI, "", false},
	{"HiddifyNext/4.1.1 (android) like ClashMeta v2ray sing-box", FormatURI, FormatURI, FormatURI, "", false},
	{"SFA (sing-box 1.14.2; language en_US)", FormatSingBox, FormatSingBox, FormatSingBox, "", false},
	{"SFI (sing-box 1.11.4; language ru_RU)", FormatURI, FormatURI, FormatURI, "", false},
	{"Karing/1.2.26 platform/tvos sing-box 1.14.0", FormatURI, FormatURI, FormatURI, "", false},
	{"Shadowrocket/2592 CFNetwork/1568.100.1", FormatURI, FormatURI, FormatURI, "", false},
	{"FlClashX/v0.9.1 Platform/android", FormatClash, FormatClash, FormatClash, "", false},
	{"clash.meta/v1.19.32 SlothClash/0.9.4", FormatClash, FormatClash, FormatClash, "", false},
	{"husi/0.9.1 (sing-box 1.15.0-alpha.10)", FormatURI, FormatURI, FormatURI, "", false},
	{"Flowvy/0.9 mihomo/1.19.31", FormatClash, FormatClash, FormatClash, "", false},
	{"ClashMetaForAndroid/2.11.19.Meta", FormatClash, FormatClash, FormatClash, "", false},
	{"mihomo/1.19.32 ClashFest/1.2.0", FormatClash, FormatClash, FormatClash, "", false},
	{"INCY/2.1.0", FormatURI, FormatURI, FormatXray, "incy://routing/off", true},
	{"v2rayN/7.0.0", FormatURI, FormatURI, FormatURI, "", false},
	{"NekoBox/1.3.7", FormatURI, FormatURI, FormatURI, "", false},
	{"v2RayTun/Android", FormatURI, FormatURI, FormatURI, "", false},

	// A row for every word and prefix of apps.Catalog, as word+"/1.0".
	{"mikan/1.0", FormatURI, FormatHTML, FormatURI, "", false}, // a bare Mikan: see Mikan/0.1.3
	{"kabosu/1.0", FormatClash, FormatClash, FormatClash, "", false},
	{"koala/1.0", FormatClash, FormatClash, FormatClash, "", false},
	{"clashfest/1.0", FormatClash, FormatClash, FormatClash, "", false},
	{"slothclash/1.0", FormatClash, FormatClash, FormatClash, "", false},
	{"flclash/1.0", FormatClash, FormatClash, FormatClash, "", false},
	{"verge/1.0", FormatClash, FormatClash, FormatClash, "", false},
	{"clashmetaforandroid/1.0", FormatClash, FormatClash, FormatClash, "", false},
	{"mihomo/1.0", FormatClash, FormatClash, FormatClash, "", false},
	{"clash/1.0", FormatClash, FormatClash, FormatClash, "", false},
	{"prizrak/1.0", FormatClash, FormatClash, FormatClash, "", false},
	{"flowvy/1.0", FormatClash, FormatClash, FormatClash, "", false},
	{"murge/1.0", FormatClash, FormatClash, FormatClash, "", false},
	{"rabbit/1.0", FormatClash, FormatClash, FormatClash, "", false},
	{"happ/1.0", FormatURI, FormatURI, FormatXray, "happ://routing/off", false},
	{"incy/1.0", FormatURI, FormatURI, FormatXray, "incy://routing/off", true},
	{"v2raytun/1.0", FormatURI, FormatURI, FormatURI, "", false},
	{"v2rayng/1.0", FormatURI, FormatURI, FormatURI, "", false},
	{"v2rayn/1.0", FormatURI, FormatURI, FormatURI, "", false},
	{"onexray/1.0", FormatURI, FormatURI, FormatURI, "", false},
	{"v2box/1.0", FormatURI, FormatURI, FormatURI, "", false},
	{"simplexray/1.0", FormatURI, FormatURI, FormatURI, "", false},
	{"renoarx/1.0", FormatURI, FormatURI, FormatURI, "", false},
	{"hiddify/1.0", FormatURI, FormatURI, FormatURI, "", false},
	{"karing/1.0", FormatURI, FormatURI, FormatURI, "", false},
	{"nekobox/1.0", FormatURI, FormatURI, FormatURI, "", false},
	{"nekoray/1.0", FormatURI, FormatURI, FormatURI, "", false},
	{"throne/1.0", FormatURI, FormatURI, FormatURI, "", false},
	{"husi/1.0", FormatURI, FormatURI, FormatURI, "", false},
	{"sing-box/1.0", FormatURI, FormatURI, FormatURI, "", false},
	{"singbox/1.0", FormatURI, FormatURI, FormatURI, "", false},
	{"deskbox/1.0", FormatURI, FormatURI, FormatURI, "", false},
	{"inhive/1.0", FormatURI, FormatURI, FormatURI, "", false},
	{"sfa/1.0", FormatURI, FormatURI, FormatURI, "", false}, // a whole-config app that names no core gets links
	{"sfi/1.0", FormatURI, FormatURI, FormatURI, "", false},
	{"sfm/1.0", FormatURI, FormatURI, FormatURI, "", false},
	{"sft/1.0", FormatURI, FormatURI, FormatURI, "", false},
	{"stash/1.0", FormatClash, FormatClash, FormatClash, "", false},
	{"streisand/1.0", FormatURI, FormatURI, FormatURI, "", false},
	{"shadowrocket/1.0", FormatURI, FormatURI, FormatURI, "", false},
	{"foxray/1.0", FormatURI, FormatURI, FormatURI, "", false},
}

func TestFormatIsPinned(t *testing.T) {
	pinned := map[string]bool{}
	for _, c := range pinnedUAs {
		pinned[c.ua] = true
		if got := Format(c.ua, "*/*", ""); got != c.format {
			t.Errorf("Format(%q, */*) = %s, want %s", c.ua, got, c.format)
		}
		if got := Format(c.ua, "text/html,application/xhtml+xml", ""); got != c.page {
			t.Errorf("Format(%q, text/html) = %s, want %s", c.ua, got, c.page)
		}
		if got := Resolve(http.Header{"User-Agent": {c.ua}}, "", apps.Decision{}, true); got != c.resolved {
			t.Errorf("Resolve(%q) = %s, want %s", c.ua, got, c.resolved)
		}
		h := http.Header{}
		appHeaders(h, c.ua, Config{Happ: Happ{Routing: "happ://routing/off"}}, "")
		if got := h.Get("routing"); got != c.routing {
			t.Errorf("appHeaders(%q): routing %q, want %q", c.ua, got, c.routing)
		}
		if got := fillAppAnnounce("a\nb", nil, c.ua) == "a\nb"; got != c.announceLines {
			t.Errorf("fillAppAnnounce(%q) keeps the lines: %v", c.ua, got)
		}
	}
	for _, k := range apps.Catalog {
		for _, w := range slices.Concat(k.Words, k.Prefixes) {
			if !pinned[w+"/1.0"] {
				t.Errorf("%s: no row for %q in pinnedUAs", k.ID, w+"/1.0")
			}
		}
	}
}

func TestLinkPreview(t *testing.T) {
	for ua, want := range map[string]bool{"TelegramBot (like TwitterBot)": true, "WhatsApp/2.23": true, "Happ/3.4.1": false, "Mozilla/5.0": false} {
		if LinkPreview(ua) != want {
			t.Errorf("%q: %v", ua, !want)
		}
	}
}
