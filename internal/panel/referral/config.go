// Package referral is the referral programme of the Telegram bot: an account invites
// others by its own link, a Telegram account belongs to one referrer for good, and what a
// referrer earns is an internal balance in kopecks and days onto their subscription.
//
// The rules are the admin's, one per line, each naming the tariffs it counts — that is how
// two tiers of subscriptions (say 70 ₽ and 150 ₽) pay differently without anything here
// knowing their prices.
package referral

import (
	"errors"
	"slices"
	"strings"
)

// KeyConfig is the settings key the whole setup is stored under, as one JSON value.
const KeyConfig = "ref_config"

// Currency is what a balance is in, always: kopecks. Whatever the invitee paid with, the
// percent is taken of what the tariff costs in rubles, and Stars are converted with the
// admin's rate. One currency means one balance and no exchange between them.
const Currency = "RUB"

// The kinds of rule. A percent rule credits the balance; a days rule extends the
// referrer's own subscription.
const (
	KindPercent = "percent"
	KindDays    = "days"
)

// Bounds the admin's setup is held to. A percent over 100 pays more than the invitee
// spent, and days past a year are not a gift but a mistake.
const (
	MaxPercent = 100
	MaxDays    = 3650
	maxRules   = 20
)

// Program is the whole setup the admin writes on the Telegram page: what a referrer
// earns, from which tariffs, and whether the balance pays for anything.
type Program struct {
	Enabled bool `json:"enabled" doc:"Реферальная программа включена"`
	// StarsRate: kopecks one Telegram Star counts as. 0: a payment in Stars earns nothing,
	// and the balance is never spent on one.
	StarsRate int64 `json:"stars_rate" minimum:"0" doc:"Сколько копеек стоит одна звёздочка Telegram; 0 — оплата звёздами не участвует"`
	// Rules is what a referrer earns from one applied payment of an invitee. Every enabled
	// rule naming that tariff pays, so a percent and a threshold can both be on.
	Rules []Rule `json:"rules"`
	// BalanceMax caps what one account may hold, kopecks; 0: no cap.
	BalanceMax int64 `json:"balance_max" minimum:"0" doc:"Потолок баланса одного аккаунта в копейках; 0 — без потолка"`
	// Spend lets the balance pay for part of an invoice on its own: the buyer pays the
	// rest with Stars or a card. Off: the balance only ever grows.
	Spend bool `json:"spend" doc:"Баланс списывается за счёт при оплате подписки"`
	// Notify tells the referrer what their invitee just brought them.
	Notify bool `json:"notify" doc:"Сообщать пригласившему о начислении"`
}

// Rule is one line of the programme: what a referrer earns, from which tariffs.
type Rule struct {
	ID   string `json:"id" doc:"Постоянный id правила"`
	Name string `json:"name" maxLength:"80" doc:"Название для панели"`
	// Kind: percent (a share into the balance) or days (a term onto the subscription).
	Kind string `json:"kind" enum:"percent,days"`
	// Value: percent — the share of the ruble price, 1 to 100; days — the term in days.
	Value int64 `json:"value" minimum:"1"`
	// Every: percent — 0 or 1 pays on every payment; N > 1 pays on every N-th payment of
	// that rule. days — how many payments earn the term, so 3 with Value 30 is the
	// "three invitees bought, a month for free" kind of rule.
	Every int64 `json:"every" minimum:"0" doc:"percent: 1 — с каждой оплаты, N — с каждой N-й; days: сколько оплат дают бонус"`
	// TariffIDs are the tariffs whose payments count; empty: any.
	TariffIDs []int64 `json:"tariff_ids"`
	Enabled   bool    `json:"enabled"`
}

// Default is a programme that is off until the admin writes rules.
func Default() Program { return Program{Notify: true, Rules: []Rule{}} }

// Errors the Telegram page translates (ref_<code>).
var (
	ErrPercent    = errors.New("ref_percent")
	ErrDays       = errors.New("ref_days")
	ErrEvery      = errors.New("ref_every")
	ErrRules      = errors.New("ref_rules")
	ErrTariffIDs  = errors.New("ref_tariff_ids")
	ErrBalanceMax = errors.New("ref_balance_max")
)

// Validate fills in what may be left out and refuses the rest.
func (p *Program) Validate() error {
	if p.StarsRate < 0 {
		return ErrPercent
	}
	if p.BalanceMax < 0 {
		return ErrBalanceMax
	}
	if len(p.Rules) > maxRules {
		return ErrRules
	}
	seen := map[string]bool{}
	for i := range p.Rules {
		r := &p.Rules[i]
		r.Name = strings.TrimSpace(r.Name)
		r.ID = strings.TrimSpace(r.ID)
		if r.ID == "" {
			r.ID = "r" + itoa(i+1)
		}
		if seen[r.ID] || len(r.ID) > 24 {
			return ErrRules
		}
		seen[r.ID] = true
		switch r.Kind {
		case KindPercent:
			if r.Value < 1 || r.Value > MaxPercent {
				return ErrPercent
			}
			// Every defaults to "each payment".
			if r.Every <= 0 {
				r.Every = 1
			}
			if r.Every > 1000 {
				return ErrEvery
			}
		case KindDays:
			if r.Value < 1 || r.Value > MaxDays {
				return ErrDays
			}
			if r.Every < 1 {
				r.Every = 1
			}
			if r.Every > 10000 {
				return ErrEvery
			}
		default:
			return ErrRules
		}
		for _, id := range r.TariffIDs {
			if id <= 0 {
				return ErrTariffIDs
			}
		}
		slices.Sort(r.TariffIDs)
		r.TariffIDs = slices.Compact(r.TariffIDs)
	}
	return nil
}

// counts tells whether this rule counts a payment of that tariff; no tariff named: any.
func (r Rule) counts(tariffID int64) bool {
	return len(r.TariffIDs) == 0 || slices.Contains(r.TariffIDs, tariffID)
}

// Rub is what amount of currency is worth in kopecks: itself for rubles, and Stars at the
// admin's rate. Anything else, or no rate, is worth nothing to the programme.
func Rub(amount int64, currency string, starsRate int64) int64 {
	switch currency {
	case "RUB":
		return amount
	case "XTR":
		if starsRate <= 0 || amount <= 0 {
			return 0
		}
		return amount * starsRate
	}
	return 0
}

// FromRub is what kopecks are worth in a currency a buyer pays with: kopecks for rubles,
// Stars at the admin's rate. 0 when the currency cannot be paid with from the balance.
func FromRub(kopecks int64, currency string, starsRate int64) int64 {
	switch currency {
	case "RUB":
		return kopecks
	case "XTR":
		if starsRate <= 0 || kopecks <= 0 {
			return 0
		}
		return kopecks / starsRate
	}
	return 0
}

// share is percent of amount without losing anything to integer division, the way
// promo.discountAmount does it.
func share(amount, percent int64) int64 {
	if amount <= 0 || percent <= 0 {
		return 0
	}
	if percent > MaxPercent {
		percent = MaxPercent
	}
	v := (amount/100)*percent + (amount%100)*percent/100
	if v < 0 {
		return 0
	}
	return v
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for ; n > 0; n /= 10 {
		b = append([]byte{byte('0' + n%10)}, b...)
	}
	return string(b)
}
