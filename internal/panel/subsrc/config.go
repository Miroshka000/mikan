package subsrc

import (
	"context"
	"log/slog"
	"time"

	"mikan/internal/panel/domain"
	"mikan/internal/panel/panelimport"
	"mikan/internal/panel/settings"
	"mikan/internal/panel/store/db"
	"mikan/internal/panel/subpage"
	"mikan/internal/release"
	"mikan/internal/sub/apps"
	submodel "mikan/internal/sub/model"
	"mikan/internal/sub/routing"
	"mikan/internal/sub/serve"
)

// ConfigDeps is what LoadConfig reads the subscriptions' config from.
type ConfigDeps struct {
	Settings *settings.Settings
	Q        *db.Queries
	Pages    *subpage.Service
	Log      *slog.Logger
	Now      func() time.Time
	// SubBase is the subscriptions' address with the secret path; "" when it has none.
	SubBase func(ctx context.Context) string
	// Nodes is what the panel knows of its remote nodes; nil when it runs without them.
	Nodes NodeState
	// TLS picks the pin of a node's certificate; nil leaves links unpinned.
	TLS TLSPicker
}

// NodeState is the panel's last word on its remote nodes.
type NodeState interface {
	// Health is the node's last health check; ok is false while there is none.
	Health(id int64) (h NodeHealth, ok bool)
	// ServedPin is the pin of the certificate the node serves as of the last state it took;
	// ok is false while that is not known.
	ServedPin(id int64) (pin string, ok bool)
}

// NodeHealth is what the subscriptions read of a node's last health check.
type NodeHealth struct {
	OK bool
	// Since is when the current failure began.
	Since time.Time
	// Version is the node's release.
	Version string
}

// TLSPicker gives the pin links carry for a node's certificate.
type TLSPicker interface {
	Pick(n db.Node) (pin string, err error)
}

// ssUsersSince is the first release whose nodes run Shadowsocks-2022 with a key per user.
const ssUsersSince = "0.5.1.1-rc.11"

// nodeDownForAuto is how long a node may not answer the panel before its servers leave the
// subscriptions' automatic choice: a restart or a short outage does not move them.
const nodeDownForAuto = 5 * time.Minute

// outOfAuto says whether a node's servers stay out of the subscriptions' automatic choice:
// the admin's word (noAuto), or the panel's last check (hv, when known) has found the node
// silent for nodeDownForAuto. The apps fetch the subscription only now and then, so a node
// that is back returns with the next fetch.
func outOfAuto(noAuto int64, hv NodeHealth, known bool, now time.Time) bool {
	return noAuto != 0 || known && !hv.OK && !hv.Since.IsZero() && now.Sub(hv.Since) >= nodeDownForAuto
}

// LoadConfig builds the subscriptions' config from the settings, the templates, the
// response rules, the tariffs and the nodes.
func LoadConfig(ctx context.Context, d ConfigDeps) (Config, error) {
	set := d.Settings
	ep, err := set.Endpoint(ctx)
	if err != nil {
		return Config{}, err
	}
	brand, _, err := settings.Get[string](ctx, set, settings.KeyBrand)
	if err != nil {
		return Config{}, err
	}
	if brand == "" {
		brand = "VPN"
	}
	support, _, err := settings.Get[string](ctx, set, settings.KeySupportURL)
	if err != nil {
		return Config{}, err
	}
	lang, err := set.Lang(ctx)
	if err != nil {
		return Config{}, err
	}
	var domainName, subDomain, publicHost, mode, fingerprint, rules string
	var groups submodel.GroupNames
	for key, dst := range map[string]*string{settings.KeyDomain: &domainName, settings.KeySubDomain: &subDomain, settings.KeyPublicHost: &publicHost, settings.KeyGroupMain: &groups.Main, settings.KeyGroupAuto: &groups.Auto,
		settings.KeyRouting: &mode, settings.KeyFingerprint: &fingerprint, settings.KeyRules: &rules} {
		if *dst, err = set.String(ctx, key); err != nil {
			return Config{}, err
		}
	}
	cfg := Config{Config: serve.Config{Brand: brand, SupportURL: support, Groups: groups, Mode: routing.ParseMode(mode), Fingerprint: fingerprint,
		Direct: []string{publicHost, domainName, subDomain}, Lang: lang, Rules: routing.ServedRules(rules, groups.WithDefaults(lang))}}
	if cfg.Routes, _, err = settings.Get[routing.Routes](ctx, set, settings.KeyRoutes); err != nil {
		return Config{}, err
	}
	if cfg.Templates, err = LoadTemplates(ctx, d.Q); err != nil {
		return Config{}, err
	}
	if cfg.Binding, err = set.On(ctx, settings.DeviceBinding); err != nil {
		return Config{}, err
	}
	if cfg.RequireHWID, err = set.On(ctx, settings.RequireHWID); err != nil {
		return Config{}, err
	}
	cfg.SubBase = d.SubBase(ctx)
	var legacyKind string
	if legacyKind, err = set.String(ctx, settings.KeyLegacySubKind); err != nil {
		return Config{}, err
	}
	cfg.Legacy.Kind = panelimport.Kind(legacyKind)
	if cfg.Legacy.Secret, err = set.String(ctx, settings.KeyLegacySubSecret); err != nil {
		return Config{}, err
	}
	if cfg.App.Enabled, err = set.On(ctx, settings.AppBranding); err != nil {
		return Config{}, err
	}
	if cfg.Happ.HideSettings, err = set.On(ctx, settings.HappHide); err != nil {
		return Config{}, err
	}
	if cfg.InactiveServer, err = set.On(ctx, settings.InactiveServer); err != nil {
		return Config{}, err
	}
	if cfg.XrayJSON, err = set.On(ctx, settings.XrayJSON); err != nil {
		return Config{}, err
	}
	if cfg.InactiveText, err = set.String(ctx, settings.KeyInactiveText); err != nil {
		return Config{}, err
	}
	policy, err := LoadPolicy(ctx, d.Q, set, d.Log)
	if err != nil {
		return Config{}, err
	}
	if cfg.Responses, err = apps.NewResponder(policy); err != nil {
		return Config{}, err
	}
	cfg.LogDays = LogDays(ctx, set)
	tariffs, err := d.Q.ListTariffs(ctx)
	if err != nil {
		return Config{}, err
	}
	cfg.Tariffs = make(map[int64]string, len(tariffs))
	for _, t := range tariffs {
		cfg.Tariffs[t.ID] = t.Name
	}
	for key, dst := range map[string]*string{settings.KeySubTitle: &cfg.Title, settings.KeyAnnounce: &cfg.Announce, settings.KeyAnnounceURL: &cfg.AnnounceURL,
		settings.KeyBrandAccent: &cfg.App.Accent, settings.KeyBrandLogo: &cfg.App.LogoURL,
		settings.KeyHappRouting: &cfg.Happ.Routing, settings.KeyHappProvider: &cfg.Happ.ProviderID, settings.KeyHappCrypt: &cfg.HappCrypt} {
		if *dst, err = set.String(ctx, key); err != nil {
			return Config{}, err
		}
	}
	// The apps take the logo uploaded for the page when no link of its own is given.
	if cfg.App.Enabled && cfg.App.LogoURL == "" && cfg.SubBase != "" {
		assets, err := d.Pages.Assets(ctx)
		if err != nil {
			return Config{}, err
		}
		if a, ok := assets[subpage.AssetLogo]; ok {
			cfg.App.LogoURL = cfg.SubBase + "/" + subpage.AssetPath(subpage.AssetLogo, a.Hash)
		}
	}
	nodes, err := d.Q.ListNodes(ctx)
	if err != nil {
		return Config{}, err
	}
	for _, n := range nodes {
		if n.Enabled == 0 {
			continue
		}
		sn := submodel.Node{ID: n.ID, Name: n.Name, Endpoint: submodel.Endpoint{Host: ep.Host, SNI: domainName}}
		if n.Address != "" {
			sn.Endpoint = submodel.Endpoint{Host: domain.NodeHost(n), SNI: n.Domain}
			cfg.Direct = append(cfg.Direct, n.PublicHost, n.Domain)
		}
		sn.Endpoint.SNIRouter = n.SniRouter != 0
		var hv NodeHealth
		known := false
		if d.Nodes != nil && n.Address != "" {
			hv, known = d.Nodes.Health(n.ID)
		}
		// A node older than the Shadowsocks-2022 key per user runs one key for everyone: its
		// links carry that one until it is updated. A version that is not a release (a
		// build of the source) is taken as new.
		if known && release.Valid(hv.Version) && release.Newer(ssUsersSince, hv.Version) {
			sn.SSOneKey = true
		}
		sn.NoAuto = outOfAuto(n.NoAuto, hv, known, d.Now())
		if d.TLS != nil {
			if pin, err := d.TLS.Pick(n); err == nil {
				sn.Endpoint.PinSHA256 = pin
			}
			// What the node serves, as of the last state it took: a certificate picked now
			// reaches it with the next state, and the links follow once it has.
			if d.Nodes != nil {
				if pin, ok := d.Nodes.ServedPin(n.ID); ok {
					sn.Endpoint.PinSHA256 = pin
				}
			}
		}
		cfg.Nodes = append(cfg.Nodes, sn)
	}
	return cfg, nil
}
