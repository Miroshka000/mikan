// Package subsrc is the panel's side of subscriptions: the serve.Source over the panel's
// database, and what only the panel has: the subscription page and its /info, the Mini App
// with its shop and promo codes, the devices a subscriber unbinds and names, the links of
// the panel users were imported from, and the profile previews of the admin panel.
package subsrc

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"mikan/internal/panel/billing"
	"mikan/internal/panel/domain"
	"mikan/internal/panel/panelimport"
	"mikan/internal/panel/promo"
	"mikan/internal/panel/server"
	"mikan/internal/panel/store"
	"mikan/internal/panel/store/db"
	"mikan/internal/panel/subpage"
	"mikan/internal/proto"
	"mikan/internal/sub/apps"
	"mikan/internal/sub/build"
	"mikan/internal/sub/mihomo"
	submodel "mikan/internal/sub/model"
	"mikan/internal/sub/routing"
	"mikan/internal/sub/serve"
	"mikan/internal/sub/singbox"
	"mikan/internal/sub/xray"
)

// Config is resolved per request, so settings changes apply without a restart: what the
// profiles are served by, and what of subscriptions only the panel has.
type Config struct {
	serve.Config
	// HappCrypt is how the subscription page's Happ button hides the subscription address:
	// "" plain, HappCryptAPI by Happ's own service, HappCryptLocal by the panel itself.
	HappCrypt string
	// Legacy checks the signed links of the panel users were imported from (Legacy).
	Legacy panelimport.Verifier
	// Tariffs names the tariffs, for {tariff} in the title and the notices.
	Tariffs map[int64]string
	// LogDays is how long the request log is kept; 0: the requests are not logged.
	LogDays int
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
	balance    *domain.Balancer // nil: every node shows
	happ       *HappLinks       // Happ crypt links, kept per subscription
	log        *slog.Logger
	promoLimit promoLimiter
	// profiles answers the fetches of the subscriptions this handler is the Source of.
	profiles *serve.Handler
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
		h.profiles.SetLogger(l)
	}
}

// warn logs a fault that repeats with every subscription fetch once an hour, not at each.
func (h *Handler) warn(key, msg string, args ...any) { h.profiles.Warn(key, msg, args...) }

// SetShop takes payments: the providers' webhooks under /pay/ and the Mini App's shop.
func (h *Handler) SetShop(s *billing.Service) { h.shop = s }
func (h *Handler) SetPromo(s *promo.Service)  { h.promos = s }

// SetBalancer keeps each client on one node of every balance group.
func (h *Handler) SetBalancer(b *domain.Balancer) { h.balance = b }

// SetPages gives the page what the admin made of it: its look, blocks, instructions, images.
func (h *Handler) SetPages(s *subpage.Service) { h.pages = s }

func NewHandler(st *store.Store, cfg func(ctx context.Context) (Config, error), page http.Handler, now func() time.Time, devices Binder, trustProxy bool) *Handler {
	h := &Handler{st: st, cfg: cfg, page: page, now: now, devices: devices, trustProxy: trustProxy, log: slog.New(slog.DiscardHandler), happ: NewHappLinks()}
	h.profiles = serve.New(h, now, h.clientIP)
	if page != nil {
		h.profiles.SetPage(h.servePage)
	}
	return h
}

// HappLink is the Happ crypt link of u's subscription, "" when the crypt link is off or
// the panel has no address for subscriptions yet.
func (h *Handler) HappLink(ctx context.Context, u db.User) (string, error) {
	cfg, err := h.cfg(ctx)
	if err != nil || cfg.HappCrypt == "" || cfg.SubBase == "" {
		return "", err
	}
	return h.happ.Link(ctx, cfg.HappCrypt, cfg.SubBase+"/"+u.SubToken)
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
// serve.Handler.Serve.
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
	if rest == "" {
		h.profiles.Serve(w, r, token)
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
	h.info(r.Context(), w, &subscription{h: h, u: u, cfg: cfg})
}

// internalError answers a request the panel failed on, without telling why: the cause is
// the panel's to log, not the client's to read.
func internalError(w http.ResponseWriter) {
	http.Error(w, "internal error", http.StatusInternalServerError)
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
	// BlockedApps are the apps of the page's catalog the admin turned away (apps.Catalog
	// ids): they would get a stub, so "Connect a device" does not offer them.
	BlockedApps []string `json:"blocked_apps,omitempty"`
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

// info answers "/<token>/info": what the page shows of the subscription sub.
func (h *Handler) info(ctx context.Context, w http.ResponseWriter, sub *subscription) {
	u, cfg, now := sub.u, sub.cfg, h.now()
	user, err := sub.User(ctx)
	if err != nil {
		internalError(w)
		return
	}
	if _, err := sub.Inbounds(ctx); err != nil {
		internalError(w)
		return
	}
	reach := sub.reach
	out := Info{Name: u.Name, Brand: cfg.Brand, SupportURL: cfg.SupportURL, State: string(user.State), UsedUp: u.UsedUp, UsedDown: u.UsedDown,
		Binding: cfg.Binding, Bound: []DeviceItem{}, BlockedApps: cfg.Responses.BlockedIDs()}
	if u.TrafficLimit.Valid {
		out.Extra = user.Grants
	}
	if h.tg != nil {
		out.Telegram = h.tg.LinkURL(ctx, u.ID)
	}
	if cfg.Announce != "" {
		out.Announce, out.AnnounceURL = serve.Announcement(ctx, sub, user, cfg.Config, now), cfg.AnnounceURL
	}
	if cfg.HappCrypt != "" && cfg.SubBase != "" {
		if link, err := h.happ.Link(ctx, cfg.HappCrypt, cfg.SubBase+"/"+u.SubToken); err == nil {
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
				CreatedAt: store.Time(d.CreatedAt), LastSeen: store.Time(d.LastSeen), Used: used[d.ID]})
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
	out.Limit = user.Limit
	out.ExpiresAt = store.TimePtr(u.ExpiresAt)
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
	for _, in := range reach.inbounds {
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
	pools, err := h.poolInfo(ctx, u, reach.grants, reach.access.Groups(reach.all))
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

// PoolInfo is one traffic pool as the subscription page shows it.
type PoolInfo struct {
	Name  string `json:"name"`
	Limit *int64 `json:"limit,omitempty"` // bytes; none: unlimited
	Used  int64  `json:"used"`
	Extra int64  `json:"extra,omitempty"` // bytes left in the pool's traffic packages
}

// poolInfo is what the page shows of u's groups: each of its own limit, or without one
// that it used, among the groups it reaches (reach: Access.Groups, an exhausted one included,
// which the page must show). A group in the main traffic is part of it; a closed one is not
// the user's.
func (h *Handler) poolInfo(ctx context.Context, u db.User, grants domain.GrantsLeft, reach map[int64]bool) ([]PoolInfo, error) {
	userID := u.ID
	rows, err := h.st.Q.ListUserPools(ctx, userID)
	if err != nil || len(rows) == 0 {
		return nil, err
	}
	names, err := domain.PoolNames(ctx, h.st.Q)
	if err != nil {
		return nil, err
	}
	var out []PoolInfo
	for _, r := range rows {
		used := r.UsedUp + r.UsedDown
		if !reach[r.PoolID] || r.Mode != domain.PoolOwn && (r.Mode != domain.PoolUnlimited || used == 0) {
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
	Mode     routing.Mode
	Routes   routing.Routes
	Template string // the own Clash profile; "": the built-in one with Mode and Routes
	Starter  bool   // the built-in one as the start of an own (mihomo.Starter)
	// Rules and Groups in place of the saved ones, when set: the routing form has them too.
	Rules  *string
	Groups *submodel.GroupNames
}

// Preview is what the admin panel shows of a profile before it is saved: the Clash profile,
// and what the other cores make of its groups they cannot carry as they are.
type Preview struct {
	Profile []byte
	Notes   []submodel.Note
}

// Preview is the profile of a user given every inbound, as req sets it. Its keys are
// placeholders; a starter has no notes, it is the start of an own profile.
func (h *Handler) Preview(ctx context.Context, req PreviewRequest) (Preview, error) {
	prof, cfg, err := h.sample(ctx)
	if err != nil {
		return Preview{}, err
	}
	prof.Routes = req.Routes
	g := cfg.Groups
	if req.Groups != nil {
		g = *req.Groups
	}
	g = g.WithDefaults(cfg.Lang)
	if req.Rules != nil {
		prof.Rules = routing.ServedRules(*req.Rules, g)
	}
	var out Preview
	switch {
	case req.Starter:
		out.Profile, err = mihomo.Starter(prof, g, req.Mode)
		return out, err
	case req.Template != "":
		out.Profile, _, err = mihomo.Template(prof, req.Template)
	default:
		out.Profile, err = mihomo.Render(prof, g, req.Mode)
	}
	if err != nil {
		return Preview{}, err
	}
	out.Notes = append(singbox.Notes(prof, g, req.Mode, req.Template), xray.Notes(prof, g, req.Mode, req.Template)...)
	return out, nil
}

// CheckTemplate checks an own Clash profile against a user given every inbound.
func (h *Handler) CheckTemplate(ctx context.Context, src string) error {
	prof, _, err := h.sample(ctx)
	if err != nil {
		return err
	}
	return mihomo.CheckTemplate(prof, src)
}

// sample is the profile of a user given every inbound, with placeholder keys.
func (h *Handler) sample(ctx context.Context) (mihomo.Profile, Config, error) {
	cfg, err := h.cfg(ctx)
	if err != nil {
		return mihomo.Profile{}, cfg, err
	}
	r, err := h.reach(ctx, db.User{}, cfg, domain.GrantsLeft{})
	if err != nil {
		return mihomo.Profile{}, cfg, err
	}
	keys := proto.Slot{Name: "preview", UUID: "00000000-0000-0000-0000-000000000000", Secret: "preview"}
	ps := build.Proxies(build.Profile{Keys: keys, Inbounds: inbounds(r.inbounds), Nodes: cfg.Nodes, Fingerprint: cfg.Fingerprint})
	return mihomo.Profile{Proxies: ps, Nodes: cfg.Nodes, Direct: cfg.Direct, Rules: cfg.Rules, Routes: cfg.Routes, Lang: cfg.Lang,
		// A current mihomo: the preview shows every list, and a template started from it keeps
		// the ones a younger core reads (an app that does not name its core still gets none).
		App: apps.App{Family: apps.FamilyMihomo, Core: previewCore}}, cfg, nil
}

// previewCore is the mihomo the nodes run (go.mod), the newest an app may have.
var previewCore = apps.Version{1, 19, 32}
