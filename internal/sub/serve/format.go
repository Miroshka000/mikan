package serve

import (
	"strings"

	"mikan/internal/sub/apps"
	"mikan/internal/sub/singbox"
)

// The formats of an answer.
const (
	FormatClash   = "clash"   // the Clash profile (mihomo)
	FormatSingBox = "singbox" // the whole sing-box config
	FormatURI     = "uri"     // share links, base64
	FormatXray    = "xray"    // whole xray-core configs, JSON (xray.Render)
	FormatHTML    = "html"    // the subscription page
)

// Format picks the response format: explicit ?format= wins, then the client's User-Agent,
// by the family apps.DetectApp makes of it, so the two never disagree about an app.
func Format(userAgent, accept, query string) string {
	if f := queryFormat(query); f != "" {
		return f
	}
	app := apps.DetectApp(userAgent)
	if app.Whole && app.Core.AtLeast(singbox.Since) {
		return FormatSingBox
	}
	switch app.Family {
	case apps.FamilyMihomo, apps.FamilyStash:
		return FormatClash
	case apps.FamilyXray, apps.FamilySingBox:
		return FormatURI
	}
	// The catalog's apps of no core family (Shadowrocket, Streisand…) take share links
	// whatever they accept: DetectApp is about what an app's core can run, not the format.
	if apps.FamilyNamed(apps.FamilyOther, userAgent) {
		return FormatURI
	}
	if strings.Contains(accept, "text/html") {
		return FormatHTML
	}
	return FormatURI
}

// linkPreviewBots are the User-Agents of the messengers and sites that fetch a pasted link
// to show its preview.
var linkPreviewBots = []string{"telegrambot", "whatsapp", "facebookexternalhit", "twitterbot", "slackbot", "discordbot", "vkshare", "skypeuripreview", "linkedinbot", "viber"}

// LinkPreview says whether the request is a messenger building a link's preview.
func LinkPreview(userAgent string) bool {
	ua := strings.ToLower(userAgent)
	for _, b := range linkPreviewBots {
		if strings.Contains(ua, b) {
			return true
		}
	}
	return false
}

// queryFormat is the format ?format= names, "" for none.
func queryFormat(query string) string {
	switch strings.ToLower(query) {
	case "clash", "mihomo", "yaml":
		return FormatClash
	case "uri", "v2ray", "base64":
		return FormatURI
	case "singbox", "sing-box":
		return FormatSingBox
	case "xray", "json", "v2ray-json":
		return FormatXray
	case "html":
		return FormatHTML
	}
	return ""
}
