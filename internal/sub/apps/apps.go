// Package apps tells the apps that fetch subscriptions apart by their User-Agent, and says
// what each can use. A subscription gives every app only what it can use: a link it cannot
// use is noise in the app at best; a proxy type an older mihomo core does not know breaks
// the whole Clash profile, and the app then keeps the old one.
package apps

import (
	"regexp"
	"slices"
	"strconv"
	"strings"

	"mikan/internal/proto"
)

// Family is an app's proxy core.
type Family string

const (
	FamilyMihomo  Family = "mihomo"  // Mikan, Kabosu, Koala Clash, SlothClash, FlClash, Clash Verge, Clash Meta for Android, ClashFest…
	FamilyXray    Family = "xray"    // Happ, v2RayTun, INCY, v2rayNG, v2rayN…
	FamilySingBox Family = "singbox" // Hiddify, Karing, NekoBox, sing-box…
	FamilyStash   Family = "stash"
	FamilyOther   Family = "other" // Shadowrocket, Streisand and apps we do not know
)

// Families in the order the admin panel lists them.
var Families = []Family{FamilyMihomo, FamilyXray, FamilySingBox, FamilyStash, FamilyOther}

// App is a subscription client as its User-Agent describes it.
type App struct {
	Family Family
	Core   Version // the mihomo or sing-box core, when the app tells it or its version implies it
	Legacy bool    // Hiddify before 4: sing-box of 2024, before AnyTLS
	// Whole: the app runs a whole sing-box config, route and DNS too (the official SFA,
	// SFI, SFM, SFT); the others take its outbounds and route by their own settings.
	Whole bool
}

// TakesOwnProfile: the app takes the admin's own Clash profile, rule lists and the rules of
// routing.Services. Stash and apps we do not know get the built-in profile without them; a
// zero App (the admin panel's preview) is mihomo.
func (a App) TakesOwnProfile() bool {
	return a.Family == "" || a.Family == FamilyMihomo
}

// Version is major.minor.patch; zero when unknown.
type Version [3]int

func (v Version) Known() bool { return v != Version{} }

func (v Version) AtLeast(o Version) bool {
	for i := range v {
		if v[i] != o[i] {
			return v[i] > o[i]
		}
	}
	return true
}

var (
	mihomoVersion = regexp.MustCompile(`(?:mihomo|clash[.-]?meta)/v?(\d+)\.(\d+)\.(\d+)`)
	// hiddifyVersion: "HiddifyNext/4.1.1 (android) like ClashMeta v2ray sing-box", or
	// HiddifyNextX with the Xray core on.
	hiddifyVersion = regexp.MustCompile(`hiddifynextx?/v?(\d+)\.(\d+)\.(\d+)`)
	koalaVersion   = regexp.MustCompile(`koala-clash/v?(\d+)\.(\d+)\.(\d+)`)
	// singboxVersion: the core the official sing-box apps, Karing and Husi name.
	singboxVersion = regexp.MustCompile(`sing-box v?(\d+)\.(\d+)\.(\d+)`)
)

func versionOf(m []string) Version {
	var v Version
	for i := range v {
		v[i], _ = strconv.Atoi(m[i+1])
	}
	return v
}

// koalaCore is the mihomo release Koala Clash bundled at build time.
func koalaCore(app Version) Version {
	switch {
	case app.AtLeast(Version{1, 4, 0}):
		return Version{1, 19, 30}
	case app.AtLeast(Version{1, 2, 0}):
		return Version{1, 19, 23}
	case app.AtLeast(Version{1, 1, 0}):
		return Version{1, 19, 21}
	case app.AtLeast(Version{1, 0, 0}):
		return Version{1, 19, 20}
	}
	return Version{}
}

// DetectApp reads the app from its User-Agent by the words of the Catalog, a family at a
// time: Stash, sing-box, mihomo, xray. sing-box apps come before mihomo: Hiddify says "like
// ClashMeta" to get Clash profiles from other panels.
func DetectApp(userAgent string) App {
	ua := strings.ToLower(userAgent)
	switch {
	case names(FamilyStash, ua, ""):
		return App{Family: FamilyStash}
	case names(FamilySingBox, ua, ""):
		if byID["hiddify"].matches(ua) {
			// Hiddify 4 (2026) runs a sing-box 1.13 fork; the 2.x of 2024 had no AnyTLS. One
			// that does not say its version is taken for the old one.
			m := hiddifyVersion.FindStringSubmatch(ua)
			return App{Family: FamilySingBox, Legacy: m == nil || versionOf(m)[0] < 4}
		}
		// "SFA (sing-box 1.14.2; language en_US)": the official apps are the prefixes of the
		// generic sing-box entry.
		a := App{Family: FamilySingBox, Whole: slices.ContainsFunc(byID["singbox"].Prefixes, func(p string) bool {
			return strings.HasPrefix(ua, p)
		})}
		if m := singboxVersion.FindStringSubmatch(ua); m != nil {
			a.Core = versionOf(m)
		}
		return a
	// The Mikan app asks for a subscription as "mihomo[/core] Mikan/x"; a bare "Mikan/x" stays
	// an app we do not know, as before the Catalog had Mikan.
	case names(FamilyMihomo, ua, "mikan"):
		a := App{Family: FamilyMihomo}
		if m := mihomoVersion.FindStringSubmatch(ua); m != nil {
			a.Core = versionOf(m)
		} else if m := koalaVersion.FindStringSubmatch(ua); m != nil {
			a.Core = koalaCore(versionOf(m))
		}
		return a
	case names(FamilyXray, ua, ""):
		return App{Family: FamilyXray}
	}
	return App{Family: FamilyOther}
}

// mihomoSince is the first mihomo release with a proxy type. The ones not in the core
// of 2024 (needKnown) go only to a core known to have them: an unknown type fails the
// profile.
var mihomoSince = map[string]struct {
	since     Version
	needKnown bool
}{
	"anytls":      {Version{1, 19, 3}, false},
	"mieru":       {Version{1, 19, 0}, true},
	"sudoku":      {Version{1, 19, 17}, true},
	"trusttunnel": {Version{1, 19, 21}, true},
	"shadowquic":  {Version{1, 19, 29}, true},
}

// xhttpSince is the first mihomo with the XHTTP transport.
var xhttpSince = Version{1, 19, 22}

// encryptionSince is the first mihomo with VLESS Encryption.
var encryptionSince = Version{1, 19, 13}

// geckoSince is the first mihomo with Hysteria2's Gecko obfuscation. Xray and sing-box
// got it later than the apps on them can be told apart, so only mihomo apps that name
// their core get a Gecko inbound.
var geckoSince = Version{1, 19, 26}

// getUplinkSince is the first mihomo client that uploads over XHTTP with GET.
var getUplinkSince = Version{1, 19, 32}

// Supports says whether the app can use an inbound.
func (a App) Supports(n proto.Needs) bool {
	switch a.Family {
	case FamilyMihomo:
		known := a.Core.Known()
		if t, ok := mihomoSince[n.Type]; ok && (known && !a.Core.AtLeast(t.since) || !known && t.needKnown) {
			return false
		}
		if known && (n.Transport == "xhttp" && !a.Core.AtLeast(xhttpSince) || n.Encryption && !a.Core.AtLeast(encryptionSince)) {
			return false
		}
		// An older core fails the whole profile on an obfs it does not know.
		if n.Gecko && !(known && a.Core.AtLeast(geckoSince)) {
			return false
		}
		// Uploading with GET came in 1.19.32; an older client sends POST and the CDN
		// in front, the reason for GET, drops it.
		if n.GetUplink && !(known && a.Core.AtLeast(getUplinkSince)) {
			return false
		}
		return true
	case FamilyXray: // no TUIC, AnyTLS or the mihomo-only types in Xray
		if n.Gecko {
			return false
		}
		switch n.Type {
		case "vless", "trojan", "hysteria2", "shadowsocks":
			return true
		}
	case FamilySingBox: // no XHTTP, VLESS Encryption or Gecko in sing-box
		if n.Transport == "xhttp" || n.Encryption || n.Gecko {
			return false
		}
		switch n.Type {
		case "vless", "trojan", "hysteria2", "tuic", "shadowsocks":
			return true
		case "anytls":
			return !a.Legacy
		}
	case FamilyStash:
		if n.Gecko || n.GetUplink {
			return false
		}
		switch n.Type {
		case "vless", "trojan", "hysteria2", "tuic", "anytls", "shadowsocks", "snell":
			return true
		}
	default: // what a share link can carry
		if n.Gecko {
			return false
		}
		switch n.Type {
		case "vless", "trojan", "hysteria2", "tuic", "anytls", "shadowsocks":
			return true
		}
	}
	return false
}

// For lists the families that get an inbound, for the admin panel.
func For(n proto.Needs) []Family {
	out := []Family{}
	for _, f := range Families {
		// A mihomo core of today: what the families can do at best.
		if (App{Family: f, Core: Version{99}}).Supports(n) {
			out = append(out, f)
		}
	}
	return out
}
