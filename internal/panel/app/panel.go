package app

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"mikan/internal/hostchan"
	"mikan/internal/logbuf"
	"mikan/internal/nodetls"
	"mikan/internal/pace"
	"mikan/internal/panel/acme"
	"mikan/internal/panel/addons"
	addonpages "mikan/internal/panel/addons/pages"
	"mikan/internal/panel/addons/panelapi"
	"mikan/internal/panel/api"
	"mikan/internal/panel/auth"
	"mikan/internal/panel/autotune"
	"mikan/internal/panel/billing"
	"mikan/internal/panel/botpanel"
	"mikan/internal/panel/dnscheck"
	"mikan/internal/panel/domain"
	"mikan/internal/panel/events"
	"mikan/internal/panel/events/feed"
	"mikan/internal/panel/infraalerts"
	"mikan/internal/panel/ipgeo"
	"mikan/internal/panel/model"
	"mikan/internal/panel/nodesync"
	"mikan/internal/panel/nodeupdate"
	"mikan/internal/panel/panelimport"
	"mikan/internal/panel/promo"
	"mikan/internal/panel/reach"
	"mikan/internal/panel/server"
	"mikan/internal/panel/settings"
	"mikan/internal/panel/store"
	"mikan/internal/panel/store/db"
	"mikan/internal/panel/subpage"
	"mikan/internal/panel/subs"
	"mikan/internal/panel/support"
	"mikan/internal/panel/tgate"
	"mikan/internal/panel/tgbackup"
	"mikan/internal/panel/tlscert"
	"mikan/internal/panel/updates"
	"mikan/internal/panel/warp"
	"mikan/internal/panel/webhooks"
	"mikan/internal/release"
)

// Panel is the fully wired HTTP side of the panel, without the listener.
type Panel struct {
	Handler  http.Handler
	Settings *settings.Settings
	// reload is what Run reads the paths and the language with every five seconds.
	reload *settings.Settings
	Nodes  *nodesync.Manager
	Tuner  *autotune.Tuner // nil without nodes
	// NodeUpdates updates the remote nodes to the panel's version; nil without nodes.
	NodeUpdates *nodeupdate.Service
	// Gateway is the panel's side of Telegram: the token, polling, Stars, the alerts' way
	// out; Telegram is the admin's setup of the bot, its notices and broadcasts on top of it.
	Gateway  *tgate.Gateway
	Telegram *botpanel.Panel
	Billing  *billing.Service
	Updates  *updates.Checker
	Alerts   *infraalerts.Monitor
	Backups  *tgbackup.Service
	Importer *panelimport.Importer
	// Webhooks sends the events to the addresses the admin names.
	Webhooks *webhooks.Service
	// Support keeps the support access grants; the operator's terminal hooks into it.
	Support   *support.Service
	Addons    *addons.Manager
	AddonsAPI *panelapi.Server
	server    *server.Server
	spa       *server.SPA
	subPage   *server.SPA
	sessions  *auth.Sessions
	st        *store.Store
	devices   *domain.Devices
	ipLimit   *auth.Limiter
	userLimit *auth.Limiter
	now       func() time.Time
	log       *slog.Logger
}

type Options struct {
	Version    string
	Web        fs.FS
	TrustProxy bool
	// SupportOff turns support access off whatever the setting says (MIKAN_SUPPORT_ACCESS=off).
	SupportOff bool
	// SupportBot is the support bot a support access is opened for; nil: none can be opened.
	SupportBot support.Bot
	Log        *slog.Logger
	Now        func() time.Time
	// Connect reaches a node; nil when the panel runs without nodes (tests, UI development).
	Connect nodesync.Connect
	// TLS picks the certificate of a node's protocols on TLS and the pin links carry for
	// it; nil leaves links unpinned (tests).
	TLS *NodeTLS
	// NodesACME orders the remote nodes' public certificates; nil in development.
	NodesACME *acme.Nodes
	// PanelCert is the client certificate remote nodes pin; join keys carry its hash.
	PanelCert func() (nodetls.Pair, error)
	// NodeCerts keeps the nodes' own certificates; nil: nodes have none.
	NodeCerts *tlscert.NodeStore
	// Certs manages the panel's public certificate; nil in development.
	Certs *acme.Manager
	// Autotune are the automatic moves' timings; zero means autotune.DefaultOptions.
	Autotune autotune.Options
	// TelegramAPI is the Bot API; "" is Telegram's.
	TelegramAPI string
	// SubPort moves the subscription port (0: none) and SubPortError says why the saved
	// one is not served; nil where the panel runs no server (tests, the CLI).
	SubPort      func(port int) error
	SubPortError func() string
	// SubDomain serves a new subscription domain; SubCerts is its certificate. nil where
	// the panel runs no server, and in development.
	SubDomain func(name string)
	SubCerts  *acme.Manager
	// DataDir is where the host updater and the panel meet (update/); "" turns that off.
	DataDir string
	// Releases finds the release to update to; nil never checks.
	Releases updates.Source
	// WarpAPI is Cloudflare's WARP client API; "" is the real one.
	WarpAPI string
	// AddonsCatalog is the marketplace's signed catalog; "" is the real one.
	AddonsCatalog string
	// AddonsCommunity is the registry of community addons; "" is the real one.
	AddonsCommunity string
	// Dev is the panel running for development: addon sources may be read over plain http
	// from 127.0.0.1.
	Dev bool
	// DNS checks new domains against public DNS; nil leaves them unchecked.
	DNS *dnscheck.Checker
	// HSTS says whether browsers are told to keep to HTTPS: for a panel that serves TLS
	// itself, while its certificate is trusted (see server.SetHSTS). nil: never.
	HSTS func() bool
	// Resolve looks up the names the panel is told to dial (REALITY targets); nil is the
	// system's resolver, tests set their own.
	Resolve func(ctx context.Context, host string) ([]netip.Addr, error)
	// Reach checks ports from Russia (Globalping); nil: the check is not offered (tests).
	Reach *reach.Client
	// Geo asks the geo bases where an address is; nil: the check is not offered (tests).
	Geo *ipgeo.Client
	// Logs holds the last lines of the panel's log for the Diagnostics page; nil: none.
	Logs *logbuf.Buffer
}

type noChanges struct{}

func (noChanges) PoliciesChanged() {}
func (noChanges) SlotsChanged()    {}

func NewPanel(st *store.Store, o Options) (*Panel, error) {
	set := settings.New(st.Q)
	p := &Panel{
		Settings:  set,
		reload:    settings.Cached(st.Q, loopSettingsTTL),
		sessions:  auth.NewSessions(st.Q, o.Now, o.Log),
		ipLimit:   auth.NewLimiter(10, 10*time.Minute, 15*time.Minute, 24*time.Hour),
		userLimit: auth.NewLimiter(30, 10*time.Minute, 15*time.Minute, 24*time.Hour),
		now:       o.Now,
		log:       o.Log,
	}
	pool := domain.NewPool(st, o.Now)
	// The feed of events for addons: what changes users, devices, nodes and payments is
	// written in its own transaction, and the hub wakes the addons reading it.
	hub := events.NewHub()
	var changes domain.Changes = noChanges{}
	deps := api.Deps{
		Version: o.Version, Store: st, Settings: set, Sessions: p.sessions,
		IPLimit: p.ipLimit, UserLimit: p.userLimit, TOTP: auth.NewTOTPGuard(),
		TrustProxy: o.TrustProxy, Log: o.Log, Now: o.Now, Pool: pool,
	}
	p.Support = support.New(st, o.Now, o.Log)
	p.Support.SetSwitch(func(ctx context.Context) (string, error) {
		if o.SupportOff {
			return support.OffEnv, nil
		}
		on, err := set.On(ctx, settings.SupportAccess)
		if err != nil || on {
			return "", err
		}
		return support.OffSetting, nil
	})
	p.Support.SetBot(o.SupportBot)
	deps.Support = p.Support
	if o.Connect != nil {
		p.Nodes = nodesync.NewManager(st, set, pool, o.Connect, o.Log, o.Now)
		p.Nodes.SetEvents(hub)
		changes = p.Nodes
		deps.Online = p.Nodes.Online
		deps.Nodes = p.Nodes
		p.NodeUpdates = nodeupdate.New(st, set, p.Nodes, o.Version, o.Log, o.Now)
		deps.NodeUpdates = p.NodeUpdates
		tune := o.Autotune
		if tune == (autotune.Options{}) {
			tune = autotune.DefaultOptions()
		}
		p.Tuner = autotune.New(st, set, p.Nodes, p.Nodes, o.Log, o.Now, tune)
		deps.Tuner = p.Tuner
	}
	deps.PanelCert = o.PanelCert
	deps.SubPort, deps.SubPortError = o.SubPort, o.SubPortError
	deps.SubDomain = o.SubDomain
	if o.SubCerts != nil {
		deps.SubCerts = o.SubCerts
	}
	deps.Changes = changes
	deps.Users = domain.NewUsers(st, pool, changes, o.Now)
	deps.Users.SetEvents(hub)
	// The nodes try a new listener before it is saved, when the panel runs them.
	var dryRun domain.DryRun
	if p.Nodes != nil {
		dryRun = p.Nodes
	}
	deps.Inbounds = domain.NewInbounds(st, dryRun, o.Now)
	if p.Nodes != nil {
		// A relay made for a new exit keeps off the ports other programs hold on its server.
		deps.Inbounds.SetHostLookup(p.Nodes.HostPorts)
	}
	// A REALITY target given by name is looked up when it is saved: the node dials it past the
	// rules that fence its users in.
	deps.Resolve = o.Resolve
	if deps.Resolve == nil {
		deps.Resolve = domain.SystemResolve
	}
	deps.Inbounds.SetResolver(deps.Resolve)
	deps.Devices = domain.NewDevices(st, pool, changes, o.Now)
	deps.Devices.SetEvents(hub)
	p.st, p.devices = st, deps.Devices
	deps.Packages = domain.NewPackages(st, o.Now)
	deps.Campaigns = domain.NewCampaigns(st, o.Now)
	deps.Balancer = domain.NewBalancer(st, o.Now, o.Log)
	if o.Certs != nil {
		deps.Cert, deps.RenewCert, deps.RenewCertNow = o.Certs.Status, o.Certs.Renew, o.Certs.RenewNow
		deps.SetCert, deps.ClearCert = o.Certs.SetCustom, o.Certs.ClearCustom
	}
	if o.NodesACME != nil {
		deps.RenewNodeCert, deps.WakeNodeCerts = o.NodesACME.Renew, o.NodesACME.Wake
	}
	if o.TLS != nil {
		deps.NodeTLS = nodeTLSView(o.TLS, p.Nodes, o.NodesACME, o.Now)
		if o.Certs != nil {
			dns := o.DNS
			if dns == nil {
				dns = dnscheck.New()
			}
			deps.CheckCerts = checkCerts(st, set, o.Certs, o.SubCerts, o.TLS, p.Nodes, o.NodesACME, dns)
		}
	}
	deps.NodeCerts = o.NodeCerts
	deps.ForgetNode = forgetNodeFiles(o)
	subBase := func(ctx context.Context) string {
		ep, err := set.SubEndpoint(ctx)
		if err != nil || ep.URL() == "" {
			return ""
		}
		paths, err := set.Paths(ctx)
		if err != nil {
			return ""
		}
		return ep.URL() + "/" + paths.Sub
	}
	p.Addons = addons.New(o.DataDir, o.AddonsCatalog, o.Version, o.Log, o.Now)
	if o.AddonsCommunity != "" {
		p.Addons.SetCommunityURL(o.AddonsCommunity)
	}
	p.Addons.SetDev(o.Dev)
	deps.Addons = p.Addons
	addonPages := addonpages.New(addonpages.Deps{Addons: p.Addons, Store: st.Q, Log: o.Log, Now: o.Now})
	deps.Pages = addonPages
	p.Webhooks = webhooks.New(webhooks.Deps{Store: st, Hub: hub, Log: o.Log, Now: o.Now, Version: o.Version})
	deps.Webhooks = p.Webhooks
	deps.DNS = o.DNS
	deps.Reach, deps.Geo = o.Reach, o.Geo
	deps.DataDir = o.DataDir
	deps.Logs, deps.Host = o.Logs, hostchan.New(o.DataDir)
	promos := promo.New(st, o.Now)
	promos.Changed = deps.Users.Changed
	p.Billing = billing.New(billing.Deps{Store: st, Settings: set, Users: deps.Users, Log: o.Log, Now: o.Now, TrustProxy: o.TrustProxy,
		MaxLinks: model.MaxLinks,
		Addons:   deps.Addons, SubBase: subBase, Promo: promos, Events: hub,
		TopupWaiting: func(ctx context.Context, t db.Topup) { p.tellAdminOfTopup(ctx, t) }})
	deps.Billing, deps.SubBase = p.Billing, subBase
	// The bot may reach Telegram through a node when the panel's server cannot.
	var tunnel func(ctx context.Context, nodeID int64, addr string) (net.Conn, error)
	if p.Nodes != nil {
		tunnel = p.Nodes.Tunnel
	}
	p.Gateway = tgate.New(tgate.Deps{Store: st, Settings: set, API: o.TelegramAPI, Log: o.Log, Now: o.Now, Billing: p.Billing, Tunnel: tunnel})
	// The bot's screens, which run as an addon, see the panel through the Panel API: Local
	// answers their calls (botpanel.RPCHandler).
	backend := &botpanel.Local{Store: st, Settings: set, Binder: deps.Devices, Users: deps.Users, Billing: p.Billing, Gateway: p.Gateway, SubBase: subBase, Now: o.Now,
		// Telegram apps refuse a Mini App on a self-signed certificate.
		MiniApp: func() bool { return o.Certs != nil && o.Certs.Trusted() }}
	// The panel's API for addons, with the Bot API proxy over the gateway for the addon that
	// holds the bot.
	p.AddonsAPI = panelapi.New(panelapi.Deps{Addons: p.Addons, Store: st, Hub: hub, Log: o.Log, Now: o.Now,
		Telegram: p.Gateway, Settings: set, SubBase: subBase, Bot: botpanel.RPCHandler(backend), Users: deps.Users,
		Inbounds: deps.Inbounds, Changes: deps.Changes, NodeState: nodeState(p.Nodes)})
	// What the bot says comes from the bot addon (the official one is installed by itself,
	// botaddon.go): the panel tells it through events, and keeps the broadcasts and the
	// previews of the admin's texts.
	emit := func(ctx context.Context, typ string, userID, tgID int64, data any, dedupe string) error {
		err := st.Tx(ctx, func(q *db.Queries) error { return feed.Emit(ctx, q, o.Now(), typ, userID, tgID, data, dedupe) })
		if err == nil {
			hub.Kick()
		}
		return err
	}
	p.Telegram = botpanel.New(botpanel.Deps{Backend: backend, Outbox: p.Gateway.Outbox, Emit: emit, Log: o.Log, Now: o.Now})
	// The updates wait in the buffer for the addon that holds the bot.
	p.Gateway.SetHandler(p.AddonsAPI.BotSlot())
	deps.Telegram, deps.Gateway = p.Telegram, p.Gateway
	p.Support.SetNotifier(p.tellAdminOfSupport)
	p.Billing.SetTelegram(p.Gateway)
	p.Updates = updates.New(o.DataDir, o.Version, o.Releases, o.Log, o.Now)
	deps.Updates = p.Updates
	var certStatus infraalerts.CertificateSource
	if o.Certs != nil {
		certStatus = o.Certs.Status
	}
	p.Alerts = infraalerts.New(st, settings.Cached(st.Q, loopSettingsTTL), p.Nodes, p.Tuner, certStatus, p.Updates, p.Gateway, o.Log, o.Now)
	if p.NodeUpdates != nil {
		p.Alerts.WatchNodeUpdates(p.NodeUpdates)
	}
	// The server's name in a backup's file name: its domain, else its address.
	serverName := func(ctx context.Context) string {
		if d, err := set.String(ctx, settings.KeyDomain); err == nil && d != "" {
			return d
		}
		h, _ := set.String(ctx, settings.KeyPublicHost)
		return h
	}
	p.Backups = tgbackup.New(selfDump, set, p.Gateway, o.DataDir, serverName, o.Now, o.Log)
	deps.Backups = p.Backups
	deps.Warp = warp.Client{API: o.WarpAPI}
	importer := panelimport.NewImporter(st, deps.Users, nil, o.Now, o.Log)
	// The old links are checked as the panel the users came from signs them.
	importer.Done = func(ctx context.Context, kind panelimport.Kind) {
		// Only the panels whose links need the secret; Remnawave's are looked up as they are.
		if kind != panelimport.Marzban && kind != panelimport.PasarGuard {
			return
		}
		if err := settings.Set(ctx, set, settings.KeyLegacySubKind, string(kind)); err != nil {
			o.Log.Warn("import: the old links' kind is not saved", "err", err)
		}
	}
	deps.Importer = importer
	p.Importer = importer
	// The subscriptions' handler comes later; the preview reaches it once it is there.
	var subHandler *subs.Handler
	deps.RoutesPreview = func(ctx context.Context, req subs.PreviewRequest) ([]byte, error) {
		return subHandler.Preview(ctx, req)
	}
	deps.CheckTemplate = func(ctx context.Context, src string) error { return subHandler.CheckTemplate(ctx, src) }
	deps.HappLink = func(ctx context.Context, u db.User) (string, error) { return subHandler.HappLink(ctx, u) }
	apiHandler, _, err := api.New(deps)
	if err != nil {
		return nil, err
	}
	p.spa, err = server.NewSPA(o.Web, "index.html")
	if err != nil {
		return nil, fmt.Errorf("web bundle: %w", err)
	}
	var subPageHandler http.Handler
	if sp, err := server.NewSPA(o.Web, "sub.html"); err == nil {
		p.subPage, subPageHandler = sp, sp
	}
	pages := subpage.NewService(st.Q)
	buildSubCfg := func(ctx context.Context) (subs.Config, error) {
		ep, err := set.Endpoint(ctx)
		if err != nil {
			return subs.Config{}, err
		}
		brand, _, err := settings.Get[string](ctx, set, settings.KeyBrand)
		if err != nil {
			return subs.Config{}, err
		}
		if brand == "" {
			brand = "VPN"
		}
		support, _, err := settings.Get[string](ctx, set, settings.KeySupportURL)
		if err != nil {
			return subs.Config{}, err
		}
		lang, err := set.Lang(ctx)
		if err != nil {
			return subs.Config{}, err
		}
		var domainName, subDomain, publicHost, routing, fingerprint, rules string
		var groups subs.Groups
		for key, dst := range map[string]*string{settings.KeyDomain: &domainName, settings.KeySubDomain: &subDomain, settings.KeyPublicHost: &publicHost, settings.KeyGroupMain: &groups.Main, settings.KeyGroupAuto: &groups.Auto,
			settings.KeyRouting: &routing, settings.KeyFingerprint: &fingerprint, settings.KeyRules: &rules} {
			if *dst, err = set.String(ctx, key); err != nil {
				return subs.Config{}, err
			}
		}
		cfg := subs.Config{Brand: brand, SupportURL: support, Groups: groups, Routing: subs.ParseRouting(routing), Fingerprint: fingerprint,
			Direct: []string{publicHost, domainName, subDomain}, Lang: lang, Rules: subs.ServedRules(rules, groups.WithDefaults(lang))}
		if cfg.Routes, _, err = settings.Get[subs.Routes](ctx, set, settings.KeyRoutes); err != nil {
			return subs.Config{}, err
		}
		if cfg.Template, err = set.String(ctx, settings.KeyTemplate); err != nil {
			return subs.Config{}, err
		}
		if cfg.Binding, err = set.On(ctx, settings.DeviceBinding); err != nil {
			return subs.Config{}, err
		}
		if cfg.RequireHWID, err = set.On(ctx, settings.RequireHWID); err != nil {
			return subs.Config{}, err
		}
		cfg.SubBase = subBase(ctx)
		var legacyKind string
		if legacyKind, err = set.String(ctx, settings.KeyLegacySubKind); err != nil {
			return subs.Config{}, err
		}
		cfg.Legacy.Kind = panelimport.Kind(legacyKind)
		if cfg.Legacy.Secret, err = set.String(ctx, settings.KeyLegacySubSecret); err != nil {
			return subs.Config{}, err
		}
		if cfg.App.Enabled, err = set.On(ctx, settings.AppBranding); err != nil {
			return subs.Config{}, err
		}
		if cfg.Happ.HideSettings, err = set.On(ctx, settings.HappHide); err != nil {
			return subs.Config{}, err
		}
		if cfg.InactiveServer, err = set.On(ctx, settings.InactiveServer); err != nil {
			return subs.Config{}, err
		}
		if cfg.InactiveText, err = set.String(ctx, settings.KeyInactiveText); err != nil {
			return subs.Config{}, err
		}
		tariffs, err := st.Q.ListTariffs(ctx)
		if err != nil {
			return subs.Config{}, err
		}
		cfg.Tariffs = make(map[int64]string, len(tariffs))
		for _, t := range tariffs {
			cfg.Tariffs[t.ID] = t.Name
		}
		for key, dst := range map[string]*string{settings.KeySubTitle: &cfg.Title, settings.KeyAnnounce: &cfg.Announce, settings.KeyAnnounceURL: &cfg.AnnounceURL,
			settings.KeyBrandAccent: &cfg.App.Accent, settings.KeyBrandLogo: &cfg.App.LogoURL,
			settings.KeyHappRouting: &cfg.Happ.Routing, settings.KeyHappProvider: &cfg.Happ.ProviderID, settings.KeyHappCrypt: &cfg.Happ.Crypt} {
			if *dst, err = set.String(ctx, key); err != nil {
				return subs.Config{}, err
			}
		}
		// The apps take the logo uploaded for the page when no link of its own is given.
		if cfg.App.Enabled && cfg.App.LogoURL == "" && cfg.SubBase != "" {
			assets, err := pages.Assets(ctx)
			if err != nil {
				return subs.Config{}, err
			}
			if a, ok := assets[subpage.AssetLogo]; ok {
				cfg.App.LogoURL = cfg.SubBase + "/" + subpage.AssetPath(subpage.AssetLogo, a.Hash)
			}
		}
		nodes, err := st.Q.ListNodes(ctx)
		if err != nil {
			return subs.Config{}, err
		}
		for _, n := range nodes {
			if n.Enabled == 0 {
				continue
			}
			sn := subs.Node{ID: n.ID, Name: n.Name, Endpoint: subs.Endpoint{Host: ep.Host, SNI: domainName}}
			if n.Address != "" {
				sn.Endpoint = subs.Endpoint{Host: domain.NodeHost(n), SNI: n.Domain}
				cfg.Direct = append(cfg.Direct, n.PublicHost, n.Domain)
			}
			sn.Endpoint.SNIRouter = n.SniRouter != 0
			// A node older than the Shadowsocks-2022 key per user runs one key for everyone: its
			// links carry that one until it is updated. A version that is not a release (a
			// build of the source) is taken as new.
			if p.Nodes != nil && n.Address != "" {
				if hv, ok := p.Nodes.Health(n.ID); ok && release.Valid(hv.Health.Version) && release.Newer(ssUsersSince, hv.Health.Version) {
					sn.SSOneKey = true
				}
			}
			if o.TLS != nil {
				if c, err := o.TLS.Pick(n); err == nil {
					sn.Endpoint.PinSHA256 = c.Pin
				}
				// What the node serves, as of the last state it took: a certificate picked now
				// reaches it with the next state, and the links follow once it has.
				if p.Nodes != nil {
					if pin, ok := p.Nodes.ServedPin(n.ID); ok {
						sn.Endpoint.PinSHA256 = pin
					}
				}
			}
			cfg.Nodes = append(cfg.Nodes, sn)
		}
		return cfg, nil
	}
	// A subscription is fetched by every app of every user every hour or so, and building
	// its config reads some twenty settings and every node and takes a node's certificate
	// from disk. It is kept for a few seconds, and dropped at once when this process changes
	// a setting, a node or a node's certificate.
	cache := &configCache{now: o.Now, gen: func() uint64 {
		g := settings.Generation()
		if p.Nodes != nil {
			g += p.Nodes.Generation()
		}
		if o.NodeCerts != nil {
			g += o.NodeCerts.Generation()
		}
		return g
	}}
	subCfg := func(ctx context.Context) (subs.Config, error) { return cache.get(ctx, buildSubCfg) }
	subHandler = subs.NewHandler(st, subCfg, subPageHandler, o.Now, deps.Devices, o.TrustProxy)
	subHandler.SetBalancer(deps.Balancer)
	subHandler.SetLogger(o.Log)
	subHandler.SetTelegram(p.Gateway)
	subHandler.SetReissuer(deps.Users)
	subHandler.SetShop(p.Billing)
	subHandler.SetPromo(promos)
	subHandler.SetPages(pages)

	adminMux := http.NewServeMux()
	adminMux.Handle("/api/", apiHandler)
	adminMux.Handle(addonpages.Prefix, addonPages)
	// What a browser installs the panel as (Settings → General → App on this device).
	adminMux.Handle("/manifest.webmanifest", server.Manifest(func(ctx context.Context) (server.ManifestInfo, error) {
		name, err := set.String(ctx, settings.KeyPanelAppName)
		if err != nil {
			return server.ManifestInfo{}, err
		}
		brand, err := set.String(ctx, settings.KeyBrand)
		if err != nil {
			return server.ManifestInfo{}, err
		}
		lang, err := set.Lang(ctx)
		return server.ManifestInfo{Name: server.AppName(name, brand), Lang: lang}, err
	}))
	adminMux.Handle("/", p.spa)
	p.server = server.New(adminMux, subHandler)
	p.server.SetLegacy(subHandler.Legacy())
	if o.DataDir != "" {
		set := settings.New(st.Q)
		p.server.SetSite(server.OwnSite(filepath.Join(o.DataDir, "www"), settingsCached(func(ctx context.Context) (string, error) {
			return set.String(ctx, settings.KeySiteTemplate)
		})))
	}
	p.server.SetHSTS(o.HSTS)
	if p.Nodes != nil && o.PanelCert != nil {
		p.server.SetHello(nodeHello(st, p.Nodes, o).Serve)
	}
	p.Handler = p.server
	return p, nil
}

// SubOnly is what the subscription port serves: the subscription path alone.
func (p *Panel) SubOnly() http.Handler { return p.server.SubOnly() }

// loopSettingsTTL is how long the loops that read settings every few seconds (Run's reload,
// the infrastructure alerts) keep a value: what this process writes is seen at once, what
// the CLI writes within this.
const loopSettingsTTL = 15 * time.Second

// Apply loads the secret paths into the router and the default language into the pages.
func (p *Panel) Apply(ctx context.Context) (settings.Paths, error) {
	paths, err := p.reload.Paths(ctx)
	if err != nil {
		return paths, err
	}
	lang, err := p.reload.Lang(ctx)
	if err != nil {
		return paths, err
	}
	p.server.SetPaths(paths)
	p.spa.SetPrefix(paths.Admin)
	p.spa.SetLang(lang)
	if p.subPage != nil {
		p.subPage.SetPrefix(paths.Sub)
		p.subPage.SetLang(lang)
	}
	return paths, nil
}

// Run keeps paths and the language in sync with the DB (the CLI and the settings page
// edit them), drives the node syncer and cleans up expired state. It returns once ctx is
// done and every one of its workers has stopped: the caller closes the database after it.
func (p *Panel) Run(ctx context.Context) {
	// The host reads the switch and the channel from a file; the settings are what the
	// admin chose. The channel is the panel's own check's too.
	auto, err := p.Settings.On(ctx, settings.AutoUpdate)
	channel, cerr := p.Settings.UpdateChannel(ctx)
	if err = errors.Join(err, cerr); err == nil {
		if err := p.Updates.SetPolicy(updates.Policy{Auto: auto, Channel: channel}); err != nil && !errors.Is(err, updates.ErrUnavailable) {
			p.log.Error("update policy", "err", err)
		}
	} else {
		p.log.Error("update policy", "err", err)
	}
	var workers []func(context.Context)
	if p.Nodes != nil {
		workers = append(workers, p.Nodes.Run)
	}
	if p.Tuner != nil {
		workers = append(workers, p.Tuner.Run)
	}
	if p.NodeUpdates != nil {
		workers = append(workers, p.NodeUpdates.Run)
	}
	workers = append(workers, p.Gateway.Run, p.Telegram.Run, p.Billing.Run, p.AddonsAPI.Run, p.AddonsAPI.RunPush, p.Webhooks.Run, (&botAddon{p: p}).Run, p.Updates.Run, p.Alerts.Run, p.Backups.Run, p.Importer.Run,
		func(ctx context.Context) {
			pace.Tick(ctx, 5*time.Second, func(ctx context.Context) {
				if _, err := p.Apply(ctx); err != nil {
					p.log.Error("reload settings", "err", err)
				}
			})
		},
		// A support grant whose time ran out is written to the journal and the admin is told
		// within the half minute, not when the next request meets it.
		func(ctx context.Context) {
			pace.Tick(ctx, 30*time.Second, func(ctx context.Context) {
				if err := p.Support.Sweep(ctx); err != nil {
					p.log.Error("support sweep", "err", err)
				}
			})
		},
		func(ctx context.Context) {
			pace.Tick(ctx, 10*time.Minute, func(ctx context.Context) {
				if err := p.sessions.Cleanup(ctx); err != nil {
					p.log.Error("session cleanup", "err", err)
				}
				p.ipLimit.Sweep(p.now())
				p.userLimit.Sweep(p.now())
				p.maintain(ctx)
			})
		})
	runAll(ctx, workers...)
}

// runAll runs the workers until ctx is done and waits for every one to return, so that
// nothing is still using what the caller shuts down after it (the database).
func runAll(ctx context.Context, workers ...func(context.Context)) {
	var wg sync.WaitGroup
	for _, w := range workers {
		wg.Go(func() { w(ctx) })
	}
	wg.Wait()
}

// configCacheTTL: the longest a changed setting is not seen, when the change was made by
// another process (the server CLI); this process's own changes drop the cache at once.
// ssUsersSince is the first release whose nodes run Shadowsocks-2022 with a key per user.
const ssUsersSince = "0.5.1.1-rc.11"

const configCacheTTL = 10 * time.Second

// configCache keeps the subscription's config between requests. Errors are not kept.
type configCache struct {
	now func() time.Time
	gen func() uint64

	mu  sync.Mutex
	at  time.Time
	g   uint64
	cfg subs.Config
	ok  bool
}

func (c *configCache) get(ctx context.Context, build func(context.Context) (subs.Config, error)) (subs.Config, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	now, g := c.now(), c.gen()
	if c.ok && g == c.g && now.Sub(c.at) < configCacheTTL && !now.Before(c.at) {
		return c.cfg, nil
	}
	cfg, err := build(ctx)
	if err != nil {
		return subs.Config{}, err
	}
	c.cfg, c.g, c.at, c.ok = cfg, g, now, true
	return cfg, nil
}

// forgetNodeFiles removes what the panel keeps on disk for a node id: its own certificate
// (Options.NodeCerts), the public one ordered for it (<data>/tls/acme-nodes/<id>) and the
// self-signed pair, <data>/tls/nodes/<id>.
func forgetNodeFiles(o Options) func(id int64) error {
	return func(id int64) error {
		var errs []error
		if o.NodeCerts != nil {
			errs = append(errs, o.NodeCerts.Clear(id))
		}
		if o.NodesACME != nil {
			errs = append(errs, o.NodesACME.Forget(id))
		}
		if o.DataDir != "" {
			errs = append(errs, os.RemoveAll(filepath.Join(o.DataDir, "tls", "nodes", strconv.FormatInt(id, 10))))
			errs = append(errs, os.RemoveAll(filepath.Join(o.DataDir, "tls", "acme-nodes", strconv.FormatInt(id, 10))))
		}
		return errors.Join(errs...)
	}
}

// settingsCached is a setting read again only once this process changed a setting
// (settings.Generation) or a minute went by (another process, the CLI): it is asked on
// every request a stranger makes.
func settingsCached(read func(context.Context) (string, error)) func() string {
	var (
		mu   sync.Mutex
		gen  uint64
		at   time.Time
		have bool
		v    string
	)
	return func() string {
		g := settings.Generation()
		mu.Lock()
		defer mu.Unlock()
		if !have || g != gen || time.Since(at) > time.Minute {
			if s, err := read(context.Background()); err == nil {
				v, gen, at, have = s, g, time.Now(), true
			}
		}
		return v
	}
}

// nodeState is what addons with nodes:read and inbounds:read learn of a node: whether the
// panel reached it, its version and connections, which listeners run.
func nodeState(m *nodesync.Manager) func(int64) panelapi.NodeState {
	return func(id int64) panelapi.NodeState {
		if m == nil {
			return panelapi.NodeState{}
		}
		hv, ok := m.Health(id)
		if !ok || hv.CheckedAt.IsZero() {
			return panelapi.NodeState{}
		}
		st := panelapi.NodeState{Known: true, OK: hv.OK, Version: hv.Health.Version, Conns: hv.Health.Conns, Statuses: map[string]bool{}}
		for _, l := range hv.Listeners {
			st.Statuses[l.Name] = l.OK
		}
		return st
	}
}
