package apps

import (
	"net/http"
	"reflect"
	"slices"
	"testing"

	"mikan/internal/proto"
)

func TestDetectApp(t *testing.T) {
	for ua, want := range map[string]App{
		"koala-clash/1.4.1":                    {Family: FamilyMihomo, Core: Version{1, 19, 30}},
		"koala-clash/1.3.1":                    {Family: FamilyMihomo, Core: Version{1, 19, 23}},
		"mihomo/1.19.31":                       {Family: FamilyMihomo, Core: Version{1, 19, 31}},
		"clash.meta/v1.19.12":                  {Family: FamilyMihomo, Core: Version{1, 19, 12}},
		"clash-verge/v2.2.3":                   {Family: FamilyMihomo},
		"FlClashX/v0.9.1 Platform/android":     {Family: FamilyMihomo},
		"ClashMetaForAndroid/2.11.19.Meta":     {Family: FamilyMihomo},
		"mihomo/1.19.32 ClashFest/1.2.0":       {Family: FamilyMihomo, Core: Version{1, 19, 32}},
		"mihomo ClashFest/1.2.0":               {Family: FamilyMihomo}, // a build off a release tag names no core
		"clash.meta/v1.19.32 SlothClash/0.9.4": {Family: FamilyMihomo, Core: Version{1, 19, 32}},
		"clash.meta/mihomo SlothClash/0.9.4":   {Family: FamilyMihomo},
		"mihomo/1.19.32 Mikan/0.1.3":           {Family: FamilyMihomo, Core: Version{1, 19, 32}},
		"Mikan/0.1.3":                          {Family: FamilyMihomo}, // its update check, or a User-Agent of the user's own
		"mihomo/1.19.33 Kabosu/1.0.0":          {Family: FamilyMihomo, Core: Version{1, 19, 33}},
		"kabosu/1.0.0":                         {Family: FamilyMihomo}, // a desktop app that names no core still gets Clash
		"Happ/3.4.1":                           {Family: FamilyXray},
		"v2rayNG/1.10.20":                      {Family: FamilyXray},
		"INCY/2.1.0":                           {Family: FamilyXray},
		"HiddifyNext/2.5.7 (android) like ClashMeta v2ray sing-box":  {Family: FamilySingBox, Legacy: true},
		"HiddifyNext/4.1.1 (android) like ClashMeta v2ray sing-box":  {Family: FamilySingBox},
		"HiddifyNextX/4.0.4 (windows) like ClashMeta v2ray sing-box": {Family: FamilySingBox},
		"Hiddify":                                     {Family: FamilySingBox, Legacy: true},
		"karing/1.1.4.920":                            {Family: FamilySingBox},
		"SFA/1.12.4":                                  {Family: FamilySingBox, Whole: true},
		"SFA (sing-box 1.14.2; language en_US)":       {Family: FamilySingBox, Core: Version{1, 14, 2}, Whole: true},
		"SFI (sing-box 1.11.4; language ru_RU)":       {Family: FamilySingBox, Core: Version{1, 11, 4}, Whole: true},
		"Karing/1.2.26 platform/tvos sing-box 1.14.0": {Family: FamilySingBox, Core: Version{1, 14, 0}},
		"husi/0.9.1 (sing-box 1.15.0-alpha.10)":       {Family: FamilySingBox, Core: Version{1, 15, 0}},
		"Stash/2.8.0 Clash/1.11.0":                    {Family: FamilyStash},
		"Shadowrocket/2592 CFNetwork/1568.100.1":      {Family: FamilyOther},
		"Streisand/1.6.44":                            {Family: FamilyOther},
		"":                                            {Family: FamilyOther},
	} {
		if got := DetectApp(ua); got != want {
			t.Errorf("%q: %+v, want %+v", ua, got, want)
		}
	}
}

func TestSupports(t *testing.T) {
	xhttp := proto.Needs{Type: "vless", Transport: "xhttp"}
	pq := proto.Needs{Type: "vless", Transport: "xhttp", Encryption: true}
	vision := proto.Needs{Type: "vless", Transport: "tcp"}
	tuic, anytls := proto.Needs{Type: "tuic"}, proto.Needs{Type: "anytls"}
	tt, sq := proto.Needs{Type: "trusttunnel"}, proto.Needs{Type: "shadowquic"}
	ss, snell := proto.Needs{Type: "shadowsocks"}, proto.Needs{Type: "snell"}
	cases := []struct {
		ua   string
		yes  []proto.Needs
		no   []proto.Needs
		note string
	}{
		{"mihomo/1.19.31", []proto.Needs{xhttp, pq, vision, tuic, anytls, tt, sq, ss, snell}, nil, "a current core gets everything"},
		{"koala-clash/1.3.1", []proto.Needs{xhttp, pq, tt}, []proto.Needs{sq}, "ShadowQUIC came with mihomo 1.19.29"},
		{"clash.meta/v1.19.12", []proto.Needs{vision, tuic, anytls}, []proto.Needs{xhttp, pq, tt}, "an old core: no XHTTP, PQ or newer types"},
		{"clash-verge/v2.2.3", []proto.Needs{xhttp, pq, tuic, anytls, ss, snell}, []proto.Needs{tt, sq}, "an unknown core: the newer types would break the profile"},
		{"Happ/3.4.1", []proto.Needs{xhttp, pq, vision, ss, {Type: "hysteria2"}}, []proto.Needs{tuic, anytls, tt, snell}, "Xray has no TUIC or AnyTLS"},
		{"karing/1.1.4", []proto.Needs{vision, tuic, anytls, ss}, []proto.Needs{xhttp, pq, tt}, "sing-box has no XHTTP or VLESS Encryption"},
		{"HiddifyNext/2.5.7 like ClashMeta", []proto.Needs{vision, tuic}, []proto.Needs{anytls, xhttp}, "Hiddify's sing-box predates AnyTLS"},
		{"HiddifyNext/4.1.1 (android) like ClashMeta v2ray sing-box", []proto.Needs{vision, tuic, anytls}, []proto.Needs{xhttp, pq}, "Hiddify 4 runs sing-box 1.13"},
		{"Stash/2.8.0", []proto.Needs{vision, tuic, snell}, []proto.Needs{tt, sq}, "Stash reads its own flavor of the newer types"},
		{"Shadowrocket/2592", []proto.Needs{xhttp, tuic, anytls, ss}, []proto.Needs{tt, snell}, "links only"},
	}
	for _, c := range cases {
		app := DetectApp(c.ua)
		for _, n := range c.yes {
			if !app.Supports(n) {
				t.Errorf("%s (%s): must get %+v", c.ua, c.note, n)
			}
		}
		for _, n := range c.no {
			if app.Supports(n) {
				t.Errorf("%s (%s): must not get %+v", c.ua, c.note, n)
			}
		}
	}
	if got := For(tt); !reflect.DeepEqual(got, []Family{FamilyMihomo}) {
		t.Errorf("TrustTunnel apps: %v", got)
	}
	if got := For(ss); !reflect.DeepEqual(got, []Family{FamilyMihomo, FamilyXray, FamilySingBox, FamilyStash, FamilyOther}) {
		t.Errorf("Shadowsocks apps: %v", got)
	}
}

// The admin's own Clash profile and the rule lists go to mihomo apps and to the panel's
// preview (a zero App) only: Stash reads its own flavor, the rest are not Clash apps.
func TestTakesOwnProfile(t *testing.T) {
	for _, c := range []struct {
		app  App
		want bool
	}{
		{App{}, true},
		{App{Family: FamilyMihomo}, true},
		{App{Family: FamilyMihomo, Core: Version{1, 19, 12}}, true},
		{App{Family: FamilyStash}, false},
		{App{Family: FamilyXray}, false},
		{App{Family: FamilySingBox}, false},
		{App{Family: FamilyOther}, false},
	} {
		if got := c.app.TakesOwnProfile(); got != c.want {
			t.Errorf("%+v: %v, want %v", c.app, got, c.want)
		}
	}
}

// Each mihomo gate holds from its first release on, and the types newer than the
// core of 2024 stay off a core that does not name its version.
func TestMihomoVersionGates(t *testing.T) {
	xhttp := proto.Needs{Type: "vless", Transport: "xhttp"}
	enc := proto.Needs{Type: "vless", Transport: "tcp", Encryption: true}
	cases := []struct {
		ua   string
		n    proto.Needs
		want bool
	}{
		{"mihomo/1.19.21", xhttp, false},
		{"mihomo/1.19.22", xhttp, true},
		{"mihomo/1.19.12", enc, false},
		{"mihomo/1.19.13", enc, true},
		{"mihomo/1.19.2", proto.Needs{Type: "anytls"}, false},
		{"mihomo/1.19.3", proto.Needs{Type: "anytls"}, true},
		{"clash-verge/v2.2.3", proto.Needs{Type: "anytls"}, true},
		{"clash-verge/v2.2.3", proto.Needs{Type: "mieru"}, false},
		{"mihomo/1.18.10", proto.Needs{Type: "mieru"}, false},
		{"mihomo/1.19.0", proto.Needs{Type: "mieru"}, true},
	}
	for _, c := range cases {
		if got := DetectApp(c.ua).Supports(c.n); got != c.want {
			t.Errorf("%s with %+v: %v, want %v", c.ua, c.n, got, c.want)
		}
	}
}

// Gecko goes only to mihomo apps that name a core of 1.19.26 or later: an older core
// fails the whole profile on it, and the other cores got it too late to tell apart.
func TestGeckoOnlyForNewMihomo(t *testing.T) {
	g := proto.Needs{Type: "hysteria2", Gecko: true}
	for ua, want := range map[string]bool{
		"mihomo/1.19.26":                       true,
		"clash.meta/v1.19.31":                  true,
		"koala-clash/1.4.2":                    true,
		"mihomo/1.19.32 ClashFest/1.2.0":       true,
		"clash.meta/v1.19.32 SlothClash/0.9.4": true,
		"mihomo/1.19.25":                       false,
		"koala-clash/1.2.0":                    false,
		"clash-verge/v2.4.0":                   false, // the core is not named
		"Happ/3.4.1":                           false,
		"Hiddify/2.5.7":                        false,
		"Karing/1.1":                           false,
		"Stash/3.0":                            false,
		"Shadowrocket/2070 CFNet":              false,
	} {
		if got := DetectApp(ua).Supports(g); got != want {
			t.Errorf("%s: %v, want %v", ua, got, want)
		}
	}
	if !DetectApp("Happ/3.4.1").Supports(proto.Needs{Type: "hysteria2"}) {
		t.Error("plain Hysteria2 must still reach Xray apps")
	}
	if fams := For(g); len(fams) != 1 || fams[0] != FamilyMihomo {
		t.Errorf("apps for Gecko: %v", fams)
	}
}

// XHTTP that uploads with GET goes to apps that send GET: mihomo from 1.19.32 and the Xray
// apps; an older core would send POST, which the CDN in front drops.
func TestGetUplinkOnlyForAppsThatSendIt(t *testing.T) {
	n := proto.Needs{Type: "vless", Transport: "xhttp", GetUplink: true}
	for ua, want := range map[string]bool{
		"mihomo/1.19.32":      true,
		"clash.meta/v1.19.33": true,
		"mihomo/1.19.31":      false,
		"clash-verge/v2.4.0":  false, // the core is not named
		"Happ/3.4.1":          true,
		"Stash/3.0":           false,
		"Hiddify/2.5.7":       false,
	} {
		if got := DetectApp(ua).Supports(n); got != want {
			t.Errorf("%s: %v, want %v", ua, got, want)
		}
	}
}

func TestHappAndINCY(t *testing.T) {
	if !Named("happ", "Happ/3.6.0/Android/abc") || Named("happ", "v2RayTun/5.1") || Named("happ", "") {
		t.Fatal("Named happ")
	}
	if !Named("incy", "INCY/2.0.8/iOS") || Named("incy", "Happ/3.6.0") || Named("incy", "") {
		t.Fatal("Named incy")
	}
	if Named("nosuchapp", "nosuchapp/1.0") {
		t.Fatal("Named for an id the catalog does not have")
	}
}

// pinnedUAs is what every detector of the package says of a User-Agent today: DetectApp,
// Identify, Named for happ and incy, TakesXrayJSON and the built-in rules of a Responder. It pins
// the answers, not what they should be: a refactor of the tables of words must leave every
// row as it is, and a row that changes on purpose is changed here with a reason. The rows
// marked "looks wrong" are answers that disagree with another detector or with the app.
var pinnedUAs = []struct {
	ua             string
	app            App
	id             string // Identify's app; "" for none
	happ, incy, xj bool   // Named happ, Named incy, TakesXrayJSON
	rule           string // the built-in rule that decides; "" for none
}{
	// Looks wrong: Happ's word is on its xray app, DetectApp makes a sing-box app of it.
	{"Happ/3.0 sing-box", App{Family: FamilySingBox}, "happ", true, false, true, ""},
	// Looks wrong: a sing-box app for DetectApp, the generic mihomo for Identify (it tries
	// the generic entries in the catalog's order, mihomo before sing-box).
	{"sing-box 1.12 mihomo", App{Family: FamilySingBox}, "mihomo", false, false, false, ""},
	// Looks wrong: Inhive is a sing-box app of the catalog, "ClashMeta" makes it mihomo for Identify.
	{"Inhive/1.0 like ClashMeta sing-box", App{Family: FamilySingBox}, "mihomo", false, false, false, ""},
	// Looks wrong: v2rayN for Identify, a mihomo app for DetectApp (mihomo is tried before xray).
	{"v2rayN/7.0.0 mihomo", App{Family: FamilyMihomo}, "v2rayn", false, false, false, ""},
	// The same, and "mihomo/1.19" names no core: the version wants three numbers.
	{"v2rayN/7 mihomo/1.19", App{Family: FamilyMihomo}, "v2rayn", false, false, false, ""},
	// Looks wrong: Happ for Identify (happ comes first in the catalog), INCY for the
	// headers and the announcement (INCY is asked first there).
	{"INCY/2.1 Happ", App{Family: FamilyXray}, "happ", true, true, true, ""},
	{"Stash/2.8.0 Clash/1.11.0", App{Family: FamilyStash}, "stash", false, false, false, ""},
	// The Mikan app asks for a subscription as "mihomo[/core] Mikan/x", the mihomo on
	// purpose (MikanApp's SubscriptionOverrides.buildDefaultUserAgent). A bare "Mikan/x"
	// comes from its update check or a user's own User-Agent, and is the Clash app too.
	{"Mikan/0.1.3", App{Family: FamilyMihomo}, "mikan", false, false, false, ""},
	{"mihomo/1.19.32 Mikan/0.1.3", App{Family: FamilyMihomo, Core: Version{1, 19, 32}}, "mikan", false, false, false, ""},
	{"mihomo Mikan/1.1.1", App{Family: FamilyMihomo}, "mikan", false, false, false, ""},
	// The discontinued Clash apps (the Clash Premium core, not mihomo) are apps of their own,
	// so the tester and the log name them; the built-in rule turns them away.
	{"ClashforWindows/0.20.39", App{Family: FamilyMihomo}, "cfw", false, false, false, RuleOld},
	{"Clash for Windows/0.20.39", App{Family: FamilyMihomo}, "cfw", false, false, false, RuleOld},
	{"ClashForAndroid/2.5.12", App{Family: FamilyMihomo}, "cfa", false, false, false, RuleOld},
	{"ClashX/1.118.0 (com.west2online.ClashX; build:1.118.0; macOS 14.0.0) Alamofire/5.8.0", App{Family: FamilyMihomo}, "clashx", false, false, false, RuleOld},
	{"ClashX Meta/1.3.0 (com.metacubex.ClashX.meta; build:1.3.0; macOS 14.0.0) Alamofire/5.8.0", App{Family: FamilyMihomo}, "clashxmeta", false, false, false, ""},
	// The rule and the catalog read the same separators: a ClashX Meta is served whichever it
	// writes, and a name with mixed ones is neither app (it is the generic Clash).
	{"ClashX-Meta/1.4.0", App{Family: FamilyMihomo}, "clashxmeta", false, false, false, ""},
	{"ClashX.Meta/1.4.0", App{Family: FamilyMihomo}, "clashxmeta", false, false, false, ""},
	{"ClashX%20Meta/1.4.0 CFNetwork/1568.100.1 Darwin/24.0.0", App{Family: FamilyMihomo}, "clashxmeta", false, false, false, ""},
	{"Clash-for-Windows/0.20.39", App{Family: FamilyMihomo}, "cfw", false, false, false, RuleOld},
	{"Clash for Android/2.5.12", App{Family: FamilyMihomo}, "cfa", false, false, false, RuleOld},
	{"Clash for-Windows/0.20.39", App{Family: FamilyMihomo}, "mihomo", false, false, false, ""},
	{"ShadowsocksR/2.5.6", App{Family: FamilyOther}, "", false, false, false, RuleSSR},
	{"ssr/1.0", App{Family: FamilyOther}, "", false, false, false, RuleSSR},
	{"", App{Family: FamilyOther}, "", false, false, false, ""},
	{"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/130.0.0.0 Safari/537.36", App{Family: FamilyOther}, "", false, false, false, ""},
	{"curl/8.9", App{Family: FamilyOther}, "", false, false, false, ""},
	{"HAPP/3.4.1", App{Family: FamilyXray}, "happ", true, false, true, ""},
	{"Happ/3.6.0/Android/abc", App{Family: FamilyXray}, "happ", true, false, true, ""},
	{"INCY/2.0.8/iOS", App{Family: FamilyXray}, "incy", false, true, true, ""},
	{"v2rayNG/1.10.20", App{Family: FamilyXray}, "v2rayng", false, false, false, ""},
	{"v2RayTun/5.1", App{Family: FamilyXray}, "v2raytun", false, false, false, ""},
	{"koala-clash/1.4.1", App{Family: FamilyMihomo, Core: Version{1, 19, 30}}, "koala", false, false, false, ""},
	{"clash-verge/v2.2.3", App{Family: FamilyMihomo}, "verge", false, false, false, ""},
	{"HiddifyNext/2.5.7 (android) like ClashMeta v2ray sing-box", App{Family: FamilySingBox, Legacy: true}, "hiddify", false, false, false, ""},
	{"HiddifyNext/4.1.1 (android) like ClashMeta v2ray sing-box", App{Family: FamilySingBox}, "hiddify", false, false, false, ""},
	{"SFA (sing-box 1.14.2; language en_US)", App{Family: FamilySingBox, Core: Version{1, 14, 2}, Whole: true}, "singbox", false, false, false, ""},
	{"SFI (sing-box 1.11.4; language ru_RU)", App{Family: FamilySingBox, Core: Version{1, 11, 4}, Whole: true}, "singbox", false, false, false, ""},
	{"Karing/1.2.26 platform/tvos sing-box 1.14.0", App{Family: FamilySingBox, Core: Version{1, 14, 0}}, "karing", false, false, false, ""},
	{"Shadowrocket/2592 CFNetwork/1568.100.1", App{Family: FamilyOther}, "shadowrocket", false, false, false, ""},
	// FlClashX is a current mihomo app: RuleOld's ClashX is a word of its own, not part of it.
	{"FlClashX/v0.9.1 Platform/android", App{Family: FamilyMihomo}, "flclash", false, false, false, ""},
	{"clash.meta/v1.19.32 SlothClash/0.9.4", App{Family: FamilyMihomo, Core: Version{1, 19, 32}}, "slothclash", false, false, false, ""},
	{"husi/0.9.1 (sing-box 1.15.0-alpha.10)", App{Family: FamilySingBox, Core: Version{1, 15, 0}}, "husi", false, false, false, ""},
	{"Flowvy/0.9 mihomo/1.19.31", App{Family: FamilyMihomo, Core: Version{1, 19, 31}}, "mihomo", false, false, false, ""},
	{"ClashMetaForAndroid/2.11.19.Meta", App{Family: FamilyMihomo}, "clashmeta", false, false, false, ""},
	{"mihomo/1.19.32 ClashFest/1.2.0", App{Family: FamilyMihomo, Core: Version{1, 19, 32}}, "clashfest", false, false, false, ""},
	{"INCY/2.1.0", App{Family: FamilyXray}, "incy", false, true, true, ""},
	{"v2rayN/7.0.0", App{Family: FamilyXray}, "v2rayn", false, false, false, ""},
	{"NekoBox/1.3.7", App{Family: FamilySingBox}, "nekobox", false, false, false, ""},
	{"v2RayTun/Android", App{Family: FamilyXray}, "v2raytun", false, false, false, ""},

	// A row for every word and prefix of the Catalog, as word+"/1.0": TestDetectionIsPinned
	// fails for one that has none, so a new entry is pinned when it is added.
	{"mikan/1.0", App{Family: FamilyMihomo}, "mikan", false, false, false, ""},
	{"kabosu/1.0", App{Family: FamilyMihomo}, "kabosu", false, false, false, ""},
	{"koala/1.0", App{Family: FamilyMihomo}, "koala", false, false, false, ""},
	{"clashfest/1.0", App{Family: FamilyMihomo}, "clashfest", false, false, false, ""},
	{"slothclash/1.0", App{Family: FamilyMihomo}, "slothclash", false, false, false, ""},
	{"flclash/1.0", App{Family: FamilyMihomo}, "flclash", false, false, false, ""},
	{"verge/1.0", App{Family: FamilyMihomo}, "verge", false, false, false, ""},
	{"clashmetaforandroid/1.0", App{Family: FamilyMihomo}, "clashmeta", false, false, false, ""},
	{"clashx meta/1.0", App{Family: FamilyMihomo}, "clashxmeta", false, false, false, ""},
	{"clashxmeta/1.0", App{Family: FamilyMihomo}, "clashxmeta", false, false, false, ""},
	{"clashx-meta/1.0", App{Family: FamilyMihomo}, "clashxmeta", false, false, false, ""},
	{"clashx.meta/1.0", App{Family: FamilyMihomo}, "clashxmeta", false, false, false, ""},
	{"clashx%20meta/1.0", App{Family: FamilyMihomo}, "clashxmeta", false, false, false, ""},
	{"clash for windows/1.0", App{Family: FamilyMihomo}, "cfw", false, false, false, RuleOld},
	{"clashforwindows/1.0", App{Family: FamilyMihomo}, "cfw", false, false, false, RuleOld},
	{"clash-for-windows/1.0", App{Family: FamilyMihomo}, "cfw", false, false, false, RuleOld},
	{"clash.for.windows/1.0", App{Family: FamilyMihomo}, "cfw", false, false, false, RuleOld},
	{"clash%20for%20windows/1.0", App{Family: FamilyMihomo}, "cfw", false, false, false, RuleOld},
	{"clashforandroid/1.0", App{Family: FamilyMihomo}, "cfa", false, false, false, RuleOld},
	{"clash for android/1.0", App{Family: FamilyMihomo}, "cfa", false, false, false, RuleOld},
	{"clash-for-android/1.0", App{Family: FamilyMihomo}, "cfa", false, false, false, RuleOld},
	{"clash.for.android/1.0", App{Family: FamilyMihomo}, "cfa", false, false, false, RuleOld},
	{"clash%20for%20android/1.0", App{Family: FamilyMihomo}, "cfa", false, false, false, RuleOld},
	{"clashx/1.0", App{Family: FamilyMihomo}, "clashx", false, false, false, RuleOld},
	{"mihomo/1.0", App{Family: FamilyMihomo}, "mihomo", false, false, false, ""},
	{"clash/1.0", App{Family: FamilyMihomo}, "mihomo", false, false, false, ""},
	{"prizrak/1.0", App{Family: FamilyMihomo}, "mihomo", false, false, false, ""},
	{"flowvy/1.0", App{Family: FamilyMihomo}, "mihomo", false, false, false, ""},
	{"murge/1.0", App{Family: FamilyMihomo}, "mihomo", false, false, false, ""},
	{"rabbit/1.0", App{Family: FamilyMihomo}, "mihomo", false, false, false, ""},
	{"happ/1.0", App{Family: FamilyXray}, "happ", true, false, true, ""},
	{"incy/1.0", App{Family: FamilyXray}, "incy", false, true, true, ""},
	{"v2raytun/1.0", App{Family: FamilyXray}, "v2raytun", false, false, false, ""},
	{"v2rayng/1.0", App{Family: FamilyXray}, "v2rayng", false, false, false, ""},
	{"v2rayn/1.0", App{Family: FamilyXray}, "v2rayn", false, false, false, ""},
	{"onexray/1.0", App{Family: FamilyXray}, "xray", false, false, false, ""},
	{"v2box/1.0", App{Family: FamilyXray}, "xray", false, false, false, ""},
	{"simplexray/1.0", App{Family: FamilyXray}, "xray", false, false, false, ""},
	{"renoarx/1.0", App{Family: FamilyXray}, "xray", false, false, false, ""},
	{"hiddify/1.0", App{Family: FamilySingBox, Legacy: true}, "hiddify", false, false, false, ""},
	{"karing/1.0", App{Family: FamilySingBox}, "karing", false, false, false, ""},
	{"nekobox/1.0", App{Family: FamilySingBox}, "nekobox", false, false, false, ""},
	{"nekoray/1.0", App{Family: FamilySingBox}, "nekobox", false, false, false, ""},
	{"throne/1.0", App{Family: FamilySingBox}, "nekobox", false, false, false, ""},
	{"husi/1.0", App{Family: FamilySingBox}, "husi", false, false, false, ""},
	{"sing-box/1.0", App{Family: FamilySingBox}, "singbox", false, false, false, ""},
	{"singbox/1.0", App{Family: FamilySingBox}, "singbox", false, false, false, ""},
	{"deskbox/1.0", App{Family: FamilySingBox}, "singbox", false, false, false, ""},
	{"inhive/1.0", App{Family: FamilySingBox}, "singbox", false, false, false, ""},
	{"sfa/1.0", App{Family: FamilySingBox, Whole: true}, "singbox", false, false, false, ""},
	{"sfi/1.0", App{Family: FamilySingBox, Whole: true}, "singbox", false, false, false, ""},
	{"sfm/1.0", App{Family: FamilySingBox, Whole: true}, "singbox", false, false, false, ""},
	{"sft/1.0", App{Family: FamilySingBox, Whole: true}, "singbox", false, false, false, ""},
	{"stash/1.0", App{Family: FamilyStash}, "stash", false, false, false, ""},
	{"streisand/1.0", App{Family: FamilyOther}, "streisand", false, false, false, ""},
	{"shadowrocket/1.0", App{Family: FamilyOther}, "shadowrocket", false, false, false, ""},
	{"foxray/1.0", App{Family: FamilyOther}, "foxray", false, false, false, ""},
}

func TestDetectionIsPinned(t *testing.T) {
	r, err := NewResponder(Policy{})
	if err != nil {
		t.Fatal(err)
	}
	pinned := map[string]bool{}
	for _, c := range pinnedUAs {
		pinned[c.ua] = true
		if got := DetectApp(c.ua); got != c.app {
			t.Errorf("DetectApp(%q) = %+v, want %+v", c.ua, got, c.app)
		}
		if k, _ := Identify(c.ua); k.ID != c.id {
			t.Errorf("Identify(%q) = %q, want %q", c.ua, k.ID, c.id)
		}
		if got := Named("happ", c.ua); got != c.happ {
			t.Errorf("Named(happ, %q) = %v", c.ua, got)
		}
		if got := Named("incy", c.ua); got != c.incy {
			t.Errorf("Named(incy, %q) = %v", c.ua, got)
		}
		if got := TakesXrayJSON(c.ua); got != c.xj {
			t.Errorf("TakesXrayJSON(%q) = %v", c.ua, got)
		}
		if got := r.Decide(http.Header{"User-Agent": {c.ua}}).Rule; got != c.rule {
			t.Errorf("Decide(%q).Rule = %q, want %q", c.ua, got, c.rule)
		}
	}
	for _, k := range Catalog {
		for _, w := range slices.Concat(k.Words, k.Prefixes) {
			if !pinned[w+"/1.0"] {
				t.Errorf("%s: no row for %q in pinnedUAs", k.ID, w+"/1.0")
			}
		}
	}
}
