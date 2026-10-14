package referral

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"time"

	"mikan/internal/panel/secure"
	"mikan/internal/panel/settings"
	"mikan/internal/panel/store"
	"mikan/internal/panel/store/db"
)

// Service is the referral programme: who invited whom, what they earned and what of that
// balance is still theirs. Everything money- or term-giving happens on the transaction
// that applied the invitee's payment, so a reward is paid out once or not at all.
type Service struct {
	Store    *store.Store
	Settings *settings.Settings
	Log      *slog.Logger
	Now      func() time.Time
	// Changed tells the nodes about a subscription the days rule extended.
	Changed func()
	// Reward is told what an invitee's payment brought a referrer; nil: nobody is told.
	Reward func(ctx context.Context, r Reward)
}

// codeLen is how long an invite code is: eight of 62 characters is nearly 48 bits, and a
// code only has to be unguessable enough that nobody links a stranger by accident.
const codeLen = 8

// Errors the bot and the admin panel answer with (ref_<code>).
var (
	ErrOff        = errors.New("ref_off")
	ErrCode       = errors.New("ref_code_invalid")
	ErrSelf       = errors.New("ref_self")
	ErrBound      = errors.New("ref_already_bound")
	ErrNoAccount  = errors.New("ref_no_account")
	ErrNoTariff   = errors.New("ref_tariff_required")
	ErrNoSub      = errors.New("ref_no_subscription")
	ErrTooManyOwn = errors.New("ref_too_many_codes")
)

func New(st *store.Store, set *settings.Settings, log *slog.Logger, now func() time.Time) *Service {
	if now == nil {
		now = time.Now
	}
	return &Service{Store: st, Settings: set, Log: log, Now: now}
}

// Load reads the setup through set, which may be a transaction's own queries. A read error
// is returned, never replaced by the defaults: settings changed on top of those and saved
// would wipe the rules.
func Load(ctx context.Context, set *settings.Settings) (Program, error) {
	c, _, err := settings.GetOver(ctx, set, KeyConfig, Default())
	if err != nil {
		return Default(), err
	}
	_ = c.Validate()
	return c, nil
}

// Setup is the programme as it is now. A read error pauses the programme rather than
// guessing: rewards are money.
func (s *Service) Setup(ctx context.Context) Program {
	c, err := Load(ctx, s.Settings)
	if err != nil {
		s.Log.Warn("referral: settings unreadable, the programme is paused", "err", err)
		c.Enabled = false
	}
	return c
}

// On says whether the programme runs: on, and with a rule to earn with.
func (p Program) On() bool { return p.Enabled && len(p.Rules) > 0 }

// Reward is what one applied payment gave a referrer, to tell them about.
type Reward struct {
	Referrer int64 // the Telegram account that earned it
	Referee  int64 // the account that brought it
	Payment  int64
	Kind     string // percent or days
	// Rub is the kopecks that went into the balance; Days the term onto a subscription.
	Rub  int64
	Days int64
	// EveryN is the rule's step: on the N-th qualifying payment, not on this one alone.
	EveryN int64
}

// Account is an account's own line of the programme: its code and what it may spend.
func (s *Service) Account(ctx context.Context, tgID int64) (db.ReferralAccount, bool, error) {
	a, err := s.Store.Q.GetReferralAccount(ctx, tgID)
	if errors.Is(err, sql.ErrNoRows) {
		return db.ReferralAccount{}, false, nil
	}
	return a, err == nil, err
}

// Code is the account's invite code, made on first use. Every account has one, so the bot
// can always show a link; whether it earns anything is the rules' business.
func (s *Service) Code(ctx context.Context, tgID int64) (string, error) {
	a, ok, err := s.Account(ctx, tgID)
	if err != nil {
		return "", err
	}
	if ok {
		return a.Code, nil
	}
	a, err = s.create(ctx, s.Store.Q, tgID)
	return a.Code, err
}

func (s *Service) create(ctx context.Context, q *db.Queries, tgID int64) (db.ReferralAccount, error) {
	now := s.Now().Unix()
	// A code is random, so a collision is possible and only means trying again: the unique
	// index on code turns it into an error rather than a shared invite link.
	for range 5 {
		code := secure.Token(codeLen)
		n, err := q.TakeReferralAccount(ctx, db.TakeReferralAccountParams{TgID: tgID, Code: code, CreatedAt: now})
		if err != nil {
			if store.IsUnique(err) {
				continue
			}
			return db.ReferralAccount{}, err
		}
		if n == 0 {
			return q.GetReferralAccount(ctx, tgID)
		}
		return db.ReferralAccount{TgID: tgID, Code: code, CreatedAt: now, UpdatedAt: now}, nil
	}
	return db.ReferralAccount{}, ErrTooManyOwn
}

// Link ties the Telegram account that followed an invite code to the account that made it.
// A Telegram account belongs to one referrer for good: a second link changes nothing, so a
// code cannot be shared around and one invitee cannot be counted twice.
func (s *Service) Link(ctx context.Context, tgID int64, code string) (db.Referral, error) {
	if !s.Setup(ctx).Enabled {
		return db.Referral{}, ErrOff
	}
	if code == "" {
		return db.Referral{}, ErrCode
	}
	referrer, err := s.Store.Q.FindReferralAccountByCode(ctx, code)
	if errors.Is(err, sql.ErrNoRows) {
		return db.Referral{}, ErrCode
	}
	if err != nil {
		return db.Referral{}, err
	}
	if referrer.TgID == tgID {
		return db.Referral{}, ErrSelf
	}
	var out db.Referral
	err = s.Store.Tx(ctx, func(q *db.Queries) error {
		out = db.Referral{}
		if _, err := q.GetReferralOfReferee(ctx, tgID); err == nil {
			return ErrBound
		} else if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		n, err := q.LinkReferral(ctx, db.LinkReferralParams{ReferrerTgID: referrer.TgID, RefereeTgID: tgID, Code: code, CreatedAt: s.Now().Unix()})
		if err != nil {
			return err
		}
		if n == 0 {
			// Bound by somebody else while this transaction waited.
			return ErrBound
		}
		out, err = q.GetReferralOfReferee(ctx, tgID)
		return err
	})
	if err != nil {
		return db.Referral{}, err
	}
	return out, nil
}

// Announce tells the referrers what they earned. It runs after the transaction the reward
// was committed with, so the bot may take its time and nothing holds the database for it.
func (s *Service) Announce(ctx context.Context, rewards []Reward) {
	if len(rewards) == 0 {
		return
	}
	terms := false
	for _, r := range rewards {
		terms = terms || r.Days > 0
	}
	if terms && s.Changed != nil {
		s.Changed()
	}
	if s.Reward == nil || !s.Setup(ctx).Notify {
		return
	}
	for _, r := range rewards {
		r := r
		s.Reward(ctx, r)
	}
}

// Stats is what the bot's referral screen shows.
type Stats struct {
	Invited int64
	Paid    int64
	Balance int64
	// Progress is the number of qualifying payments under each rule, by rule id.
	Progress map[string]int64
}

// StatsOf is the account's own numbers. A missing account is not an error: everybody has
// an invite code, and only some of them have invited anybody.
func (s *Service) StatsOf(ctx context.Context, tgID int64) (Stats, error) {
	q := s.Store.Q
	var st Stats
	invited, err := q.CountReferralsOf(ctx, tgID)
	if err != nil {
		return st, err
	}
	st.Invited, st.Paid = invited.Invited, invited.Paid
	if bal, err := q.ReferralBalance(ctx, tgID); err == nil {
		st.Balance = bal
	} else if !errors.Is(err, sql.ErrNoRows) {
		return st, err
	}
	rows, err := q.ListReferralCounts(ctx, tgID)
	if err != nil {
		return st, err
	}
	st.Progress = make(map[string]int64, len(rows))
	for _, r := range rows {
		st.Progress[r.RuleID] = r.Paid
	}
	return st, nil
}

// RewardPayment gives the referrer what the applied payment p earned, on q's transaction —
// the one that created or renewed the buyer's subscription. Nothing is paid twice: the
// reward row's payment id is unique, and a payment is applied once.
func (s *Service) RewardPayment(ctx context.Context, q *db.Queries, p db.Payment) ([]Reward, error) {
	cfg, err := Load(ctx, settings.New(q))
	if err != nil {
		return nil, err
	}
	return s.reward(ctx, q, cfg, p)
}

func (s *Service) reward(ctx context.Context, q *db.Queries, cfg Program, p db.Payment) ([]Reward, error) {
	if !cfg.On() {
		return nil, nil
	}
	// Only what a referrer brought in: a subscription bought or renewed. Traffic packages
	// are the owner's own top-up.
	if p.Kind != "new" && p.Kind != "renew" {
		return nil, nil
	}
	if !p.TariffID.Valid {
		return nil, nil
	}
	ref, err := q.GetReferralOfReferee(ctx, p.TgID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	// Nobody earns from themselves, whatever a hand-edited row says.
	if ref.ReferrerTgID == p.TgID {
		return nil, nil
	}
	if _, err := q.MarkReferralPaid(ctx, db.MarkReferralPaidParams{ID: ref.ID,
		FirstPaidAt: sql.NullInt64{Int64: s.Now().Unix(), Valid: true}}); err != nil {
		return nil, err
	}
	rubles := Rub(p.Amount, p.Currency, cfg.StarsRate)
	now := s.Now().Unix()
	payID := sql.NullInt64{Int64: p.ID, Valid: true}
	var out []Reward
	for _, rule := range cfg.Rules {
		if !rule.Enabled || !rule.counts(p.TariffID.Int64) {
			continue
		}
		switch rule.Kind {
		case KindPercent:
			amount := share(rubles, rule.Value)
			if rule.Every > 1 {
				paid, err := q.AddReferralCount(ctx, db.AddReferralCountParams{TgID: ref.ReferrerTgID, RuleID: rule.ID})
				if err != nil {
					return nil, err
				}
				if paid%rule.Every != 0 {
					continue
				}
			}
			credited, err := s.credit(ctx, q, ref.ReferrerTgID, amount, cfg, now)
			if err != nil || credited <= 0 {
				if err != nil {
					return nil, err
				}
				continue
			}
			if _, err := q.CreateReferralReward(ctx, db.CreateReferralRewardParams{ReferrerTgID: ref.ReferrerTgID, ReferralID: ref.ID,
				PaymentID: payID, RuleID: rule.ID, Kind: KindPercent, Amount: credited, Days: 0, EveryN: rule.Every, Currency: Currency, CreatedAt: now}); err != nil {
				return nil, err
			}
			out = append(out, Reward{Referrer: ref.ReferrerTgID, Referee: p.TgID, Payment: p.ID, Kind: KindPercent, Rub: credited, EveryN: rule.Every})
		case KindDays:
			paid, err := q.AddReferralCount(ctx, db.AddReferralCountParams{TgID: ref.ReferrerTgID, RuleID: rule.ID})
			if err != nil {
				return nil, err
			}
			if paid%rule.Every != 0 {
				continue
			}
			// The term goes onto a subscription the referrer has; with none there is
			// nothing to extend, and the payment is still counted towards the rule.
			target, ok, err := s.currentSub(ctx, q, ref.ReferrerTgID)
			if err != nil {
				return nil, err
			}
			if !ok {
				continue
			}
			if err := extend(ctx, q, target, rule.Value, now); err != nil {
				return nil, err
			}
			if _, err := q.CreateReferralReward(ctx, db.CreateReferralRewardParams{ReferrerTgID: ref.ReferrerTgID, ReferralID: ref.ID,
				PaymentID: payID, RuleID: rule.ID, Kind: KindDays, Amount: 0, Days: rule.Value, EveryN: rule.Every, Currency: Currency, CreatedAt: now}); err != nil {
				return nil, err
			}
			out = append(out, Reward{Referrer: ref.ReferrerTgID, Referee: p.TgID, Payment: p.ID, Kind: KindDays, Days: rule.Value, EveryN: rule.Every})
		}
	}
	return out, nil
}

// credit puts amount kopecks into an account's balance, under the admin's cap, and says how
// much really went in: an amount over the cap is cut, an account at the cap gets nothing.
func (s *Service) credit(ctx context.Context, q *db.Queries, tgID, amount int64, cfg Program, now int64) (int64, error) {
	if amount <= 0 {
		return 0, nil
	}
	// The referrer's account is guaranteed by the foreign key on referrals.referrer_tg_id:
	// a row that is not there is a database that is not the one this code expects, and the
	// payment waits for the next attempt rather than quietly paying nothing.
	bal, err := q.ReferralBalance(ctx, tgID)
	if err != nil {
		return 0, err
	}
	if cfg.BalanceMax > 0 && bal+amount > cfg.BalanceMax {
		amount = max(cfg.BalanceMax-bal, 0)
	}
	if amount <= 0 {
		return 0, nil
	}
	n, err := q.AddReferralBalance(ctx, db.AddReferralBalanceParams{TgID: tgID, Amount: amount, UpdatedAt: now})
	if err != nil || n != 1 {
		return 0, err
	}
	return amount, nil
}

// currentSub is the subscription the referrer is shown in their chat: the current one,
// else the first they linked.
func (s *Service) currentSub(ctx context.Context, q *db.Queries, tgID int64) (db.User, bool, error) {
	list, err := q.ListTgLinksOf(ctx, tgID)
	if err != nil {
		return db.User{}, false, err
	}
	if len(list) == 0 {
		return db.User{}, false, nil
	}
	if ch, err := q.GetTgChat(ctx, tgID); err == nil {
		for _, u := range list {
			if u.ID == ch.Current {
				return u, true, nil
			}
		}
	}
	return list[0], true, nil
}

// extend adds days to a subscription from its own end, or from now when that has passed,
// and turns it on: what a rule promised is what the account gets to use.
func extend(ctx context.Context, q *db.Queries, u db.User, days, now int64) error {
	base := now
	if u.ExpiresAt.Valid && u.ExpiresAt.Int64 > now {
		base = u.ExpiresAt.Int64
	}
	_, err := q.UpdateUser(ctx, db.UpdateUserParams{Name: u.Name, Contact: u.Contact, Note: u.Note, Tags: u.Tags, Status: "active",
		TariffID: u.TariffID, TrafficLimit: u.TrafficLimit, DeviceLimit: u.DeviceLimit, ResetStrategy: u.ResetStrategy,
		PeriodDays: u.PeriodDays, PeriodStart: u.PeriodStart,
		ExpiresAt: sql.NullInt64{Int64: base + days*int64(24*time.Hour/time.Second), Valid: true},
		Inbounds:  u.Inbounds, BillingDay: u.BillingDay, UpdatedAt: now, ID: u.ID})
	return err
}

// Reserve holds what the balance pays of an invoice and says how much of the price that is,
// in the invoice's own currency. Nothing is held when the programme is off, when the
// balance cannot pay any of it, or when paying all of it would leave the provider nothing
// to take: an invoice is a payment, and a payment of zero has no provider behind it.
func (s *Service) Reserve(ctx context.Context, q *db.Queries, tgID, paymentID, amount int64, currency string, cfg Program) (int64, error) {
	if !cfg.Spend || amount <= 1 {
		return 0, nil
	}
	bal, err := q.ReferralBalance(ctx, tgID)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	off := min(FromRub(bal, currency, cfg.StarsRate), amount-1)
	if off <= 0 {
		return 0, nil
	}
	kopecks := Rub(off, currency, cfg.StarsRate)
	if kopecks <= 0 {
		return 0, nil
	}
	n, err := q.TakeReferralBalance(ctx, db.TakeReferralBalanceParams{TgID: tgID, Amount: kopecks, UpdatedAt: s.Now().Unix()})
	if err != nil {
		return 0, err
	}
	if n != 1 {
		return 0, nil
	}
	if _, err := q.CreateReferralSpend(ctx, db.CreateReferralSpendParams{PaymentID: sql.NullInt64{Int64: paymentID, Valid: true},
		TgID: tgID, Amount: kopecks, CreatedAt: s.Now().Unix()}); err != nil {
		return 0, err
	}
	return off, nil
}

// Off is what the balance would pay of an invoice, in the invoice's currency, without
// touching it: the price an open invoice is compared with.
func (s *Service) Off(ctx context.Context, tgID, amount int64, currency string, cfg Program) (int64, error) {
	if !cfg.Spend || amount <= 1 {
		return 0, nil
	}
	bal, err := s.Store.Q.ReferralBalance(ctx, tgID)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	return min(FromRub(bal, currency, cfg.StarsRate), amount-1), nil
}

// Held says what the balance is already holding for an invoice, in the invoice's currency,
// and whether that hold still stands. An open invoice made a minute ago is shown again
// with the discount it was made with, rather than taking the balance a second time.
func (s *Service) Held(ctx context.Context, q *db.Queries, paymentID int64, currency string, cfg Program) (int64, bool, error) {
	sp, err := q.GetReferralSpendByPayment(ctx, sql.NullInt64{Int64: paymentID, Valid: paymentID != 0})
	if errors.Is(err, sql.ErrNoRows) || sp.RevertedAt.Valid {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	return FromRub(sp.Amount, currency, cfg.StarsRate), true, nil
}

// TakeBack gives back what a payment held or earned for a referrer, on q's transaction. A
// hold on an invoice nobody paid is money of its owner again; what a percent rule already
// put into a balance comes out of that balance, so a spent one simply stays spent. Days
// already added to a subscription are left: the subscription may well have been used.
func (s *Service) TakeBack(ctx context.Context, q *db.Queries, paymentID int64) (int64, error) {
	now := s.Now().Unix()
	var back int64
	if n, err := q.RevertReferralSpend(ctx, db.RevertReferralSpendParams{RevertedAt: sql.NullInt64{Int64: now, Valid: true},
		PaymentID: sql.NullInt64{Int64: paymentID, Valid: paymentID != 0}}); err != nil {
		return 0, err
	} else if n == 1 {
		if sp, err := q.GetReferralSpendByPayment(ctx, sql.NullInt64{Int64: paymentID, Valid: true}); err == nil {
			if _, err := q.AddReferralBalance(ctx, db.AddReferralBalanceParams{TgID: sp.TgID, Amount: sp.Amount, UpdatedAt: now}); err != nil {
				return 0, err
			}
			back += sp.Amount
		}
	}
	rows, err := q.ListReferralRewardsByPayment(ctx, sql.NullInt64{Int64: paymentID, Valid: paymentID != 0})
	if err != nil {
		return back, err
	}
	for _, r := range rows {
		if r.Kind != KindPercent || r.RevertedAt.Valid {
			continue
		}
		n, err := q.RevertReferralReward(ctx, db.RevertReferralRewardParams{ID: r.ID, RevertedAt: sql.NullInt64{Int64: now, Valid: true}})
		if err != nil {
			return back, err
		}
		if n == 1 {
			// A balance already spent on something else does not go below zero; what is
			// left of the reward is simply lost to the panel.
			if _, err := q.AddReferralBalance(ctx, db.AddReferralBalanceParams{TgID: r.ReferrerTgID, Amount: -r.Amount, UpdatedAt: now}); err != nil {
				return back, err
			}
			back += r.Amount
		}
	}
	return back, nil
}

// ReleaseClosed gives back the holds of invoices nobody paid and will not: it runs with
// the billing's reconcile pass, right after the pending ones have been expired.
func (s *Service) ReleaseClosed(ctx context.Context) error {
	rows, err := s.Store.Q.ListSpentClosedPayments(ctx)
	if err != nil {
		return err
	}
	for _, r := range rows {
		if err := s.Store.Tx(ctx, func(q *db.Queries) error {
			if _, err := s.TakeBack(ctx, q, r.PaymentID.Int64); err != nil {
				return err
			}
			_, err := q.ReleaseReferralSpend(ctx, db.ReleaseReferralSpendParams{ID: r.ID, RevertedAt: sql.NullInt64{Int64: s.Now().Unix(), Valid: true}})
			return err
		}); err != nil {
			return err
		}
	}
	return nil
}

// AdminStats is what the admin's page shows above the rules.
type AdminStats struct {
	Invited  int64 `json:"invited"`
	Paid     int64 `json:"paid"`
	Accounts int64 `json:"accounts"`
	// Owed is every balance in the programme: what it would cost to pay them all out.
	Owed       int64 `json:"owed"`
	Earned     int64 `json:"earned"`
	EarnedDays int64 `json:"earned_days"`
	Spent      int64 `json:"spent"`
}

// View is the setup with the numbers behind it.
func (s *Service) View(ctx context.Context) (Program, AdminStats, error) {
	q := s.Store.Q
	cfg := s.Setup(ctx)
	var out AdminStats
	refs, err := q.CountReferrals(ctx)
	if err != nil {
		return cfg, out, err
	}
	accs, err := q.CountReferralAccounts(ctx)
	if err != nil {
		return cfg, out, err
	}
	rw, err := q.CountReferralRewards(ctx)
	if err != nil {
		return cfg, out, err
	}
	sp, err := q.ReferralSpendTotals(ctx)
	if err != nil {
		return cfg, out, err
	}
	out = AdminStats{Invited: refs.Total, Paid: refs.Paid, Accounts: accs.Accounts, Owed: accs.Owed,
		Earned: rw.Rub, EarnedDays: rw.Days, Spent: sp.Total}
	return cfg, out, nil
}
