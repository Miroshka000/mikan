package serve

import (
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

func TestLinkPreview(t *testing.T) {
	for ua, want := range map[string]bool{"TelegramBot (like TwitterBot)": true, "WhatsApp/2.23": true, "Happ/3.4.1": false, "Mozilla/5.0": false} {
		if LinkPreview(ua) != want {
			t.Errorf("%q: %v", ua, !want)
		}
	}
}
