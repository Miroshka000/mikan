package subs

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
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
	"mikan/internal/proto"
)

// Config is resolved per request, so settings changes apply without a restart.
type Config struct {
	Brand      string
	SupportURL string
	// Announce is the text apps show over the profile, AnnounceURL where a tap on it leads.
	Announce    string
	AnnounceURL string
	App         AppBrand // the brand in apps that read operator headers
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
}

type Handler struct {
	st      *store.Store
	cfg     func(ctx context.Context) (Config, error)
	page    http.Handler // browser page (SPA entry); nil = plain links
	now     func() time.Time
	devices Binder
	// trustProxy takes the client's IP from X-Forwarded-For (a reverse proxy in front).
	trustProxy bool
	tg         Telegram         // nil: no bot
	shop       *billing.Service // nil: nothing on sale
	promos     *promo.Service
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

func NewHandler(st *store.Store, cfg func(ctx context.Context) (Config, error), page http.Handler, now func() time.Time, devices Binder, trustProxy bool) *Handler {
	return &Handler{st: st, cfg: cfg, page: page, now: now, devices: devices, trustProxy: trustProxy, log: slog.New(slog.DiscardHandler)}
}

// clientIP is the device's address as the nodes see it too: clients reach the panel
// directly (its host is a DIRECT rule in the profile).
func (h *Handler) clientIP(r *http.Request) string {
	return server.ClientIP(r.Header, r.RemoteAddr, h.trustProxy)
}

var unbindPath = regexp.MustCompile(`^devices/([0-9]{1,18})/unbind$`)

// ServeHTTP handles "/<token>", "/<token>/info", "POST /<token>/devices/<id>/unbind" (the
// subscription page) and the page assets under the sub prefix.
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
	unbind := unbindPath.FindStringSubmatch(rest)
	switch {
	case r.Method == http.MethodPost && unbind != nil:
	case r.Method != http.MethodGet && r.Method != http.MethodHead:
		server.NotFound(w)
		return
	case (strings.HasPrefix(p, "assets/") || p == "favicon.png" || p == "apple-touch-icon.png") && h.page != nil:
		h.page.ServeHTTP(w, r)
		return
	case rest != "" && rest != "info":
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
	if unbind != nil {
		id, _ := strconv.ParseInt(unbind[1], 10, 64)
		h.unbind(w, r, u, id)
		return
	}
	cfg, err := h.cfg(r.Context())
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if rest == "info" {
		prof, err := h.profile(r.Context(), u, cfg, db.Slot{})
		if err != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		h.info(r.Context(), w, u, prof, cfg)
		return
	}
	format := Format(r.Header.Get("User-Agent"), r.Header.Get("Accept"), r.URL.Query().Get("format"))
	if format == "html" && h.page != nil {
		h.page.ServeHTTP(w, r)
		return
	}
	grants, err := domain.UserGrantsLeft(r.Context(), h.st.Q, u.ID, h.now())
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	h.userInfoHeaders(w, u, grants.Main(u.ID), cfg)
	h.operatorHeaders(w, r, u, cfg)
	w.Header().Set("Cache-Control", "no-store")
	if r.Method == http.MethodHead {
		return // apps peek at the traffic headers; the keys go only with a real fetch
	}
	slot, err := h.slotFor(r, u, cfg)
	switch {
	case errors.Is(err, domain.ErrDeviceLimit), errors.Is(err, domain.ErrNoHWID):
		h.stub(w, u, cfg, format, err)
		return
	case err != nil:
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	prof, err := h.profile(r.Context(), u, cfg, slot)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
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
		body, err := Mihomo(prof, cfg.Groups.WithDefaults(cfg.Lang), cfg.Routing)
		if errors.Is(err, ErrNoProxies) {
			h.stub(w, u, cfg, format, err)
			return
		}
		if err != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/yaml; charset=utf-8")
		w.Header().Set("Content-Disposition", "attachment; filename*=UTF-8''"+url.PathEscape(cfg.Brand)+".yaml")
		_, _ = w.Write(body)
	default:
		links, err := URIs(prof)
		if errors.Is(err, ErrNoProxies) {
			h.stub(w, u, cfg, format, err)
			return
		}
		if err != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte(base64.StdEncoding.EncodeToString([]byte(links))))
	}
}

// miniApp serves the subscription page inside Telegram and signs its user in.
func (h *Handler) miniApp(w http.ResponseWriter, r *http.Request, rest string) {
	switch {
	case r.Method == http.MethodGet && rest == "" && h.page != nil:
		// Telegram Web opens Mini Apps in a frame; the apps open them in a web view.
		hd := w.Header()
		hd.Set("Content-Security-Policy", strings.Replace(hd.Get("Content-Security-Policy"), "frame-ancestors 'none'", "frame-ancestors https://web.telegram.org", 1))
		hd.Del("X-Frame-Options")
		h.page.ServeHTTP(w, r)
	case r.Method == http.MethodPost && rest == "session" && sameOrigin(r):
		var in struct {
			InitData string `json:"init_data"`
		}
		if json.NewDecoder(io.LimitReader(r.Body, 16<<10)).Decode(&in) != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		_, users, err := h.tg.MiniAppUser(r.Context(), in.InitData)
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Type", "application/json")
		if err != nil {
			w.WriteHeader(http.StatusUnauthorized)
			_ = json.NewEncoder(w).Encode(map[string]string{"code": "init_data"})
			return
		}
		type sub struct {
			Token string `json:"token"`
			Name  string `json:"name"`
		}
		out := struct {
			Subs []sub `json:"subs"`
		}{Subs: []sub{}}
		for _, u := range users {
			out.Subs = append(out.Subs, sub{Token: u.SubToken, Name: u.Name})
		}
		_ = json.NewEncoder(w).Encode(out)
	case r.Method == http.MethodPost && (rest == "shop" || rest == "pay") && sameOrigin(r) && h.shop != nil:
		h.miniAppShop(w, r, rest)
	case r.Method == http.MethodPost && (rest == "promo" || rest == "promo-history") && sameOrigin(r) && h.promos != nil:
		if rest == "promo-history" {
			h.miniAppPromoHistory(w, r)
		} else {
			h.miniAppPromo(w, r)
		}
	default:
		server.NotFound(w)
	}
}

// miniAppShop: "shop" lists what the Telegram account can buy (with token: the traffic
// packages of that subscription too), "pay" opens an invoice for a new subscription
// (token ""), one of the account's own, or a traffic package for it (package_id).
func (h *Handler) miniAppShop(w http.ResponseWriter, r *http.Request, rest string) {
	var in struct {
		InitData  string `json:"init_data"`
		TariffID  int64  `json:"tariff_id"`
		TermDays  *int64 `json:"term_days"` // the tariff's term; absent: its first
		PackageID int64  `json:"package_id"`
		Provider  string `json:"provider"`
		Token     string `json:"token"`
		PromoCode string `json:"promo_code"`
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json")
	fail := func(status int, code string) {
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(map[string]string{"code": code})
	}
	if json.NewDecoder(io.LimitReader(r.Body, 16<<10)).Decode(&in) != nil {
		fail(http.StatusBadRequest, "bad_request")
		return
	}
	tgID, users, err := h.tg.MiniAppUser(r.Context(), in.InitData)
	if err != nil {
		fail(http.StatusUnauthorized, "init_data")
		return
	}
	ctx := r.Context()
	var userID int64
	if in.Token != "" {
		for _, u := range users {
			if u.SubToken == in.Token {
				userID = u.ID
			}
		}
		if userID == 0 {
			fail(http.StatusForbidden, billing.ErrNotYours.Error())
			return
		}
	}
	if rest == "shop" {
		offers, av, err := h.shop.Offers(ctx)
		if err != nil {
			fail(http.StatusInternalServerError, "internal")
			return
		}
		cfg, _ := h.cfg(ctx)
		packages, err := h.shopPackages(ctx, userID, cfg.Lang)
		if err != nil {
			fail(http.StatusInternalServerError, "internal")
			return
		}
		// A tariff's terms, the first one also in the offer itself (as before terms).
		type term struct {
			Days        int64  `json:"days"`
			Label       string `json:"label"`
			Description string `json:"description"`
			Stars       int64  `json:"stars,omitempty"`
			Rub         int64  `json:"rub,omitempty"`
		}
		type offer struct {
			ID          int64  `json:"id"`
			Name        string `json:"name"`
			Description string `json:"description"`
			Stars       int64  `json:"stars,omitempty"`
			Rub         int64  `json:"rub,omitempty"`
			Terms       []term `json:"terms"`
		}
		// Marketplace adapters take rubles; the buyer sees each by its own name.
		type addon struct {
			Provider string `json:"provider"`
			Name     string `json:"name"`
		}
		out := struct {
			AllowNew  bool            `json:"allow_new"`
			Providers map[string]bool `json:"providers"`
			Addons    []addon         `json:"addons"`
			Offers    []offer         `json:"offers"`
			Packages  []shopPackage   `json:"packages"`
		}{AllowNew: h.shop.Config(ctx).AllowNew, Offers: []offer{}, Packages: packages, Addons: []addon{},
			Providers: map[string]bool{billing.Stars: av.Stars}}
		for _, id := range av.Addons {
			out.Addons = append(out.Addons, addon{Provider: billing.AddonPrefix + id, Name: h.shop.AddonName(ctx, id, cfg.Lang)})
		}
		for _, o := range offers {
			v := offer{ID: o.Tariff.ID, Name: o.Tariff.Name, Description: billing.DescribeOffer(o, cfg.Lang), Stars: o.Stars, Rub: o.Rub}
			for _, t := range o.Terms {
				v.Terms = append(v.Terms, term{Days: t.Days, Label: billing.TermLabel(o.Tariff, t.Days, cfg.Lang),
					Description: billing.Describe(o.Tariff, t.Days, cfg.Lang), Stars: t.Stars, Rub: t.Rub})
			}
			out.Offers = append(out.Offers, v)
		}
		_ = json.NewEncoder(w).Encode(out)
		return
	}
	if strings.TrimSpace(in.PromoCode) != "" && !h.promoLimit.allow(tgID, h.now(), in.PromoCode) {
		fail(http.StatusTooManyRequests, "promo_try_later")
		return
	}
	var p db.Payment
	if in.PackageID != 0 {
		p, err = h.shop.PackageInvoice(ctx, billing.PackageRequest{TgID: tgID, UserID: userID, PackageID: in.PackageID, Provider: billing.AdapterOf(in.Provider), PromoCode: in.PromoCode})
	} else {
		p, err = h.shop.Invoice(ctx, billing.InvoiceRequest{TgID: tgID, UserID: userID, TariffID: in.TariffID, TermDays: in.TermDays, Provider: billing.AdapterOf(in.Provider), PromoCode: in.PromoCode})
	}
	if err != nil {
		if strings.TrimSpace(in.PromoCode) != "" && promoError(err) {
			fail(http.StatusConflict, "promo_unavailable")
			return
		}
		status, code, unexplained := invoiceFailure(err)
		if unexplained {
			h.log.Warn("mini app: the invoice was not made", "provider", in.Provider, "err", err)
		}
		fail(status, code)
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]string{"url": p.PayUrl, "provider": p.Provider})
}

func promoError(err error) bool {
	for _, candidate := range []error{promo.ErrNotFound, promo.ErrInactive, promo.ErrExpired, promo.ErrLimit, promo.ErrUserLimit,
		promo.ErrTariff, promo.ErrMinimum, promo.ErrNewUser, promo.ErrFirstPurchase, promo.ErrCurrency, promo.ErrUnavailable,
		promo.ErrNotDiscount, promo.ErrInvalidValue, promo.ErrRefundUnsupported, promo.ErrAlreadyApplied,
		promo.ErrSubscription, promo.ErrNoExpiry, promo.ErrReservationExpired} {
		if errors.Is(err, candidate) {
			return true
		}
	}
	return false
}

// miniAppPromo validates a code or immediately redeems a bonus. Discount codes are only
// validated here; they are reserved atomically with the payment when the order is opened.
func (h *Handler) miniAppPromo(w http.ResponseWriter, r *http.Request) {
	var in struct {
		InitData     string `json:"init_data"`
		Token        string `json:"token"`
		Code         string `json:"code"`
		TariffID     int64  `json:"tariff_id"`
		TermDays     *int64 `json:"term_days"`
		PackageID    int64  `json:"package_id"`
		Provider     string `json:"provider"`
		ValidateOnly bool   `json:"validate_only"`
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json")
	fail := func(status int, code string) {
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(map[string]string{"code": code})
	}
	if json.NewDecoder(io.LimitReader(r.Body, 16<<10)).Decode(&in) != nil || strings.TrimSpace(in.Code) == "" {
		fail(http.StatusBadRequest, "bad_request")
		return
	}
	tgID, users, err := h.tg.MiniAppUser(r.Context(), in.InitData)
	if err != nil {
		fail(http.StatusUnauthorized, "init_data")
		return
	}
	if !h.promoLimit.allow(tgID, h.now(), in.Code) {
		fail(http.StatusTooManyRequests, "promo_try_later")
		return
	}
	var userID int64
	if in.Token != "" {
		for _, u := range users {
			if u.SubToken == in.Token {
				userID = u.ID
				break
			}
		}
		if userID == 0 {
			fail(http.StatusForbidden, billing.ErrNotYours.Error())
			return
		}
	}
	if in.ValidateOnly {
		tariffID, amount, currency, err := h.miniAppPromoOrder(r.Context(), userID, in.TariffID, in.TermDays, in.PackageID, billing.AdapterOf(in.Provider))
		if err != nil {
			fail(http.StatusConflict, "promo_unavailable")
			return
		}
		p, err := h.promos.Validate(r.Context(), tgID, userID, tariffID, amount, currency, in.Code)
		if err != nil || p.Type != "percent" && p.Type != "fixed" {
			fail(http.StatusConflict, "promo_unavailable")
			return
		}
		discount := promo.DiscountAmount(p, amount)
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "type": p.Type, "discount": discount, "final_amount": amount - discount, "currency": currency})
		return
	}
	p, err := h.promos.Validate(r.Context(), tgID, userID, 0, 0, "", in.Code)
	if err != nil {
		fail(h.promoAttempt(err))
		return
	}
	if p.Type == "days" || p.Type == "traffic" {
		r, err := h.promos.RedeemBonus(r.Context(), tgID, userID, in.Code)
		if err != nil {
			fail(h.promoAttempt(err))
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "type": p.Type, "days": r.Days, "bytes": r.Bytes, "message": "promo_applied"})
		return
	}
	fail(http.StatusConflict, "promo_unavailable")
}

// miniAppPromoOrder is the order a discount code is checked against: the tariff, and the
// price of the chosen term (days nil: the first) or of the package, with provider.
func (h *Handler) miniAppPromoOrder(ctx context.Context, userID, tariffID int64, days *int64, packageID int64, provider string) (int64, int64, string, error) {
	if h.shop == nil || provider == "" {
		return 0, 0, "", promo.ErrUnavailable
	}
	if packageID != 0 {
		offers, _, err := h.shop.PackageOffers(ctx, userID)
		if err != nil {
			return 0, 0, "", err
		}
		user, err := h.st.Q.GetUser(ctx, userID)
		if err != nil || !user.TariffID.Valid {
			return 0, 0, "", promo.ErrTariff
		}
		for _, offer := range offers {
			if offer.Package.ID == packageID {
				if amount, currency, ok := promoOrderPrice(provider, offer.Stars, offer.Rub); ok {
					return user.TariffID.Int64, amount, currency, nil
				}
			}
		}
		return 0, 0, "", promo.ErrUnavailable
	}
	offers, _, err := h.shop.Offers(ctx)
	if err != nil {
		return 0, 0, "", err
	}
	for _, offer := range offers {
		if offer.Tariff.ID == tariffID {
			t, ok := offer.Term(days)
			if !ok {
				break
			}
			if amount, currency, ok := promoOrderPrice(provider, t.Stars, t.Rub); ok {
				return offer.Tariff.ID, amount, currency, nil
			}
		}
	}
	return 0, 0, "", promo.ErrUnavailable
}

func promoOrderPrice(provider string, stars, rub int64) (int64, string, bool) {
	switch {
	case provider == billing.Stars && stars > 0:
		return stars, "XTR", true
	case strings.HasPrefix(provider, billing.AddonPrefix) && rub > 0:
		return rub, "RUB", true
	default:
		return 0, "", false
	}
}

func (h *Handler) miniAppPromoHistory(w http.ResponseWriter, r *http.Request) {
	var in struct {
		InitData string `json:"init_data"`
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json")
	if json.NewDecoder(io.LimitReader(r.Body, 16<<10)).Decode(&in) != nil {
		http.Error(w, "bad request", 400)
		return
	}
	tgID, _, err := h.tg.MiniAppUser(r.Context(), in.InitData)
	if err != nil {
		w.WriteHeader(401)
		_ = json.NewEncoder(w).Encode(map[string]string{"code": "init_data"})
		return
	}
	rs, err := h.st.Q.ListPromoRedemptionsByTgWithCode(r.Context(), db.ListPromoRedemptionsByTgWithCodeParams{TgID: tgID, Lim: 100})
	if err != nil {
		w.WriteHeader(500)
		_ = json.NewEncoder(w).Encode(map[string]string{"code": "internal"})
		return
	}
	type item struct {
		Code     string `json:"code"`
		Days     int64  `json:"days"`
		Bytes    int64  `json:"bytes"`
		Discount int64  `json:"discount"`
		Currency string `json:"currency"`
		At       int64  `json:"at"`
	}
	out := struct {
		Items []item `json:"items"`
	}{Items: []item{}}
	for _, row := range rs {
		redemption := row
		out.Items = append(out.Items, item{Code: row.Code, Days: redemption.Days, Bytes: redemption.Bytes, Discount: redemption.DiscountAmount, Currency: redemption.Currency, At: redemption.RedeemedAt})
	}
	_ = json.NewEncoder(w).Encode(out)
}

// promoAttempt is the answer to a code that could not be used. Why a code does not apply is
// not told: "promo_unavailable" for all of it, so codes cannot be probed. Anything else is
// the panel's trouble, logged and answered as such.
func (h *Handler) promoAttempt(err error) (int, string) {
	if code := promoAttemptCode(err); code != "" {
		return http.StatusConflict, code
	}
	h.log.Error("mini app: promo code failed", "err", err)
	return http.StatusInternalServerError, "internal"
}

func promoAttemptCode(err error) string {
	if promoError(err) || errors.Is(err, promo.ErrNotBonus) {
		return "promo_unavailable"
	}
	return ""
}

// invoiceFailure is what the Mini App answers when no invoice could be made. What the buyer
// can act on is told as it is. A provider that is switched off is "provider_off". Anything
// else, a provider that failed, a database that was busy, is the panel's trouble: the buyer
// is told it did not work, not that payment is off, and unexplained says nobody has logged
// the cause yet (billing logs a provider's failure itself).
func invoiceFailure(err error) (status int, code string, unexplained bool) {
	for _, e := range []error{billing.ErrNotForSale, billing.ErrNewOff, billing.ErrTooManySubs, billing.ErrTooMany, billing.ErrNotYours,
		promo.ErrNotFound, promo.ErrInactive, promo.ErrExpired, promo.ErrLimit, promo.ErrUserLimit, promo.ErrTariff, promo.ErrMinimum, promo.ErrNewUser, promo.ErrFirstPurchase, promo.ErrCurrency, promo.ErrUnavailable, promo.ErrNotDiscount, promo.ErrInvalidValue, promo.ErrRefundUnsupported} {
		if errors.Is(err, e) {
			if strings.HasPrefix(e.Error(), "promo_") {
				return http.StatusConflict, "promo_unavailable", false
			}
			return http.StatusConflict, e.Error(), false
		}
	}
	switch {
	case err == billing.ErrProviderOff:
		return http.StatusConflict, billing.ErrProviderOff.Error(), false
	case errors.Is(err, billing.ErrProviderOff): // the provider failed
		return http.StatusBadGateway, "invoice_failed", false
	}
	return http.StatusBadGateway, "invoice_failed", true
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
func (h *Handler) profile(ctx context.Context, u db.User, cfg Config, slot db.Slot) (Profile, error) {
	prof := Profile{Nodes: cfg.Nodes, Direct: cfg.Direct, Slot: slot, Fingerprint: cfg.Fingerprint}
	all, err := h.st.Q.ListInbounds(ctx)
	if err != nil {
		return prof, err
	}
	allowed := domain.DecodeInbounds(u.Inbounds)
	// A traffic pool that ran out leaves the subscription; the node already turns it away.
	spent, err := domain.ExhaustedPools(ctx, h.st.Q, u.ID, h.now())
	if err != nil {
		return prof, err
	}
	nodes := map[int64]bool{}
	for _, n := range cfg.Nodes {
		nodes[n.ID] = true
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
func (h *Handler) userInfoHeaders(w http.ResponseWriter, u db.User, grants int64, cfg Config) {
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
	hd.Set("Profile-Title", "base64:"+base64.StdEncoding.EncodeToString([]byte(cfg.Brand)))
	if cfg.SupportURL != "" {
		hd.Set("Support-Url", cfg.SupportURL)
	}
}

// operatorHeaders: the subscription page, the announcement and the app branding
// (OperatorHeaders). Devices are counted only for branding, which shows them.
func (h *Handler) operatorHeaders(w http.ResponseWriter, r *http.Request, u db.User, cfg Config) {
	devices := int64(-1)
	var bot string
	if cfg.App.Enabled {
		if cfg.Binding && u.DeviceLimit.Valid {
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
	Protocols  []string   `json:"protocols"`
	Locations  []string   `json:"locations,omitempty"`
	// Telegram opens the bot with this subscription tied to the account; empty without a bot.
	Telegram string `json:"telegram,omitempty"`
	// Bound devices, when binding is on. The device id itself stays in the admin panel.
	Binding     bool         `json:"binding"`
	Bound       []DeviceItem `json:"devices"`
	UnbindAfter *time.Time   `json:"unbind_after,omitempty" doc:"The subscriber may unbind again from then"`
	// Pools: the user's traffic pools with a limit or with traffic used.
	Pools []PoolInfo `json:"pools,omitempty"`
}

// DeviceItem is a bound device as the subscription page lists it.
type DeviceItem struct {
	ID        int64     `json:"id"`
	OS        string    `json:"os"`
	OSVersion string    `json:"os_version"`
	Model     string    `json:"model"`
	App       string    `json:"app"`
	Shared    bool      `json:"shared" doc:"Apps that send no device id, seated together"`
	CreatedAt time.Time `json:"created_at"`
	LastSeen  time.Time `json:"last_seen"`
}

func (h *Handler) info(ctx context.Context, w http.ResponseWriter, u db.User, prof Profile, cfg Config) {
	now := h.now()
	grants, err := domain.UserGrantsLeft(ctx, h.st.Q, u.ID, now)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
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
	if cfg.Binding {
		devs, err := h.st.Q.ListBoundDevices(ctx, u.ID)
		if err != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		for _, d := range devs {
			out.Bound = append(out.Bound, DeviceItem{ID: d.ID, OS: d.Os, OSVersion: d.OsVersion, Model: d.Model, App: d.App, Shared: d.Hwid == "",
				CreatedAt: time.Unix(d.CreatedAt, 0).UTC(), LastSeen: time.Unix(d.LastSeen, 0).UTC()})
		}
		if t := domain.NextUnbind(u, now); !t.IsZero() {
			out.UnbindAfter = &t
		}
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
		http.Error(w, "internal error", http.StatusInternalServerError)
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
		w.WriteHeader(http.StatusNoContent)
	case errors.Is(err, domain.ErrUnbindCooldown):
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		_ = json.NewEncoder(w).Encode(map[string]any{"code": "unbind_cooldown", "unbind_after": domain.NextUnbind(u, h.now())})
	case errors.Is(err, domain.ErrNotFound):
		server.NotFound(w)
	default:
		http.Error(w, "internal error", http.StatusInternalServerError)
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
		// JSON is YAML: the same as the real profile (see Mihomo).
		body, _ := json.Marshal(map[string]any{
			"proxies":      []map[string]any{{"name": name, "type": "socks5", "server": "127.0.0.1", "port": 1}},
			"proxy-groups": []map[string]any{{"name": main, "type": "select", "proxies": []string{name}}},
			"rules":        []string{"MATCH," + main},
		})
		w.Header().Set("Content-Type", "text/yaml; charset=utf-8")
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
	case "html":
		return "html"
	}
	switch DetectApp(userAgent).Family {
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
		if !r.TrafficLimit.Valid && used == 0 {
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
