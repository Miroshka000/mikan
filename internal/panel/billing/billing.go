// Package billing sells tariffs and traffic packages: the bot and the Mini App offer what
// is on sale, a payment through Telegram Stars or a marketplace adapter (addon.go)
// creates or renews the buyer's subscription, and the buyer gets the link.
//
// A payment row is made before the buyer pays, with an unguessable payload. Money is
// trusted only from the provider itself: Telegram's successful_payment on the bot's own
// long poll, or the invoice's status the panel asks the adapter for, compared with the
// payment it made (a webhook only makes it ask). The provider's payment id is unique per
// provider and a payment moves from paid to applied once, on the same transaction that
// changes the subscription, so a repeated or concurrent notification never pays out twice.
package billing

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"mikan/internal/pace"
	"mikan/internal/panel/addons"
	"mikan/internal/panel/domain"
	"mikan/internal/panel/events"
	"mikan/internal/panel/model"
	"mikan/internal/panel/promo"
	"mikan/internal/panel/secure"
	"mikan/internal/panel/settings"
	"mikan/internal/panel/store"
	"mikan/internal/panel/store/db"
	"mikan/internal/textutil"
)

// Stars is Telegram's own currency; every other provider is a marketplace adapter,
// "addon:<id>".
const Stars = model.Stars

// Settings keys.
const (
	KeyConfig       = "pay_config"
	KeyWebhookToken = "pay_webhook_token" // the secret part of the webhook URLs
	invoiceReuse    = 10 * time.Minute    // an open invoice for the same purchase is shown again
	balanceRepeat   = 2 * time.Minute     // a balance payment of the same purchase this recent is a second tap, not a second purchase
	pendingTTL      = 24 * time.Hour      // unpaid invoices expire
	reconcileEvery  = time.Minute
	maxPerHour      = 20 // invoices one Telegram account may open in an hour
)

// Config is what the admin sets; the adapters' settings are their own (addon.go).
type Config struct {
	// Enabled is the switch for selling at all: off, the bot and the Mini App offer
	// nothing and take no new invoices, while invoices already opened are still applied.
	Enabled bool `json:"enabled"`
	Stars   bool `json:"stars"`
	// AllowNew lets people without a subscription buy one; off: only renewals.
	AllowNew bool `json:"allow_new"`
	// RenewResetsTraffic: a paid renewal also starts a new traffic period; off, the
	// counter keeps running and only the term is extended.
	RenewResetsTraffic bool `json:"renew_resets_traffic"`
	// TrialTariffID is the tariff of the free trial a Telegram account may take once in
	// the bot; 0: no trial. It works with selling off too: it takes no payment.
	TrialTariffID int64 `json:"trial_tariff_id,omitempty"`
	// TrialStars and TrialRub (kopecks) put a price on the trial; both 0: it is free. A
	// priced trial is bought in the bot and the Mini App, once per account like the free
	// one, through the providers that take its price: it needs selling on.
	TrialStars int64 `json:"trial_stars,omitempty"`
	TrialRub   int64 `json:"trial_rub,omitempty"`
	// FreeTariffID is the tariff of the free plan (free.go): unlike the trial, an account
	// takes it again once its previous free subscription has ended, and the trial's counts
	// as one while it runs; 0: no free plan. It takes no payment, so it works with selling
	// off too.
	FreeTariffID int64 `json:"free_tariff_id,omitempty"`
	// FreeCondition is what the free plan asks of an account: FreeNoCondition, or
	// FreeChannel, a subscription to the bot's channel.
	FreeCondition string `json:"free_condition,omitempty"`
	// ExtraDevicesKeep: devices bought on top of the tariff stay with renewals, which are
	// then priced with them; off, they last until the term ends.
	ExtraDevicesKeep bool `json:"extra_devices_keep,omitempty"`
	// Balance is the accounts' internal balance (balance.go).
	Balance BalanceConfig `json:"balance"`
}

// TrialPaid: the trial has a price and is bought, not taken.
func (c Config) TrialPaid() bool { return c.Model().TrialPaid() }

// DefaultConfig: selling off until the admin turns it on; then Stars (it needs nothing but
// the bot) and new buyers are welcome.
var DefaultConfig = Config{Stars: true, AllowNew: true, RenewResetsTraffic: true}

// Telegram is the Telegram gateway's part (tgate.Gateway): Stars invoices and refunds, the
// bot's link and its channel. It stays in the panel whatever runs the bot's screens.
type Telegram interface {
	// InvoiceLink makes a Stars invoice link (createInvoiceLink); "" when the bot is off.
	InvoiceLink(ctx context.Context, title, description, payload string, stars int64) (string, error)
	RefundStars(ctx context.Context, tgID int64, chargeID string) error
	// BotURL is https://t.me/<bot>, "" while the bot is off.
	BotURL(ctx context.Context) string
	// Channel is the link to join the bot's channel (the one its mandatory subscription
	// asks for); ok is false while no channel is set up.
	Channel(ctx context.Context) (url string, ok bool)
	// Subscribed: the account is a member of the bot's channel. An error when the bot
	// cannot ask: it is off, or it is not an admin of the channel.
	Subscribed(ctx context.Context, tgID int64) (bool, error)
}

type Deps struct {
	// TopupWaiting tells the admin a top-up waits for them (its screenshot came); nil: nobody
	// is told, the Payments page shows it anyway.
	TopupWaiting func(ctx context.Context, t db.Topup)
	Store        *store.Store
	Settings     *settings.Settings
	Users        *domain.Users
	Log          *slog.Logger
	Now          func() time.Time
	// TrustProxy reads the client's IP from X-Forwarded-For: adapters check where a
	// webhook came from (YooKassa's addresses).
	TrustProxy bool
	MaxLinks   int64 // subscriptions one Telegram account may hold (model.MaxLinks)
	// Addons are the marketplace's payment adapters; nil: none.
	Addons *addons.Manager
	// SubBase is https://host:port/<sub path>, where the webhooks are; "" without an address.
	SubBase func(ctx context.Context) string
	Promo   *promo.Service
	// Events wakes the addons reading the feed after a payment, a trial or a refund; nil: no
	// one waits (the events are written all the same).
	Events *events.Hub
}

type Service struct {
	d            Deps
	mu           sync.Mutex
	promoRefunds keyedLock // by payment: one late-promo refund of it at a time
	tg           Telegram
	buyers       keyedLock // by Telegram account: one invoice opened at a time
	trialRefunds keyedLock // by payment: one refund of a trial that may not be given at a time
}

func New(d Deps) *Service {
	return &Service{d: d}
}

// SetTelegram connects the bot; until then Stars are off.
func (s *Service) SetTelegram(tg Telegram) {
	s.mu.Lock()
	s.tg = tg
	s.mu.Unlock()
}

func (s *Service) telegram() Telegram {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.tg
}

// Errors the bot and the Mini App show to buyers.
var (
	ErrNotForSale   = model.ErrNotForSale
	ErrProviderOff  = model.ErrProviderOff
	ErrNotYours     = model.ErrNotYours
	ErrNewOff       = model.ErrNewOff
	ErrTooManySubs  = model.ErrTooManySubs
	ErrTooMany      = model.ErrTooMany
	ErrBadPayment   = errors.New("bad_payment")
	ErrNotRefunable = errors.New("not_refundable")
	// ErrRefundNotApplied: Telegram returned the Stars, but taking back what the payment
	// gave failed; refunding again finishes it.
	ErrRefundNotApplied = errors.New("refund_not_applied")
	// ErrRecorded: the payment is saved with its charge, only applying it failed; the
	// reconcile pass finishes it, so the report of the payment need not come again.
	ErrRecorded = errors.New("payment_recorded")
)

// LoadConfig reads the payment settings. A read error is returned, never replaced by the
// defaults: settings changed on top of those and saved would switch selling off and
// forget the providers.
func (s *Service) LoadConfig(ctx context.Context) (Config, error) {
	return LoadConfig(ctx, s.d.Settings)
}

// LoadConfig is Service.LoadConfig over the given settings: a transaction's, when the
// payment settings are read to be changed.
func LoadConfig(ctx context.Context, set *settings.Settings) (Config, error) {
	// Payment settings saved before the switch existed (0.4.0, 0.4.1) come from panels that
	// set up selling: they keep selling. A panel that never saved them starts with it off.
	saved := DefaultConfig
	saved.Enabled = true
	c, found, err := settings.GetOver(ctx, set, KeyConfig, saved)
	switch {
	case err != nil:
		return DefaultConfig, err
	case !found:
		return DefaultConfig, nil
	}
	return c, nil
}

// Config is what decides what is on offer. When the settings cannot be read nothing is:
// selling fails closed until the store answers again.
func (s *Service) Config(ctx context.Context) Config {
	c, err := s.LoadConfig(ctx)
	if err != nil {
		s.d.Log.Warn("billing: payment settings unreadable, selling paused", "err", err)
		c.Enabled = false
	}
	return c
}

// Available says which providers can take a payment right now: selling on, Stars with
// the bot running, and the adapters that are on, set up and running.
type Available struct {
	Stars bool
	// Addons are the adapters that take rubles now, by id.
	Addons []string
	// Balance pays at the ruble prices, in its own currency; zero while it is off.
	Balance BalanceConfig
}

func (a Available) Any() bool { return a.Model().Any() }

// Rub: some provider takes rubles (the balance counts: it pays the ruble prices).
func (a Available) Rub() bool { return a.Model().Rub() }

func (a Available) has(provider string) bool {
	switch provider {
	case Stars:
		return a.Stars
	case Balance:
		return a.Balance.On()
	}
	id := model.AddonID(provider)
	return id != "" && slices.Contains(a.Addons, id)
}

func (s *Service) Available(ctx context.Context) Available {
	c := s.Config(ctx)
	if !c.Enabled {
		return Available{}
	}
	tg := s.telegram()
	av := Available{
		Stars:  c.Stars && tg != nil && tg.BotURL(ctx) != "",
		Addons: s.availableAddons(ctx),
	}
	if c.Balance.On() {
		av.Balance = c.Balance
	}
	return av
}

// Offer is a tariff on sale with the terms a buyer can pay for now. Stars and Rub are
// those of the first of Terms.
type Offer struct {
	Tariff db.Tariff
	Terms  []OfferTerm // at least one
	First  int64       // days of the tariff's first term, which Terms may not hold
	Stars  int64       // 0: not for Stars
	Rub    int64       // kopecks; 0: not for rubles
	// Groups are the server groups with servers in them, as the tariff has each; Gifts the
	// packages it gives with every payment. Both say what the tariff is made of.
	Groups []OfferGroup
	Gifts  []OfferGift
}

// OfferGroup is a server group as a tariff on sale has it.
// OfferGroup is a server group of a tariff on sale, as the bot and the Mini App list it.
type OfferGroup struct {
	Name  string `json:"name"`
	Mode  string `json:"mode"`            // domain.PoolMain, PoolOwn, PoolUnlimited, PoolClosed
	Limit int64  `json:"limit,omitempty"` // bytes, with PoolOwn
}

// OfferGift is a traffic package a tariff on sale gives with every payment.
type OfferGift struct {
	Package db.TrafficPackage
	Pool    string // the group's name; "" for the main traffic
}

// OfferTerm is a term of a tariff on sale with the prices the available providers take.
type OfferTerm struct {
	Days  int64
	Stars int64 // 0: not for Stars
	Rub   int64 // kopecks; 0: not for rubles
}

// Offers lists the tariffs a buyer can pay for now, each with the terms that have a price
// an available provider takes.
func (s *Service) Offers(ctx context.Context) ([]Offer, Available, error) {
	av := s.Available(ctx)
	if !av.Any() {
		return nil, av, nil
	}
	ts, err := s.d.Store.Q.ListTariffsOnSale(ctx)
	if err != nil {
		return nil, av, err
	}
	rows, err := s.d.Store.Q.ListAllTariffTerms(ctx)
	if err != nil {
		return nil, av, err
	}
	groups, gifts, err := s.contents(ctx, ts)
	if err != nil {
		return nil, av, err
	}
	var out []Offer
	for _, t := range ts {
		o := Offer{Tariff: t, Groups: groups[t.ID], Gifts: gifts[t.ID]}
		terms := domain.TariffTerms(t, rows)
		o.First = terms[0].Days
		for _, term := range terms {
			ot := OfferTerm{Days: term.Days}
			ot.Stars, ot.Rub = av.prices(term.PriceStars, term.PriceRub)
			if ot.Stars > 0 || ot.Rub > 0 {
				o.Terms = append(o.Terms, ot)
			}
		}
		if len(o.Terms) > 0 {
			o.Stars, o.Rub = o.Terms[0].Stars, o.Terms[0].Rub
			out = append(out, o)
		}
	}
	return out, av, nil
}

// contents reads what the tariffs ts are made of, by tariff id: their groups (only a group
// with a server of the tariff in it says anything to a buyer) and their gifts (only one that
// adds to a quota the tariff gives, domain.PackageForTariff: what a payment will grant). Each
// table is read once, whatever the number of tariffs.
func (s *Service) contents(ctx context.Context, ts []db.Tariff) (map[int64][]OfferGroup, map[int64][]OfferGift, error) {
	q := s.d.Store.Q
	pools, err := q.ListTrafficPools(ctx)
	if err != nil {
		return nil, nil, err
	}
	ins, err := q.ListInbounds(ctx)
	if err != nil {
		return nil, nil, err
	}
	servers, err := domain.AllTariffServers(ctx, q)
	if err != nil {
		return nil, nil, err
	}
	tps, err := q.ListAllTariffPools(ctx)
	if err != nil {
		return nil, nil, err
	}
	byTariff := map[int64][]db.TariffPool{}
	for _, r := range tps {
		byTariff[r.TariffID] = append(byTariff[r.TariffID], r)
	}
	packages, err := q.ListTrafficPackages(ctx)
	if err != nil {
		return nil, nil, err
	}
	byID := make(map[int64]db.TrafficPackage, len(packages))
	for _, p := range packages {
		byID[p.ID] = p
	}
	giftIDs, err := domain.TariffGifts(ctx, q)
	if err != nil {
		return nil, nil, err
	}
	names := map[int64]string{}
	for _, p := range pools {
		names[p.ID] = p.Name
	}
	groups, gifts := map[int64][]OfferGroup{}, map[int64][]OfferGift{}
	for _, t := range ts {
		serves := domain.ServersOf(servers, sql.NullInt64{Int64: t.ID, Valid: true}).Groups(ins)
		for _, p := range pools {
			if serves[p.ID] {
				mode, limit := domain.TariffPoolMode(byTariff[t.ID], p.ID)
				groups[t.ID] = append(groups[t.ID], OfferGroup{Name: p.Name, Mode: mode, Limit: limit.Int64})
			}
		}
		for _, id := range giftIDs[t.ID] {
			p, ok := byID[id]
			if !ok || p.Archived != 0 || !domain.PackageForTariff(t, byTariff[t.ID], p, serves) {
				continue
			}
			gifts[t.ID] = append(gifts[t.ID], OfferGift{Package: p, Pool: names[p.PoolID.Int64]})
		}
	}
	return groups, gifts, nil
}

// Term is the offer's term of so many days; nil days: the tariff's first term, the one an
// invoice without days is for (not there when no available provider takes its price).
func (o Offer) Term(days *int64) (OfferTerm, bool) {
	t, ok := o.Model().Term(days)
	return OfferTerm(t), ok
}

// InvoiceRequest: who buys which tariff for which term with what. UserID 0 buys a new
// subscription.
type InvoiceRequest struct {
	TgID      int64
	UserID    int64
	TariffID  int64
	TermDays  *int64 // the term's days; nil: the tariff's first term
	Provider  string
	PromoCode string
}

// Invoice opens a payment and returns it with the URL to pay at. An open invoice for the
// same purchase made in the last minutes is returned again instead of a new one.
func (s *Service) Invoice(ctx context.Context, req InvoiceRequest) (db.Payment, error) {
	if err := s.promoGuard(ctx, req.Provider, req.PromoCode); err != nil {
		return db.Payment{}, err
	}
	q := s.d.Store.Q
	t, err := q.GetTariff(ctx, req.TariffID)
	if errors.Is(err, sql.ErrNoRows) || err == nil && (t.Archived != 0 || t.OnSale == 0) {
		return db.Payment{}, ErrNotForSale
	}
	if err != nil {
		return db.Payment{}, err
	}
	terms, err := domain.TermsOf(ctx, q, t)
	if err != nil {
		return db.Payment{}, err
	}
	term := terms[0]
	if req.TermDays != nil {
		var found bool
		if term, found = domain.FindTerm(terms, *req.TermDays); !found {
			return db.Payment{}, ErrNotForSale
		}
	}
	av := s.Available(ctx)
	stars, rub := term.PriceStars, term.PriceRub
	// Devices bought that stay with renewals are paid for with each of them.
	if req.UserID != 0 {
		if u, err := q.GetUser(ctx, req.UserID); err == nil {
			moreStars, moreRub := model.RenewalExtras(s.Config(ctx).Model(), model.User(u), model.Tariff(t))
			stars, rub = plus(stars, moreStars), plus(rub, moreRub)
		}
	}
	amount, currency, ok := av.price(req.Provider, stars, rub)
	if !ok {
		return db.Payment{}, ErrProviderOff
	}
	termDays := sql.NullInt64{Int64: term.Days, Valid: true}
	kind := "renew"
	if req.UserID == 0 {
		kind = "new"
		if !s.Config(ctx).AllowNew {
			return db.Payment{}, ErrNewOff
		}
		if n, err := q.CountTgLinksOf(ctx, req.TgID); err != nil {
			return db.Payment{}, err
		} else if n >= s.d.MaxLinks {
			return db.Payment{}, ErrTooManySubs
		}
	} else if link, err := q.GetTgLink(ctx, req.UserID); err != nil || link.TgID != req.TgID {
		return db.Payment{}, ErrNotYours
	}
	unlock, err := s.lockBuyer(ctx, req.TgID)
	if err != nil {
		return db.Payment{}, err
	}
	defer unlock()
	now := s.d.Now()
	userID := sql.NullInt64{Int64: req.UserID, Valid: req.UserID != 0}
	tariffID := sql.NullInt64{Int64: t.ID, Valid: true}
	p, open, err := s.newPayment(ctx, req.TgID, now,
		func(q *db.Queries) ([]db.Payment, error) {
			return q.FindOpenPayments(ctx, db.FindOpenPaymentsParams{TgID: req.TgID, TariffID: tariffID, Provider: req.Provider, Kind: kind,
				UserID: req.UserID, Since: now.Add(-invoiceReuse).Unix(), RepeatSince: now.Add(-balanceRepeat).Unix()})
		},
		func(q *db.Queries, existing db.Payment) (bool, error) {
			if existing.TermDays != termDays {
				return false, nil
			}
			return s.matchesOpenPayment(ctx, q, existing, amount, req.PromoCode)
		},
		func(q *db.Queries) (db.Payment, error) {
			p, err := q.CreatePayment(ctx, db.CreatePaymentParams{Provider: req.Provider, Payload: secure.Token(32), TgID: req.TgID, Kind: kind, UserID: userID,
				TariffID: tariffID, TariffName: t.Name, Amount: amount, Currency: currency, CreatedAt: now.Unix(), TermDays: termDays})
			if err != nil {
				return db.Payment{}, err
			}
			if err := s.discount(ctx, q, &p, req.UserID, t.ID, req.PromoCode); err != nil {
				return db.Payment{}, err
			}
			return p, nil
		})
	if err != nil {
		return p, err
	}
	if open {
		return p, nil
	}
	lang, _ := s.d.Settings.Lang(ctx)
	return s.openPayment(ctx, p, t.Name, model.Describe(model.Tariff(t), term.Days, lang))
}

// promoGuard refuses a promo code before anything is opened: codes are off, or the
// provider takes none. Invoice and PackageInvoice both ask it.
func (s *Service) promoGuard(ctx context.Context, provider, code string) error {
	if strings.TrimSpace(code) == "" {
		return nil
	}
	if s.d.Promo == nil {
		return promo.ErrUnavailable
	}
	return s.validatePromoProvider(ctx, provider)
}

// discount reserves a promo code's discount for the payment p just created, in the same
// transaction, and sets the amount to pay to what is left: for a tariff, or for a package
// of the subscription on tariffID. Without a code it does nothing.
func (s *Service) discount(ctx context.Context, q *db.Queries, p *db.Payment, userID, tariffID int64, code string) error {
	if s.d.Promo == nil || strings.TrimSpace(code) == "" {
		return nil
	}
	d, err := s.d.Promo.ReserveDiscount(ctx, q, p.TgID, userID, tariffID, p.Amount, p.Currency, code, p.ID)
	if err != nil {
		return err
	}
	if _, err := q.SetPaymentAmount(ctx, db.SetPaymentAmountParams{Amount: d.Final, ID: p.ID}); err != nil {
		return err
	}
	p.Amount = d.Final
	return nil
}

// lockBuyer lets one invoice of a Telegram account be opened at a time, the provider's
// answer included: a second tap waits and gets the first invoice again instead of a new
// one. It gives up when ctx ends.
func (s *Service) lockBuyer(ctx context.Context, tgID int64) (unlock func(), err error) {
	return s.buyers.lock(ctx, tgID)
}

// keyedLock is a lock per key (a buyer, a payment), there only while it is held or
// waited for.
type keyedLock struct {
	mu   sync.Mutex
	keys map[int64]*keyTurn
}

// keyTurn is one key's turn.
type keyTurn struct {
	turn    chan struct{}
	waiting int // holder and waiters: the key goes when none is left
}

// lock waits for key's turn, or gives up when ctx ends.
func (k *keyedLock) lock(ctx context.Context, key int64) (unlock func(), err error) {
	k.mu.Lock()
	if k.keys == nil {
		k.keys = map[int64]*keyTurn{}
	}
	t := k.keys[key]
	if t == nil {
		t = &keyTurn{turn: make(chan struct{}, 1)}
		k.keys[key] = t
	}
	t.waiting++
	k.mu.Unlock()
	leave := func() {
		k.mu.Lock()
		if t.waiting--; t.waiting == 0 {
			delete(k.keys, key)
		}
		k.mu.Unlock()
	}
	select {
	case t.turn <- struct{}{}:
		return func() { <-t.turn; leave() }, nil
	case <-ctx.Done():
		leave()
		return nil, ctx.Err()
	}
}

// newPayment returns an open invoice matching the purchase (open true), or makes one with
// create while the account is under its hourly limit. The check
// and the insert are one transaction under a lock per account in the database, so taps at
// once cannot pass the limit together. READ COMMITTED: after the lock each statement sees
// what the previous holder committed (a serializable snapshot would be taken before the
// lock is granted). The provider is asked after the commit, outside the transaction.
func (s *Service) newPayment(ctx context.Context, tgID int64, now time.Time,
	find func(q *db.Queries) ([]db.Payment, error), matches func(q *db.Queries, p db.Payment) (bool, error),
	create func(q *db.Queries) (db.Payment, error)) (p db.Payment, open bool, err error) {
	err = s.d.Store.TxRC(ctx, func(q *db.Queries) error {
		p, open = db.Payment{}, false
		if err := q.LockBuyerInvoices(ctx, tgID); err != nil {
			return err
		}
		found, err := find(q)
		if err != nil {
			return err
		}
		for _, candidate := range found {
			matched, err := matches(q, candidate)
			if err != nil {
				return err
			}
			if matched {
				p, open = candidate, true
				return nil
			}
		}
		n, err := q.CountRecentInvoices(ctx, db.CountRecentInvoicesParams{TgID: tgID, CreatedAt: now.Add(-time.Hour).Unix()})
		if err != nil {
			return err
		}
		if n >= maxPerHour {
			return ErrTooMany
		}
		p, err = create(q)
		return err
	})
	return p, open, err
}

func (s *Service) matchesOpenPayment(ctx context.Context, q *db.Queries, existing db.Payment, amount int64, code string) (bool, error) {
	// A balance payment of the same purchase made just now (the finders give them for
	// balanceRepeat): a second tap or a retry of the screen, which gets it again.
	if existing.Provider == Balance && existing.Status != PaymentPending {
		return true, nil
	}
	if strings.TrimSpace(code) == "" {
		if existing.Amount != amount {
			return false, nil
		}
		if s.d.Promo == nil {
			return true, nil
		}
		r, err := q.GetPromoRedemptionByPayment(ctx, sql.NullInt64{Int64: existing.ID, Valid: true})
		if errors.Is(err, sql.ErrNoRows) {
			return true, nil
		}
		if err != nil {
			return false, err
		}
		return r.Status != promo.RedemptionReserved, nil
	}
	if s.d.Promo == nil {
		return false, nil
	}
	r, err := q.GetPromoRedemptionByPayment(ctx, sql.NullInt64{Int64: existing.ID, Valid: true})
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if r.Status != promo.RedemptionReserved || existing.Amount != r.FinalAmount || r.OriginalAmount != amount || r.Currency != existing.Currency || r.ExpiresAt.Valid && s.d.Now().Unix() >= r.ExpiresAt.Int64 {
		return false, nil
	}
	pc, err := q.GetPromoCode(ctx, r.PromoID)
	return err == nil && pc.Code == promo.Normalize(code), err
}

// prices are the prices of an item the available providers take: 0 where none does.
func (av Available) prices(stars, rub sql.NullInt64) (inStars, inRub int64) {
	if av.Stars && stars.Valid {
		inStars = stars.Int64
	}
	if av.Rub() && rub.Valid {
		inRub = rub.Int64
	}
	return inStars, inRub
}

// price is what a buyer pays with provider for an item with these prices: Stars for
// Stars, rubles for the others; ok false when the provider cannot take it now.
func (av Available) price(provider string, stars, rub sql.NullInt64) (amount int64, currency string, ok bool) {
	switch {
	case provider == Stars && av.Stars && stars.Valid:
		return stars.Int64, "XTR", true
	case provider == Balance && av.has(provider) && rub.Valid:
		return av.Balance.FromRub(rub.Int64), av.Balance.Currency, true
	case provider != Stars && av.has(provider) && rub.Valid:
		return rub.Int64, "RUB", true
	}
	return 0, "", false
}

// openPayment opens the provider's invoice for a payment just made and returns it with the
// URL to pay at; a provider that fails marks the payment failed.
func (s *Service) openPayment(ctx context.Context, p db.Payment, title, desc string) (db.Payment, error) {
	q := s.d.Store.Q
	// The balance has no invoice: it pays at once, or the payment fails for want of money.
	if p.Provider == Balance {
		charged, err := s.chargeBalance(ctx, p)
		if !charged {
			// Nothing was taken (too little money, or the charge failed): the payment fails,
			// and the buyer is not told it is paid.
			_, _ = q.SetPaymentStatus(context.WithoutCancel(ctx), db.SetPaymentStatusParams{NewStatus: PaymentFailed, ID: p.ID, OldStatus: PaymentPending})
			if s.d.Promo != nil {
				_ = s.d.Promo.ReleasePayment(context.WithoutCancel(ctx), p.ID)
			}
			_ = q.SetPaymentError(context.WithoutCancel(ctx), db.SetPaymentErrorParams{Error: errCode(err), ID: p.ID})
			if !errors.Is(err, ErrBalanceLow) {
				s.d.Log.Warn("billing: charge a balance payment", "payment", p.ID, "err", err)
			}
			return db.Payment{}, err
		}
		if err != nil {
			// Paid but not applied: Reconcile applies it, the buyer sees it paid.
			s.d.Log.Warn("billing: apply a balance payment", "payment", p.ID, "err", err)
		}
		return q.GetPayment(ctx, p.ID)
	}
	ext, url, err := s.openInvoice(ctx, p, title, desc)
	if err != nil {
		_, _ = q.SetPaymentStatus(ctx, db.SetPaymentStatusParams{NewStatus: PaymentFailed, ID: p.ID, OldStatus: PaymentPending})
		if s.d.Promo != nil {
			_ = s.d.Promo.ReleasePayment(ctx, p.ID)
		}
		_ = q.SetPaymentError(ctx, db.SetPaymentErrorParams{Error: errCode(err), ID: p.ID})
		s.d.Log.Warn("billing: invoice", "provider", p.Provider, "payment", p.ID, "err", err)
		return db.Payment{}, fmt.Errorf("%w: %s", ErrProviderOff, errCode(err))
	}
	if err := q.SetPaymentInvoice(ctx, db.SetPaymentInvoiceParams{ExternalID: ext, PayUrl: url, ID: p.ID}); err != nil {
		return db.Payment{}, err
	}
	p.ExternalID, p.PayUrl = ext, url
	return p, nil
}

// openInvoice asks the provider for the invoice: its id (none for Stars until paid) and
// the URL the buyer pays at.
func (s *Service) openInvoice(ctx context.Context, p db.Payment, title, desc string) (sql.NullString, string, error) {
	switch p.Provider {
	case Stars:
		tg := s.telegram()
		if tg == nil {
			return sql.NullString{}, "", ErrProviderOff
		}
		url, err := tg.InvoiceLink(ctx, title, desc, p.Payload, p.Amount)
		return sql.NullString{}, url, err
	}
	if model.AddonID(p.Provider) != "" {
		return s.openAddonInvoice(ctx, p, title+" — "+desc)
	}
	return sql.NullString{}, "", ErrProviderOff
}

// PreCheckout is Telegram asking whether a Stars payment may go ahead.
func (s *Service) PreCheckout(ctx context.Context, tgID int64, payload, currency string, amount int64) error {
	p, err := s.d.Store.Q.GetPaymentByPayload(ctx, payload)
	if err != nil || p.Provider != Stars || p.Status != PaymentPending || p.TgID != tgID || p.Currency != currency || p.Amount != amount {
		return ErrBadPayment
	}
	if s.d.Promo != nil {
		if r, e := s.d.Promo.GetPaymentRedemption(ctx, p.ID); e == nil && r.ExpiresAt.Valid && s.d.Now().Unix() >= r.ExpiresAt.Int64 {
			return ErrBadPayment
		}
	}
	if p.Kind == KindPackage {
		return s.packageOnSale(ctx, p)
	}
	if p.Kind == KindDevice {
		return s.deviceOnSale(ctx, p)
	}
	if p.Kind == KindTrial {
		// The trial's tariff is seldom on sale itself: the price is the trial's. What has
		// to hold is the offer, as at the invoice: a priced trial on this very tariff,
		// and an account that may still be given it. Stars are not taken otherwise.
		if t, ok := s.TrialTariff(ctx); !ok || !s.Config(ctx).TrialPaid() || t.ID != p.TariffID.Int64 {
			return ErrTrialOff
		}
		allowed, err := s.trialAllowed(ctx, s.d.Store.Q, tgID)
		if err != nil {
			return err
		}
		if !allowed {
			return ErrTrialUsed
		}
		return nil
	}
	t, err := s.d.Store.Q.GetTariff(ctx, p.TariffID.Int64)
	if err != nil || t.Archived != 0 || t.OnSale == 0 {
		return ErrNotForSale
	}
	// The invoice keeps the term and the price it was made for, as with every other
	// provider: a term edited or taken off since then is still the one paid for.
	return nil
}

// StarsPaid records a successful_payment from the bot's own update stream and applies it.
// Only a payment that matches no invoice is ErrBadPayment: a database that failed to
// answer is returned as it is, so the bot offers the payment again instead of losing it.
func (s *Service) StarsPaid(ctx context.Context, tgID int64, payload, chargeID, currency string, amount int64) error {
	p, err := s.d.Store.Q.GetPaymentByPayload(ctx, payload)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("stars payment lookup: %w", err)
	}
	if err != nil || p.Provider != Stars || p.TgID != tgID || p.Currency != currency || p.Amount != amount || chargeID == "" {
		s.d.Log.Error("billing: stars payment does not match an invoice", "tg", tgID, "amount", amount)
		return ErrBadPayment
	}
	err = s.paid(ctx, p, chargeID)
	if err == nil {
		return nil
	}
	// Saved with this charge: the reconcile pass applies or refunds it from here.
	if cur, e := s.d.Store.Q.GetPayment(ctx, p.ID); e == nil && cur.ExternalID.Valid && cur.ExternalID.String == chargeID {
		return fmt.Errorf("%w: %w", ErrRecorded, err)
	}
	return err
}

// paid marks the payment paid (once) and applies it.
func (s *Service) paid(ctx context.Context, p db.Payment, externalID string) error {
	if s.d.Promo != nil {
		r, err := s.d.Promo.GetPaymentRedemption(ctx, p.ID)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		late := err == nil && promo.PaidLate(r, paymentTime(p, s.d.Now().Unix()))
		if late {
			handled, err := s.refundLatePromoPayment(ctx, p.ID, externalID)
			if err != nil {
				return err
			}
			if handled {
				return nil
			}
		}
	}
	n, err := s.d.Store.Q.MarkPaymentPaid(ctx, db.MarkPaymentPaidParams{ExternalID: sql.NullString{String: externalID, Valid: true},
		PaidAt: sql.NullInt64{Int64: s.d.Now().Unix(), Valid: true}, ID: p.ID})
	if err != nil {
		return err
	}
	if n == 1 {
		s.d.Log.Info("billing: paid", "payment", p.ID, "provider", p.Provider, "amount", p.Amount, "currency", p.Currency)
	} else if second, err := s.secondCharge(ctx, p, externalID); err != nil || second {
		return err
	} else if cur, err := s.d.Store.Q.GetPayment(ctx, p.ID); err != nil {
		return err
	} else if cur.Status == PaymentFailed || cur.Status == PaymentRefunded {
		// The provider took the money of a payment the panel had closed (canceled at the
		// provider, refunded): nothing is given for it, and the admin sees it, to refund it
		// or to give by hand what was paid for.
		s.d.Log.Error("billing: paid after the payment was closed", "payment", p.ID, "provider", p.Provider, "status", cur.Status)
		return s.d.Store.Q.SetPaymentError(ctx, db.SetPaymentErrorParams{Error: "paid_after_close", ID: p.ID})
	}
	err = s.Apply(ctx, p.ID)
	if errors.Is(err, promo.ErrReservationExpired) && s.d.Promo != nil {
		handled, refundErr := s.refundLatePromoPayment(ctx, p.ID, externalID)
		if refundErr != nil {
			return refundErr
		}
		if handled {
			return nil
		}
	}
	return err
}

// secondCharge handles a report of an invoice already paid with another charge: a second
// tap or a second device paid the same Stars invoice. That charge gave nothing, so its
// Stars go back; another provider's is logged for the admin.
func (s *Service) secondCharge(ctx context.Context, p db.Payment, externalID string) (bool, error) {
	cur, err := s.d.Store.Q.GetPayment(ctx, p.ID)
	if err != nil {
		return false, err
	}
	if !cur.ExternalID.Valid || cur.ExternalID.String == externalID {
		return false, nil
	}
	if p.Provider != Stars {
		s.d.Log.Error("billing: a second charge for a paid invoice", "payment", p.ID, "provider", p.Provider, "charge", externalID)
		return true, nil
	}
	tg := s.telegram()
	if tg == nil {
		return true, errors.New("cannot refund a second Stars charge: Telegram bot is unavailable")
	}
	if err := tg.RefundStars(ctx, p.TgID, externalID); err != nil {
		return true, err
	}
	s.d.Log.Warn("billing: a second charge for a paid invoice refunded", "payment", p.ID, "charge", externalID)
	return true, nil
}

// Apply turns a paid payment into the subscription. It runs once per payment: the status
// moves to applied on the transaction that creates or renews the user.
func (s *Service) Apply(ctx context.Context, id int64) error {
	var (
		pay     db.Payment
		u       db.User
		created bool
		done    bool
	)
	// A payment is applied with the settings as they are; unreadable, it waits for the
	// next attempt rather than guessing.
	cfg, err := s.LoadConfig(ctx)
	if err != nil {
		return err
	}
	reset := cfg.RenewResetsTraffic
	run := func(q *db.Queries) error {
		// A conflict runs this again: nothing from an attempt that rolled back may stay.
		pay, u, created, done = db.Payment{}, db.User{}, false, false
		var err error
		if pay, err = q.GetPayment(ctx, id); err != nil {
			return err
		}
		if pay.Status != PaymentPaid {
			done = true
			return nil
		}
		if pay.Kind == KindPackage {
			if u, err = s.applyPackage(ctx, q, pay); err != nil {
				return err
			}
			if s.d.Promo != nil {
				if err := s.d.Promo.ApplyPayment(ctx, q, id, u.ID); err != nil {
					return err
				}
			}
			return markApplied(ctx, q, id, u.ID, s.d.Now())
		}
		if pay.Kind == KindDevice {
			if u, err = s.applyDevice(ctx, q, pay); err != nil {
				return err
			}
			return markApplied(ctx, q, id, u.ID, s.d.Now())
		}
		if pay.Kind == KindTrial {
			if u, err = s.applyTrial(ctx, q, pay); err != nil {
				return err
			}
			created = true
			if err := markApplied(ctx, q, id, u.ID, s.d.Now()); err != nil {
				return err
			}
			return q.SetPaymentRevert(ctx, db.SetPaymentRevertParams{Revert: purchaseRevert(true, false, db.User{}, u), ID: id})
		}
		var userID int64
		var prior db.User
		hadPrior := false
		if pay.Kind == "renew" && pay.UserID.Valid {
			userID = pay.UserID.Int64
			// What the renewal changes is recorded for a refund; a user gone since the
			// invoice is made anew.
			if prior, err = q.GetUser(ctx, userID); err == nil {
				hadPrior = true
			} else if !errors.Is(err, sql.ErrNoRows) {
				return err
			}
		}
		if u, created, err = s.d.Users.Purchase(ctx, q, domain.PurchaseInput{UserID: userID, TariffID: pay.TariffID.Int64,
			Term: pay.TermDays, Name: buyerName(ctx, q, pay.TgID), TgID: pay.TgID, ResetTraffic: reset, KeepExtraDevices: cfg.ExtraDevicesKeep,
			PaymentID: pay.ID}); err != nil {
			return err
		}
		// The tariff's gifts come with every payment for it, on its transaction: a refund
		// takes them back with it (takeBack).
		if _, err := domain.GrantGifts(ctx, q, u, pay.TariffID.Int64, pay.ID, s.d.Now()); err != nil {
			return err
		}
		if created {
			if err := q.LinkTg(ctx, db.LinkTgParams{UserID: u.ID, TgID: pay.TgID, CreatedAt: s.d.Now().Unix()}); err != nil {
				return err
			}
			// Not ignored: a failed statement aborts the whole PostgreSQL transaction anyway.
			if err := q.SetTgCurrent(ctx, db.SetTgCurrentParams{Current: u.ID, TgID: pay.TgID}); err != nil {
				return err
			}
		}
		if s.d.Promo != nil {
			if err := s.d.Promo.ApplyPayment(ctx, q, id, u.ID); err != nil {
				return err
			}
		}
		if err := markApplied(ctx, q, id, u.ID, s.d.Now()); err != nil {
			return err
		}
		return q.SetPaymentRevert(ctx, db.SetPaymentRevertParams{Revert: purchaseRevert(created, hadPrior, prior, u), ID: id})
	}
	// The addons hear of it in the same transaction: never of a payment that rolled back.
	told := func(q *db.Queries) error {
		if err := run(q); err != nil || done {
			return err
		}
		if err := emitPayment(ctx, q, s.d.Now(), events.PaymentPaid, pay, u.ID, created, false); err != nil {
			return err
		}
		if pay.Kind == KindTrial {
			return emitTrialStarted(ctx, q, s.d.Now(), u.ID, pay.TgID, pay.TariffID.Int64, pay.ID)
		}
		return nil
	}
	err = s.d.Users.WithSlots(ctx, func() error { return s.d.Store.Tx(ctx, told) })
	if err == nil && !done {
		s.d.Events.Kick()
	}
	if errors.Is(err, ErrTrialUsed) {
		// The account may no longer be given the trial it paid for: nothing was made, so
		// the money goes back instead of the payment waiting for an apply that never comes.
		if err := s.refundTrial(ctx, pay); err != nil {
			// The admin sees why on the Payments page, as for an apply that failed; a
			// refund the provider refused has said so already (trial_refund_failed).
			if cur, qerr := s.d.Store.Q.GetPayment(ctx, id); qerr == nil && cur.Error == "" {
				_ = s.d.Store.Q.SetPaymentError(ctx, db.SetPaymentErrorParams{Error: errCode(err), ID: id})
			}
			s.d.Log.Error("billing: refund of a trial", "payment", id, "err", err)
			return err
		}
		return nil
	}
	if err != nil {
		// Paid but not applied: the reconcile pass tries again, the admin sees why.
		_ = s.d.Store.Q.SetPaymentError(ctx, db.SetPaymentErrorParams{Error: errCode(err), ID: id})
		s.d.Log.Error("billing: apply", "payment", id, "err", err)
		return err
	}
	if done {
		return nil
	}
	s.d.Users.Changed()
	pay.Status, pay.UserID = PaymentApplied, sql.NullInt64{Int64: u.ID, Valid: true}
	s.d.Log.Info("billing: applied", "payment", id, "user", u.ID, "created", created)
	return nil
}

// markApplied moves a paid payment to applied on the transaction that applied it: the
// paid→applied step happens once, so a payment never applies twice.
func markApplied(ctx context.Context, q *db.Queries, id, userID int64, now time.Time) error {
	n, err := q.MarkPaymentApplied(ctx, db.MarkPaymentAppliedParams{UserID: sql.NullInt64{Int64: userID, Valid: true},
		AppliedAt: sql.NullInt64{Int64: now.Unix(), Valid: true}, ID: id})
	if err == nil && n != 1 {
		err = errors.New("payment changed while applying")
	}
	return err
}

// buyerName names a subscription bought by a Telegram account: its @username, its name,
// or its id.
func buyerName(ctx context.Context, q *db.Queries, tgID int64) string {
	if c, err := q.GetTgChat(ctx, tgID); err == nil {
		if c.Username != "" {
			return "@" + c.Username
		}
		if name := strings.TrimSpace(c.FirstName); name != "" {
			return name
		}
	}
	return fmt.Sprintf("tg %d", tgID)
}

// Refund returns a payment to the buyer and takes back what it gave (revert.go): Stars
// through Telegram, the balance onto the balance, an adapter's payment through the adapter
// when it can refund (else ErrNotRefunable: the admin refunds in the dashboard and marks it,
// RefundedByHand). A failure after the money went back leaves the payment applied:
// refunding again finishes it, Telegram and the adapters answer a repeated refund as done. A payment paid but never applied (its subscription
// or package deleted since the invoice) is refunded too: it gave nothing to take back.
func (s *Service) Refund(ctx context.Context, id int64) (Reverted, error) {
	p, err := s.d.Store.Q.GetPayment(ctx, id)
	if err != nil {
		return Reverted{}, err
	}
	if p.Provider != Stars && p.Provider != Balance && model.AddonID(p.Provider) == "" || p.Status != PaymentApplied && p.Status != PaymentPaid || !p.ExternalID.Valid {
		return Reverted{}, ErrNotRefunable
	}
	if p.Provider == Balance {
		// The money goes back on the balance, on the transaction of the refund (refunded).
		return s.refunded(ctx, id)
	}
	if model.AddonID(p.Provider) != "" {
		// An adapter that can give money back does, with a key of its own per payment so a
		// retry does not refund twice; one that cannot leaves it to the admin's dashboard
		// (RefundedByHand).
		if err := s.validatePromoProvider(ctx, p.Provider); errors.Is(err, promo.ErrRefundUnsupported) {
			return Reverted{}, ErrNotRefunable
		} else if err != nil {
			return Reverted{}, err
		}
		if err := s.refundCharge(ctx, p, p.ExternalID.String, "mikan-refund-"+strconv.FormatInt(p.ID, 10)); err != nil {
			return Reverted{}, err
		}
		rv, err := s.refunded(ctx, id)
		if err != nil {
			return Reverted{}, fmt.Errorf("%w: %w", ErrRefundNotApplied, err)
		}
		return rv, nil
	}
	tg := s.telegram()
	if tg == nil {
		return Reverted{}, ErrProviderOff
	}
	if err := tg.RefundStars(ctx, p.TgID, p.ExternalID.String); err != nil {
		return Reverted{}, err
	}
	rv, err := s.refunded(ctx, id)
	if err != nil {
		return Reverted{}, fmt.Errorf("%w: %w", ErrRefundNotApplied, err)
	}
	return rv, nil
}

// StarsRefunded takes Telegram's word that a Stars payment was refunded (refunded_payment:
// the buyer asked Telegram, or the refund above came back as an update). A charge that is
// not a payment of ours, or one already refunded, changes nothing.
func (s *Service) StarsRefunded(ctx context.Context, tgID int64, chargeID string) error {
	p, err := s.d.Store.Q.GetPaymentByExternal(ctx, db.GetPaymentByExternalParams{Provider: Stars, ExternalID: sql.NullString{String: chargeID, Valid: chargeID != ""}})
	if errors.Is(err, sql.ErrNoRows) || err == nil && p.TgID != tgID {
		s.d.Log.Warn("billing: stars refund does not match a payment", "tg", tgID)
		return nil
	}
	if err != nil {
		return err
	}
	_, err = s.refunded(ctx, p.ID)
	return err
}

// Run reconciles in the background: applies paid payments that failed to apply, asks the
// adapters about open invoices (a webhook may never come) and expires invoices nobody paid.
func (s *Service) Run(ctx context.Context) {
	pace.Tick(ctx, reconcileEvery, s.Reconcile)
}

// Reconcile is one pass of Run.
func (s *Service) Reconcile(ctx context.Context) {
	now := s.d.Now()
	q := s.d.Store.Q
	// Paid but not applied payments are tried for as long as they stay so: the buyer
	// paid, and the admin sees why on the Payments page. Pending ones are asked about while
	// they are open (pendingTTL).
	paid, err := q.ListPaidPayments(ctx)
	if err != nil {
		s.d.Log.Error("billing: reconcile", "err", err)
		return
	}
	for _, p := range paid {
		if ctx.Err() != nil {
			return
		}
		if p.Error == TrialRefundByHand {
			continue // the admin's now: nothing a pass can do (RefundedByHand closes it)
		}
		if err := s.Apply(ctx, p.ID); errors.Is(err, promo.ErrReservationExpired) && s.d.Promo != nil && p.ExternalID.Valid {
			if _, refundErr := s.refundLatePromoPayment(ctx, p.ID, p.ExternalID.String); refundErr != nil {
				s.d.Log.Error("billing: reconcile late promo refund", "payment", p.ID, "err", refundErr)
			}
		}
	}
	failedRefunds, err := q.ListLatePromoRefunds(ctx)
	if err != nil {
		s.d.Log.Error("billing: reconcile late promo refunds", "err", err)
	} else if s.d.Promo != nil {
		for _, id := range failedRefunds {
			if ctx.Err() != nil {
				return
			}
			p, err := q.GetPayment(ctx, id)
			if err != nil {
				s.d.Log.Error("billing: load late promo refund", "payment", id, "err", err)
				continue
			}
			if !p.ExternalID.Valid {
				continue
			}
			if _, err := s.refundLatePromoPayment(ctx, p.ID, p.ExternalID.String); err != nil {
				s.d.Log.Error("billing: retry late promo refund", "payment", p.ID, "err", err)
			}
		}
	}
	open, err := q.ListPendingPayments(ctx, now.Add(-pendingTTL).Unix())
	if err != nil {
		s.d.Log.Error("billing: reconcile", "err", err)
		return
	}
	// A provider that failed to answer is not asked about its other invoices this pass:
	// each call may take AdapterTimeout, and the next pass comes in a minute.
	down := map[string]bool{}
	for _, p := range open {
		if ctx.Err() != nil {
			return
		}
		if model.AddonID(p.Provider) == "" || !p.ExternalID.Valid || !dueCheck(p, now) || down[p.Provider] {
			continue
		}
		// A refused amount is logged where it is found; the rest (the adapter or the provider
		// is down, the credentials stopped working) would leave the invoice pending unseen.
		err := s.checkAddonPayment(ctx, p)
		if err == nil || errors.Is(err, ErrBadPayment) {
			continue
		}
		s.d.Log.Warn("billing: invoice check", "payment", p.ID, "provider", p.Provider, "err", err)
		var ae *addons.Error
		if !errors.As(err, &ae) || ae.Status >= 500 {
			down[p.Provider] = true
		}
	}
	if _, err := q.ExpirePayments(ctx, now.Add(-pendingTTL).Unix()); err != nil {
		s.d.Log.Error("billing: expire", "err", err)
	}
	if s.d.Promo != nil {
		_ = s.d.Promo.ReleaseExpired(ctx, now.Unix())
	}
}

// dueCheck spreads the asking out: an invoice is asked about every minute while the buyer
// is likely paying it, then every ten minutes until it expires (a webhook comes anyway).
func dueCheck(p db.Payment, now time.Time) bool {
	if now.Sub(time.Unix(p.CreatedAt, 0)) < 15*time.Minute {
		return true
	}
	return (now.Unix()/60+p.ID)%10 == 0
}

// WebhookToken is the secret path part of the webhook URLs, made once.
func (s *Service) WebhookToken(ctx context.Context) (string, error) {
	tok, err := s.d.Settings.String(ctx, KeyWebhookToken)
	if err != nil || len(tok) == 32 {
		return tok, err
	}
	tok = secure.Token(32)
	return tok, settings.Set(ctx, s.d.Settings, KeyWebhookToken, tok)
}

// errCode keeps provider errors short and free of secrets for the payments list.
func errCode(err error) string {
	var ae *addons.Error
	switch {
	case errors.As(err, &ae):
		return ae.Code
	case errors.Is(err, addons.ErrUnreachable), errors.Is(err, addons.ErrNotInstalled):
		return "addon_unreachable"
	case errors.Is(err, domain.ErrNoSlots):
		return "no_slots"
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	}
	// A cut rune would make the row unwritable, and the reason would be lost.
	return textutil.Clip(err.Error(), 120)
}
