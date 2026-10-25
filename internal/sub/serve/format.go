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
	FormatHTML    = "html"    // the subscription page
)

// Apps that take share links and that apps.DetectApp does not tell apart (it is about what
// the app's core can run, not about the format).
var linkApps = []string{"streisand", "shadowrocket", "foxray"}

// Format picks the response format: explicit ?format= wins, then the client's User-Agent,
// by the family apps.DetectApp makes of it, so the two never disagree about an app.
func Format(userAgent, accept, query string) string {
	switch strings.ToLower(query) {
	case "clash", "mihomo", "yaml":
		return FormatClash
	case "uri", "v2ray", "base64":
		return FormatURI
	case "singbox", "sing-box":
		return FormatSingBox
	case "html":
		return FormatHTML
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
	ua := strings.ToLower(userAgent)
	for _, a := range linkApps {
		if strings.Contains(ua, a) {
			return FormatURI
		}
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
