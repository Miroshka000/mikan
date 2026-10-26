package apps

import (
	"reflect"
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
	if !IsHapp("Happ/3.6.0/Android/abc") || IsHapp("v2RayTun/5.1") || IsHapp("") {
		t.Fatal("IsHapp")
	}
	if !IsINCY("INCY/2.0.8/iOS") || IsINCY("Happ/3.6.0") || IsINCY("") {
		t.Fatal("IsINCY")
	}
}
