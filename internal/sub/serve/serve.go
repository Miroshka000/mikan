// Package serve answers the fetches of subscriptions over HTTP. It reads a subscription
// from a Source, picks the format the app takes (Format), sets every header the answer
// carries (headers.go), and has the cores' renderers write the profile, or a stub in place
// of the servers when there is nothing to connect to (stub.go).
package serve

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"sync"
	"time"

	"mikan/internal/frontpage"
	"mikan/internal/proto"
	"mikan/internal/sub/apps"
	"mikan/internal/sub/build"
	"mikan/internal/sub/mihomo"
	"mikan/internal/sub/model"
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
	// device only once it took a profile with an inbound's current port and target.
	Fetched(ctx context.Context, ip string)
	// Groups are the server groups the Mikan app shows (X-Mikan-Groups), their servers
	// named among ps, the proxies of the profile the app gets.
	Groups(ctx context.Context, ps []model.Proxy) (AppGroups, error)
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
	Direct  []string     // the panel's and nodes' hosts: kept out of the tunnel
	Groups  model.GroupNames
	Mode    routing.Mode
	Rules   []string       // the admin's own Clash rules, checked (routing.ServedRules)
	Routes  routing.Routes // services, direct apps and DNS (settings.KeyRoutes)
	// Template is the admin's own Clash profile (settings.KeyTemplate); it takes the place
	// of Groups, Mode, Rules and Routes for mihomo apps. "": none.
	Template string
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
// messenger's preview, the profile in the format the app asks for otherwise (Format). The
// order is fixed: the headers go before the keys are handed out (a HEAD stops after them),
// the device gets its keys before the profile is made, and the fetch is recorded once
// there is a profile to give.
func (h *Handler) Serve(w http.ResponseWriter, r *http.Request, token string) {
	ctx := r.Context()
	sub, err := h.src.Subscription(ctx, token)
	switch {
	case errors.Is(err, ErrNotFound):
		frontpage.NotFound(w)
		return
	case err != nil:
		internalError(w)
		return
	}
	cfg := sub.Config()
	ua := r.Header.Get("User-Agent")
	format := Format(ua, r.Header.Get("Accept"), r.URL.Query().Get("format"))
	// A messenger that builds a preview of a pasted link gets the page, which has the
	// preview's title and picture and nothing of the user's.
	if (format == FormatHTML || LinkPreview(ua) && r.URL.Query().Get("format") == "") && h.page != nil {
		h.page(w, r, cfg)
		return
	}
	u, err := sub.User(ctx)
	if err != nil {
		internalError(w)
		return
	}
	// The values are worked out only when a title or an announcement can use them.
	var vars map[string]string
	if cfg.Title != "" || cfg.Announce != "" || cfg.InactiveServer && cfg.InactiveText != "" {
		vars = Values(u, bound(ctx, sub, cfg), cfg, h.now())
		if cfg.Announce != "" {
			cfg.Announce = fillAppAnnounce(cfg.Announce, vars, ua)
		}
	}
	hd := w.Header()
	userInfoHeaders(hd, u, cfg, vars)
	h.operatorHeaders(hd, r, sub, u, cfg)
	appHeaders(hd, ua, cfg)
	hd.Set("Cache-Control", "no-store")
	if r.Method == http.MethodHead {
		return // apps peek at the traffic headers; the keys go only with a real fetch
	}
	keys, err := sub.Keys(ctx, Device{HWID: r.Header.Get("X-Hwid"), OS: r.Header.Get("X-Device-Os"), OSVersion: r.Header.Get("X-Ver-Os"),
		Model: r.Header.Get("X-Device-Model"), App: ua, IP: h.ip(r)})
	switch {
	case errors.Is(err, ErrDeviceLimit), errors.Is(err, ErrNoHWID), errors.Is(err, ErrDeviceBanned), errors.As(err, new(*UnboundError)):
		h.stub(w, cfg, format, err)
		return
	case err != nil:
		internalError(w)
		return
	}
	if notice := inactiveNotice(cfg, u.State, vars); notice != nil {
		h.stub(w, cfg, format, notice)
		return
	}
	ins, err := sub.Inbounds(ctx)
	if err != nil {
		internalError(w)
		return
	}
	app := apps.DetectApp(ua)
	ins = build.ForApp(ins, app, u.State.CanConnect())
	sub.Fetched(ctx, h.ip(r))
	proxies := build.Proxies(build.Profile{Keys: keys, Inbounds: ins, Nodes: cfg.Nodes, Fingerprint: cfg.Fingerprint,
		Skip: func(in model.Inbound, err error) {
			h.Warn("skip/"+strconv.FormatInt(in.ID, 10)+"/"+err.Error(), "subscription: an inbound is left out of the profiles", "inbound", in.Label, "err", err)
		}})
	prof := mihomo.Profile{Proxies: proxies, Nodes: cfg.Nodes, Direct: cfg.Direct, Lang: cfg.Lang}
	switch format {
	case FormatClash:
		prof.Rules, prof.Routes, prof.App = mihomo.RulesFor(cfg.Rules, app), cfg.Routes, app
		body, ps, err := h.clashProfile(prof, cfg)
		if err != nil {
			h.profileFailed(w, cfg, format, err)
			return
		}
		h.groupsHeader(ctx, hd, r, sub, ps)
		profileHeaders(hd, format, profileTitle(cfg, vars))
		_, _ = w.Write(body)
	case FormatSingBox:
		prof.Rules, prof.Routes = cfg.Rules, cfg.Routes
		body, err := singbox.Render(ctx, prof, cfg.Groups, cfg.Mode, cfg.Template)
		if err != nil {
			h.profileFailed(w, cfg, format, err)
			return
		}
		profileHeaders(hd, format, profileTitle(cfg, vars))
		_, _ = w.Write(body)
	default:
		links, ps, err := xray.Links(proxies)
		if err != nil {
			h.profileFailed(w, cfg, format, err)
			return
		}
		h.groupsHeader(ctx, hd, r, sub, ps)
		profileHeaders(hd, format, "")
		_, _ = w.Write(xray.Encode(links))
	}
}

// clashProfile is the Clash profile of prof: the admin's own (cfg.Template) when the app
// can take it, the built-in one otherwise; and the panel's proxies in it.
func (h *Handler) clashProfile(prof mihomo.Profile, cfg Config) ([]byte, []model.Proxy, error) {
	builtIn := func() ([]byte, []model.Proxy, error) {
		body, err := mihomo.Render(prof, cfg.Groups.WithDefaults(cfg.Lang), cfg.Mode)
		return body, prof.Proxies, err
	}
	if cfg.Template == "" || !prof.Routable() {
		return builtIn()
	}
	body, ps, err := mihomo.Template(prof, cfg.Template)
	var te *mihomo.TemplateError
	if errors.As(err, &te) {
		// Saved templates are checked; one that went bad must not leave apps without a profile.
		h.Warn("template/"+te.Code, "subscription: the own Clash profile does not render, the built-in one goes out", "err", err)
		return builtIn()
	}
	return body, ps, err
}

// profileFailed answers a profile that could not be made: with nothing for this app to
// connect to (model.ErrNoProxies) the stub that says so, otherwise the panel's own failure.
func (h *Handler) profileFailed(w http.ResponseWriter, cfg Config, format string, err error) {
	if errors.Is(err, model.ErrNoProxies) {
		h.stub(w, cfg, format, err)
		return
	}
	internalError(w)
}

// Announcement is cfg's announcement as the subscription page and the Mini App show it to
// u, the user of sub: filled with the user's values; "" without one.
func Announcement(ctx context.Context, sub Subscription, u User, cfg Config, now time.Time) string {
	if cfg.Announce == "" {
		return ""
	}
	return FillAnnounce(cfg.Announce, Values(u, bound(ctx, sub, cfg), cfg, now))
}

// bound counts the devices bound to the subscription for {devices}: -1 when the title and
// the announcement do not use it or binding is off.
func bound(ctx context.Context, sub Subscription, cfg Config) int64 {
	if !cfg.Binding || !wantsDevices(cfg.Title, cfg.Announce) {
		return -1
	}
	n, err := sub.Bound(ctx)
	if err != nil {
		return -1
	}
	return n
}

// operatorHeaders: the subscription page, the announcement and the app branding
// (OperatorHeaders). Devices are counted only for branding, which shows them.
func (h *Handler) operatorHeaders(hd http.Header, r *http.Request, sub Subscription, u User, cfg Config) {
	devices := int64(-1)
	var bot string
	if cfg.App.Enabled {
		if cfg.Binding && u.Devices != nil && u.Places {
			if n, err := sub.Bound(r.Context()); err == nil {
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
	g, err := sub.Groups(ctx, ps)
	if err != nil {
		// The profile still goes out: the app shows the groups of its last fetch.
		h.Warn("mikan_groups", "subscription: the server groups for the Mikan app are not read", "err", err)
		return
	}
	if v := g.header(); v != "" {
		hd.Set(GroupsHeader, v)
	}
}
