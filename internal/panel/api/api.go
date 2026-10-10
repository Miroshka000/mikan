package api

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/netip"
	"sync"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humago"

	"mikan/internal/nodeapi"
	"mikan/internal/nodetls"
	"mikan/internal/panel/acme"
	"mikan/internal/panel/addons"
	"mikan/internal/panel/audit"
	"mikan/internal/panel/auth"
	"mikan/internal/panel/autotune"
	"mikan/internal/panel/billing"
	"mikan/internal/panel/certcheck"
	"mikan/internal/panel/checkhost"
	"mikan/internal/panel/dnscheck"
	"mikan/internal/panel/domain"
	"mikan/internal/panel/nodesync"
	"mikan/internal/panel/nodeupdate"
	"mikan/internal/panel/panelimport"
	"mikan/internal/panel/secure"
	"mikan/internal/panel/server"
	"mikan/internal/panel/settings"
	"mikan/internal/panel/store"
	"mikan/internal/panel/store/db"
	"mikan/internal/panel/subs"
	"mikan/internal/panel/tgbackup"
	"mikan/internal/panel/tgbot"
	"mikan/internal/panel/tlscert"
	"mikan/internal/panel/updates"
	"mikan/internal/panel/warp"
)

type Deps struct {
	Version    string
	Store      *store.Store
	Settings   *settings.Settings
	Sessions   *auth.Sessions
	IPLimit    *auth.Limiter
	UserLimit  *auth.Limiter
	TOTP       *auth.TOTPGuard
	TrustProxy bool
	Log        *slog.Logger
	Now        func() time.Time

	Users     *domain.Users
	Inbounds  *domain.Inbounds
	Devices   *domain.Devices
	Packages  *domain.Packages
	Pool      *domain.Pool
	Changes   domain.Changes
	Online    func() map[string]nodeapi.Online
	Cert      func() acme.Status
	RenewCert func()
	// RenewCertNow tries the panel's certificate now and waits up to wait for the outcome;
	// done is false when the order still runs then. nil in development.
	RenewCertNow func(ctx context.Context, wait time.Duration) (st acme.Status, done bool)
	// NodeTLS is the certificate a node's protocols on TLS use now; nil: not known.
	NodeTLS func(ctx context.Context, n db.Node) *NodeTLSView
	// RenewNodeCert orders a remote node's public certificate now (acme.Nodes.Renew).
	RenewNodeCert func(ctx context.Context, id int64, wait time.Duration) (acme.NodeStatus, bool, error)
	// WakeNodeCerts has every remote node's certificate looked at now: another CA.
	WakeNodeCerts func()
	// CheckCerts is «Проверить сертификат»; nil in development.
	CheckCerts func(ctx context.Context) (certcheck.CertReport, error)
	// RoutesPreview renders a Clash profile with routing in place of the saved one
	// (subs.Handler.Preview); nil: no preview.
	RoutesPreview func(ctx context.Context, req subs.PreviewRequest) ([]byte, error)
	// CheckTemplate checks an own Clash profile (subs.Handler.CheckTemplate); nil: unchecked.
	CheckTemplate func(ctx context.Context, src string) error
	// HappLink is the Happ crypt link of a user's subscription (subs.Handler.HappLink); nil: none.
	HappLink func(ctx context.Context, u db.User) (string, error)
	// Nodes is the live side of the nodes; nil when the panel runs without them.
	Nodes NodeRuntime
	// PanelCert is the client certificate remote nodes pin; their join keys carry its hash.
	PanelCert func() (nodetls.Pair, error)
	// Tuner reports what the automatic moves see; nil when the panel runs without nodes.
	Tuner interface {
		Status(inboundID int64) (autotune.Status, bool)
	}
	// Telegram is the subscription owners' bot.
	Telegram *tgbot.Bot
	// Backups send the database to the admin's Telegram chat; nil in tests.
	Backups *tgbackup.Service
	// Importer brings users over from another panel; nil in tests that do not need it.
	Importer *panelimport.Importer
	// Billing sells tariffs; SubBase is https://host:port/<sub path> ("" without an address).
	Billing *billing.Service
	// Warp registers WARP accounts with Cloudflare.
	Warp    warp.Client
	SubBase func(ctx context.Context) string
	// SubPort opens subscriptions on a port of their own (0: closes it); SubPortError says
	// why the saved one is not served. nil: the panel runs no server (tests).
	SubPort      func(port int) error
	SubPortError func() string
	// SetCert installs the admin's own certificate for the panel, ClearCert goes back to
	// Let's Encrypt; nil in development.
	SetCert   func(ctx context.Context, certPEM, keyPEM []byte) error
	ClearCert func() error
	// NodeCerts keeps the nodes' own certificates; nil: nodes cannot have one.
	NodeCerts *tlscert.NodeStore
	// ForgetNode removes the certificates and keys kept on disk for a node id; nil: none.
	ForgetNode func(id int64) error
	// Updates knows the newest release and talks to the host updater; nil in tests.
	Updates *updates.Checker
	// NodeUpdates updates the remote nodes to the panel's version; nil without nodes.
	NodeUpdates *nodeupdate.Service
	// Addons are the marketplace's payment adapters; nil in tests.
	Addons *addons.Manager
	// Resolve looks a name up for what the panel dials on the admin's word (a REALITY
	// target); nil asks the system's resolver.
	Resolve func(ctx context.Context, host string) ([]netip.Addr, error)
	// DNS checks that a domain leads to the panel's or the node's server; nil: unchecked
	// (tests, development).
	DNS *dnscheck.Checker
	// CheckHost checks a node's ports from Russia (check-host.net); nil: not offered.
	CheckHost *checkhost.Client
	// DataDir is the panel's data directory, whose disk "Check server" looks at; "": none.
	DataDir string
}

// NodeRuntime is what the API needs from the running nodes.
type NodeRuntime interface {
	Health(id int64) (nodesync.HealthView, bool)
	// Validate runs mihomo's parser on an inbound on the node that will run it.
	Validate(ctx context.Context, id int64, req nodeapi.ValidateRequest) error
	// Retire makes a node drop its listeners and users before it is removed.
	Retire(ctx context.Context, id int64) error
	NodesChanged()
	// ScanTargets looks for REALITY targets from the node itself (its RTTs, its routes).
	ScanTargets(ctx context.Context, id int64, req nodeapi.TargetScanRequest) (nodeapi.TargetScan, error)
	// Warp checks the node's way out through WARP; force skips the node's cached answer.
	Warp(ctx context.Context, id int64, force bool) (nodeapi.WarpStatus, error)
	// Probe checks the internet through one outbound of a node (NODE-<id> of a cascade).
	Probe(ctx context.Context, id int64, proxy string) (nodeapi.ProbeResult, error)
	// SpeedTest measures a node's own way to the internet.
	SpeedTest(ctx context.Context, id int64) (nodeapi.SpeedTest, error)
	// CheckNow asks a node for its health at once (Check node, a node's hello).
	CheckNow(ctx context.Context, id int64) (nodesync.HealthView, bool)
	// Hello is a node's last hello since the panel started.
	Hello(id int64) (nodesync.HelloView, bool)
	// Diagnose asks a node how its server is: DNS, the internet, GitHub, disk, memory.
	Diagnose(ctx context.Context, id int64) (nodeapi.Diagnosis, error)
}

type ctxKey int

const (
	keyClient ctxKey = iota
	keySession
)

type client struct{ IP, UserAgent string }

type handlers struct {
	d         Deps
	api       huma.API
	dummyHash string
	// hashSem bounds the password hashes that run at once (see verifyPassword).
	hashSem chan struct{}

	pendingMu sync.Mutex
	pending   map[int64]pendingTOTP

	metricsCache metricsCache

	// speedCooldown spaces the speed tests of a node.
	speedCooldown speedCooldown
}

// Config builds the huma config shared by the server and the `mikan openapi` command.
func Config(version string) huma.Config {
	// Handlers always return non-nil slices; nullable arrays would force null checks in the UI.
	huma.DefaultArrayNullable = false
	cfg := huma.DefaultConfig("mikan", version)
	// No docs UI, no spec endpoint and no $schema links at runtime: the spec is
	// exported by the CLI at build time for the TypeScript client.
	cfg.DocsPath = ""
	cfg.OpenAPIPath = ""
	cfg.SchemasPath = ""
	cfg.CreateHooks = nil
	cfg.Info.Description = "REST API панели mikan. Все пути — под секретным адресом админки: https://<панель>/<секретный путь>/api/v1/…\n\n" +
		"Скрипты и интеграции авторизуются ключом API (Настройки → API): заголовок `Authorization: Bearer mk_…`. " +
		"Ключ «чтение» выполняет только GET и не получает ссылок подписок и секретных адресов; «полный» меняет данные, кроме входа, сессий, самих ключей и операций, где уходят деньги, ключи и адреса клиентов (они помечены «только сессия»).\n\n" +
		"Админка в браузере ходит с cookie сессии; изменяющие запросы тогда требуют заголовок `X-CSRF-Token` из `GET /auth/me`.\n\n" +
		"Ошибки — RFC 9457 (application/problem+json): `detail` — код ошибки, `errors[].message` — код по полю.\n\n" +
		"## Подписка в приложениях\n\n" +
		"Ссылка подписки (`sub_url` пользователя) отдаёт конфиг и заголовки, которые читают приложения: " +
		"`profile-title` — название профиля (настройка `sub_title`, пусто — бренд), `announce` — строка над профилем или под его названием (`sub_announce`), " +
		"`subscription-userinfo` — трафик и срок, `support-url`, `profile-web-page-url`. Настраиваются в `PATCH /api/v1/settings`.\n\n" +
		"В `sub_title` и `sub_announce` подставляются переменные каждого пользователя: `{brand}` — бренд, `{name}` — имя, " +
		"`{date}` — дата окончания (ДД.ММ.ГГГГ, МСК), `{days}` — дней осталось, `{used}` — израсходовано, `{left}` — осталось трафика, " +
		"`{total}` — лимит с пакетами. Без срока или лимита — `∞`. Неизвестное слово в фигурных скобках остаётся текстом. Если название после подстановки пустое, берётся бренд. Пример: `{brand} · до {date}`."
	cfg.Components.SecuritySchemes = map[string]*huma.SecurityScheme{
		"apiKey":  {Type: "http", Scheme: "bearer", Description: "Ключ API: Authorization: Bearer mk_…"},
		"session": {Type: "apiKey", In: "cookie", Name: auth.CookieName, Description: "Сессия админки + заголовок X-CSRF-Token на изменяющих запросах"},
	}
	cfg.Security = []map[string][]string{{"apiKey": {}}, {"session": {}}}
	return cfg
}

var hideInternalOnce sync.Once

// hideInternal keeps the text of unexpected errors (SQL constraints, file paths) out of
// responses: huma would put it into the 500 body. It is logged instead.
func hideInternal(log *slog.Logger) {
	hideInternalOnce.Do(func() {
		base := huma.NewError
		huma.NewError = func(status int, msg string, errs ...error) huma.StatusError {
			if status >= 500 && len(errs) > 0 {
				if log != nil {
					log.Error("api internal error", "status", status, "err", errors.Join(errs...))
				}
				return base(status, msg)
			}
			return base(status, msg, errs...)
		}
	})
}

func New(d Deps) (http.Handler, huma.API, error) {
	hideInternal(d.Log)
	mux := http.NewServeMux()
	api := humago.New(mux, Config(d.Version))
	dummy, err := auth.HashPassword(secure.Token(32))
	if err != nil {
		return nil, nil, err
	}
	h := &handlers{d: d, api: api, dummyHash: dummy, hashSem: make(chan struct{}, 4), pending: map[int64]pendingTOTP{}}
	api.UseMiddleware(h.middleware)
	h.registerAuth()
	h.registerUsers()
	h.registerFolders()
	h.registerHapp()
	h.registerCatalog()
	h.registerInbounds()
	h.registerTargets()
	h.registerStats()
	h.registerNodeTraffic()
	h.registerMetrics()
	h.registerBackups()
	h.registerImport()
	h.registerSettings()
	h.registerSubPage()
	h.registerRoutes()
	h.registerCerts()
	h.registerTelegram()
	h.registerUpdates()
	h.registerNodes()
	h.registerNodeChecks()
	h.registerTorrent()
	h.registerFilters()
	h.registerSpeedTests()
	h.registerNodeUpdates()
	h.registerAPIKeys()
	h.registerPayments()
	h.registerPromocodes()
	h.registerAddons()
	h.registerWarp()
	h.registerCascade()
	h.registerPools()
	h.registerPackages()
	h.registerAudit()
	return noStore(mux), api, nil
}

func noStore(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		next.ServeHTTP(w, r)
	})
}

func (h *handlers) middleware(ctx huma.Context, next func(huma.Context)) {
	ctx = huma.WithValue(ctx, keyClient, client{IP: server.ClientIP(http.Header{"X-Forwarded-For": {ctx.Header("X-Forwarded-For")}}, ctx.RemoteAddr(), h.d.TrustProxy), UserAgent: ctx.Header("User-Agent")})
	op := ctx.Operation()
	mutating := op.Method != http.MethodGet && op.Method != http.MethodHead
	if mutating && !sameOrigin(ctx) {
		_ = huma.WriteErr(h.api, ctx, http.StatusForbidden, "csrf")
		return
	}
	if public, _ := op.Metadata["public"].(bool); public {
		next(ctx)
		return
	}
	if ctx.Header("Authorization") != "" {
		h.bearer(ctx, next, mutating)
		return
	}
	ck, err := huma.ReadCookie(ctx, auth.CookieName)
	if err != nil {
		_ = huma.WriteErr(h.api, ctx, http.StatusUnauthorized, "unauthorized")
		return
	}
	sess, err := h.d.Sessions.Lookup(ctx.Context(), ck.Value)
	if err != nil {
		if err != auth.ErrNoSession {
			h.d.Log.Error("session lookup", "err", err)
			_ = huma.WriteErr(h.api, ctx, http.StatusInternalServerError, "internal error")
			return
		}
		_ = huma.WriteErr(h.api, ctx, http.StatusUnauthorized, "unauthorized")
		return
	}
	if mutating && !secure.Equal(ctx.Header("X-CSRF-Token"), sess.CsrfToken) {
		_ = huma.WriteErr(h.api, ctx, http.StatusForbidden, "csrf")
		return
	}
	next(huma.WithValue(ctx, keySession, sess))
}

// sameOrigin rejects cross-site browser requests. Non-browser clients send neither
// Sec-Fetch-Site nor Origin; they still need the CSRF header for authenticated calls.
func sameOrigin(ctx huma.Context) bool {
	h := http.Header{"Sec-Fetch-Site": {ctx.Header("Sec-Fetch-Site")}, "Origin": {ctx.Header("Origin")}}
	return server.FetchSite(h, ctx.Host()) != server.SiteCross
}

func clientOf(ctx context.Context) client {
	c, _ := ctx.Value(keyClient).(client)
	return c
}

func sessionOf(ctx context.Context) db.Session {
	s, _ := ctx.Value(keySession).(db.Session)
	return s
}

func (h *handlers) audit(ctx context.Context, adminID int64, action, targetType, targetID string, details any) {
	// What an API key did says which key: the admin may hand keys to several scripts.
	if k, ok := apiKeyOf(ctx); ok {
		m := map[string]any{"api_key": k.Prefix}
		if d, isMap := details.(map[string]any); isMap {
			for key, v := range d {
				m[key] = v
			}
		} else if details != nil {
			m["details"] = details
		}
		details = m
	}
	err := audit.Write(ctx, h.d.Store.Q, h.d.Now(), audit.Entry{
		AdminID: adminID, Action: action, TargetType: targetType, TargetID: targetID,
		IP: clientOf(ctx).IP, Details: details,
	})
	if err != nil {
		h.d.Log.Warn("audit write failed", "action", action, "err", err)
	}
}
