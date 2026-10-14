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
	"strings"
	"sync"
	"time"

	"mikan/internal/panel/addons"
	"mikan/internal/panel/campaigns"
	"mikan/internal/panel/domain"
	"mikan/internal/panel/events"
	"mikan/internal/panel/promo"
	"mikan/internal/panel/referral"
	"mikan/internal/panel/secure"
	"mikan/internal/panel/settings"
	"mikan/internal/panel/store"
	"mikan/internal/panel/store/db"
)

// Stars is Telegram's own currency; every other provider is a marketplace adapter,
// "addon:<id>".
const Stars = "stars"

// Settings keys.
const (
	KeyConfig       = "pay_config"
	KeyWebhookToken = "pay_webhook_token" // the secret part of the webhook URLs
	invoiceReuse    = 10 * time.Minute    // an open invoice for the same purchase is shown again
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
	// takes it again once its previous free subscription has ended; 0: no free plan. It
	// takes no payment, so it works with selling off too.
	FreeTariffID int64 `json:"free_tariff_id,omitempty"`
	// FreeCondition is what the free plan asks of an account: FreeNoCondition, or
	// FreeChannel, a subscription to the bot's channel.
	FreeCondition string `json:"free_condition,omitempty"`
}

// TrialPaid: the trial has a price and is bought, not taken.
func (c Config) TrialPaid() bool { return c.TrialStars > 0 || c.TrialRub > 0 }

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

// Notifier tells buyers what came of their payments. The panel writes the deprecated bot.*
// events through it (tgbot.Notifications), for the bot builds released before the payment
// events; the payment.* and trial.not_given events the bot addon reads are written by billing
// itself, in the transaction of the change.
type Notifier interface {
	// Paid tells the buyer the subscription is ready.
	Paid(ctx context.Context, p db.Payment, u db.User, created bool)
	// Refunded tells the buyer the payment was refunded and what it gave is taken back:
	// u is the subscription after that (zero when it is gone), disabled that it was turned off.
	Refunded(ctx context.Context, p db.Payment, u db.User, disabled bool)
	// TrialNotGiven tells the buyer a paid trial was not given (the account had its trial)
	// and the money comes back by hand: the payment method cannot refund it.
	TrialNotGiven(ctx context.Context, p db.Payment)
}

type Deps struct {
	Store    *store.Store
	Settings *settings.Settings
	Users    *domain.Users
	Log      *slog.Logger
	Now      func() time.Time
	// TrustProxy reads the client's IP from X-Forwarded-For: adapters check where a
	// webhook came from (YooKassa's addresses).
	TrustProxy bool
	MaxLinks   int64 // subscriptions one Telegram account may hold (tgate.MaxLinks)
	// Addons are the marketplace's payment adapters; nil: none.
	Addons *addons.Manager
	// SubBase is https://host:port/<sub path>, where the webhooks are; "" without an address.
	SubBase func(ctx context.Context) string
	Promo   *promo.Service
	// Referral is the bot's referral programme: what an invitee's payment gives the
	// account that invited them, and what a balance pays of an invoice. nil: none.
	Referral *referral.Service
	// Campaigns are the ad campaign tags: which tagged link a buyer arrived through, so a
	// campaign's first purchases and renewals are counted. nil: none.
	Campaigns *campaigns.Service
	// Events wakes the addons reading the feed after a payment, a trial or a refund; nil: no
	// one waits (the events are written all the same).
	Events *events.Hub
}

type Service struct {
	d            Deps
	mu           sync.Mutex
	promoRefunds keyedLock // by payment: one late-promo refund of it at a time
	tg           Telegram
	notify       Notifier
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

// SetNotifier connects what tells buyers; until then nobody is told.
func (s *Service) SetNotifier(n Notifier) {
	s.mu.Lock()
	s.notify = n
	s.mu.Unlock()
}

func (s *Service) notifier() Notifier {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.notify
}

// Errors the bot and the Mini App show to buyers.
var (
	ErrNotForSale   = errors.New("not_for_sale")
	ErrProviderOff  = errors.New("provider_off")
	ErrNotYours     = errors.New("not_yours")
	ErrNewOff       = errors.New("new_off")
	ErrTooManySubs  = errors.New("too_many_subs")
	ErrTooMany      = errors.New("too_many_invoices")
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
}

func (a Available) Any() bool { return a.Stars || a.Rub() }

// Rub: some provider takes rubles.
func (a Available) Rub() bool { return len(a.Addons) > 0 }

func (a Available) has(provider string) bool {
	if provider == Stars {
		return a.Stars
	}
	id := AddonID(provider)
	return id != "" && slices.Contains(a.Addons, id)
}

func (s *Service) Available(ctx context.Context) Available {
	c := s.Config(ctx)
	if !c.Enabled {
		return Available{}
	}
	tg := s.telegram()
	return Available{
		Stars:  c.Stars && tg != nil && tg.BotURL(ctx) != "",
		Addons: s.availableAddons(ctx),
	}
}

// Offer is a tariff on sale with the terms a buyer can pay for now. Stars and Rub are
// those of the first of Terms.
type Offer struct {
	Tariff db.Tariff
	Terms  []OfferTerm // at least one
	First  int64       // days of the tariff's first term, which Terms may not hold
	Stars  int64       // 0: not for Stars
	Rub    int64       // kopecks; 0: not for rubles
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
	var out []Offer
	for _, t := range ts {
		o := Offer{Tariff: t}
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

// Term is the offer's term of so many days; nil days: the tariff's first term, the one an
// invoice without days is for (not there when no available provider takes its price).
func (o Offer) Term(days *int64) (OfferTerm, bool) {
	first := o.First
	if days == nil {
		days = &first
	}
	for _, t := range o.Terms {
		if t.Days == *days {
			return t, true
		}
	}
	return OfferTerm{}, false
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
	amount, currency, ok := s.Available(ctx).price(req.Provider, term.PriceStars, term.PriceRub)
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
	// What the referral balance pays of the price comes off first: it is the buyer's own
	// money, and a promo code is worked out against what is left to pay.
	refCfg := s.referralConfig(ctx)
	held := int64(0)
	if s.d.Referral != nil && refCfg.Spend {
		if held, err = s.d.Referral.Off(ctx, req.TgID, amount, currency, refCfg); err != nil {
			return db.Payment{}, err
		}
	}
	base := amount - held
	now := s.d.Now()
	userID := sql.NullInt64{Int64: req.UserID, Valid: req.UserID != 0}
	tariffID := sql.NullInt64{Int64: t.ID, Valid: true}
	p, open, err := s.newPayment(ctx, req.TgID, now,
		func(q *db.Queries) ([]db.Payment, error) {
			return q.FindOpenPayments(ctx, db.FindOpenPaymentsParams{TgID: req.TgID, TariffID: tariffID, Provider: req.Provider, Kind: kind,
				UserID: req.UserID, Since: now.Add(-invoiceReuse).Unix()})
		},
		func(q *db.Queries, existing db.Payment) (bool, error) {
			if existing.TermDays != termDays {
				return false, nil
			}
			return s.matchesOpenPayment(ctx, q, existing, base, req.PromoCode)
		},
		func(q *db.Queries) (db.Payment, error) {
			p, err := q.CreatePayment(ctx, db.CreatePaymentParams{Provider: req.Provider, Payload: secure.Token(32), TgID: req.TgID, Kind: kind, UserID: userID,
				TariffID: tariffID, TariffName: t.Name, Amount: amount, Currency: currency, CreatedAt: now.Unix(), TermDays: termDays})
			if err != nil {
				return db.Payment{}, err
			}
			// The referral balance pays what it can of the invoice first, then the promo
			// code takes its share off what is left to pay.
			if s.d.Referral != nil && refCfg.Spend {
				// Recomputed on the account's own row: what the balance can pay may have
				// changed since it was looked at outside.
				off, err := s.d.Referral.Reserve(ctx, q, req.TgID, p.ID, amount, currency, refCfg)
				if err != nil {
					return db.Payment{}, err
				}
				if off > 0 {
					if _, err := q.SetPaymentAmount(ctx, db.SetPaymentAmountParams{Amount: amount - off, ID: p.ID}); err != nil {
						return db.Payment{}, err
					}
					p.Amount = amount - off
				}
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
	return s.openPayment(ctx, p, t.Name, Describe(t, term.Days, lang))
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
	// amount is what the price is after the referral balance, not the tariff's own price.
	// An open invoice that already holds a share of that balance is the one to show again:
	// taking the balance a second time for the same purchase is not what the buyer meant.
	// With it, want is what is left to pay — the figure a promo code was worked out against.
	want := amount
	if s.d.Referral != nil {
		held, ok, err := s.d.Referral.Held(ctx, q, existing.ID, existing.Currency, s.referralConfig(ctx))
		if err != nil {
			return false, err
		}
		if ok {
			want -= held
		}
	}
	if strings.TrimSpace(code) == "" {
		if existing.Amount != want {
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
	if r.Status != promo.RedemptionReserved || existing.Amount != r.FinalAmount || r.OriginalAmount != want || r.Currency != existing.Currency || r.ExpiresAt.Valid && s.d.Now().Unix() >= r.ExpiresAt.Int64 {
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
	case provider != Stars && av.has(provider) && rub.Valid:
		return rub.Int64, "RUB", true
	}
	return 0, "", false
}

// openPayment opens the provider's invoice for a payment just made and returns it with the
// URL to pay at; a provider that fails marks the payment failed.
func (s *Service) openPayment(ctx context.Context, p db.Payment, title, desc string) (db.Payment, error) {
	q := s.d.Store.Q
	ext, url, err := s.openInvoice(ctx, p, title, desc)
	if err != nil {
		_, _ = q.SetPaymentStatus(ctx, db.SetPaymentStatusParams{NewStatus: PaymentFailed, ID: p.ID, OldStatus: PaymentPending})
		if s.d.Promo != nil {
			_ = s.d.Promo.ReleasePayment(ctx, p.ID)
		}
		// The provider never took the invoice: what the balance held of it is its owner's.
		s.referralBack(ctx, p.ID)
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
	if AddonID(p.Provider) != "" {
		return s.openAddonInvoice(ctx, p, title+" — "+desc)
	}
	return sql.NullString{}, "", ErrProviderOff
}

// Describe is tariff t bought for a term of days in a line, "30 days · 100 GB · 3
// devices", in lang ("en", else Russian): invoices, the bot and the Mini App show it.
func Describe(t db.Tariff, days int64, lang string) string {
	return TermLabel(t, days, lang) + " · " + DescribeLimits(t, lang)
}

// TermLabel is a term of tariff t in a few words: "30 days", "3 months" for a tariff that
// ends on a billing day, "no end date".
func TermLabel(t db.Tariff, days int64, lang string) string {
	pick := func(ru, en string) string {
		if lang == "en" {
			return en
		}
		return ru
	}
	switch {
	case days <= 0:
		return pick("бессрочно", "no end date")
	case t.BillingDay.Valid:
		n := domain.TermMonths(days)
		if n == 1 {
			return pick("1 мес.", "1 month")
		}
		return fmt.Sprintf(pick("%d мес.", "%d months"), n)
	}
	return fmt.Sprintf(pick("%d дн.", "%d days"), days)
}

// DescribeOffer is a tariff on sale in a line: Describe with its one term, or the range
// of its terms, "7 days – 90 days · 100 GB · 3 devices".
func DescribeOffer(o Offer, lang string) string {
	switch len(o.Terms) {
	case 0:
		return Describe(o.Tariff, o.Tariff.DurationDays, lang)
	case 1:
		return Describe(o.Tariff, o.Terms[0].Days, lang)
	}
	var lo, hi int64 = -1, -1
	forever := false
	for _, t := range o.Terms {
		if t.Days <= 0 {
			forever = true
			continue
		}
		if lo < 0 || t.Days < lo {
			lo = t.Days
		}
		hi = max(hi, t.Days)
	}
	if forever {
		hi = 0
	}
	return TermLabel(o.Tariff, lo, lang) + " – " + TermLabel(o.Tariff, hi, lang) + " · " + DescribeLimits(o.Tariff, lang)
}

// DescribeLimits is what tariff t gives whatever the term: "100 GB · 3 devices".
func DescribeLimits(t db.Tariff, lang string) string {
	en := lang == "en"
	pick := func(ru, en_ string) string {
		if en {
			return en_
		}
		return ru
	}
	parts := []string{}
	if t.TrafficLimit.Valid {
		parts = append(parts, fmt.Sprintf(pick("%d ГБ", "%d GB"), t.TrafficLimit.Int64>>30))
	} else {
		parts = append(parts, pick("трафик без лимита", "unlimited traffic"))
	}
	if t.DeviceLimit.Valid && t.DeviceMode == domain.DeviceModeOnline {
		// The buyer is not told of places: any number of devices, so many online at once.
		parts = append(parts, fmt.Sprintf(pick("устройств одновременно: %d", "devices at once: %d"), t.DeviceLimit.Int64))
	} else if t.DeviceLimit.Valid {
		parts = append(parts, fmt.Sprintf(pick("устройств: %d", "devices: %d"), t.DeviceLimit.Int64))
	}
	if t.SpeedLimit.Valid {
		parts = append(parts, fmt.Sprintf(pick("до %d Мбит/с", "up to %d Mbit/s"), t.SpeedLimit.Int64))
	}
	return strings.Join(parts, " · ")
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
			handled, err := s.refundLatePromoPayment(ctx, p.ID, externalID, func(current db.Payment) error {
				return s.refundPromoPayment(ctx, current, externalID)
			})
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
	}
	err = s.Apply(ctx, p.ID)
	if errors.Is(err, promo.ErrReservationExpired) && s.d.Promo != nil {
		handled, refundErr := s.refundLatePromoPayment(ctx, p.ID, externalID, func(current db.Payment) error {
			return s.refundPromoPayment(ctx, current, externalID)
		})
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
		rewards []referral.Reward
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
		pay, u, created, done, rewards = db.Payment{}, db.User{}, false, false, nil
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
			Term: pay.TermDays, Name: buyerName(ctx, q, pay.TgID), TgID: pay.TgID, ResetTraffic: reset}); err != nil {
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
		if s.d.Referral != nil {
			if rewards, err = s.d.Referral.RewardPayment(ctx, q, pay); err != nil {
				return err
			}
		}
		if s.d.Campaigns != nil {
			if err := s.d.Campaigns.Attribute(ctx, q, pay); err != nil {
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
	err = s.d.Store.Tx(ctx, told)
	if errors.Is(err, domain.ErrNoSlots) {
		if err = s.d.Users.RefillSlots(ctx); err == nil {
			err = s.d.Store.Tx(ctx, told)
		}
	}
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
	// Told outside the transaction the reward was committed with: the bot may take a
	// moment to answer, and nothing may hold the database for it.
	if s.d.Referral != nil && len(rewards) > 0 {
		s.d.Referral.Announce(ctx, rewards)
	}
	if n := s.notifier(); n != nil {
		n.Paid(ctx, pay, u, created)
	}
	return nil
}

// referralConfig is the referral programme as it is now; a read error leaves it off, which
// only means nobody earns and no balance is spent for as long as the store is unwell.
func (s *Service) referralConfig(ctx context.Context) referral.Program {
	if s.d.Referral == nil {
		return referral.Program{}
	}
	c, err := referral.Load(ctx, s.d.Settings)
	if err != nil {
		return referral.Program{}
	}
	return c
}

// referralBack gives the referral balance back what a payment held or earned for somebody,
// on its own transaction. Used where the payment will never be applied.
func (s *Service) referralBack(ctx context.Context, paymentID int64) {
	if s.d.Referral == nil {
		return
	}
	err := s.d.Store.Tx(ctx, func(q *db.Queries) error {
		_, err := s.d.Referral.TakeBack(ctx, q, paymentID)
		return err
	})
	if err != nil {
		s.d.Log.Warn("billing: referral balance not given back", "payment", paymentID, "err", err)
	}
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

// Refund returns a Stars payment to the buyer and takes back what it gave (revert.go).
// Payments through adapters are refunded in the provider's dashboard. A failure after
// Telegram refunded leaves the payment applied: refunding again finishes it, Telegram
// answers a repeated refund as done. A payment paid but never applied (its subscription
// or package deleted since the invoice) is refunded too: it gave nothing to take back.
func (s *Service) Refund(ctx context.Context, id int64) (Reverted, error) {
	p, err := s.d.Store.Q.GetPayment(ctx, id)
	if err != nil {
		return Reverted{}, err
	}
	if p.Provider != Stars || p.Status != PaymentApplied && p.Status != PaymentPaid || !p.ExternalID.Valid {
		return Reverted{}, ErrNotRefunable
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
	t := time.NewTicker(reconcileEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.Reconcile(ctx)
		}
	}
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
			if _, refundErr := s.refundLatePromoPayment(ctx, p.ID, p.ExternalID.String, func(current db.Payment) error {
				return s.refundPromoPayment(ctx, current, p.ExternalID.String)
			}); refundErr != nil {
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
			if _, err := s.refundLatePromoPayment(ctx, p.ID, p.ExternalID.String, func(current db.Payment) error {
				return s.refundPromoPayment(ctx, current, p.ExternalID.String)
			}); err != nil {
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
		if AddonID(p.Provider) == "" || !p.ExternalID.Valid || !dueCheck(p, now) || down[p.Provider] {
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
	if s.d.Referral != nil {
		_ = s.d.Referral.ReleaseClosed(ctx)
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
	msg := err.Error()
	if len(msg) > 120 {
		msg = msg[:120]
	}
	return msg
}
