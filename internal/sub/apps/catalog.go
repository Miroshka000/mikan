package apps

import (
	"slices"
	"strings"
)

// Known is an app the panel tells by its User-Agent and the admin may allow or block. The
// list is plain on purpose: what an app can run is Family and App's business, this is only
// who is asking, and what to say to the ones the admin does not want.
type Known struct {
	// ID is what the admin's list keeps: lowercase letters and digits, never renamed. The
	// subscription page's catalog (web/src/sub/apps.ts) names its apps by the same ids.
	ID     string
	Name   string
	Family Family
	// Generic stands for every app of the family that has no entry of its own.
	Generic bool
	// Words are the lowercase fragments the User-Agent holds, Prefixes the ones it starts with.
	Words    []string
	Prefixes []string
}

// Catalog lists the known apps in the order the admin panel shows them, a family at a time.
// Identify tries the apps with an entry of their own before the generic ones, and within
// each in this order: Hiddify says "like ClashMeta" and Koala Clash holds "clash" too, so
// the app is told before the core it is built on. DetectApp reads the same words for the
// family (rules_test.go checks every word and prefix).
var Catalog = []Known{
	{ID: "mikan", Name: "Mikan", Family: FamilyMihomo, Words: []string{"mikan"}},
	{ID: "kabosu", Name: "Kabosu", Family: FamilyMihomo, Words: []string{"kabosu"}},
	{ID: "koala", Name: "Koala Clash", Family: FamilyMihomo, Words: []string{"koala"}},
	{ID: "clashfest", Name: "ClashFest", Family: FamilyMihomo, Words: []string{"clashfest"}},
	{ID: "slothclash", Name: "SlothClash", Family: FamilyMihomo, Words: []string{"slothclash"}},
	{ID: "flclash", Name: "FlClash", Family: FamilyMihomo, Words: []string{"flclash"}},
	{ID: "verge", Name: "Clash Verge", Family: FamilyMihomo, Words: []string{"verge"}},
	{ID: "clashmeta", Name: "Clash Meta for Android", Family: FamilyMihomo, Words: []string{"clashmetaforandroid"}},
	{ID: "mihomo", Name: "mihomo", Family: FamilyMihomo, Generic: true, Words: []string{"mihomo", "clash", "prizrak", "flowvy", "murge", "rabbit"}},

	{ID: "happ", Name: "Happ", Family: FamilyXray, Words: []string{"happ"}},
	{ID: "incy", Name: "INCY", Family: FamilyXray, Words: []string{"incy"}},
	{ID: "v2raytun", Name: "v2RayTun", Family: FamilyXray, Words: []string{"v2raytun"}},
	// v2rayNG holds "v2rayn": it is told first.
	{ID: "v2rayng", Name: "v2rayNG", Family: FamilyXray, Words: []string{"v2rayng"}},
	{ID: "v2rayn", Name: "v2rayN", Family: FamilyXray, Words: []string{"v2rayn"}},
	{ID: "xray", Name: "Xray", Family: FamilyXray, Generic: true, Words: []string{"onexray", "v2box", "simplexray", "renoarx"}},

	{ID: "hiddify", Name: "Hiddify", Family: FamilySingBox, Words: []string{"hiddify"}},
	{ID: "karing", Name: "Karing", Family: FamilySingBox, Words: []string{"karing"}},
	{ID: "nekobox", Name: "NekoBox", Family: FamilySingBox, Words: []string{"nekobox", "nekoray", "throne"}},
	{ID: "husi", Name: "Husi", Family: FamilySingBox, Words: []string{"husi"}},
	{ID: "singbox", Name: "sing-box", Family: FamilySingBox, Generic: true, Words: []string{"sing-box", "singbox", "deskbox", "inhive"}, Prefixes: []string{"sfa", "sfi", "sfm", "sft"}},

	{ID: "stash", Name: "Stash", Family: FamilyStash, Prefixes: []string{"stash"}},

	{ID: "streisand", Name: "Streisand", Family: FamilyOther, Words: []string{"streisand"}},
	{ID: "shadowrocket", Name: "Shadowrocket", Family: FamilyOther, Words: []string{"shadowrocket"}},
	{ID: "foxray", Name: "FoxRay", Family: FamilyOther, Words: []string{"foxray"}},
}

var byID = func() map[string]Known {
	m := make(map[string]Known, len(Catalog))
	for _, k := range Catalog {
		m[k.ID] = k
	}
	return m
}()

// Lookup is the known app with this id.
func Lookup(id string) (Known, bool) {
	k, ok := byID[id]
	return k, ok
}

// Identify tells which known app asks by its User-Agent; false for an app the list does not
// know (a browser, curl, an app of a family nobody listed).
func Identify(userAgent string) (Known, bool) {
	ua := strings.ToLower(userAgent)
	for _, generic := range [...]bool{false, true} {
		for _, k := range Catalog {
			if k.Generic == generic && k.matches(ua) {
				return k, true
			}
		}
	}
	return Known{}, false
}

func (k Known) matches(ua string) bool {
	for _, w := range k.Words {
		if strings.Contains(ua, w) {
			return true
		}
	}
	for _, p := range k.Prefixes {
		if strings.HasPrefix(ua, p) {
			return true
		}
	}
	return false
}

// names says whether ua (lowercase) holds a word or prefix of an app of family f, the app
// except left out.
func names(f Family, ua, except string) bool {
	for _, k := range Catalog {
		if k.Family == f && k.ID != except && k.matches(ua) {
			return true
		}
	}
	return false
}

// FamilyNamed says whether the User-Agent holds a word or prefix of an app of family f.
func FamilyNamed(f Family, userAgent string) bool {
	return names(f, strings.ToLower(userAgent), "")
}

// Named says whether the User-Agent holds a word or prefix of the app id, whatever app
// Identify takes it for: "INCY/2.1 Happ" names both INCY and Happ, and a caller that
// treats the two apart says which one comes first.
func Named(id, userAgent string) bool {
	k, ok := byID[id]
	return ok && k.matches(strings.ToLower(userAgent))
}

// Recommended are the apps a notice sends a visitor to, best first: the ones the panel
// serves best on every system and the ones that are free. A notice names the first three
// the admin has not blocked.
var Recommended = []string{"happ", "incy", "koala", "mikan", "kabosu", "hiddify"}

// Alternatives are the names of up to n Recommended apps that blocked leaves allowed.
func Alternatives(blocked map[string]bool, n int) []string {
	var out []string
	for _, id := range Recommended {
		if len(out) == n {
			break
		}
		if k, ok := byID[id]; ok && !blocked[id] {
			out = append(out, k.Name)
		}
	}
	return out
}

// XrayJSON are the apps that get whole xray configs (the xray JSON profile) in place of
// share links, by id: the ones checked on devices to run them. v2RayTun, Streisand and
// v2rayNG are next.
var XrayJSON = []string{"happ", "incy"}

// TakesXrayJSON says whether the app asking by its User-Agent is one of XrayJSON.
func TakesXrayJSON(userAgent string) bool {
	k, ok := Identify(userAgent)
	return ok && slices.Contains(XrayJSON, k.ID)
}

// ValidID says whether id is a known app's.
func ValidID(id string) bool {
	_, ok := byID[id]
	return ok
}
