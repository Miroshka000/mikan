package api

import (
	"context"
	"strings"

	"mikan/internal/panel/domain"
	"mikan/internal/panel/settings"
	"mikan/internal/sub/routing"
)

// settingField is one field of PATCH /settings, declared once: whether only the session
// may change it (a leaked API key must not move clients to another server, change what
// every subscriber's app shows, decide which apps get a subscription or who issues the
// certificates), and how it is written. updateSettings gates and writes by this table, so
// a field cannot be writable and forget its gate; the validation of what goes in stays in
// updateSettings, where fields are checked against each other.
type settingField struct {
	name    string // the JSON name, the location of its errors
	session bool
	given   func(*patchSettingsBody) bool
	write   func(context.Context, *settings.Settings, *patchSettingsBody) error
}

// text writes a string trimmed.
func text(name, key string, session bool, get func(*patchSettingsBody) *string) settingField {
	return settingField{name, session, func(b *patchSettingsBody) bool { return get(b) != nil },
		func(ctx context.Context, set *settings.Settings, b *patchSettingsBody) error {
			return settings.Set(ctx, set, key, strings.TrimSpace(*get(b)))
		}}
}

// value writes a value as it came.
func value[T any](name, key string, session bool, get func(*patchSettingsBody) *T) settingField {
	return settingField{name, session, func(b *patchSettingsBody) bool { return get(b) != nil },
		func(ctx context.Context, set *settings.Settings, b *patchSettingsBody) error {
			return settings.Set(ctx, set, key, *get(b))
		}}
}

// custom writes a string by its own rule.
func custom(name string, session bool, get func(*patchSettingsBody) *string, write func(context.Context, *settings.Settings, string) error) settingField {
	return settingField{name, session, func(b *patchSettingsBody) bool { return get(b) != nil },
		func(ctx context.Context, set *settings.Settings, b *patchSettingsBody) error {
			return write(ctx, set, *get(b))
		}}
}

var settingFields = []settingField{
	// Where clients are sent, and what they are told to trust.
	text("public_host", settings.KeyPublicHost, true, func(b *patchSettingsBody) *string { return b.PublicHost }),
	text("domain", settings.KeyDomain, true, func(b *patchSettingsBody) *string { return b.Domain }),
	text("sub_domain", settings.KeySubDomain, true, func(b *patchSettingsBody) *string { return b.SubDomain }),
	value("sub_port", settings.KeySubPort, true, func(b *patchSettingsBody) *int { return b.SubPort }),
	custom("sub_rules", true, func(b *patchSettingsBody) *string { return b.SubRules }, func(ctx context.Context, set *settings.Settings, v string) error {
		return settings.Set(ctx, set, settings.KeyRules, strings.TrimRight(v, " \n\r\t"))
	}),
	value("sub_routes", settings.KeyRoutes, true, func(b *patchSettingsBody) *routing.Routes { return b.SubRoutes }),
	custom("sub_template", true, func(b *patchSettingsBody) *string { return b.SubTemplate }, func(ctx context.Context, set *settings.Settings, v string) error {
		if strings.TrimSpace(v) == "" {
			v = "" // blank: back to the built-in profile
		}
		return settings.Set(ctx, set, settings.KeyTemplate, v)
	}),
	text("sub_routing", settings.KeyRouting, true, func(b *patchSettingsBody) *string { return b.SubRouting }),
	text("support_url", settings.KeySupportURL, true, func(b *patchSettingsBody) *string { return b.SupportURL }),
	// What every subscriber's app shows: text, links and the logo it downloads.
	text("sub_title", settings.KeySubTitle, true, func(b *patchSettingsBody) *string { return b.SubTitle }),
	value("sub_inactive_server", settings.KeyInactiveServer, true, func(b *patchSettingsBody) *bool { return b.InactiveServer }),
	text("sub_inactive_text", settings.KeyInactiveText, true, func(b *patchSettingsBody) *string { return b.InactiveText }),
	// The log keeps addresses and apps of the subscribers: how long, or none at all, is the admin's call.
	value("sub_log_days", settings.KeySubLogDays, true, func(b *patchSettingsBody) *int { return b.SubLogDays }),
	text("site_template", settings.KeySiteTemplate, true, func(b *patchSettingsBody) *string { return b.SiteTemplate }),
	text("sub_announce", settings.KeyAnnounce, true, func(b *patchSettingsBody) *string { return b.Announce }),
	text("sub_announce_url", settings.KeyAnnounceURL, true, func(b *patchSettingsBody) *string { return b.AnnounceURL }),
	value("app_branding", settings.KeyAppBranding, true, func(b *patchSettingsBody) *bool { return b.AppBranding }),
	text("brand_accent", settings.KeyBrandAccent, true, func(b *patchSettingsBody) *string { return b.BrandAccent }),
	text("brand_logo_url", settings.KeyBrandLogo, true, func(b *patchSettingsBody) *string { return b.BrandLogoURL }),
	// Which apps get a subscription at all: requiring an ID turns away every app without one.
	value("device_binding", settings.KeyDeviceBinding, true, func(b *patchSettingsBody) *bool { return b.DeviceBinding }),
	value("device_require_hwid", settings.KeyRequireHWID, true, func(b *patchSettingsBody) *bool { return b.RequireHWID }),
	value("device_unbind", settings.KeyUnbindRules, true, func(b *patchSettingsBody) *domain.UnbindRules { return b.DeviceUnbind }),
	value("link_reissue", settings.KeyReissueRules, true, func(b *patchSettingsBody) *domain.ReissueRules { return b.LinkReissue }),
	// What Happ is told to apply, to hide, and where the subscription address goes.
	text("happ_routing", settings.KeyHappRouting, true, func(b *patchSettingsBody) *string { return b.HappRouting }),
	custom("happ_provider_id", true, func(b *patchSettingsBody) *string { return b.HappProvider }, func(ctx context.Context, set *settings.Settings, v string) error {
		v = strings.TrimSpace(v)
		if err := settings.Set(ctx, set, settings.KeyHappProvider, v); err != nil || v != "" {
			return err
		}
		// Without a provider id Happ ignores hide-settings: the switch goes off with it, so
		// the panel never shows a setting that does nothing.
		return settings.Set(ctx, set, settings.KeyHappHide, false)
	}),
	value("happ_hide_settings", settings.KeyHappHide, true, func(b *patchSettingsBody) *bool { return b.HappHide }),
	custom("happ_crypt", true, func(b *patchSettingsBody) *string { return b.HappCrypt }, func(ctx context.Context, set *settings.Settings, v string) error {
		if v == "off" {
			v = ""
		}
		return settings.Set(ctx, set, settings.KeyHappCrypt, v)
	}),
	// Who the panel's and the nodes' certificates come from, and the account's secret.
	text("acme_ca", settings.KeyACMECA, true, func(b *patchSettingsBody) *string { return b.ACMECA }),
	text("acme_email", settings.KeyACMEEmail, true, func(b *patchSettingsBody) *string { return b.ACMEEmail }),
	text("acme_eab_kid", settings.KeyACMEEABKID, true, func(b *patchSettingsBody) *string { return b.ACMEEABKID }),
	text("acme_eab_hmac", settings.KeyACMEEABHMAC, true, func(b *patchSettingsBody) *string { return b.ACMEEABHMAC }),
	// What a key may change: names, the default language, quiet hours, the automatic tuning.
	text("brand", settings.KeyBrand, false, func(b *patchSettingsBody) *string { return b.Brand }),
	text("panel_app_name", settings.KeyPanelAppName, false, func(b *patchSettingsBody) *string { return b.PanelAppName }),
	text("sub_group_main", settings.KeyGroupMain, false, func(b *patchSettingsBody) *string { return b.SubGroupMain }),
	text("sub_group_auto", settings.KeyGroupAuto, false, func(b *patchSettingsBody) *string { return b.SubGroupAuto }),
	text("client_fingerprint", settings.KeyFingerprint, false, func(b *patchSettingsBody) *string { return b.Fingerprint }),
	text("default_lang", settings.KeyDefaultLang, false, func(b *patchSettingsBody) *string { return b.DefaultLang }),
	value("quiet_hour_utc", settings.KeyQuietHour, false, func(b *patchSettingsBody) *int { return b.QuietHourUTC }),
	value("auto_port", settings.KeyAutoPort, false, func(b *patchSettingsBody) *bool { return b.AutoPort }),
	value("auto_sni", settings.KeyAutoSNI, false, func(b *patchSettingsBody) *bool { return b.AutoSNI }),
	// Ports past the pool are not opened in the firewall: the panel's admin decides, not a key.
	value("any_port", settings.KeyAnyPort, true, func(b *patchSettingsBody) *bool { return b.AnyPort }),
	// Who may open the panel to an outsider is the admin's call alone.
	value("support_access", settings.KeySupportAccess, true, func(b *patchSettingsBody) *bool { return b.SupportAccess }),
	// Thousands of connections from the server, which a hoster may take for a scan.
	value("wide_sni_scan", settings.KeyWideScan, true, func(b *patchSettingsBody) *bool { return b.WideScan }),
}
