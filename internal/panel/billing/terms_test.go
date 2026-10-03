package billing

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"mikan/internal/panel/domain"
	"mikan/internal/panel/promo"
	"mikan/internal/panel/store/db"
)

// sellTerms puts the tariff on sale for 7, 30 and 90 days: the 30-day term at the price of
// the 90-day one in Stars, so only the term tells two invoices apart.
func (e *env) sellTerms() {
	e.t.Helper()
	must(e.t, domain.SetTariffTerms(context.Background(), e.st.Q, e.sale.ID, []domain.Term{
		{Days: 30, PriceStars: sql.NullInt64{Int64: 150, Valid: true}, PriceRub: sql.NullInt64{Int64: 19900, Valid: true}},
		{Days: 7, PriceStars: sql.NullInt64{Int64: 50, Valid: true}, PriceRub: sql.NullInt64{Int64: 9900, Valid: true}},
		{Days: 90, PriceStars: sql.NullInt64{Int64: 150, Valid: true}, PriceRub: sql.NullInt64{Int64: 49900, Valid: true}},
	}))
}

func days(n int64) *int64 { return &n }

func TestOffersListTheTerms(t *testing.T) {
	e := newEnv(t)
	e.sellTerms()
	offers, _, err := e.s.Offers(context.Background())
	must(t, err)
	if len(offers) != 1 || len(offers[0].Terms) != 3 {
		t.Fatalf("offers %+v", offers)
	}
	o := offers[0]
	if o.Terms[0].Days != 30 || o.Terms[1].Days != 7 || o.Terms[2].Days != 90 || o.Terms[1].Stars != 50 {
		t.Fatalf("terms out of order or priced wrong: %+v", o.Terms)
	}
	// Stars only (no adapter): the ruble prices are not offered.
	if o.Terms[2].Rub != 0 || o.Stars != 150 {
		t.Fatalf("prices the providers do not take: %+v", o)
	}
	if got := DescribeOffer(o, "en"); got != "7 days – 90 days · 150 GB · devices: 3" {
		t.Fatalf("offer line %q", got)
	}
}

// The invoice is for the chosen term's price, the payment keeps its days, and the term
// added on payment is that one: whatever the admin changes in between, and with the
// traffic limit not multiplied.
func TestInvoiceForATerm(t *testing.T) {
	e := newEnv(t)
	e.sellTerms()
	ctx := context.Background()
	p, err := e.s.Invoice(ctx, InvoiceRequest{TgID: 555, TariffID: e.sale.ID, TermDays: days(7), Provider: Stars})
	must(t, err)
	if p.Amount != 50 || !p.TermDays.Valid || p.TermDays.Int64 != 7 {
		t.Fatalf("payment %+v", p)
	}
	if _, err := e.s.Invoice(ctx, InvoiceRequest{TgID: 555, TariffID: e.sale.ID, TermDays: days(14), Provider: Stars}); !errors.Is(err, ErrNotForSale) {
		t.Fatalf("a term the tariff has not: %v", err)
	}
	// The admin makes the 7-day term 10 days after the invoice: the buyer gets what they paid for.
	must(t, domain.SetTariffTerms(ctx, e.st.Q, e.sale.ID, []domain.Term{
		{Days: 30, PriceStars: sql.NullInt64{Int64: 150, Valid: true}},
		{Days: 7, PriceStars: sql.NullInt64{Int64: 70, Valid: true}},
	}))
	must(t, e.s.PreCheckout(ctx, 555, p.Payload, "XTR", 50))
	must(t, e.s.StarsPaid(ctx, 555, p.Payload, "ch-7", "XTR", 50))
	u, err := e.st.Q.GetUser(ctx, e.payment(p.ID).UserID.Int64)
	must(t, err)
	if want := e.now.Add(7 * 24 * time.Hour).Unix(); u.ExpiresAt.Int64 != want {
		t.Fatalf("expires %v, want %v", time.Unix(u.ExpiresAt.Int64, 0).UTC(), time.Unix(want, 0).UTC())
	}
	if u.TrafficLimit != e.sale.TrafficLimit {
		t.Fatalf("traffic limit %v, the tariff's %v", u.TrafficLimit, e.sale.TrafficLimit)
	}
	// A renewal for 90 days adds 90 days after the current term; the limit stays per period.
	r, err := e.s.Invoice(ctx, InvoiceRequest{TgID: 555, UserID: u.ID, TariffID: e.sale.ID, TermDays: days(90), Provider: Stars})
	if err == nil {
		t.Fatal("a term taken off the tariff is still sold")
	}
	must(t, domain.SetTariffTerms(ctx, e.st.Q, e.sale.ID, []domain.Term{
		{Days: 30, PriceStars: sql.NullInt64{Int64: 150, Valid: true}},
		{Days: 90, PriceStars: sql.NullInt64{Int64: 400, Valid: true}},
	}))
	r, err = e.s.Invoice(ctx, InvoiceRequest{TgID: 555, UserID: u.ID, TariffID: e.sale.ID, TermDays: days(90), Provider: Stars})
	must(t, err)
	must(t, e.s.StarsPaid(ctx, 555, r.Payload, "ch-90", "XTR", 400))
	u, _ = e.st.Q.GetUser(ctx, u.ID)
	if want := e.now.Add(97 * 24 * time.Hour).Unix(); u.ExpiresAt.Int64 != want {
		t.Fatalf("renewed to %v, want %v", time.Unix(u.ExpiresAt.Int64, 0).UTC(), time.Unix(want, 0).UTC())
	}
	if u.TrafficLimit != e.sale.TrafficLimit {
		t.Fatalf("a 90-day term multiplied the limit: %v", u.TrafficLimit)
	}
}

// Two terms at one price are two purchases: an open invoice for one is not handed out
// for the other.
func TestOpenInvoiceIsPerTerm(t *testing.T) {
	e := newEnv(t)
	e.sellTerms()
	ctx := context.Background()
	month, err := e.s.Invoice(ctx, InvoiceRequest{TgID: 555, TariffID: e.sale.ID, TermDays: days(30), Provider: Stars})
	must(t, err)
	quarter, err := e.s.Invoice(ctx, InvoiceRequest{TgID: 555, TariffID: e.sale.ID, TermDays: days(90), Provider: Stars})
	must(t, err)
	if month.ID == quarter.ID || month.Amount != quarter.Amount {
		t.Fatalf("30 days %+v, 90 days %+v", month, quarter)
	}
	// No term asked for is the first one, the same purchase as 30 days.
	again, err := e.s.Invoice(ctx, InvoiceRequest{TgID: 555, TariffID: e.sale.ID, Provider: Stars})
	must(t, err)
	if again.ID != month.ID {
		t.Fatalf("the first term's invoice was not reused: %d, want %d", again.ID, month.ID)
	}
}

// Telegram's last question refuses a term taken off the tariff since the invoice.
func TestPreCheckoutRefusesARemovedTerm(t *testing.T) {
	e := newEnv(t)
	e.sellTerms()
	ctx := context.Background()
	p, err := e.s.Invoice(ctx, InvoiceRequest{TgID: 555, TariffID: e.sale.ID, TermDays: days(90), Provider: Stars})
	must(t, err)
	must(t, domain.SetTariffTerms(ctx, e.st.Q, e.sale.ID, nil))
	if err := e.s.PreCheckout(ctx, 555, p.Payload, "XTR", p.Amount); !errors.Is(err, ErrNotForSale) {
		t.Fatalf("a removed term: %v", err)
	}
}

// A payment made before terms (no days on it) adds the tariff's own term, as before.
func TestPaymentWithoutTermTakesTheTariffs(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	p, err := e.st.Q.CreatePayment(ctx, db.CreatePaymentParams{Provider: Stars, Payload: "old-payment-without-term-0000000", TgID: 555, Kind: "new",
		TariffID: sql.NullInt64{Int64: e.sale.ID, Valid: true}, TariffName: e.sale.Name, Amount: 150, Currency: "XTR", CreatedAt: e.now.Unix()})
	must(t, err)
	must(t, e.s.StarsPaid(ctx, 555, p.Payload, "ch-old", "XTR", 150))
	u, err := e.st.Q.GetUser(ctx, e.payment(p.ID).UserID.Int64)
	must(t, err)
	if want := e.now.Add(30 * 24 * time.Hour).Unix(); u.ExpiresAt.Int64 != want {
		t.Fatalf("expires %v", time.Unix(u.ExpiresAt.Int64, 0).UTC())
	}
}

// A discount code takes the chosen term's price: its minimum order too.
func TestPromoOnATermsPrice(t *testing.T) {
	e := newEnv(t)
	e.sellTerms()
	ctx := context.Background()
	_, err := e.st.Q.CreatePromoCode(ctx, db.CreatePromoCodeParams{Code: "BIG", Type: "fixed", Value: 20, Currency: "XTR", MinOrder: 100, PerUserLimit: 1, TariffIds: "[]", Enabled: 1, CreatedAt: e.now.Unix()})
	must(t, err)
	e.s.d.Promo = promo.New(e.st, func() time.Time { return e.now })
	if _, err := e.s.Invoice(ctx, InvoiceRequest{TgID: 555, TariffID: e.sale.ID, TermDays: days(7), Provider: Stars, PromoCode: "BIG"}); err == nil {
		t.Fatal("the 7-day term (50) passed a minimum order of 100")
	}
	p, err := e.s.Invoice(ctx, InvoiceRequest{TgID: 555, TariffID: e.sale.ID, TermDays: days(90), Provider: Stars, PromoCode: "BIG"})
	must(t, err)
	if p.Amount != 130 || p.TermDays.Int64 != 90 {
		t.Fatalf("discounted 90 days: %+v", p)
	}
}

func TestTermLabel(t *testing.T) {
	plain := db.Tariff{}
	toDay := db.Tariff{BillingDay: sql.NullInt64{Int64: 5, Valid: true}}
	for _, c := range []struct {
		t    db.Tariff
		days int64
		want string
	}{{plain, 0, "no end date"}, {plain, 7, "7 days"}, {toDay, 30, "1 month"}, {toDay, 90, "3 months"}, {toDay, 0, "no end date"}} {
		if got := TermLabel(c.t, c.days, "en"); got != c.want {
			t.Errorf("%d days (billing day %v): %q, want %q", c.days, c.t.BillingDay.Valid, got, c.want)
		}
	}
}
