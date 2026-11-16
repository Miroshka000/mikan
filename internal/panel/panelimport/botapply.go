package panelimport

import (
	"context"
	"database/sql"
	"math"
	"slices"
	"strconv"
	"time"

	"mikan/internal/panel/model"
	"mikan/internal/panel/store"
	"mikan/internal/panel/store/db"
)

// BotPreview is what a bot's import would do, without doing it.
type BotPreview struct {
	Accounts  int  `json:"accounts" doc:"Telegram-аккаунты бота"`
	Matched   int  `json:"matched" doc:"Аккаунты, чьи подписки нашлись среди пользователей mikan"`
	Links     int  `json:"links" doc:"Подписки, которые получат аккаунт-владельца"`
	Owned     List `json:"owned" doc:"Подписки, у которых уже другой владелец: он остаётся"`
	Unmatched List `json:"unmatched" doc:"Аккаунты с подпиской в боте, которой нет в mikan: сначала импортируйте пользователей Remnawave"`
	Balances  int  `json:"balances" doc:"Аккаунты, чей баланс будет перенесён"`
	// BalanceTotal is in minor units of Currency.
	BalanceTotal int64  `json:"balance_total" doc:"Сколько всего, в копейках (сотых) валюты баланса"`
	Currency     string `json:"currency" doc:"Валюта баланса mikan; пусто — не выбрана, и балансы не переносятся"`
	Carried      int    `json:"carried" doc:"Аккаунты, чей баланс уже перенесён прежде: второй раз он не добавится"`
	Payments     int    `json:"payments" doc:"Завершённые транзакции бота"`
}

// BotReport is what a bot's import did.
type BotReport struct {
	Accounts     int    `json:"accounts" doc:"Аккаунты, у которых что-то перенесено"`
	Links        int    `json:"links" doc:"Подписки, получившие аккаунт-владельца"`
	Owned        List   `json:"owned" doc:"Подписки с другим владельцем: оставлены как были"`
	Balances     int    `json:"balances" doc:"Аккаунты, чей баланс перенесён"`
	BalanceTotal int64  `json:"balance_total"`
	Currency     string `json:"currency"`
	Payments     int    `json:"payments" doc:"Транзакции, добавленные в историю (уже перенесённые не повторяются)"`
	Failed       List   `json:"failed" doc:"Аккаунты, которые не удалось сохранить"`
}

// carry is a ruble balance (kopecks) in the minor units of mikan's balance, rounded to the
// nearest; false while the balance has no currency to keep it in.
func carry(c model.BalanceConfig, kopecks int64) (int64, bool) {
	switch {
	case !slices.Contains(model.BalanceCurrencies, c.Currency):
		return 0, false
	case c.Currency == "RUB":
		return kopecks, true
	case c.Rate <= 0:
		return 0, false
	}
	return int64(math.Round(float64(kopecks) / c.Rate)), true
}

// botPlan is how the bot's accounts meet mikan's users.
type botPlan struct {
	users   map[int64][]int64 // account → the users it gets as owner
	owned   map[int64][]int64 // account → its users that another account owns
	same    map[int64]int     // account → its users it owns already
	names   map[int64]string
	carried map[int64]bool
}

// planBot finds each account's users: by its links' short UUIDs among the old links, and
// failing those by the contact tg:<id> the Remnawave import wrote. A user goes to one
// account only, the first that names it.
func planBot(ctx context.Context, q *db.Queries, bot Bot) (botPlan, error) {
	p := botPlan{users: map[int64][]int64{}, owned: map[int64][]int64{}, same: map[int64]int{}, names: map[int64]string{}, carried: map[int64]bool{}}
	var tokens, contacts []string
	for _, a := range bot.Accounts {
		tokens = append(tokens, a.Tokens...)
		contacts = append(contacts, contactOf(a.TgID))
	}
	byToken := map[string]int64{}
	if err := chunked(tokens, func(c []string) error {
		rows, err := q.UsersOfLegacyTokens(ctx, c)
		for _, r := range rows {
			byToken[r.Token] = r.UserID
		}
		return err
	}); err != nil {
		return p, err
	}
	byContact := map[string][]int64{}
	if err := chunked(contacts, func(c []string) error {
		rows, err := q.UsersOfContacts(ctx, c)
		for _, r := range rows {
			byContact[r.Contact] = append(byContact[r.Contact], r.ID)
		}
		return err
	}); err != nil {
		return p, err
	}
	taken := map[int64]bool{}
	found := map[int64][]int64{}
	var all []int64
	for _, a := range bot.Accounts {
		var ids []int64
		for _, t := range a.Tokens {
			if id, ok := byToken[t]; ok {
				ids = append(ids, id)
			}
		}
		if len(ids) == 0 {
			ids = byContact[contactOf(a.TgID)]
		}
		for _, id := range ids {
			if !taken[id] {
				taken[id] = true
				found[a.TgID] = append(found[a.TgID], id)
				all = append(all, id)
			}
		}
	}
	owner := map[int64]int64{}
	if err := chunked(all, func(c []int64) error {
		rows, err := q.TgLinksOfUsers(ctx, c)
		for _, r := range rows {
			owner[r.UserID] = r.TgID
		}
		if err != nil {
			return err
		}
		names, err := q.UserNamesOf(ctx, c)
		for _, n := range names {
			p.names[n.ID] = n.Name
		}
		return err
	}); err != nil {
		return p, err
	}
	for tg, ids := range found {
		for _, id := range ids {
			switch o, ok := owner[id]; {
			case !ok:
				p.users[tg] = append(p.users[tg], id)
			case o == tg:
				p.same[tg]++
			default:
				p.owned[tg] = append(p.owned[tg], id)
			}
		}
	}
	done, err := q.ImportedAccounts(ctx, string(Bedolaga))
	if err != nil {
		return p, err
	}
	for _, tg := range done {
		p.carried[tg] = true
	}
	return p, nil
}

func contactOf(tg int64) string { return "tg:" + strconv.FormatInt(tg, 10) }

func chunked[T any](list []T, fn func([]T) error) error {
	for start := 0; start < len(list); start += 1000 {
		if err := fn(list[start:min(start+1000, len(list))]); err != nil {
			return err
		}
	}
	return nil
}

func accountName(a BotAccount) string {
	if a.Username != "" {
		return "@" + a.Username
	}
	return strconv.FormatInt(a.TgID, 10)
}

// matched: the account found some of mikan's users.
func (p botPlan) matched(tg int64) bool {
	return len(p.users[tg])+len(p.owned[tg])+p.same[tg] > 0
}

// CheckBot counts what ApplyBot would do.
func CheckBot(ctx context.Context, q *db.Queries, bc model.BalanceConfig, bot Bot) (BotPreview, error) {
	r := BotPreview{Accounts: len(bot.Accounts), Owned: newList(), Unmatched: newList(), Payments: len(bot.Payments)}
	p, err := planBot(ctx, q, bot)
	if err != nil {
		return r, err
	}
	_, canCarry := carry(bc, 0)
	if canCarry {
		r.Currency = bc.Currency
	}
	for _, a := range bot.Accounts {
		if p.matched(a.TgID) {
			r.Matched++
		} else if len(a.Tokens) > 0 {
			r.Unmatched.add(accountName(a))
		}
		r.Links += len(p.users[a.TgID])
		for _, id := range p.owned[a.TgID] {
			r.Owned.add(p.names[id])
		}
		if a.Balance > 0 {
			switch amount, _ := carry(bc, a.Balance); {
			case p.carried[a.TgID]:
				r.Carried++
			case canCarry && amount > 0:
				r.Balances++
				r.BalanceTotal += amount
			}
		}
	}
	return r, nil
}

// botNote is what the account sees against the carried balance in its history.
const botNote = "Bedolaga"

// ApplyBot brings the bot's accounts over: each in one transaction (its chat, links, client
// mark and balance), then the history. adminID is who ran it, for the balance's audit.
// progress, when not nil, is told after each account.
func ApplyBot(ctx context.Context, st *store.Store, now time.Time, bc model.BalanceConfig, adminID int64, bot Bot, progress func(done int)) (BotReport, error) {
	r := BotReport{Owned: newList(), Failed: newList()}
	p, err := planBot(ctx, st.Q, bot)
	if err != nil {
		return r, err
	}
	if _, ok := carry(bc, 0); ok {
		r.Currency = bc.Currency
	}
	at := now.Unix()
	for i, a := range bot.Accounts {
		if err := ctx.Err(); err != nil {
			return r, err
		}
		amount, canCarry := carry(bc, a.Balance)
		canCarry = canCarry && amount > 0 && !p.carried[a.TgID]
		client := p.matched(a.TgID) || len(a.Tokens) > 0
		if !client && !canCarry {
			if progress != nil {
				progress(i + 1)
			}
			continue
		}
		var links int
		var carried bool
		err := st.Tx(ctx, func(q *db.Queries) error {
			links, carried = 0, false
			if err := q.AddTgChatIfNew(ctx, db.AddTgChatIfNewParams{TgID: a.TgID, Username: a.Username, FirstName: a.FirstName, CreatedAt: at}); err != nil {
				return err
			}
			for _, id := range p.users[a.TgID] {
				n, err := q.LinkTgIfFree(ctx, db.LinkTgIfFreeParams{UserID: id, TgID: a.TgID, CreatedAt: at})
				if err != nil {
					return err
				}
				links += int(n)
			}
			if client {
				since := at
				if !a.Created.IsZero() && a.Created.Unix() < at {
					since = a.Created.Unix()
				}
				if err := q.AddTgClient(ctx, db.AddTgClientParams{TgID: a.TgID, Since: since}); err != nil {
					return err
				}
			}
			if !canCarry {
				return nil
			}
			n, err := q.MarkImportedAccount(ctx, db.MarkImportedAccountParams{Source: string(Bedolaga), TgID: a.TgID, Amount: amount, At: at})
			if err != nil || n == 0 {
				return err // 0: another run carried it meanwhile
			}
			if _, err := q.CreditBalance(ctx, db.CreditBalanceParams{TgID: a.TgID, Amount: amount, UpdatedAt: at}); err != nil {
				return err
			}
			carried = true
			return q.AddBalanceOp(ctx, db.AddBalanceOpParams{TgID: a.TgID, Amount: amount, Kind: model.OpAdjust,
				AdminID: sql.NullInt64{Int64: adminID, Valid: adminID != 0}, Note: botNote, CreatedAt: at})
		})
		if err != nil {
			r.Failed.add(accountName(a) + ": " + failure(err))
		} else {
			r.Accounts++
			r.Links += links
			for _, id := range p.owned[a.TgID] {
				r.Owned.add(p.names[id])
			}
			if carried {
				r.Balances++
				r.BalanceTotal += amount
			}
		}
		if progress != nil {
			progress(i + 1)
		}
	}
	err = chunked(bot.Payments, func(c []BotPayment) error {
		added := 0
		err := st.Tx(ctx, func(q *db.Queries) error {
			added = 0
			for _, x := range c {
				created := at
				if !x.At.IsZero() {
					created = x.At.Unix()
				}
				n, err := q.AddImportedPayment(ctx, db.AddImportedPaymentParams{Source: string(Bedolaga), ExternalID: x.ID, TgID: x.TgID, Kind: x.Kind,
					Amount: x.Amount, Currency: "RUB", Method: x.Method, Description: x.Description, CreatedAt: created})
				if err != nil {
					return err
				}
				added += int(n)
			}
			return nil
		})
		if err == nil {
			r.Payments += added
		}
		return err
	})
	return r, err
}
