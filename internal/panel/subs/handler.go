package subs

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"mikan/internal/panel/billing"
	"mikan/internal/panel/domain"
	"mikan/internal/panel/panelimport"
	"mikan/internal/panel/promo"
	"mikan/internal/panel/server"
	"mikan/internal/panel/store"
	"mikan/internal/panel/store/db"
	"mikan/internal/panel/subpage"
	"mikan/internal/panel/turn"
	"mikan/internal/proto"
)

// Config is resolved per request, so settings changes apply without a restart.
type Config struct {
	Brand      string
	SupportURL string
	// Title is the profile's name in the apps (Profile-Title); "" names it Brand. Title and
	// Announce may hold the variables of titleValues, filled for each user.
	Title string
	// Announce is the text apps show over the profile, AnnounceURL where a tap on it leads.
	Announce    string
	AnnounceURL string
	App         AppBrand // the brand in apps that read operator headers
	Happ        Happ     // Happ's provider headers and the crypt link of the page
	// SubBase is https://host:port/<sub path> as the panel hands links out; "" without an
	// address, and the request's own address is taken.
	SubBase string
	// Legacy checks the signed links of the panel users were imported from (Legacy).
	Legacy  panelimport.Verifier
	Nodes   []Node   // enabled nodes in display order
	Direct  []string // the panel's and nodes' hosts: kept out of the tunnel
	Groups  Groups
	Routing Routing
	Rules   []string // the admin's own Clash rules, checked (ServedRules)
	Routes  Routes   // services, direct apps and DNS (settings.KeyRoutes)
	// Template is the admin's own Clash profile (settings.KeyTemplate, see Template); it
	// takes the place of Groups, Routing, Rules and Routes for mihomo apps. "": none.
	Template string
	// Fingerprint is the default uTLS profile for inbounds that set none.
	Fingerprint string
	// Binding gives every device that sends its id keys of its own (domain.Devices);
	// RequireHWID refuses apps that send none instead of seating them together.
	Binding     bool
	RequireHWID bool
	// Lang is the panel's default language, "" when unset: default group names and the
	// notices in place of servers are in it.
	Lang string
}

// Binder hands devices their keys (domain.Devices).
type Binder interface {
	Bind(ctx context.Context, u db.User, in domain.DeviceInfo, requireHWID bool) (db.Slot, error)
	Unbind(ctx context.Context, userID, deviceID int64, bySubscriber bool) error
	UnbindState(ctx context.Context, userID int64) (domain.UnbindState, error)
	Rename(ctx context.Context, userID, deviceID int64, name string) (string, error)
	Banned(ctx context.Context, userID int64, hwid string) (bool, error)
}

// Reissuer gives a subscription a new link at its subscriber's request (domain.Users).
type Reissuer interface {
	ReissueByOwner(ctx context.Context, userID int64, via string) (db.User, error)
	ReissueState(ctx context.Context, userID int64) (domain.ReissueState, error)
}

type Handler struct {
	st       *store.Store
	cfg      func(ctx context.Context) (Config, error)
	page     http.Handler // browser page (SPA entry); nil = plain links
	now      func() time.Time
	devices  Binder
	reissuer Reissuer // nil: the Mini App has no new-link button
	// trustProxy takes the client's IP from X-Forwarded-For (a reverse proxy in front).
	trustProxy bool
	tg         Telegram         // nil: no bot
	shop       *billing.Service // nil: nothing on sale
	promos     *promo.Service
	pages      *subpage.Service // nil: the page as built, nothing of the admin's
	happ       *HappLinks       // Happ crypt links, kept per subscription
	log        *slog.Logger
	logged     sync.Map // what has been logged lately → when, so a standing fault is one line an hour
	promoLimit promoLimiter
}

// Telegram is the bot's part in subscriptions: the page's "Open in Telegram" link and the
// Mini App's sign-in.
type Telegram interface {
	LinkURL(ctx context.Context, userID int64) string
	MiniAppUser(ctx context.Context, initData string) (int64, []db.User, error)
}

// SetReissuer lets subscribers get a new link from the Mini App.
func (h *Handler) SetReissuer(r Reissuer) { h.reissuer = r }

// SetTelegram plugs in the bot.
func (h *Handler) SetTelegram(tg Telegram) { h.tg = tg }

// SetLogger sets where the handler says what it could not serve; without one it is silent.
func (h *Handler) SetLogger(l *slog.Logger) {
	if l != nil {
		h.log = l
	}
}

// warn logs a fault that repeats with every subscription fetch once an hour, not at each.
func (h *Handler) warn(key, msg string, args ...any) {
	now := h.now()
	if at, ok := h.logged.Load(key); ok && now.Sub(at.(time.Time)) < time.Hour {
		return
	}
	h.logged.Store(key, now)
	h.log.Warn(msg, args...)
}

// SetShop takes payments: the providers' webhooks under /pay/ and the Mini App's shop.
func (h *Handler) SetShop(s *billing.Service) { h.shop = s }
func (h *Handler) SetPromo(s *promo.Service)  { h.promos = s }

// SetPages gives the page what the admin made of it: its look, blocks, instructions, images.
func (h *Handler) SetPages(s *subpage.Service) { h.pages = s }

func NewHandler(st *store.Store, cfg func(ctx context.Context) (Config, error), page http.Handler, now func() time.Time, devices Binder, trustProxy bool) *Handler {
	return &Handler{st: st, cfg: cfg, page: page, now: now, devices: devices, trustProxy: trustProxy, log: slog.New(slog.DiscardHandler), happ: NewHappLinks()}
}

// HappLink is the Happ crypt link of u's subscription, "" when the crypt link is off or
// the panel has no address for subscriptions yet.
func (h *Handler) HappLink(ctx context.Context, u db.User) (string, error) {
	cfg, err := h.cfg(ctx)
	if err != nil || cfg.Happ.Crypt == "" || cfg.SubBase == "" {
		return "", err
	}
	return h.happ.Link(ctx, cfg.Happ.Crypt, cfg.SubBase+"/"+u.SubToken)
}

// clientIP is the device's address as the nodes see it too: clients reach the panel
// directly (its host is a DIRECT rule in the profile).
func (h *Handler) clientIP(r *http.Request) string {
	return server.ClientIP(r.Header, r.RemoteAddr, h.trustProxy)
}

var devicePath = regexp.MustCompile(`^devices/([0-9]{1,18})/(unbind|name)$`)

// ServeHTTP handles "/<token>", "/<token>/info", "POST /<token>/devices/<id>/unbind" and
// "POST /<token>/devices/<id>/name" (the subscription page), the page assets under the sub
// prefix, the admin's images
// ("/brand/<name>") and instructions ("/<token>/docs/<id>": for subscribers only, not for
// whoever counts ids). It only routes: the Mini App is miniApp, a subscription is
// serveProfile.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	p := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
	token, rest, _ := strings.Cut(p, "/")
	if token == "tg" && h.tg != nil {
		h.miniApp(w, r, rest)
		return
	}
	if token == "pay" && h.shop != nil {
		r2 := r.Clone(r.Context())
		r2.URL.Path = "/" + rest
		h.shop.Webhook().ServeHTTP(w, r2)
		return
	}
	device := devicePath.FindStringSubmatch(rest)
	switch {
	case r.Method == http.MethodPost && device != nil:
	case r.Method != http.MethodGet && r.Method != http.MethodHead:
		server.NotFound(w)
		return
	case (strings.HasPrefix(p, "assets/") || p == "favicon.png" || p == "apple-touch-icon.png") && h.page != nil:
		h.page.ServeHTTP(w, r)
		return
	case token == "brand" && h.pages != nil:
		h.image(w, r, rest)
		return
	case rest != "" && rest != "info" && !strings.HasPrefix(rest, "docs/"):
		server.NotFound(w)
		return
	}
	if len(token) != 24 {
		server.NotFound(w)
		return
	}
	u, err := h.st.Q.GetUserBySubToken(r.Context(), token)
	if err != nil {
		server.NotFound(w)
		return
	}
	if device != nil {
		id, _ := strconv.ParseInt(device[1], 10, 64)
		if device[2] == "name" {
			h.renameDevice(w, r, u, id)
		} else {
			h.unbind(w, r, u, id)
		}
		return
	}
	if id, ok := strings.CutPrefix(rest, "docs/"); ok {
		if h.pages == nil {
			server.NotFound(w)
			return
		}
		h.doc(w, r, id)
		return
	}
	cfg, err := h.cfg(r.Context())
	if err != nil {
		internalError(w)
		return
	}
	if rest == "info" {
		prof, err := h.profile(r.Context(), u, cfg, db.Slot{}, nil)
		if err != nil {
			internalError(w)
			return
		}
		h.info(r.Context(), w, u, prof, cfg)
		return
	}
	h.serveProfile(w, r, u, cfg)
}

// internalError answers a request the panel failed on, without telling why: the cause is
// the panel's to log, not the client's to read.
func internalError(w http.ResponseWriter) {
	http.Error(w, "internal error", http.StatusInternalServerError)
}

// serveProfile answers a fetch of "/<token>" by u: the page for a browser or a messenger's
// preview, the profile in the format the app asks for otherwise (Format). The order is
// fixed: the headers go before the keys are handed out (a HEAD stops after them), the
// device gets its keys before the profile is made, and the fetch is recorded once there
// is a profile to give.
func (h *Handler) serveProfile(w http.ResponseWriter, r *http.Request, u db.User, cfg Config) {
	format := Format(r.Header.Get("User-Agent"), r.Header.Get("Accept"), r.URL.Query().Get("format"))
	// A messenger that builds a preview of a pasted link gets the page, which has the
	// preview's title and picture and nothing of the user's.
	if (format == "html" || LinkPreview(r.Header.Get("User-Agent")) && r.URL.Query().Get("format") == "") && h.page != nil {
		h.servePage(w, r, cfg)
		return
	}
	grants, err := domain.UserGrantsLeft(r.Context(), h.st.Q, u.ID, h.now())
	if err != nil {
		internalError(w)
		return
	}
	// The values are worked out only when a title or an announcement can use them.
	var vars map[string]string
	if cfg.Title != "" || cfg.Announce != "" {
		vars = titleValues(u, grants.Main(u.ID), h.boundFor(r.Context(), u, cfg), cfg, h.now())
		if cfg.Announce != "" {
			cfg.Announce = fillAppAnnounce(cfg.Announce, vars, r.UserAgent())
		}
	}
	h.userInfoHeaders(w, u, grants.Main(u.ID), cfg, vars)
	h.operatorHeaders(w, r, u, cfg)
	switch ua := r.UserAgent(); {
	case IsINCY(ua):
		// INCY reads Happ's routing profile from the same header (docs.incy.cc/en/routing),
		// and none of Happ's provider headers.
		if routing := cfg.Happ.Routing; routing == HappRoutingAuto {
			w.Header().Set("routing", incyLink(happAutoRouting(cfg)))
		} else if routing != "" {
			w.Header().Set("routing", incyLink(routing))
		}
	case IsHapp(ua):
		happ := cfg.Happ
		if happ.Routing == HappRoutingAuto {
			happ.Routing = happAutoRouting(cfg)
		}
		happHeaders(w.Header(), happ)
	}
	w.Header().Set("Cache-Control", "no-store")
	if r.Method == http.MethodHead {
		return // apps peek at the traffic headers; the keys go only with a real fetch
	}
	slot, err := h.slotFor(r, u, cfg)
	switch {
	case errors.Is(err, domain.ErrDeviceLimit), errors.Is(err, domain.ErrNoHWID), errors.Is(err, domain.ErrDeviceBanned), errors.Is(err, domain.ErrDeviceUnbound):
		h.stub(w, u, cfg, format, err)
		return
	case err != nil:
		internalError(w)
		return
	}
	prof, err := h.profile(r.Context(), u, cfg, slot, grants)
	if err != nil {
		internalError(w)
		return
	}
	prof.Skip = func(in db.Inbound, err error) {
		h.warn("skip/"+strconv.FormatInt(in.ID, 10)+"/"+err.Error(), "subscription: an inbound is left out of the profiles", "inbound", in.Name, "err", err)
	}
	app := DetectApp(r.Header.Get("User-Agent"))
	prof.Inbounds = forApp(prof.Inbounds, app, domain.State(u, grants.Main(u.ID), h.now()))
	// The block detector trusts a device only once it took a profile with an inbound's
	// current port and target (see autotune.Detect).
	_ = h.st.Q.RecordSubFetch(r.Context(), db.RecordSubFetchParams{UserID: u.ID, Ip: h.clientIP(r), FetchedAt: h.now().Unix()})
	switch format {
	case "clash":
		prof.Rules = RulesFor(cfg.Rules, app)
		prof.Routes, prof.App, prof.Lang = cfg.Routes, app, cfg.Lang
		body, err := h.clashProfile(prof, cfg)
		if err != nil {
			h.profileFailed(w, u, cfg, format, err)
			return
		}
		w.Header().Set("Content-Type", "text/yaml; charset=utf-8")
		// Clash apps name the profile after the file: the same name as Profile-Title.
		w.Header().Set("Content-Disposition", "attachment; filename*=UTF-8''"+url.PathEscape(fileName(profileTitle(cfg, vars)))+".yaml")
		_, _ = w.Write(body)
	case "singbox":
		prof.Rules, prof.Routes, prof.Lang = cfg.Rules, cfg.Routes, cfg.Lang
		body, err := SingBox(r.Context(), prof, cfg.Groups, cfg.Routing, cfg.Template)
		if err != nil {
			h.profileFailed(w, u, cfg, format, err)
			return
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Header().Set("Content-Disposition", "attachment; filename*=UTF-8''"+url.PathEscape(fileName(profileTitle(cfg, vars)))+".json")
		_, _ = w.Write(body)
	case "turn":
		// The parameters of the WL servers, for the client program next to the apps.
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte(TurnSubscription(prof)))
	default:
		links, err := URIs(prof)
		if err != nil {
			h.profileFailed(w, u, cfg, format, err)
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte(base64.StdEncoding.EncodeToString([]byte(links))))
	}
}

// clashProfile is the Clash profile of prof: the admin's own (cfg.Template) when the app can
// take it, the built-in one otherwise.
func (h *Handler) clashProfile(prof Profile, cfg Config) ([]byte, error) {
	if cfg.Template == "" || !prof.routable() {
		return Mihomo(prof, cfg.Groups.WithDefaults(cfg.Lang), cfg.Routing)
	}
	body, err := Template(prof, cfg.Template)
	var te *TemplateError
	if errors.As(err, &te) {
		// Saved templates are checked; one that went bad must not leave apps without a profile.
		h.warn("template/"+te.Code, "subscription: the own Clash profile does not render, the built-in one goes out", "err", err)
		return Mihomo(prof, cfg.Groups.WithDefaults(cfg.Lang), cfg.Routing)
	}
	return body, err
}

// profileFailed answers a profile that could not be made: with nothing for this app to
// connect to (ErrNoProxies) the stub that says so, otherwise the panel's own failure.
func (h *Handler) profileFailed(w http.ResponseWriter, u db.User, cfg Config, format string, err error) {
	if errors.Is(err, ErrNoProxies) {
		h.stub(w, u, cfg, format, err)
		return
	}
	internalError(w)
}

// forApp keeps the inbounds the app can use. Inbounds with one key for everyone go only
// to users whose access is on: the node cannot cut anyone off there.
func forApp(ins []db.Inbound, app App, state string) []db.Inbound {
	active := domain.CanConnect(state)
	out := make([]db.Inbound, 0, len(ins))
	for _, in := range ins {
		t, err := parseTemplate(in.Config)
		if err != nil || !app.Supports(proto.NeedsOf(t)) || proto.Shared(t.Type()) && !active {
			continue
		}
		out = append(out, in)
	}
	return out
}

// slotFor is whose keys this request gets: its device's own with binding on, the user's
// otherwise.
func (h *Handler) slotFor(r *http.Request, u db.User, cfg Config) (db.Slot, error) {
	if !cfg.Binding || h.devices == nil {
		// Without binding every device shares the user's keys, yet a device the admin banned
		// while binding was on still gets none.
		if h.devices != nil {
			if banned, err := h.devices.Banned(r.Context(), u.ID, r.Header.Get("X-Hwid")); err != nil {
				return db.Slot{}, err
			} else if banned {
				return db.Slot{}, domain.ErrDeviceBanned
			}
		}
		if !u.SlotID.Valid {
			return db.Slot{}, errors.New("user has no slot")
		}
		return h.st.Q.GetSlot(r.Context(), u.SlotID.Int64)
	}
	return h.devices.Bind(r.Context(), u, domain.DeviceInfo{
		HWID: r.Header.Get("X-Hwid"), OS: r.Header.Get("X-Device-Os"), OSVersion: r.Header.Get("X-Ver-Os"),
		Model: r.Header.Get("X-Device-Model"), App: r.Header.Get("User-Agent"), IP: h.clientIP(r),
	}, cfg.RequireHWID)
}

// profile lists what the user may use; slot is whose keys go in (zero for the page).
// grants is what is left of the user's grants when the caller read it already; nil reads it.
func (h *Handler) profile(ctx context.Context, u db.User, cfg Config, slot db.Slot, grants domain.GrantsLeft) (Profile, error) {
	prof := Profile{Nodes: cfg.Nodes, Direct: cfg.Direct, Slot: slot, Fingerprint: cfg.Fingerprint, UserID: u.ID}
	all, err := h.st.Q.ListInbounds(ctx)
	if err != nil {
		return prof, err
	}
	allowed := domain.DecodeInbounds(u.Inbounds)
	if grants == nil {
		if grants, err = domain.UserGrantsLeft(ctx, h.st.Q, u.ID, h.now()); err != nil {
			return prof, err
		}
	}
	// A traffic pool that ran out leaves the subscription; the node already turns it away.
	spent, err := domain.ExhaustedPools(ctx, h.st.Q, u.ID, grants)
	if err != nil {
		return prof, err
	}
	nodes := map[int64]bool{}
	for _, n := range cfg.Nodes {
		nodes[n.ID] = true
	}
	turns, err := h.st.Q.ListNodeTurns(ctx)
	if err != nil {
		return prof, err
	}
	for _, row := range turns {
		if tc := turn.FromRow(row); tc.Enabled && nodes[row.NodeID] {
			if prof.Turn == nil {
				prof.Turn = map[int64]turn.Config{}
			}
			prof.Turn[row.NodeID] = tc
		}
	}
	for _, in := range all {
		if in.Enabled == 0 || !nodes[in.NodeID] || (len(allowed) > 0 && !slices.Contains(allowed, in.ID)) || in.PoolID.Valid && spent[in.PoolID.Int64] {
			continue
		}
		prof.Inbounds = append(prof.Inbounds, in)
	}
	return prof, nil
}

// userInfoHeaders: the traffic and term apps show. With traffic packages left the total
// is what the user can reach: what is used plus what is left.
func (h *Handler) userInfoHeaders(w http.ResponseWriter, u db.User, grants int64, cfg Config, vars map[string]string) {
	var total, expire int64
	if u.TrafficLimit.Valid {
		total = u.TrafficLimit.Int64
		if grants > 0 {
			total = max(total, u.UsedUp+u.UsedDown) + grants
		}
	}
	if u.ExpiresAt.Valid {
		expire = u.ExpiresAt.Int64
	}
	hd := w.Header()
	hd.Set("Subscription-Userinfo", "upload="+strconv.FormatInt(u.UsedUp, 10)+"; download="+strconv.FormatInt(u.UsedDown, 10)+
		"; total="+strconv.FormatInt(total, 10)+"; expire="+strconv.FormatInt(expire, 10))
	// Hourly: a port or target the panel changed on its own reaches clients soon.
	hd.Set("Profile-Update-Interval", "1")
	hd.Set("Profile-Title", "base64:"+base64.StdEncoding.EncodeToString([]byte(profileTitle(cfg, vars))))
	if cfg.SupportURL != "" {
		hd.Set("Support-Url", cfg.SupportURL)
	}
}

// boundFor counts the devices bound to u for {devices}: -1 when the title and the
// announcement do not use it or binding is off.
func (h *Handler) boundFor(ctx context.Context, u db.User, cfg Config) int64 {
	if !cfg.Binding || !wantsDevices(cfg.Title, cfg.Announce) {
		return -1
	}
	n, err := h.st.Q.CountBoundDevices(ctx, u.ID)
	if err != nil {
		return -1
	}
	return n
}

// operatorHeaders: the subscription page, the announcement and the app branding
// (OperatorHeaders). Devices are counted only for branding, which shows them.
func (h *Handler) operatorHeaders(w http.ResponseWriter, r *http.Request, u db.User, cfg Config) {
	devices := int64(-1)
	var bot string
	if cfg.App.Enabled {
		if cfg.Binding && u.DeviceLimit.Valid && domain.BindsDevices(u.DeviceMode) {
			if n, err := h.st.Q.CountBoundDevices(r.Context(), u.ID); err == nil {
				devices = n
			}
		}
		if h.tg != nil {
			bot = BotURL(h.tg.LinkURL(r.Context(), u.ID))
		}
	}
	page := PageURL(r)
	if cfg.SubBase != "" {
		page = cfg.SubBase + "/" + u.SubToken
	}
	OperatorHeaders(w.Header(), u, cfg, page, bot, devices)
}

// Info is what the subscription page shows. Credentials are not included: the page
// offers import buttons that point back at this subscription URL.
type Info struct {
	Name       string     `json:"name"`
	Brand      string     `json:"brand"`
	SupportURL string     `json:"support_url,omitempty"`
	State      string     `json:"state"`
	UsedUp     int64      `json:"used_up"`
	UsedDown   int64      `json:"used_down"`
	Limit      *int64     `json:"limit,omitempty"`
	Extra      int64      `json:"extra,omitempty"` // bytes left in traffic packages, spent after Limit
	ExpiresAt  *time.Time `json:"expires_at,omitempty"`
	ResetsAt   *time.Time `json:"resets_at,omitempty"`
	Devices    int        `json:"device_limit"`
	// DeviceMode is how the limit counts (domain.DeviceMode*): "online" means the number of
	// devices online at once, not of bound ones.
	DeviceMode string `json:"device_mode" enum:"bound,online"`
	// The subscriber's own new link (domain.ReissueRules), for the Mini App to offer it.
	// Reissue is absent when the panel has no such button at all.
	Reissue      bool       `json:"reissue,omitempty" doc:"The Mini App may offer a new link"`
	ReissueOff   bool       `json:"reissue_off,omitempty" doc:"Only the admin reissues links"`
	ReissueLimit int        `json:"reissue_limit,omitempty" doc:"New links allowed per reissue_days; 0: no limit"`
	ReissueDays  int        `json:"reissue_days,omitempty"`
	ReissueAfter *time.Time `json:"reissue_after,omitempty" doc:"The subscriber may reissue the link again from then"`
	Speed        int64      `json:"speed_limit,omitempty"` // Mbit/s each way; 0: no cap
	Protocols    []string   `json:"protocols"`
	Locations    []string   `json:"locations,omitempty"`
	// Telegram opens the bot with this subscription tied to the account; empty without a bot.
	Telegram string `json:"telegram,omitempty"`
	// Bound devices, when binding is on. The device id itself stays in the admin panel.
	Binding     bool         `json:"binding"`
	Bound       []DeviceItem `json:"devices"`
	UnbindAfter *time.Time   `json:"unbind_after,omitempty" doc:"The subscriber may unbind again from then"`
	// The admin's rules for unbinding (domain.UnbindRules), for the page to tell them.
	UnbindOff   bool `json:"unbind_off,omitempty" doc:"Only the admin unbinds devices"`
	UnbindLimit int  `json:"unbind_limit" doc:"Unbinds allowed per unbind_days; 0: no limit"`
	UnbindDays  int  `json:"unbind_days"`
	UnbindsLeft int  `json:"unbinds_left" doc:"Unbinds left in the window now; -1: no limit"`
	ReturnHours int  `json:"return_hours" doc:"An unbound device may not bind again for this long; 0: at once, -1: until the admin lets it"`
	// Pools: the user's traffic pools with a limit or with traffic used.
	Pools []PoolInfo `json:"pools,omitempty"`
	// The announcement the apps show, with the user's values in it; the page shows it when
	// the admin turns its block on.
	Announce    string `json:"announce,omitempty"`
	AnnounceURL string `json:"announce_url,omitempty"`
	// HappLink is the Happ button's crypt link: the app opens the subscription without
	// showing its address. Absent: the plain happ://add/ link.
	HappLink string `json:"happ_link,omitempty"`
}

// DeviceItem is a bound device as the subscription page lists it.
type DeviceItem struct {
	ID        int64     `json:"id"`
	Name      string    `json:"name" doc:"The device's own name; empty: the one the app reports"`
	OS        string    `json:"os"`
	OSVersion string    `json:"os_version"`
	Model     string    `json:"model"`
	App       string    `json:"app"`
	Shared    bool      `json:"shared" doc:"Apps that send no device id, seated together"`
	CreatedAt time.Time `json:"created_at"`
	LastSeen  time.Time `json:"last_seen"`
	Used      int64     `json:"used,omitempty" doc:"Bytes both ways since the device was bound"`
}

func (h *Handler) info(ctx context.Context, w http.ResponseWriter, u db.User, prof Profile, cfg Config) {
	now := h.now()
	grants, err := domain.UserGrantsLeft(ctx, h.st.Q, u.ID, now)
	if err != nil {
		internalError(w)
		return
	}
	out := Info{Name: u.Name, Brand: cfg.Brand, SupportURL: cfg.SupportURL, State: domain.State(u, grants.Main(u.ID), now), UsedUp: u.UsedUp, UsedDown: u.UsedDown,
		Binding: cfg.Binding, Bound: []DeviceItem{}}
	if u.TrafficLimit.Valid {
		out.Extra = grants.Main(u.ID)
	}
	if h.tg != nil {
		out.Telegram = h.tg.LinkURL(ctx, u.ID)
	}
	if cfg.Announce != "" {
		out.Announce, out.AnnounceURL = fillAnnounce(cfg.Announce, titleValues(u, grants.Main(u.ID), h.boundFor(ctx, u, cfg), cfg, now)), cfg.AnnounceURL
	}
	if cfg.Happ.Crypt != "" && cfg.SubBase != "" {
		if link, err := h.happ.Link(ctx, cfg.Happ.Crypt, cfg.SubBase+"/"+u.SubToken); err == nil {
			out.HappLink = link
		} else {
			h.warn("happ_link", "subscription: no Happ crypt link", "err", err)
		}
	}
	if cfg.Binding {
		devs, err := h.st.Q.ListBoundDevices(ctx, u.ID)
		if err != nil {
			internalError(w)
			return
		}
		used := map[int64]int64{}
		if rows, err := h.st.Q.ListBoundDeviceTraffic(ctx, u.ID); err == nil {
			for _, r := range rows {
				used[r.DeviceID] = r.Up + r.Down
			}
		}
		for _, d := range devs {
			out.Bound = append(out.Bound, DeviceItem{ID: d.ID, Name: d.Name, OS: d.Os, OSVersion: d.OsVersion, Model: d.Model, App: d.App, Shared: d.Hwid == "",
				CreatedAt: time.Unix(d.CreatedAt, 0).UTC(), LastSeen: time.Unix(d.LastSeen, 0).UTC(), Used: used[d.ID]})
		}
		st, err := h.devices.UnbindState(ctx, u.ID)
		if err != nil {
			h.log.Warn("subs: unbind rules not read", "user", u.ID, "err", err)
		}
		if !st.Next.IsZero() {
			out.UnbindAfter = &st.Next
		}
		out.UnbindOff, out.UnbindLimit, out.UnbindDays, out.UnbindsLeft, out.ReturnHours = st.Rules.Off, st.Rules.Limit, st.Rules.Days, st.Left(), st.Rules.ReturnHours
	}
	if u.TrafficLimit.Valid {
		out.Limit = &u.TrafficLimit.Int64
	}
	if u.ExpiresAt.Valid {
		t := time.Unix(u.ExpiresAt.Int64, 0).UTC()
		out.ExpiresAt = &t
	}
	if t, ok := domain.NextReset(u, now); ok {
		out.ResetsAt = &t
	}
	if u.DeviceLimit.Valid {
		out.Devices = int(u.DeviceLimit.Int64)
	}
	out.DeviceMode = u.DeviceMode
	if h.reissuer != nil && h.tg != nil {
		out.Reissue = true
		st, err := h.reissuer.ReissueState(ctx, u.ID)
		if err != nil {
			h.log.Warn("subs: reissue rules not read", "user", u.ID, "err", err)
		}
		if !st.Next.IsZero() {
			out.ReissueAfter = &st.Next
		}
		out.ReissueOff, out.ReissueLimit, out.ReissueDays = st.Rules.Off, st.Rules.Limit, st.Rules.Days
	}
	if u.SpeedLimit.Valid {
		out.Speed = u.SpeedLimit.Int64
	}
	onNode := map[int64]bool{}
	for _, in := range prof.Inbounds {
		onNode[in.NodeID] = true
		if !slices.Contains(out.Protocols, in.Preset) {
			out.Protocols = append(out.Protocols, in.Preset)
		}
	}
	for _, n := range cfg.Nodes {
		if onNode[n.ID] && n.Name != "" {
			out.Locations = append(out.Locations, n.Name)
		}
	}
	pools, err := h.poolInfo(ctx, u.ID, grants)
	if err != nil {
		internalError(w)
		return
	}
	out.Pools = pools
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(out)
}

// unbind frees a device's place from the subscription page. Only the page itself may do
// it (a POST from another site is refused), and only once a day.
func (h *Handler) unbind(w http.ResponseWriter, r *http.Request, u db.User, id int64) {
	if !sameOrigin(r) || h.devices == nil {
		server.NotFound(w)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	err := h.devices.Unbind(r.Context(), u.ID, id, true)
	switch {
	case err == nil:
		h.log.Info("subs: the subscriber unbound a device", "user", u.ID, "device", id)
		w.WriteHeader(http.StatusNoContent)
	case errors.Is(err, domain.ErrUnbindCooldown):
		out := map[string]any{"code": "unbind_cooldown"}
		// Without the moment the page says only that it cannot: never a date of year one.
		if st, err := h.devices.UnbindState(r.Context(), u.ID); err == nil && !st.Next.IsZero() {
			out["unbind_after"] = st.Next
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		_ = json.NewEncoder(w).Encode(out)
	case errors.Is(err, domain.ErrUnbindOff):
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_ = json.NewEncoder(w).Encode(map[string]any{"code": "unbind_off"})
	case errors.Is(err, domain.ErrNotFound):
		server.NotFound(w)
	default:
		h.log.Warn("subs: device not unbound", "user", u.ID, "device", id, "err", err)
		internalError(w)
	}
}

// renameDevice gives a device of this subscription its own name from the subscription
// page: {"name": "…"}, "" for the name the app reports. Same site only, like unbind.
func (h *Handler) renameDevice(w http.ResponseWriter, r *http.Request, u db.User, id int64) {
	if !sameOrigin(r) || h.devices == nil {
		server.NotFound(w)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	var in struct {
		Name string `json:"name"`
	}
	if json.NewDecoder(io.LimitReader(r.Body, 4<<10)).Decode(&in) != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	name, err := h.devices.Rename(r.Context(), u.ID, id, in.Name)
	var fe *domain.FieldError
	switch {
	case err == nil:
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"name": name})
	case errors.As(err, &fe):
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnprocessableEntity)
		_ = json.NewEncoder(w).Encode(map[string]string{"code": fe.Code})
	case errors.Is(err, domain.ErrNotFound):
		server.NotFound(w)
	default:
		internalError(w)
	}
}

// sameOrigin: the browser says the request comes from this very site.
func sameOrigin(r *http.Request) bool { return server.FetchSite(r.Header, r.Host) == server.SiteSame }

// stub answers a device that gets no keys: one placeholder server named after the reason,
// so the app shows it where the servers would be, and the headers Happ-like apps read.
func (h *Handler) stub(w http.ResponseWriter, u db.User, cfg Config, format string, reason error) {
	en := cfg.Lang == "en"
	name := "⛔ Все места для устройств заняты — откройте ссылку подписки в браузере"
	if en {
		name = "⛔ All device places are taken — open the subscription link in a browser"
	}
	switch {
	case errors.Is(reason, ErrNoProxies):
		// Nothing this app can use, or every server of the subscription is switched off: a
		// profile with an empty group would be refused by the app as a whole.
		name = "⛔ Для этого приложения нет подходящих серверов — откройте ссылку подписки в браузере"
		if en {
			name = "⛔ There are no servers this app can use — open the subscription link in a browser"
		}
	case errors.Is(reason, domain.ErrDeviceBanned):
		name = "⛔ Устройство заблокировано"
		if en {
			name = "⛔ This device is blocked"
		}
	case errors.Is(reason, domain.ErrDeviceUnbound):
		// The subscriber unbound it a moment ago: its app, still open, asks again.
		var ue *domain.UnboundError
		hours := 1
		if errors.As(reason, &ue) {
			hours = max(1, int(math.Ceil(ue.Until.Sub(h.now()).Hours())))
		}
		name = fmt.Sprintf("⏸ Устройство отвязано — подключится снова через %d ч", hours)
		if en {
			name = fmt.Sprintf("⏸ This device was unbound — it can connect again in %d h", hours)
		}
		if ue != nil && domain.Forever(ue.Until) {
			name = "⏸ Устройство отвязано — подключить его снова может только администратор"
			if en {
				name = "⏸ This device was unbound — only the administrator can let it connect again"
			}
		}
	case errors.Is(reason, domain.ErrNoHWID):
		name = "⛔ Приложение не сообщает ID устройства — поставьте Happ, Koala Clash или INCY"
		if en {
			name = "⛔ The app does not send a device ID — install Happ, Koala Clash or INCY"
		}
		w.Header().Set("X-Hwid-Not-Supported", "true")
	default:
		w.Header().Set("X-Hwid-Max-Devices-Reached", "true")
		w.Header().Set("X-Hwid-Limit", "true") // v2RayTun shows the notice only with it
	}
	if format == "clash" {
		main := cfg.Groups.WithDefaults(cfg.Lang).Main
		body, _ := marshalYAML(map[string]any{
			"proxies":      []map[string]any{{"name": name, "type": "socks5", "server": "127.0.0.1", "port": 1}},
			"proxy-groups": []map[string]any{{"name": main, "type": "select", "proxies": []string{name}}},
			"rules":        []string{"MATCH," + main},
		})
		w.Header().Set("Content-Type", "text/yaml; charset=utf-8")
		_, _ = w.Write(body)
		return
	}
	if format == "singbox" {
		main := cfg.Groups.WithDefaults(cfg.Lang).Main
		body, _ := json.MarshalIndent(map[string]any{
			"outbounds": []map[string]any{{"type": "selector", "tag": main, "outbounds": []string{name}},
				{"type": "socks", "tag": name, "server": "127.0.0.1", "server_port": 1}},
			"route": map[string]any{"final": main},
		}, "", "  ")
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_, _ = w.Write(body)
		return
	}
	link := "vless://00000000-0000-0000-0000-000000000000@127.0.0.1:1?encryption=none&type=tcp#" + url.PathEscape(name)
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write([]byte(base64.StdEncoding.EncodeToString([]byte(link))))
}

// Apps that take share links and that DetectApp does not tell apart (it is about what
// the app's core can run, not about the format).
var linkApps = []string{"streisand", "shadowrocket", "foxray"}

// Format picks the response format: explicit ?format= wins, then the client's User-Agent,
// by the family DetectApp makes of it, so the two never disagree about an app.
func Format(userAgent, accept, query string) string {
	switch strings.ToLower(query) {
	case "clash", "mihomo", "yaml":
		return "clash"
	case "uri", "v2ray", "base64":
		return "uri"
	case "singbox", "sing-box":
		return "singbox"
	case "turn", "wl":
		return "turn"
	case "html":
		return "html"
	}
	app := DetectApp(userAgent)
	if app.Whole && app.Core.AtLeast(singboxSince) {
		return "singbox"
	}
	switch app.Family {
	case FamilyMihomo, FamilyStash:
		return "clash"
	case FamilyXray, FamilySingBox:
		return "uri"
	}
	ua := strings.ToLower(userAgent)
	for _, a := range linkApps {
		if strings.Contains(ua, a) {
			return "uri"
		}
	}
	if strings.Contains(accept, "text/html") {
		return "html"
	}
	return "uri"
}

// PoolInfo is one traffic pool as the subscription page shows it.
type PoolInfo struct {
	Name  string `json:"name"`
	Limit *int64 `json:"limit,omitempty"` // bytes; none: unlimited
	Used  int64  `json:"used"`
	Extra int64  `json:"extra,omitempty"` // bytes left in the pool's traffic packages
}

// poolInfo lists the user's pools worth showing: those with a limit or some traffic.
func (h *Handler) poolInfo(ctx context.Context, userID int64, grants domain.GrantsLeft) ([]PoolInfo, error) {
	rows, err := h.st.Q.ListUserPools(ctx, userID)
	if err != nil || len(rows) == 0 {
		return nil, err
	}
	pools, err := h.st.Q.ListTrafficPools(ctx)
	if err != nil {
		return nil, err
	}
	names := map[int64]string{}
	for _, p := range pools {
		names[p.ID] = p.Name
	}
	var out []PoolInfo
	for _, r := range rows {
		used := r.UsedUp + r.UsedDown
		if r.Excluded || !r.TrafficLimit.Valid && used == 0 {
			continue
		}
		pi := PoolInfo{Name: names[r.PoolID], Used: used}
		if r.TrafficLimit.Valid {
			l := r.TrafficLimit.Int64
			pi.Limit = &l
			pi.Extra = grants.Pool(userID, r.PoolID)
		}
		out = append(out, pi)
	}
	return out, nil
}

// PreviewRequest is what the admin panel shows a profile with, in place of the saved
// settings.
type PreviewRequest struct {
	Routing  Routing
	Routes   Routes
	Template string // the own Clash profile; "": the built-in one with Routing and Routes
	Starter  bool   // the built-in one as the start of an own (Starter)
	// Rules and Groups in place of the saved ones, when set: the routing form has them too.
	Rules  *string
	Groups *Groups
}

// Preview is the Clash profile of a user given every inbound, as req sets it: what the
// admin panel shows before it is saved. Its keys are placeholders.
func (h *Handler) Preview(ctx context.Context, req PreviewRequest) ([]byte, error) {
	prof, cfg, err := h.sample(ctx)
	if err != nil {
		return nil, err
	}
	prof.Routes = req.Routes
	g := cfg.Groups
	if req.Groups != nil {
		g = *req.Groups
	}
	g = g.WithDefaults(cfg.Lang)
	if req.Rules != nil {
		prof.Rules = ServedRules(*req.Rules, g)
	}
	switch {
	case req.Starter:
		return Starter(prof, g, req.Routing)
	case req.Template != "":
		return Template(prof, req.Template)
	}
	return Mihomo(prof, g, req.Routing)
}

// CheckTemplate checks an own Clash profile against a user given every inbound.
func (h *Handler) CheckTemplate(ctx context.Context, src string) error {
	prof, _, err := h.sample(ctx)
	if err != nil {
		return err
	}
	return CheckTemplate(prof, src)
}

// sample is the profile of a user given every inbound, with placeholder keys.
func (h *Handler) sample(ctx context.Context) (Profile, Config, error) {
	cfg, err := h.cfg(ctx)
	if err != nil {
		return Profile{}, cfg, err
	}
	prof, err := h.profile(ctx, db.User{}, cfg, db.Slot{Name: "preview", Uuid: "00000000-0000-0000-0000-000000000000", Secret: "preview"}, domain.GrantsLeft{})
	if err != nil {
		return Profile{}, cfg, err
	}
	prof.Rules, prof.Routes, prof.Lang = cfg.Rules, cfg.Routes, cfg.Lang
	// A current mihomo: the preview shows every list, and a template started from it keeps
	// the ones a younger core reads (an app that does not name its core still gets none).
	prof.App = App{Family: FamilyMihomo, Core: previewCore}
	return prof, cfg, nil
}

// previewCore is the mihomo the nodes run (go.mod), the newest an app may have.
var previewCore = Version{1, 19, 32}
