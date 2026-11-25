package panelimport

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"testing"
	"time"

	"mikan/internal/panel/domain"
	"mikan/internal/panel/model"
	"mikan/internal/panel/store/db"
)

const (
	tokA = "AAAAaaaaBBBBbbbb01"
	tokC = "CCCCccccDDDDdddd03"
	tokX = "XXXXxxxxYYYYyyyy09" // not in mikan
)

func bedolagaServer(t *testing.T, users, txs []map[string]any) string {
	srv := serve(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-API-Key") != "bot-key" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		off, _ := strconv.Atoi(r.URL.Query().Get("offset"))
		lim, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		var all []map[string]any
		switch r.URL.Path {
		case "/users":
			all = users
		case "/transactions":
			if r.URL.Query().Get("is_completed") != "true" {
				t.Error("transactions asked without is_completed")
			}
			all = txs
		default:
			w.WriteHeader(http.StatusNotFound)
			return
		}
		end := min(off+lim, len(all))
		_ = json.NewEncoder(w).Encode(map[string]any{"items": all[min(off, end):end], "total": len(all), "limit": lim, "offset": off})
	})
	return srv.URL
}

func sub(token string) map[string]any {
	return map[string]any{"subscription_url": "https://sub.example.com/" + token}
}

func bedolagaFixture() (users, txs []map[string]any) {
	users = []map[string]any{
		{"id": 1, "telegram_id": 111, "username": "alice", "first_name": "Alice", "status": "active", "balance_kopeks": 15000, "created_at": "2025-03-01T10:00:00",
			"subscription": sub(tokA), "subscriptions": []any{sub(tokA), sub(tokC)}},
		{"id": 2, "telegram_id": 222, "username": nil, "status": "active", "balance_kopeks": 0, "created_at": "2025-04-01T10:00:00Z", "subscriptions": []any{}},
		{"id": 3, "telegram_id": 333, "username": "carol", "status": "active", "balance_kopeks": 500, "subscriptions": []any{sub(tokX)}},
		{"id": 4, "telegram_id": nil, "email": "e@example.com", "status": "active", "balance_kopeks": 900},
		{"id": 5, "telegram_id": 555, "status": "deleted", "balance_kopeks": 700},
	}
	txs = []map[string]any{
		{"id": 10, "user_id": 1, "type": "deposit", "amount_kopeks": 20000, "payment_method": "yookassa", "is_completed": true, "created_at": "2025-03-02T10:00:00"},
		{"id": 11, "user_id": 1, "type": "subscription_payment", "amount_kopeks": 5000, "description": "30 дней", "is_completed": true, "created_at": "2025-03-02T10:01:00"},
		{"id": 12, "user_id": 4, "type": "deposit", "amount_kopeks": 900, "is_completed": true, "created_at": "2025-03-03T10:00:00"},
		{"id": 13, "user_id": 3, "type": "Bad Type", "amount_kopeks": 1, "is_completed": true, "created_at": "2025-03-03T10:00:00"},
	}
	return users, txs
}

// Every page is read; accounts without Telegram and deleted ones are left out, a
// subscription's link gives its short UUID, and transactions go to their account.
func TestFetchBedolaga(t *testing.T) {
	defer func(n int) { bedolagaPage = n }(bedolagaPage)
	bedolagaPage = 2
	users, txs := bedolagaFixture()
	url := bedolagaServer(t, users, txs)
	if _, err := FetchBot(context.Background(), Client(), Source{Kind: Bedolaga, URL: url, Token: "wrong"}); err != ErrAuth {
		t.Fatalf("a wrong key: %v", err)
	}
	bot, err := FetchBot(context.Background(), Client(), Source{Kind: Bedolaga, URL: url + "/", Token: "bot-key"})
	if err != nil {
		t.Fatal(err)
	}
	if len(bot.Accounts) != 3 {
		t.Fatalf("accounts: %+v", bot.Accounts)
	}
	a := bot.Accounts[0]
	if a.TgID != 111 || a.Username != "alice" || a.Balance != 15000 || len(a.Tokens) != 2 || a.Tokens[0] != tokA || a.Tokens[1] != tokC ||
		!a.Created.Equal(time.Date(2025, 3, 1, 10, 0, 0, 0, time.UTC)) {
		t.Fatalf("alice: %+v", a)
	}
	if len(bot.Payments) != 2 || bot.Payments[0].TgID != 111 || bot.Payments[0].Method != "yookassa" || bot.Payments[1].Description != "30 дней" {
		t.Fatalf("payments: %+v", bot.Payments)
	}
}

func TestCarry(t *testing.T) {
	for _, c := range []struct {
		bc   model.BalanceConfig
		in   int64
		want int64
		ok   bool
	}{
		{model.BalanceConfig{Currency: "RUB"}, 15000, 15000, true},
		{model.BalanceConfig{Currency: "USD", Rate: 90}, 9000, 100, true},
		{model.BalanceConfig{Currency: "USD", Rate: 90}, 9040, 100, true},
		{model.BalanceConfig{Currency: "USD"}, 9000, 0, false},
		{model.BalanceConfig{}, 9000, 9000, true}, // unset is rubles, as the settings show it
		{model.BalanceConfig{Currency: "XYZ"}, 9000, 0, false},
	} {
		if got, ok := carry(c.bc, c.in); got != c.want || ok != c.ok {
			t.Errorf("carry(%+v, %d) = %d, %v", c.bc, c.in, got, ok)
		}
	}
}

// The bot's accounts meet the users from its Remnawave: by the short UUID, else by the
// contact; a subscription with another owner keeps it; the balance comes once and the
// history once.
func TestApplyBot(t *testing.T) {
	st, tariff := seeded(t)
	ctx := context.Background()
	now := time.Now()
	users := domain.NewUsers(st, domain.NewPool(st, time.Now), noChanges{}, time.Now)
	if _, err := Apply(ctx, st, users, now, Remnawave, tariff, []User{
		{Name: "a", Status: StatusActive, Token: tokA},
		{Name: "b", Status: StatusActive, Contact: "tg:222", Token: "BBBBbbbbBBBBbbbb02"},
		{Name: "c", Status: StatusActive, Token: tokC},
		// carol's contact, but carol's link names another subscription: not hers.
		{Name: "d", Status: StatusActive, Contact: "tg:333", Token: "DDDDddddDDDDdddd04"},
	}, nil); err != nil {
		t.Fatal(err)
	}
	ids := map[string]int64{}
	rows, err := st.Q.UsersOfLegacyTokens(ctx, []string{tokA, "BBBBbbbbBBBBbbbb02", tokC, "DDDDddddDDDDdddd04"})
	if err != nil || len(rows) != 4 {
		t.Fatal(rows, err)
	}
	for _, r := range rows {
		ids[map[string]string{tokA: "a", "BBBBbbbbBBBBbbbb02": "b", tokC: "c", "DDDDddddDDDDdddd04": "d"}[r.Token]] = r.UserID
	}
	if err := st.Q.LinkTg(ctx, db.LinkTgParams{UserID: ids["c"], TgID: 999, CreatedAt: now.Unix()}); err != nil {
		t.Fatal(err)
	}
	users2, txs := bedolagaFixture()
	bot, err := FetchBot(ctx, Client(), Source{Kind: Bedolaga, URL: bedolagaServer(t, users2, txs), Token: "bot-key"})
	if err != nil {
		t.Fatal(err)
	}
	rub := model.BalanceConfig{Currency: "RUB"}
	setup := func(context.Context, *db.Queries) (model.BalanceConfig, error) { return rub, nil }

	if p, err := CheckBot(ctx, st.Q, model.BalanceConfig{Currency: "USD"}, bot); err != nil || p.Balances != 0 || p.Currency != "" {
		t.Fatalf("no currency: %+v %v", p, err)
	}
	p, err := CheckBot(ctx, st.Q, rub, bot)
	if err != nil {
		t.Fatal(err)
	}
	if p.Accounts != 3 || p.Matched != 2 || p.Links != 2 || p.Owned.Count != 1 || p.Owned.Names[0] != "c" || p.Unmatched.Count != 1 || p.Unmatched.Names[0] != "@carol" ||
		p.Balances != 2 || p.BalanceTotal != 15500 || p.Payments != 2 || p.Currency != "RUB" {
		t.Fatalf("preview: %+v", p)
	}

	r, err := ApplyBot(ctx, st, now, setup, 7, bot, nil)
	if err != nil {
		t.Fatal(err)
	}
	if r.Accounts != 3 || r.Links != 2 || r.Owned.Count != 1 || r.Balances != 2 || r.BalanceTotal != 15500 || r.Payments != 2 || r.Failed.Count != 0 {
		t.Fatalf("report: %+v", r)
	}
	for name, tg := range map[string]int64{"a": 111, "b": 222, "c": 999} {
		l, err := st.Q.GetTgLink(ctx, ids[name])
		if err != nil || l.TgID != tg {
			t.Errorf("%s is owned by %d (%v), want %d", name, l.TgID, err, tg)
		}
	}
	if n, _ := st.Q.GetBalance(ctx, 111); n != 15000 {
		t.Errorf("alice's balance %d", n)
	}
	if l, err := st.Q.GetTgLink(ctx, ids["d"]); err == nil {
		t.Errorf("d went to %d by the contact although carol's link names another", l.TgID)
	}
	if n, _ := st.Q.GetBalance(ctx, 333); n != 500 {
		t.Errorf("carol's balance %d", n)
	}
	for _, tg := range []int64{111, 222, 333} {
		if was, _ := st.Q.WasClient(ctx, tg); !was {
			t.Errorf("%d is not a client", tg)
		}
	}
	if c, err := st.Q.GetTgChat(ctx, 111); err != nil || c.Username != "alice" {
		t.Errorf("alice's chat: %+v %v", c, err)
	}
	hist, err := st.Q.ListImportedPayments(ctx, db.ListImportedPaymentsParams{TgID: 111, Limit: 10})
	if err != nil || len(hist) != 2 || hist[0].Kind != "subscription_payment" || hist[0].Currency != "RUB" {
		t.Fatalf("history: %+v %v", hist, err)
	}
	ops, _ := st.Q.ListBalanceOps(ctx, db.ListBalanceOpsParams{TgID: 111, Limit: 10})
	if len(ops) != 1 || ops[0].Kind != model.OpAdjust || ops[0].Note != botNote || ops[0].AdminID.Int64 != 7 {
		t.Fatalf("ops: %+v", ops)
	}

	// Again: nothing is added twice.
	if p, _ := CheckBot(ctx, st.Q, rub, bot); p.Balances != 0 || p.Carried != 2 || p.Links != 0 {
		t.Fatalf("second preview: %+v", p)
	}
	r, err = ApplyBot(ctx, st, now, setup, 7, bot, nil)
	if err != nil || r.Links != 0 || r.Balances != 0 || r.Payments != 0 {
		t.Fatalf("second run: %+v %v", r, err)
	}
	if n, _ := st.Q.GetBalance(ctx, 111); n != 15000 {
		t.Errorf("alice's balance after the second run %d", n)
	}
}
