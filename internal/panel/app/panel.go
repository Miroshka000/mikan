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

	"mikan/internal/nodeapi"
	"mikan/internal/nodetls"
	"mikan/internal/panel/acme"
	"mikan/internal/panel/addons"
	"mikan/internal/panel/api"
	"mikan/internal/panel/auth"
	"mikan/internal/panel/autotune"
	"mikan/internal/panel/billing"
	"mikan/internal/panel/checkhost"
	"mikan/internal/panel/dnscheck"
	"mikan/internal/panel/domain"
	"mikan/internal/panel/infraalerts"
	"mikan/internal/panel/nodesync"
	"mikan/internal/panel/nodeupdate"
	"mikan/internal/panel/panelimport"
	"mikan/internal/panel/promo"
	"mikan/internal/panel/server"
	"mikan/internal/panel/settings"
	"mikan/internal/panel/store"
	"mikan/internal/panel/store/db"
	"mikan/internal/panel/subpage"
	"mikan/internal/panel/subs"
	"mikan/internal/panel/tgbackup"
	"mikan/internal/panel/tgbot"
	"mikan/internal/panel/tlscert"
	"mikan/internal/panel/updates"
	"mikan/internal/panel/warp"
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
	Telegram    *tgbot.Bot
	Billing     *billing.Service
	Updates     *updates.Checker
	Alerts      *infraalerts.Monitor
	Backups     *tgbackup.Service
	Importer    *panelimport.Importer
	Addons      *addons.Manager
	server      *server.Server
	spa         *server.SPA
	subPage     *server.SPA
	sessions    *auth.Sessions
	st          *store.Store
	devices     *domain.Devices
	ipLimit     *auth.Limiter
	userLimit   *auth.Limiter
	now         func() time.Time
	log         *slog.Logger
}

type Options struct {
	Version    string
	Web        fs.FS
	TrustProxy bool
	Log        *slog.Logger
	Now        func() time.Time
	// Connect reaches a node; nil when the panel runs without nodes (tests, UI development).
	Connect nodesync.Connect
	// QUIC is a node's long-lived self-signed Hysteria2/TUIC certificate and its pin,
	// which subscription links carry.
	QUIC func(n db.Node) (*nodeapi.TLSFiles, string, error)
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
	// DataDir is where the host updater and the panel meet (update/); "" turns that off.
	DataDir string
	// Releases finds the release to update to; nil never checks.
	Releases updates.Source
	// WarpAPI is Cloudflare's WARP client API; "" is the real one.
	WarpAPI string
	// AddonsCatalog is the marketplace's signed catalog; "" is the real one.
	AddonsCatalog string
	// DNS checks new domains against public DNS; nil leaves them unchecked.
	DNS *dnscheck.Checker
	// HSTS says whether browsers are told to keep to HTTPS: for a panel that serves TLS
	// itself, while its certificate is trusted (see server.SetHSTS). nil: never.
	HSTS func() bool
	// Resolve looks up the names the panel is told to dial (REALITY targets); nil is the
	// system's resolver, tests set their own.
	Resolve func(ctx context.Context, host string) ([]netip.Addr, error)
	// CheckHost checks nodes' ports from Russia; nil: the check is not offered (tests).
	CheckHost *checkhost.Client
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
	var changes domain.Changes = noChanges{}
	deps := api.Deps{
		Version: o.Version, Store: st, Settings: set, Sessions: p.sessions,
		IPLimit: p.ipLimit, UserLimit: p.userLimit, TOTP: auth.NewTOTPGuard(),
		TrustProxy: o.TrustProxy, Log: o.Log, Now: o.Now, Pool: pool,
	}
	if o.Connect != nil {
		p.Nodes = nodesync.NewManager(st, set, pool, o.Connect, o.Log, o.Now)
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
	deps.Changes = changes
	deps.Users = domain.NewUsers(st, pool, changes, o.Now)
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
	p.st, p.devices = st, deps.Devices
	deps.Packages = domain.NewPackages(st, o.Now)
	if o.Certs != nil {
		deps.Cert, deps.RenewCert = o.Certs.Status, o.Certs.Renew
		deps.SetCert, deps.ClearCert = o.Certs.SetCustom, o.Certs.ClearCustom
	}
	deps.NodeCerts = o.NodeCerts
	deps.ForgetNode = forgetNodeFiles(o)
	subBase := func(ctx context.Context) string {
		ep, err := set.SubEndpoint(ctx)
		if err != nil || ep.Host == "" {
			return ""
		}
		paths, err := set.Paths(ctx)
		if err != nil {
			return ""
		}
		return "https://" + net.JoinHostPort(ep.Host, strconv.Itoa(ep.Port)) + "/" + paths.Sub
	}
	p.Addons = addons.New(o.DataDir, o.AddonsCatalog, o.Version, o.Log, o.Now)
	deps.Addons = p.Addons
	deps.DNS = o.DNS
	deps.CheckHost = o.CheckHost
	deps.DataDir = o.DataDir
	promos := promo.New(st, o.Now)
	promos.Changed = deps.Users.Changed
	p.Billing = billing.New(billing.Deps{Store: st, Settings: set, Users: deps.Users, Log: o.Log, Now: o.Now, TrustProxy: o.TrustProxy,
		MaxLinks: tgbot.MaxLinks,
		Addons:   deps.Addons, SubBase: subBase, Promo: promos})
	deps.Billing, deps.SubBase = p.Billing, subBase
	// The bot may reach Telegram through a node when the panel's server cannot.
	var tunnel func(ctx context.Context, nodeID int64, addr string) (net.Conn, error)
	if p.Nodes != nil {
		tunnel = p.Nodes.Tunnel
	}
	p.Telegram = tgbot.New(tgbot.Deps{Store: st, Settings: set, Devices: deps.Devices, SubBase: subBase, API: o.TelegramAPI, Log: o.Log, Now: o.Now, Billing: p.Billing,
		// Telegram apps refuse a Mini App on a self-signed certificate.
		MiniApp: func() bool { return o.Certs != nil && o.Certs.Status().Kind == "letsencrypt" }, Tunnel: tunnel})
	deps.Telegram = p.Telegram
	p.Billing.SetTelegram(p.Telegram)
	p.Updates = updates.New(o.DataDir, o.Version, o.Releases, o.Log, o.Now)
	deps.Updates = p.Updates
	var certStatus infraalerts.CertificateSource
	if o.Certs != nil {
		certStatus = o.Certs.Status
	}
	p.Alerts = infraalerts.New(st, settings.Cached(st.Q, loopSettingsTTL), p.Nodes, p.Tuner, certStatus, p.Updates, p.Telegram, o.Log, o.Now)
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
	p.Backups = tgbackup.New(selfDump, set, p.Telegram, o.DataDir, serverName, o.Now, o.Log)
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
		var domainName, publicHost, routing, fingerprint, rules string
		var groups subs.Groups
		for key, dst := range map[string]*string{settings.KeyDomain: &domainName, settings.KeyPublicHost: &publicHost, settings.KeyGroupMain: &groups.Main, settings.KeyGroupAuto: &groups.Auto,
			settings.KeyRouting: &routing, settings.KeyFingerprint: &fingerprint, settings.KeyRules: &rules} {
			if *dst, err = set.String(ctx, key); err != nil {
				return subs.Config{}, err
			}
		}
		cfg := subs.Config{Brand: brand, SupportURL: support, Groups: groups, Routing: subs.ParseRouting(routing), Fingerprint: fingerprint,
			Direct: []string{publicHost, domainName}, Lang: lang, Rules: subs.ServedRules(rules, groups.WithDefaults(lang))}
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
			if o.QUIC != nil {
				if _, pin, err := o.QUIC(n); err == nil {
					sn.Endpoint.PinSHA256 = pin
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
	subHandler.SetLogger(o.Log)
	subHandler.SetTelegram(p.Telegram)
	subHandler.SetShop(p.Billing)
	subHandler.SetPromo(promos)
	subHandler.SetPages(pages)

	adminMux := http.NewServeMux()
	adminMux.Handle("/api/", apiHandler)
	adminMux.Handle("/", p.spa)
	p.server = server.New(adminMux, subHandler)
	p.server.SetLegacy(subHandler.Legacy())
	if o.DataDir != "" {
		p.server.SetSite(server.OwnSite(filepath.Join(o.DataDir, "www")))
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
	// Before the reconcile loop: payments of the built-in providers take their adapters' names.
	if err := p.Billing.MoveBuiltin(ctx); err != nil {
		p.log.Error("billing: move the built-in providers", "err", err)
	}
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
	workers = append(workers, p.Telegram.Run, p.Billing.Run, p.Updates.Run, p.Alerts.Run, p.Backups.Run, p.Importer.Run,
		func(ctx context.Context) {
			every(ctx, 5*time.Second, func() {
				if _, err := p.Apply(ctx); err != nil {
					p.log.Error("reload settings", "err", err)
				}
			})
		},
		func(ctx context.Context) {
			every(ctx, 10*time.Minute, func() {
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
// (Options.NodeCerts) and the self-signed pair its QUIC protocols use, <data>/tls/nodes/<id>.
func forgetNodeFiles(o Options) func(id int64) error {
	return func(id int64) error {
		var errs []error
		if o.NodeCerts != nil {
			errs = append(errs, o.NodeCerts.Clear(id))
		}
		if o.DataDir != "" {
			errs = append(errs, os.RemoveAll(filepath.Join(o.DataDir, "tls", "nodes", strconv.FormatInt(id, 10))))
		}
		return errors.Join(errs...)
	}
}

func every(ctx context.Context, d time.Duration, fn func()) {
	t := time.NewTicker(d)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			fn()
		}
	}
}
