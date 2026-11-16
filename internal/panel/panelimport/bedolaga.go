package panelimport

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Bedolaga (BEDOLAGA-DEV/remnawave-bedolaga-telegram-bot) sells Remnawave subscriptions in
// Telegram. Its admin API (WEB_API_ENABLED, an X-API-Key) lists the accounts with their
// subscriptions and the transactions: GET /users and /transactions, limit and offset, at
// most 200 a page, {items, total}. Amounts are kopecks.
//
// A bot account comes over on top of the users imported from its Remnawave: a subscription's
// link carries the Remnawave short UUID, which that import filed as an old link; failing
// that, the user whose contact is the account's Telegram id (tg:…). What comes over:
//
//   - the Telegram account, linked to those users where they have no owner yet, and counted
//     as a client since the bot met it;
//   - its balance, once (a second run adds nothing), in the balance's currency;
//   - its completed transactions, as a history apart from mikan's payments.

// bedolagaPage is the page size; a variable for the tests.
var bedolagaPage = 200

// Bot is a sales bot's data.
type Bot struct {
	Accounts []BotAccount
	Payments []BotPayment
}

// BotAccount is a Telegram account of the bot.
type BotAccount struct {
	TgID      int64
	Username  string
	FirstName string
	Balance   int64 // kopecks
	Created   time.Time
	// Tokens are the Remnawave short UUIDs of its subscriptions' links.
	Tokens []string
}

// BotPayment is a completed transaction of an account.
type BotPayment struct {
	ID          string
	TgID        int64
	Kind        string // deposit, subscription_payment, refund, …
	Amount      int64  // kopecks
	Method      string
	Description string
	At          time.Time
}

type bedolagaUser struct {
	ID            int64   `json:"id"`
	TelegramID    *int64  `json:"telegram_id"`
	Username      *string `json:"username"`
	FirstName     *string `json:"first_name"`
	Status        string  `json:"status"`
	BalanceKopeks int64   `json:"balance_kopeks"`
	CreatedAt     string  `json:"created_at"`
	Subscription  *struct {
		SubscriptionURL *string `json:"subscription_url"`
	} `json:"subscription"`
	Subscriptions []struct {
		SubscriptionURL *string `json:"subscription_url"`
	} `json:"subscriptions"`
}

type bedolagaTransaction struct {
	ID           int64   `json:"id"`
	UserID       int64   `json:"user_id"`
	Type         string  `json:"type"`
	AmountKopeks int64   `json:"amount_kopeks"`
	Description  *string `json:"description"`
	Method       *string `json:"payment_method"`
	IsCompleted  bool    `json:"is_completed"`
	CreatedAt    string  `json:"created_at"`
}

// botKind is what a transaction's type has to look like to be kept.
var botKind = regexp.MustCompile(`^[a-z_]{1,32}$`)

// FetchBot reads a sales bot, within FetchTimeout.
func FetchBot(ctx context.Context, hc *http.Client, src Source) (Bot, error) {
	if src.Kind != Bedolaga {
		return Bot{}, fmt.Errorf("unknown bot %q", src.Kind)
	}
	if src.Token == "" {
		return Bot{}, ErrAuth
	}
	src.URL = strings.TrimRight(strings.TrimSpace(src.URL), "/")
	hc, err := checkedClient(hc, src.URL)
	if err != nil {
		return Bot{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, FetchTimeout)
	defer cancel()
	var bot Bot
	tg := map[int64]int64{} // the bot's user id → Telegram id
	err = bedolagaPages(ctx, hc, src, "/users", func(raw []bedolagaUser) error {
		for _, u := range raw {
			if u.TelegramID == nil || *u.TelegramID <= 0 || u.Status == "deleted" {
				continue // an e-mail account, or one the bot deleted: nothing for Telegram here
			}
			a := BotAccount{TgID: *u.TelegramID, Balance: max(0, u.BalanceKopeks)}
			if u.Username != nil {
				a.Username = cut(strings.TrimPrefix(strings.TrimSpace(*u.Username), "@"), 64)
			}
			if u.FirstName != nil {
				a.FirstName = cut(strings.TrimSpace(*u.FirstName), 128)
			}
			a.Created = parseBotTime(u.CreatedAt)
			seen := map[string]bool{}
			add := func(link *string) {
				if t := linkToken(link); t != "" && !seen[t] {
					seen[t] = true
					a.Tokens = append(a.Tokens, t)
				}
			}
			if u.Subscription != nil {
				add(u.Subscription.SubscriptionURL)
			}
			for _, s := range u.Subscriptions {
				add(s.SubscriptionURL)
			}
			tg[u.ID] = a.TgID
			bot.Accounts = append(bot.Accounts, a)
		}
		if len(bot.Accounts) > maxUsers {
			return fmt.Errorf("%w: more than %d accounts", ErrAnswer, maxUsers)
		}
		return nil
	})
	if err != nil {
		return Bot{}, err
	}
	err = bedolagaPages(ctx, hc, src, "/transactions", func(raw []bedolagaTransaction) error {
		for _, x := range raw {
			id, ok := tg[x.UserID]
			if !ok || !x.IsCompleted || !botKind.MatchString(x.Type) {
				continue
			}
			p := BotPayment{ID: strconv.FormatInt(x.ID, 10), TgID: id, Kind: x.Type, Amount: x.AmountKopeks, At: parseBotTime(x.CreatedAt)}
			if x.Method != nil {
				p.Method = cut(strings.TrimSpace(*x.Method), 50)
			}
			if x.Description != nil {
				p.Description = cut(strings.TrimSpace(*x.Description), 500)
			}
			bot.Payments = append(bot.Payments, p)
		}
		if len(bot.Payments) > 10*maxUsers {
			return fmt.Errorf("%w: more than %d transactions", ErrAnswer, 10*maxUsers)
		}
		return nil
	})
	if err != nil {
		return Bot{}, err
	}
	return bot, nil
}

// bedolagaPages reads every page of a list: {items, total}, limit and offset.
func bedolagaPages[T any](ctx context.Context, hc *http.Client, src Source, what string, take func([]T) error) error {
	for page, offset := 0, 0; ; page, offset = page+1, offset+bedolagaPage {
		if page >= 10*maxPages {
			return fmt.Errorf("%w: more than %d pages", ErrAnswer, 10*maxPages)
		}
		q := url.Values{"limit": {strconv.Itoa(bedolagaPage)}, "offset": {strconv.Itoa(offset)}}
		if what == "/transactions" {
			q.Set("is_completed", "true")
		}
		req, err := http.NewRequest(http.MethodGet, src.URL+what+"?"+q.Encode(), nil)
		if err != nil {
			return err
		}
		req.Header.Set("X-API-Key", src.Token)
		var resp struct {
			Items []T `json:"items"`
			Total int `json:"total"`
		}
		if err := getJSON(ctx, hc, req, &resp); err != nil {
			return err
		}
		if err := take(resp.Items); err != nil {
			return err
		}
		if len(resp.Items) < bedolagaPage || offset+len(resp.Items) >= resp.Total {
			if offset+len(resp.Items) < resp.Total {
				return fmt.Errorf("%w: %s gave %d of %d", ErrIncomplete, what, offset+len(resp.Items), resp.Total)
			}
			return nil
		}
	}
}

// linkToken is the Remnawave short UUID a subscription's link ends in, if it looks like one.
func linkToken(link *string) string {
	if link == nil {
		return ""
	}
	u, err := url.Parse(strings.TrimSpace(*link))
	if err != nil || u.Host == "" {
		return ""
	}
	t := path.Base(strings.TrimRight(u.Path, "/"))
	if !strongToken.MatchString(t) {
		return ""
	}
	return t
}

// parseBotTime reads the bot's datetimes: ISO 8601, with or without a zone (then UTC).
func parseBotTime(s string) time.Time {
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02T15:04:05.999999999", "2006-01-02 15:04:05.999999999"} {
		if t, err := time.Parse(layout, strings.TrimSpace(s)); err == nil {
			return t.UTC()
		}
	}
	return time.Time{}
}
