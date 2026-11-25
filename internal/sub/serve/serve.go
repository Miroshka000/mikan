// Package serve answers the fetches of subscriptions over HTTP. It reads a subscription
// from a Source, picks the format the app takes (Resolve), sets every header the answer
// carries (headers.go), and has the cores' renderers write the profile, or a stub in place
// of the servers when there is nothing to connect to (stub.go).
package serve

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"slices"
	"strconv"
	"sync"
	"time"

	"mikan/internal/frontpage"
	"mikan/internal/proto"
	"mikan/internal/sub/apps"
	"mikan/internal/sub/build"
	"mikan/internal/sub/mihomo"
	"mikan/internal/sub/model"
	"mikan/internal/sub/profile"
	"mikan/internal/sub/routing"
	"mikan/internal/sub/singbox"
	"mikan/internal/sub/xray"
)

// Source is where subscriptions come from: the panel's database (internal/panel/subsrc),
// or the panel's API for a subscription service apart from the panel.
type Source interface {
	// Subscription is the subscription of token, with the settings it is served by;
	// ErrNotFound when the token is no subscription's.
	Subscription(ctx context.Context, token string) (Subscription, error)
}

// ErrNotFound: the token is no subscription's.
var ErrNotFound = errors.New("subscription not found")

// Subscription is one user's subscription as one request reads it. A request asks for
// what it needs in the order it needs it, Inbounds before Groups.
type Subscription interface {
	// Config is the panel's settings the subscription is served by.
	Config() Config
	// User is what the subscription's headers and texts say of its user.
	User(ctx context.Context) (User, error)
	// Bound counts the devices bound to the subscription.
	Bound(ctx context.Context) (int64, error)
	// Bot is the link that opens the panel's bot for the subscription; "" without a bot.
	Bot(ctx context.Context) string
	// Keys are the keys of the device that asks: its own with device binding on, the
	// user's otherwise. ErrDeviceLimit, ErrNoHWID, ErrDeviceBanned or an *UnboundError
	// when it gets none.
	Keys(ctx context.Context, dev Device) (proto.Slot, error)
	// Inbounds are the inbounds the user may use now, in display order.
	Inbounds(ctx context.Context) ([]model.Inbound, error)
	// Fetched records that the device at ip took a profile: the block detector trusts a
	// device only once it took a profile with an inbound's current port and target. It is
	// told once a profile went out, never of a stub, a HEAD or a failure.
	Fetched(ctx context.Context, ip string)
	// Groups are the server groups of the Mikan app (X-Mikan-Groups): the ones the user
	// reaches and is not shut out of, with the traffic each used and no Proxies. An own
	// limit without a number is GroupUnlimited; a group on the main traffic or apart without
	// a limit is there with no servers too. The handler names the servers of each in the
	// profile and drops the groups left with nothing to show (AppGroups.named).
	Groups(ctx context.Context) (AppGroups, error)
	// Record writes one request in the subscription's request log. It is best effort: a
	// log that cannot be written is the source's to report, and never fails the request.
	Record(ctx context.Context, e LogEntry)
}

// Config is the panel's settings for subscriptions, read for every request so that a
// change applies without a restart.
type Config struct {
	Brand      string
	SupportURL string
	// Title is the profile's name in the apps (Profile-Title); "" names it Brand. Title and
	// Announce may hold the variables of Values, filled for each user.
	Title string
	// Announce is the text apps show over the profile, AnnounceURL where a tap on it leads.
	Announce    string
	AnnounceURL string
	App         AppBrand // the brand in apps that read operator headers
	Happ        Happ     // what Happ and INCY take beside the profile
	// SubBase is https://host:port/<sub path> as the panel hands links out; "" without an
	// address, and the request's own address is taken.
	SubBase string
	Nodes   []model.Node // enabled nodes in display order
	Direct  []string     // the panel's hosts: kept out of the tunnel; a node adds its own (Profile)
	Groups  model.GroupNames
	Mode    routing.Mode
	Rules   []routing.Rule // the admin's own Clash rules, checked (routing.ServedRules)
	Routes  routing.Routes // services, direct apps and DNS (settings.KeyRoutes)
	// Templates are the admin's own profiles of the cores, the subscription templates; the
	// one a profile is made of (Template) takes the place of Groups, Mode, Rules and Routes.
	// A simple one keeps the built-in profile and takes the place of Mode, Rules and Routes
	// with its routing in Routings, by its id (Routed).
	Templates []model.Template
	Routings  map[int64]Routing
	// Fingerprint is the default uTLS profile for inbounds that set none.
	Fingerprint string
	// Binding gives every device that sends its id keys of its own; RequireHWID refuses
	// apps that send none instead of seating them together.
	Binding     bool
	RequireHWID bool
	// Lang is the panel's default language, "" when unset: default group names and the
	// notices in place of servers are in it.
	Lang string
	// InactiveServer: a subscription that cannot connect gets one notice in place of its
	// servers (stub.go), InactiveText its words ("": the built-in ones by state).
	InactiveServer bool
	InactiveText   string
	// Responses decides a request by its headers before anything is made for it: the apps
	// the admin blocked and the response rules (rules.go); nil serves every request.
	Responses *apps.Responder
	// XrayJSON: the apps that run whole xray configs (apps.XrayJSON) get the xray JSON
	// profile in place of share links; a request that names a format gets it either way.
	XrayJSON bool
	// UpdateHours is how often the apps fetch the subscription again (Profile-Update-Interval);
	// 0: hourly, so a port or target the panel changed on its own reaches clients soon.
	UpdateHours int
	// Headers are more headers every answer with the profile or a stub carries
	// (apps.CheckHeaders); a response rule's own go over them.
	Headers []apps.Header
	// Page names which of its pages the source shows as the subscription page; 0: its own.
	// Only the source reads it (Handler.SetPage).
	Page int64
}

// Routing is what the built-in profile is made by beside the servers: the mode, the routes and
// the admin's own rules, checked (routing.ServedRules). The settings are one; a simple template
// has one of its own (Config.Routings).
type Routing struct {
	Mode   routing.Mode
	Routes routing.Routes
	Rules  []routing.Rule
}

// Routed is cfg and t as the profiles made of t are rendered by them. A simple template is the
// zero one, the built-in profile, with cfg made by its routing (WithRouting; one whose routing
// is not there keeps the settings'); any other template is as it is, with cfg as it is.
func (cfg Config) Routed(t model.Template) (Config, model.Template) {
	if !t.Simple {
		return cfg, t
	}
	if r, ok := cfg.Routings[t.ID]; ok {
		cfg = cfg.WithRouting(r)
	}
	return cfg, model.Template{}
}

// WithRouting is cfg with r in place of the settings' Mode, Rules and Routes: what the built-in
// profile is made by.
func (cfg Config) WithRouting(r Routing) Config {
	cfg.Mode, cfg.Rules, cfg.Routes = r.Mode, r.Rules, r.Routes
	return cfg
}

// Profile is the profile of the proxies ps as cfg sets it for every core: the admin's rules
// as they are saved, the routes, the nodes and the hosts kept out of the tunnel. The App
// and the rules its core parses (mihomo.RulesFor) are the Clash profile's to set.
func (cfg Config) Profile(ps []model.Proxy) profile.Profile {
	return profile.Profile{Proxies: ps, Nodes: cfg.Nodes, Direct: cfg.direct(ps), Rules: cfg.Rules, Routes: cfg.Routes, Lang: cfg.Lang}
}

// direct are the hosts ps' profile keeps out of the tunnel: the panel's, and those of the
// nodes ps has a proxy on. The hosts of the other nodes stay out of it: a subscription
// must not list servers its client has no access to (a node of another tariff, the server
// behind a CDN), or one trial account would give away them all.
func (cfg Config) direct(ps []model.Proxy) []string {
	on := map[int64]bool{}
	for _, p := range ps {
		on[p.Node] = true
	}
	out := slices.Clone(cfg.Direct)
	for _, n := range cfg.Nodes {
		if on[n.ID] {
			out = append(out, n.Hosts...)
		}
	}
	return out
}

// User is what a subscription's headers and texts say of its user.
type User struct {
	Token  string // the subscription's own, in its link
	Name   string
	Tariff string // the tariff's name; "" without one
	State  State
	// UsedUp and UsedDown are the bytes of the period.
	UsedUp, UsedDown int64
	Limit            *int64 // the traffic limit in bytes; nil: unlimited
	// Grants are the bytes left in the main traffic's packages, spent after Limit.
	Grants int64
	// Left is what is left of Limit and Grants; -1: unlimited.
	Left int64
	// Expires is when the subscription ends; zero: never.
	Expires time.Time
	// Devices is the device limit; nil: none. Places: it counts the devices bound, not the
	// ones online at once.
	Devices *int64
	Places  bool
}

// State is what a subscription can do now, as the panel works it out.
type State string

const (
	StateActive   State = "active"
	StateExpiring State = "expiring"
	StateLimited  State = "limited"
	StateExpired  State = "expired"
	StateDisabled State = "disabled"
)

// CanConnect: the subscription's apps connect.
func (s State) CanConnect() bool { return s == StateActive || s == StateExpiring }

// Device is the app that asks for its keys, as it says of itself.
type Device struct {
	HWID, OS, OSVersion, Model, App, IP string
	// SkipHWID: a response rule gives a device that sends no id its keys all the same,
	// whatever the panel requires of devices.
	SkipHWID bool
}

// Why a device gets no keys.
var (
	ErrDeviceLimit  = errors.New("device_limit")  // the user's places are taken
	ErrNoHWID       = errors.New("no_hwid")       // the app sends no device id and one is required
	ErrDeviceBanned = errors.New("device_banned") // the admin banned this device from the subscription
)

// UnboundError: the subscriber unbound the device; it connects again from Until, or, when
// Forever, once the admin lets it.
type UnboundError struct {
	Until   time.Time
	Forever bool
}

func (e *UnboundError) Error() string { return "device_unbound until " + e.Until.Format(time.RFC3339) }

// Handler answers the fetches of subscriptions.
type Handler struct {
	src Source
	now func() time.Time
	// ip is the address a request came from, as the nodes see it too: clients reach the
	// panel directly (its host is a DIRECT rule in the profile).
	ip func(r *http.Request) string
	// page sends the subscription page to a browser; nil: a browser gets the links.
	page   func(w http.ResponseWriter, r *http.Request, cfg Config)
	log    *slog.Logger
	logged sync.Map // what has been logged lately → when, so a standing fault is one line an hour
}

// New is a Handler of src's subscriptions.
func New(src Source, now func() time.Time, ip func(r *http.Request) string) *Handler {
	return &Handler{src: src, now: now, ip: ip, log: slog.New(slog.DiscardHandler)}
}

// SetPage sends the subscription page to browsers and to messengers building a link's
// preview.
func (h *Handler) SetPage(page func(w http.ResponseWriter, r *http.Request, cfg Config)) {
	h.page = page
}

// SetLogger sets where the handler says what it could not serve; without one it is silent.
func (h *Handler) SetLogger(l *slog.Logger) {
	if l != nil {
		h.log = l
	}
}

// Warn logs a fault that repeats with every subscription fetch once an hour, not at each.
func (h *Handler) Warn(key, msg string, args ...any) {
	now := h.now()
	if at, ok := h.logged.Load(key); ok && now.Sub(at.(time.Time)) < time.Hour {
		return
	}
	h.logged.Store(key, now)
	h.log.Warn(msg, args...)
}

// internalError answers a request the panel failed on, without telling why: the cause is
// the panel's to log, not the client's to read.
func internalError(w http.ResponseWriter) {
	http.Error(w, "internal error", http.StatusInternalServerError)
}

// Serve answers a fetch of the subscription of token: the page for a browser or a
// messenger's preview, the profile in the format the app asks for otherwise (Resolve). The
// response rules go first (rules.go): they may answer for the profile, or name the format
// and what the device is asked for. The order is fixed: the headers go before the keys are
// handed out (a HEAD stops after them), the device gets its keys before the profile is made,
// and the fetch is recorded once the profile went out.
func (h *Handler) Serve(w http.ResponseWriter, r *http.Request, token string) {
	sub, err := h.src.Subscription(r.Context(), token)
	switch {
	case errors.Is(err, ErrNotFound):
		frontpage.NotFound(w)
		return
	case err != nil:
		internalError(w)
		return
	}
	cfg := sub.Config()
	// Go keeps the request's Host out of its headers; a rule on Host must see it here as the
	// tester, which takes it as a header, does.
	hdr := r.Header.Clone()
	hdr.Set("Host", r.Host)
	ex := &exchange{dec: cfg.Responses.Decide(hdr)}
	outcome := h.answer(w, r, sub, cfg, ex)
	h.record(r, sub, ex, outcome)
	if ex.abort {
		// A connection that cannot be taken over to be closed (HTTP/2) is ended the way
		// net/http ends one on purpose: the client sees it cut, and nothing is logged.
		panic(http.ErrAbortHandler)
	}
}

// answer writes the answer to the request and says what it came to, for the request log;
// an empty outcome is a request that is not logged (a HEAD, a failure of the panel's own).
func (h *Handler) answer(w http.ResponseWriter, r *http.Request, sub Subscription, cfg Config, ex *exchange) Outcome {
	ctx := r.Context()
	dec := ex.dec
	switch dec.Responds() {
	case apps.Respond403, apps.Respond404, apps.RespondHTML, apps.RespondDrop:
		return h.refuse(w, r, ex)
	}
	ua := r.Header.Get("User-Agent")
	format := Resolve(r.Header, r.URL.Query().Get("format"), dec, cfg.XrayJSON)
	// A messenger that builds a preview of a pasted link gets the page, which has the
	// preview's title and picture and nothing of the user's.
	if (format == FormatHTML || LinkPreview(ua) && r.URL.Query().Get("format") == "") && h.page != nil {
		ex.format = FormatHTML
		h.page(w, r, cfg)
		return OutcomeServed
	}
	if format == FormatHTML {
		format = FormatURI // no page to send: a browser gets the links
	}
	ex.format = format
	u, err := sub.User(ctx)
	if err != nil {
		internalError(w)
		return ""
	}
	// The devices are counted once for the texts and the branding, which both may show them.
	count := sync.OnceValues(func() (int64, error) { return sub.Bound(ctx) })
	// The values are worked out only when a title or an announcement can use them.
	var vars map[string]string
	if cfg.Title != "" || cfg.Announce != "" || cfg.InactiveServer && cfg.InactiveText != "" {
		vars = Values(u, bound(cfg, count, cfg.Title, cfg.Announce), cfg, h.now())
		if cfg.Announce != "" {
			cfg.Announce = fillAppAnnounce(cfg.Announce, vars, ua)
		}
	}
	hd := w.Header()
	userInfoHeaders(hd, u, cfg, vars)
	h.operatorHeaders(hd, r, sub, u, cfg, count)
	// Happ's routing profile is the one of the Clash profile the request would get.
	clashCfg, clash := cfg.Routed(Template(cfg.Templates, FormatClash, dec.Action.Template))
	appHeaders(hd, ua, clashCfg, clash.Body)
	ruleHeaders(hd, cfg.Headers)
	ruleHeaders(hd, dec.Action.Headers)
	noStore(hd)
	if r.Method == http.MethodHead {
		return "" // apps peek at the traffic headers; the keys go only with a real fetch
	}
	if dec.Responds() == apps.RespondStub {
		// An app that is turned away gets no keys: it must not take a place of the
		// subscriber's devices.
		h.stub(w, cfg, ex, &unsupportedError{text: StubText(cfg.Lang, dec)})
		return OutcomeStub
	}
	keys, err := sub.Keys(ctx, Device{HWID: r.Header.Get("X-Hwid"), OS: r.Header.Get("X-Device-Os"), OSVersion: r.Header.Get("X-Ver-Os"),
		Model: r.Header.Get("X-Device-Model"), App: ua, IP: h.ip(r), SkipHWID: dec.Action.SkipHWID})
	switch {
	case errors.Is(err, ErrDeviceLimit), errors.Is(err, ErrNoHWID), errors.Is(err, ErrDeviceBanned), errors.As(err, new(*UnboundError)):
		h.stub(w, cfg, ex, err)
		return OutcomeStub
	case err != nil:
		internalError(w)
		return ""
	}
	var ins []model.Inbound
	payment := false
	if u.State == StateExpired {
		if s, ok := sub.(interface {
			PaymentProfile(context.Context) (proto.Slot, []model.Inbound, error)
		}); ok {
			var payKeys proto.Slot
			payKeys, ins, err = s.PaymentProfile(ctx)
			if err != nil {
				internalError(w)
				return ""
			}
			if payment = len(ins) > 0; payment {
				if payKeys.UUID != "" {
					keys = payKeys
				}
				// The renewal profile has one server: what was routed to the others goes by the
				// mode, which stays the panel's. With ru_direct the Russian checkout and bank
				// pages go past the tunnel, whose node lets only the listed hosts through.
				cfg.Routes, cfg.Rules, cfg.Templates = routing.Routes{}, nil, nil
				if cfg.Happ.Routing == xray.HappRoutingAuto {
					appHeaders(hd, ua, cfg, "")
					ruleHeaders(hd, cfg.Headers)
					ruleHeaders(hd, dec.Action.Headers)
				}
			}
		}
	}
	if !payment {
		if notice := inactiveNotice(cfg, u.State, vars); notice != nil {
			h.stub(w, cfg, ex, notice)
			return OutcomeStub
		}
		ins, err = sub.Inbounds(ctx)
	}
	if err != nil {
		internalError(w)
		return ""
	}
	ex.trace.gave(ins)
	app := apps.DetectApp(ua)
	ins = build.ForApp(ins, app, u.State.CanConnect())
	ex.trace.keep(ins)
	// The names of hosts may have the variables too.
	var names map[string]string
	if texts := build.HostNames(ins); len(texts) > 0 {
		names = Values(u, bound(cfg, count, texts...), cfg, h.now())
	}
	proxies := build.Proxies(build.Profile{Keys: keys, Inbounds: ins, Nodes: cfg.Nodes, Fingerprint: cfg.Fingerprint, Vars: names, Seed: u.Token, Groups: cfg.Groups.WithDefaults(cfg.Lang),
		Skip: func(in model.Inbound, err error) {
			ex.trace.skip(in, err)
			h.Warn("skip/"+strconv.FormatInt(in.ID, 10)+"/"+err.Error(), "subscription: an inbound is left out of the profiles", "inbound", in.Label, "err", err)
		}})
	ex.trace.made(proxies)
	tpl := ProfileTemplate(cfg.Templates, format, dec.Action.Template, app)
	ex.trace.template(tpl)
	// A simple template is the built-in profile made by its own routing.
	cfg, tpl = cfg.Routed(tpl)
	prof := cfg.Profile(proxies)
	switch format {
	case FormatClash:
		prof.Rules, prof.App = mihomo.RulesFor(cfg.Rules, app), app
		body, ps, err := h.clashProfile(ex, prof, cfg, tpl)
		if err != nil {
			return h.profileFailed(w, cfg, ex, err)
		}
		ex.trace.profile(prof, ps)
		h.groupsHeader(ctx, hd, r, sub, ps)
		profileHeaders(hd, format, profileTitle(cfg, vars))
		_, _ = w.Write(body)
	case FormatSingBox:
		body, err := h.render(ex, tpl, func(t model.Template) ([]byte, error) { return singbox.Render(ctx, prof, cfg.Groups, cfg.Mode, t) })
		if err != nil {
			return h.profileFailed(w, cfg, ex, err)
		}
		ex.trace.profile(prof, proxies)
		profileHeaders(hd, format, profileTitle(cfg, vars))
		_, _ = w.Write(body)
	case FormatXray:
		body, err := h.render(ex, tpl, func(t model.Template) ([]byte, error) { return xray.Render(prof, cfg.Groups, cfg.Mode, t) })
		if err != nil {
			return h.profileFailed(w, cfg, ex, err)
		}
		ex.trace.profile(prof, proxies)
		profileHeaders(hd, format, "")
		_, _ = w.Write(body)
	default:
		links, ps, err := xray.Links(proxies)
		if err != nil {
			return h.profileFailed(w, cfg, ex, err)
		}
		ex.trace.profile(prof, ps)
		h.groupsHeader(ctx, hd, r, sub, ps)
		profileHeaders(hd, format, "")
		_, _ = w.Write(xray.Encode(links))
	}
	// The profile went out. A client that hung up as it was written does not take the
	// record with it: the request's context may be cancelled by now.
	sub.Fetched(context.WithoutCancel(ctx), h.ip(r))
	return OutcomeServed
}

// clashProfile is the Clash profile of prof made of t (render), and the panel's proxies in
// it: of the built-in profile, prof's own.
func (h *Handler) clashProfile(ex *exchange, prof profile.Profile, cfg Config, t model.Template) ([]byte, []model.Proxy, error) {
	var ps []model.Proxy
	body, err := h.render(ex, t, func(t model.Template) (body []byte, err error) {
		if t.Body == "" {
			ps = prof.Proxies
			return mihomo.Render(prof, cfg.Groups.WithDefaults(cfg.Lang), cfg.Mode)
		}
		body, ps, err = mihomo.Template(prof, t.Body)
		return body, err
	})
	return body, ps, err
}

// profileFailed answers a profile that could not be made: with nothing for this app to
// connect to (model.ErrNoProxies) the stub that says so, otherwise the panel's own failure.
// The outcome is the request log's: a failure of the panel's own is not logged.
func (h *Handler) profileFailed(w http.ResponseWriter, cfg Config, ex *exchange, err error) Outcome {
	if errors.Is(err, model.ErrNoProxies) {
		h.stub(w, cfg, ex, err)
		return OutcomeStub
	}
	internalError(w)
	return ""
}

// Announcement is cfg's announcement as the subscription page and the Mini App show it to
// u, the user of sub: filled with the user's values; "" without one.
func Announcement(ctx context.Context, sub Subscription, u User, cfg Config, now time.Time) string {
	if cfg.Announce == "" {
		return ""
	}
	return FillAnnounce(cfg.Announce, Values(u, bound(cfg, func() (int64, error) { return sub.Bound(ctx) }, cfg.Announce), cfg, now))
}

// bound is the count of the devices bound to the subscription for {devices}: -1 when none
// of texts uses it or binding is off. count is the Subscription's Bound.
func bound(cfg Config, count func() (int64, error), texts ...string) int64 {
	if !cfg.Binding || !wantsDevices(texts...) {
		return -1
	}
	n, err := count()
	if err != nil {
		return -1
	}
	return n
}

// operatorHeaders: the subscription page, the announcement and the app branding
// (OperatorHeaders). Devices are counted (count, the Subscription's Bound) only for
// branding, which shows them.
func (h *Handler) operatorHeaders(hd http.Header, r *http.Request, sub Subscription, u User, cfg Config, count func() (int64, error)) {
	devices := int64(-1)
	var bot string
	if cfg.App.Enabled {
		if cfg.Binding && u.Devices != nil && u.Places {
			if n, err := count(); err == nil {
				devices = n
			}
		}
		bot = BotURL(sub.Bot(r.Context()))
	}
	page := PageURL(r)
	if cfg.SubBase != "" {
		page = cfg.SubBase + "/" + u.Token
	}
	OperatorHeaders(hd, u, cfg, page, bot, devices)
}

// groupsHeader puts X-Mikan-Groups on the answer to a Mikan app that asked for it: the
// groups of the subscription, whose servers in the profile are ps.
func (h *Handler) groupsHeader(ctx context.Context, hd http.Header, r *http.Request, sub Subscription, ps []model.Proxy) {
	if !wantsFeature(r.Header, featureGroups) {
		return
	}
	g, err := sub.Groups(ctx)
	if err != nil {
		// The profile still goes out: the app shows the groups of its last fetch.
		h.Warn("mikan_groups", "subscription: the server groups for the Mikan app are not read", "err", err)
		return
	}
	groupsHeaders(hd, g.named(ps))
}
