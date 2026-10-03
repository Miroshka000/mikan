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

	"mikan/internal/panel/addons"
	"mikan/internal/panel/domain"
	"mikan/internal/panel/promo"
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
}

// DefaultConfig: selling off until the admin turns it on; then Stars (it needs nothing but
// the bot) and new buyers are welcome.
var DefaultConfig = Config{Stars: true, AllowNew: true, RenewResetsTraffic: true}

// Telegram is the bot's part: Stars invoices and refunds, and telling buyers.
type Telegram interface {
	// InvoiceLink makes a Stars invoice link (createInvoiceLink); "" when the bot is off.
	InvoiceLink(ctx context.Context, title, description, payload string, stars int64) (string, error)
	RefundStars(ctx context.Context, tgID int64, chargeID string) error
	// Paid tells the buyer the subscription is ready.
	Paid(ctx context.Context, p db.Payment, u db.User, created bool)
	// BotURL is https://t.me/<bot>, "" while the bot is off.
	BotURL(ctx context.Context) string
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
	MaxLinks   int64 // subscriptions one Telegram account may hold
	// Addons are the marketplace's payment adapters; nil: none.
	Addons *addons.Manager
	// SubBase is https://host:port/<sub path>, where the webhooks are; "" without an address.
	SubBase func(ctx context.Context) string
	Promo   *promo.Service
}

type Service struct {
	d             Deps
	mu            sync.Mutex
	promoRefundMu sync.Mutex
	promoRefunds  map[int64]*promoRefundLock
	tg            Telegram
	buyersMu      sync.Mutex
	buyers        map[int64]*buyerLock
}

func New(d Deps) *Service {
	if d.MaxLinks == 0 {
		d.MaxLinks = 5
	}
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
	ErrNotForSale   = errors.New("not_for_sale")
	ErrProviderOff  = errors.New("provider_off")
	ErrNotYours     = errors.New("not_yours")
	ErrNewOff       = errors.New("new_off")
	ErrTooManySubs  = errors.New("too_many_subs")
	ErrTooMany      = errors.New("too_many_invoices")
	ErrBadPayment   = errors.New("bad_payment")
	ErrNotRefunable = errors.New("not_refundable")
)

// LoadConfig reads the payment settings. A read error is returned, never replaced by the
// defaults: settings changed on top of those and saved would switch selling off and
// forget the providers.
func (s *Service) LoadConfig(ctx context.Context) (Config, error) {
	// Payment settings saved before the switch existed (0.4.0, 0.4.1) come from panels that
	// set up selling: they keep selling. A panel that never saved them starts with it off.
	saved := DefaultConfig
	saved.Enabled = true
	c, found, err := settings.GetOver(ctx, s.d.Settings, KeyConfig, saved)
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
// the first term's.
type Offer struct {
	Tariff db.Tariff
	Terms  []OfferTerm // at least one
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
		for _, term := range domain.TariffTerms(t, rows) {
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

// Term is the offer's term of so many days; nil days: the first one.
func (o Offer) Term(days *int64) (OfferTerm, bool) {
	if days == nil {
		return o.Terms[0], true
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
	if strings.TrimSpace(req.PromoCode) != "" && s.d.Promo == nil {
		return db.Payment{}, promo.ErrUnavailable
	}
	if strings.TrimSpace(req.PromoCode) != "" {
		if err := s.validatePromoProvider(ctx, req.Provider); err != nil {
			return db.Payment{}, err
		}
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
			return s.matchesOpenPayment(ctx, q, existing, amount, req.PromoCode)
		},
		func(q *db.Queries) (db.Payment, error) {
			p, err := q.CreatePayment(ctx, db.CreatePaymentParams{Provider: req.Provider, Payload: secure.Token(32), TgID: req.TgID, Kind: kind, UserID: userID,
				TariffID: tariffID, TariffName: t.Name, Amount: amount, Currency: currency, CreatedAt: now.Unix(), TermDays: termDays})
			if err != nil {
				return db.Payment{}, err
			}
			if s.d.Promo != nil && strings.TrimSpace(req.PromoCode) != "" {
				disc, err := s.d.Promo.ReserveDiscount(ctx, q, req.TgID, req.UserID, t.ID, amount, currency, req.PromoCode, p.ID)
				if err != nil {
					return db.Payment{}, err
				}
				if _, err := q.SetPaymentAmount(ctx, db.SetPaymentAmountParams{Amount: disc.Final, ID: p.ID}); err != nil {
					return db.Payment{}, err
				}
				p.Amount = disc.Final
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

// lockBuyer lets one invoice of a Telegram account be opened at a time, the provider's
// answer included: a second tap waits and gets the first invoice again instead of a new
// one. It gives up when ctx ends.
func (s *Service) lockBuyer(ctx context.Context, tgID int64) (unlock func(), err error) {
	s.buyersMu.Lock()
	if s.buyers == nil {
		s.buyers = map[int64]*buyerLock{}
	}
	l := s.buyers[tgID]
	if l == nil {
		l = &buyerLock{turn: make(chan struct{}, 1)}
		s.buyers[tgID] = l
	}
	l.waiting++
	s.buyersMu.Unlock()
	leave := func() {
		s.buyersMu.Lock()
		if l.waiting--; l.waiting == 0 {
			delete(s.buyers, tgID)
		}
		s.buyersMu.Unlock()
	}
	select {
	case l.turn <- struct{}{}:
		return func() { <-l.turn; leave() }, nil
	case <-ctx.Done():
		leave()
		return nil, ctx.Err()
	}
}

// buyerLock is one Telegram account's turn at opening invoices.
type buyerLock struct {
	turn    chan struct{}
	waiting int // holder and waiters: the lock goes when none is left
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
		return r.Status != "reserved", nil
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
	if r.Status != "reserved" || existing.Amount != r.FinalAmount || r.OriginalAmount != amount || r.Currency != existing.Currency || r.ExpiresAt.Valid && s.d.Now().Unix() >= r.ExpiresAt.Int64 {
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
		_, _ = q.SetPaymentStatus(ctx, db.SetPaymentStatusParams{NewStatus: "failed", ID: p.ID, OldStatus: "pending"})
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
		n := max(1, (days+15)/30) // domain.termMonths
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
	if len(o.Terms) < 2 {
		return Describe(o.Tariff, o.Tariff.DurationDays, lang)
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
	if t.DeviceLimit.Valid {
		parts = append(parts, fmt.Sprintf(pick("устройств: %d", "devices: %d"), t.DeviceLimit.Int64))
	}
	return strings.Join(parts, " · ")
}

// PreCheckout is Telegram asking whether a Stars payment may go ahead.
func (s *Service) PreCheckout(ctx context.Context, tgID int64, payload, currency string, amount int64) error {
	p, err := s.d.Store.Q.GetPaymentByPayload(ctx, payload)
	if err != nil || p.Provider != Stars || p.Status != "pending" || p.TgID != tgID || p.Currency != currency || p.Amount != amount {
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
	t, err := s.d.Store.Q.GetTariff(ctx, p.TariffID.Int64)
	if err != nil || t.Archived != 0 || t.OnSale == 0 {
		return ErrNotForSale
	}
	// A term taken off the tariff since the invoice is no longer sold either.
	if p.TermDays.Valid {
		terms, err := domain.TermsOf(ctx, s.d.Store.Q, t)
		if err != nil {
			return err
		}
		if _, ok := domain.FindTerm(terms, p.TermDays.Int64); !ok {
			return ErrNotForSale
		}
	}
	return nil
}

// StarsPaid records a successful_payment from the bot's own update stream and applies it.
func (s *Service) StarsPaid(ctx context.Context, tgID int64, payload, chargeID, currency string, amount int64) error {
	p, err := s.d.Store.Q.GetPaymentByPayload(ctx, payload)
	if err != nil || p.Provider != Stars || p.TgID != tgID || p.Currency != currency || p.Amount != amount || chargeID == "" {
		s.d.Log.Error("billing: stars payment does not match an invoice", "tg", tgID, "amount", amount)
		return ErrBadPayment
	}
	return s.paid(ctx, p, chargeID)
}

// paid marks the payment paid (once) and applies it.
func (s *Service) paid(ctx context.Context, p db.Payment, externalID string) error {
	if s.d.Promo != nil {
		r, err := s.d.Promo.GetPaymentRedemption(ctx, p.ID)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		late := err == nil && latePromoPayment(r, paymentTime(p, s.d.Now().Unix()))
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

func paymentTime(p db.Payment, fallback int64) int64 {
	if p.PaidAt.Valid {
		return p.PaidAt.Int64
	}
	return fallback
}

func latePromoPayment(r db.PromoRedemption, paidAt int64) bool {
	if r.ExpiresAt.Valid && paidAt < r.ExpiresAt.Int64 {
		return false
	}
	return r.Status == "released" || r.Status == "reserved" && r.ExpiresAt.Valid
}

func (s *Service) refundPromoPayment(ctx context.Context, p db.Payment, externalID string) error {
	switch {
	case p.Provider == Stars:
		tg := s.telegram()
		if tg == nil {
			return errors.New("cannot refund expired Stars promo payment: Telegram bot is unavailable")
		}
		return tg.RefundStars(ctx, p.TgID, externalID)
	case AddonID(p.Provider) != "":
		cl, cfg, err := s.addonClient(ctx, AddonID(p.Provider))
		if err != nil {
			return err
		}
		return cl.Refund(ctx, cfg.Values, externalID, p.Amount, "mikan-promo-late-"+strconv.FormatInt(p.ID, 10))
	default:
		return errors.New("cannot refund expired promo payment from this provider")
	}
}

// refundLatePromoPayment serializes late-payment refunds within the panel process and
// reloads persistent state after taking the lock so webhook and reconciliation cannot
// both refund the same invoice. Addon refunds also carry a stable provider idempotency key
// to cover retries after a process restart.
func (s *Service) refundLatePromoPayment(ctx context.Context, paymentID int64, externalID string, refund func(db.Payment) error) (bool, error) {
	unlock := s.lockPromoRefund(paymentID)
	defer unlock()

	pay, err := s.d.Store.Q.GetPayment(ctx, paymentID)
	if err != nil {
		return false, err
	}
	if pay.Status == "refunded" {
		if err := s.d.Promo.ReleasePayment(ctx, paymentID); err != nil {
			return true, err
		}
		return true, nil
	}
	r, err := s.d.Promo.GetPaymentRedemption(ctx, paymentID)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	late := latePromoPayment(r, paymentTime(pay, s.d.Now().Unix()))
	if !late {
		return false, nil
	}
	if externalID != "" {
		if err := s.d.Store.Q.SetPaymentExternalID(ctx, db.SetPaymentExternalIDParams{ExternalID: sql.NullString{String: externalID, Valid: true}, ID: paymentID}); err != nil {
			return true, s.markLatePromoRefundFailed(ctx, paymentID, err)
		}
	}
	n, err := s.d.Store.Q.ClaimLatePromoRefund(ctx, db.ClaimLatePromoRefundParams{
		RefundStartedAt: sql.NullInt64{Int64: s.d.Now().Unix(), Valid: true},
		ID:              r.ID,
		Now:             sql.NullInt64{Int64: s.d.Now().Unix(), Valid: true},
		RetryAfter:      sql.NullInt64{Int64: s.d.Now().Add(-2 * time.Minute).Unix(), Valid: true},
	})
	if err != nil {
		return true, err
	}
	if n != 1 {
		// Another panel process owns the current refund attempt. Its stable provider key
		// makes retry after a crashed process safe once the lease expires.
		return true, nil
	}
	if err := refund(pay); err != nil {
		_ = s.markLatePromoRefundFailed(ctx, paymentID, err)
		_ = s.d.Store.Q.ReleaseLatePromoRefundClaim(ctx, sql.NullInt64{Int64: paymentID, Valid: true})
		return true, err
	}
	n, err = s.d.Store.Q.MarkLatePromoRefunded(ctx, db.MarkLatePromoRefundedParams{
		RefundedAt: sql.NullInt64{Int64: s.d.Now().Unix(), Valid: true}, ID: paymentID,
	})
	if err != nil {
		return true, s.markLatePromoRefundFailed(ctx, paymentID, err)
	}
	if n != 1 {
		latest, err := s.d.Store.Q.GetPayment(ctx, paymentID)
		if err != nil {
			return true, err
		}
		if latest.Status != "refunded" {
			return true, s.markLatePromoRefundFailed(ctx, paymentID, errors.New("promo late refund completed but payment status changed"))
		}
	}
	if err := s.d.Promo.ReleasePayment(ctx, paymentID); err != nil {
		return true, s.markLatePromoRefundFailed(ctx, paymentID, err)
	}
	s.d.Log.Warn("billing: late discounted payment refunded", "payment", paymentID, "provider", pay.Provider)
	return true, nil
}

func (s *Service) markLatePromoRefundFailed(ctx context.Context, paymentID int64, cause error) error {
	if err := s.d.Store.Q.SetPaymentError(ctx, db.SetPaymentErrorParams{Error: "promo_late_refund_failed", ID: paymentID}); err != nil {
		return errors.Join(cause, err)
	}
	return cause
}

type promoRefundLock struct {
	mu   sync.Mutex
	refs int
}

func (s *Service) lockPromoRefund(paymentID int64) func() {
	s.promoRefundMu.Lock()
	if s.promoRefunds == nil {
		s.promoRefunds = make(map[int64]*promoRefundLock)
	}
	l := s.promoRefunds[paymentID]
	if l == nil {
		l = &promoRefundLock{}
		s.promoRefunds[paymentID] = l
	}
	l.refs++
	s.promoRefundMu.Unlock()
	l.mu.Lock()
	return func() {
		l.mu.Unlock()
		s.promoRefundMu.Lock()
		l.refs--
		if l.refs == 0 {
			delete(s.promoRefunds, paymentID)
		}
		s.promoRefundMu.Unlock()
	}
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
		if pay.Status != "paid" {
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
		var userID int64
		if pay.Kind == "renew" && pay.UserID.Valid {
			userID = pay.UserID.Int64
		}
		if u, created, err = s.d.Users.Purchase(ctx, q, userID, pay.TariffID.Int64, pay.TermDays, buyerName(ctx, q, pay.TgID), reset); err != nil {
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
		return markApplied(ctx, q, id, u.ID, s.d.Now())
	}
	err = s.d.Store.Tx(ctx, run)
	if errors.Is(err, domain.ErrNoSlots) {
		if err = s.d.Users.RefillSlots(ctx); err == nil {
			err = s.d.Store.Tx(ctx, run)
		}
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
	pay.Status, pay.UserID = "applied", sql.NullInt64{Int64: u.ID, Valid: true}
	s.d.Log.Info("billing: applied", "payment", id, "user", u.ID, "created", created)
	if tg := s.telegram(); tg != nil {
		tg.Paid(ctx, pay, u, created)
	}
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

// Refund returns a Stars payment to the buyer. The subscription stays as it is: the admin
// decides about it. Payments through adapters are refunded in the provider's dashboard.
func (s *Service) Refund(ctx context.Context, id int64) error {
	p, err := s.d.Store.Q.GetPayment(ctx, id)
	if err != nil {
		return err
	}
	if p.Provider != Stars || p.Status != "applied" || !p.ExternalID.Valid {
		return ErrNotRefunable
	}
	tg := s.telegram()
	if tg == nil {
		return ErrProviderOff
	}
	if err := tg.RefundStars(ctx, p.TgID, p.ExternalID.String); err != nil {
		return err
	}
	_, err = s.d.Store.Q.MarkPaymentRefunded(ctx, db.MarkPaymentRefundedParams{RefundedAt: sql.NullInt64{Int64: s.d.Now().Unix(), Valid: true}, ID: id})
	return err
}

// Run reconciles in the background: applies paid payments that failed to apply, asks the
// adapters about open invoices (a webhook may never come), expires invoices nobody paid
// and gets the host to install the adapters that took over the built-in providers.
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
	s.installMoved(ctx)
	// Paid but not applied payments are tried for as long as they stay so: the buyer
	// paid, and the admin sees why on the Payments page. Pending ones are asked about for
	// the week an invoice can still be paid in.
	paid, err := q.ListPaidPayments(ctx)
	if err != nil {
		s.d.Log.Error("billing: reconcile", "err", err)
		return
	}
	for _, p := range paid {
		if ctx.Err() != nil {
			return
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
	for _, p := range open {
		if ctx.Err() != nil {
			return
		}
		if AddonID(p.Provider) == "" || !p.ExternalID.Valid || !dueCheck(p, now) {
			continue
		}
		_ = s.checkAddon(ctx, p.Provider, p.ExternalID.String)
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
	msg := err.Error()
	if len(msg) > 120 {
		msg = msg[:120]
	}
	return msg
}
