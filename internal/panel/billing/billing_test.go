package billing

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"
	"time"

	"mikan/internal/panel/domain"
	"mikan/internal/panel/promo"
	"mikan/internal/panel/settings"
	"mikan/internal/panel/store"
	"mikan/internal/panel/store/db"
	"mikan/internal/panel/store/storetest"
)

type noChanges struct{}

func (noChanges) PoliciesChanged() {}
func (noChanges) SlotsChanged()    {}

// fakeTG is the gateway and the bot: Stars links, refunds and what buyers were told.
type fakeTG struct {
	mu        sync.Mutex
	paid      []db.Payment
	refunds   []string
	refundErr error
	invoices  int
	refunded  []refundNote
	notGiven  []db.Payment // TrialNotGiven
	onRefund  func()       // runs after Telegram accepted a refund
	// The bot's channel: its link ("" none), who is in it, and why it cannot be asked.
	channel   string
	members   map[int64]bool
	memberErr error
}

// refundNote is what the buyer was told about a refund.
type refundNote struct {
	p        db.Payment
	u        db.User
	disabled bool
}

func (f *fakeTG) InvoiceLink(_ context.Context, _, _, payload string, stars int64) (string, error) {
	f.mu.Lock()
	f.invoices++
	f.mu.Unlock()
	return "https://t.me/$" + payload[:8] + "?stars=" + strconv.FormatInt(stars, 10), nil
}
func (f *fakeTG) RefundStars(_ context.Context, _ int64, charge string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.refundErr != nil {
		return f.refundErr
	}
	if f.onRefund != nil {
		f.onRefund()
	}
	f.refunds = append(f.refunds, charge)
	return nil
}
func (f *fakeTG) Paid(_ context.Context, p db.Payment, _ db.User, _ bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.paid = append(f.paid, p)
}
func (f *fakeTG) TrialNotGiven(_ context.Context, p db.Payment) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.notGiven = append(f.notGiven, p)
}
func (f *fakeTG) Refunded(_ context.Context, p db.Payment, u db.User, disabled bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.refunded = append(f.refunded, refundNote{p, u, disabled})
}
func (f *fakeTG) refundNotes() []refundNote {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]refundNote(nil), f.refunded...)
}
func (f *fakeTG) BotURL(context.Context) string { return "https://t.me/mikan_test_bot" }
func (f *fakeTG) Channel(context.Context) (string, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.channel, f.channel != ""
}
func (f *fakeTG) Subscribed(_ context.Context, tgID int64) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.members[tgID], f.memberErr
}
func (f *fakeTG) told() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.paid)
}
func (f *fakeTG) invoiceCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.invoices
}

type env struct {
	t     *testing.T
	st    *store.Store
	s     *Service
	tg    *fakeTG
	now   time.Time
	logs  *bytes.Buffer
	sale  db.Tariff
	token string
}

func newEnv(t *testing.T) *env {
	t.Helper()
	ctx := context.Background()
	e := &env{t: t, now: time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC), logs: &bytes.Buffer{}, tg: &fakeTG{}}
	var err error
	if e.st, err = storetest.Open(ctx, t.TempDir()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { e.st.Close() })
	if err := domain.Seed(ctx, e.st, e.now); err != nil {
		t.Fatal(err)
	}
	clock := func() time.Time { return e.now }
	set := settings.New(e.st.Q)
	e.s = New(Deps{Store: e.st, Settings: set, Users: domain.NewUsers(e.st, domain.NewPool(e.st, clock), noChanges{}, clock),
		Log: slog.New(slog.NewTextHandler(e.logs, &slog.HandlerOptions{Level: slog.LevelDebug})), Now: clock, MaxLinks: 5})
	e.s.SetTelegram(e.tg)
	e.s.SetNotifier(e.tg)
	must(t, settings.Set(ctx, set, KeyConfig, Config{Enabled: true, Stars: true, AllowNew: true, RenewResetsTraffic: true}))
	ts, _ := e.st.Q.ListTariffs(ctx)
	std := ts[1]
	e.sale, err = e.st.Q.UpdateTariff(ctx, db.UpdateTariffParams{Name: std.Name, TrafficLimit: std.TrafficLimit, DurationDays: 30, DeviceLimit: std.DeviceLimit,
		ResetStrategy: std.ResetStrategy, Sort: std.Sort, PriceStars: sql.NullInt64{Int64: 150, Valid: true}, PriceRub: sql.NullInt64{Int64: 19900, Valid: true}, OnSale: 1, ID: std.ID})
	must(t, err)
	_ = e.st.Q.UpsertTgChat(ctx, db.UpsertTgChatParams{TgID: 555, Username: "buyer", CreatedAt: e.now.Unix(), UpdatedAt: e.now.Unix()})
	e.token, err = e.s.WebhookToken(ctx)
	must(t, err)
	return e
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func (e *env) invoice(tg, user int64, provider string) db.Payment {
	e.t.Helper()
	p, err := e.s.Invoice(context.Background(), InvoiceRequest{TgID: tg, UserID: user, TariffID: e.sale.ID, Provider: provider})
	if err != nil {
		e.t.Fatalf("invoice %s: %v", provider, err)
	}
	if p.PayUrl == "" || p.Status != "pending" {
		e.t.Fatalf("invoice %s: %+v", provider, p)
	}
	return p
}

func (e *env) payment(id int64) db.Payment {
	e.t.Helper()
	p, err := e.st.Q.GetPayment(context.Background(), id)
	must(e.t, err)
	return p
}

func (e *env) users() int {
	var n int
	_ = e.st.DB.QueryRowContext(context.Background(), "SELECT count(*) FROM users").Scan(&n)
	return n
}

func (e *env) hook(provider, token, ip string, body []byte, hdr map[string]string) int {
	r := httptest.NewRequest(http.MethodPost, "/"+provider+"/"+token, bytes.NewReader(body))
	r.RemoteAddr = ip + ":40000"
	for k, v := range hdr {
		r.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	e.s.Webhook().ServeHTTP(w, r)
	return w.Code
}

func TestDiscountedStarsInvoicePersistsAmountAndReusesInvoice(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	pc, err := e.st.Q.CreatePromoCode(ctx, db.CreatePromoCodeParams{Code: "SALE25", Type: "percent", Value: 25, Currency: "XTR", PerUserLimit: 1, TariffIds: "[]", Enabled: 1, CreatedAt: e.now.Unix()})
	must(t, err)
	e.s.d.Promo = promo.New(e.st, func() time.Time { return e.now })
	req := InvoiceRequest{TgID: 555, TariffID: e.sale.ID, Provider: Stars, PromoCode: pc.Code}
	p, err := e.s.Invoice(ctx, req)
	must(t, err)
	if p.Amount != 113 {
		t.Fatalf("invoice amount = %d, want 113", p.Amount)
	}
	if stored := e.payment(p.ID); stored.Amount != 113 {
		t.Fatalf("stored amount = %d, want discounted 113", stored.Amount)
	}
	ordinary, err := e.s.Invoice(ctx, InvoiceRequest{TgID: 555, TariffID: e.sale.ID, Provider: Stars})
	must(t, err)
	if ordinary.ID == p.ID {
		t.Fatal("ordinary invoice unexpectedly reused the discounted payment")
	}
	calls := e.tg.invoiceCount()
	if again, err := e.s.Invoice(ctx, req); err != nil || again.ID != p.ID || e.tg.invoiceCount() != calls {
		t.Fatalf("invoice was not reused: payment=%+v err=%v calls=%d->%d", again, err, calls, e.tg.invoiceCount())
	}
	must(t, e.s.PreCheckout(ctx, 555, p.Payload, "XTR", 113))
	must(t, e.s.StarsPaid(ctx, 555, p.Payload, "ch-discount", "XTR", 113))
	if e.payment(p.ID).Status != "applied" {
		t.Fatalf("discounted payment not applied: %+v", e.payment(p.ID))
	}
}

func TestConcurrentInvoicesWithSingleUsePromoShareReservation(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	pc, err := e.st.Q.CreatePromoCode(ctx, db.CreatePromoCodeParams{Code: "ONCE", Type: "percent", Value: 10, Currency: "XTR", MaxUses: sql.NullInt64{Int64: 1, Valid: true}, PerUserLimit: 1, TariffIds: "[]", Enabled: 1, CreatedAt: e.now.Unix()})
	must(t, err)
	e.s.d.Promo = promo.New(e.st, func() time.Time { return e.now })
	req := InvoiceRequest{TgID: 555, TariffID: e.sale.ID, Provider: Stars, PromoCode: pc.Code}
	payments := make([]db.Payment, 2)
	errs := make([]error, 2)
	var wg sync.WaitGroup
	for i := range payments {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			payments[i], errs[i] = e.s.Invoice(ctx, req)
		}(i)
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			t.Fatalf("concurrent promo invoice: %v", err)
		}
	}
	if payments[0].ID != payments[1].ID {
		t.Fatalf("parallel calls opened payments %d and %d", payments[0].ID, payments[1].ID)
	}
	var uses, redemptions int
	if err := e.st.DB.QueryRowContext(ctx, `SELECT used_count FROM promo_codes WHERE id=$1`, pc.ID).Scan(&uses); err != nil {
		t.Fatal(err)
	}
	if err := e.st.DB.QueryRowContext(ctx, `SELECT count(*) FROM promo_redemptions WHERE promo_id=$1 AND status='reserved'`, pc.ID).Scan(&redemptions); err != nil {
		t.Fatal(err)
	}
	if uses != 1 || redemptions != 1 {
		t.Fatalf("promo use count=%d reservations=%d, want 1 each", uses, redemptions)
	}
}

func TestExpiredDiscountedStarsCaptureIsRefunded(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	pc, err := e.st.Q.CreatePromoCode(ctx, db.CreatePromoCodeParams{Code: "SHORT", Type: "percent", Value: 10,
		Currency: "XTR", PerUserLimit: 1, DiscountTtl: 30, TariffIds: "[]", Enabled: 1, CreatedAt: e.now.Unix()})
	must(t, err)
	e.s.d.Promo = promo.New(e.st, func() time.Time { return e.now })
	p, err := e.s.Invoice(ctx, InvoiceRequest{TgID: 555, TariffID: e.sale.ID, Provider: Stars, PromoCode: pc.Code})
	must(t, err)
	e.now = e.now.Add(31 * time.Second)
	if err := e.s.StarsPaid(ctx, 555, p.Payload, "late-charge", p.Currency, p.Amount); err != nil {
		t.Fatalf("late capture should be refunded: %v", err)
	}
	if got := e.payment(p.ID); got.Status != "refunded" {
		t.Fatalf("payment status = %q, want refunded", got.Status)
	}
	e.tg.mu.Lock()
	refunds := append([]string(nil), e.tg.refunds...)
	e.tg.mu.Unlock()
	if len(refunds) != 1 || refunds[0] != "late-charge" {
		t.Fatalf("refunds = %v", refunds)
	}
	if e.users() != 0 {
		t.Fatal("late payment created a subscription")
	}
	r, err := e.s.d.Promo.GetPaymentRedemption(ctx, p.ID)
	if err != nil || r.Status != "released" {
		t.Fatalf("reservation status = %q, err=%v; want released", r.Status, err)
	}
}

func TestLateStarsRefundFailureRetriesInReconcile(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	pc, err := e.st.Q.CreatePromoCode(ctx, db.CreatePromoCodeParams{Code: "RETRY", Type: "percent", Value: 10,
		Currency: "XTR", PerUserLimit: 1, DiscountTtl: 30, TariffIds: "[]", Enabled: 1, CreatedAt: e.now.Unix()})
	must(t, err)
	e.s.d.Promo = promo.New(e.st, func() time.Time { return e.now })
	p, err := e.s.Invoice(ctx, InvoiceRequest{TgID: 555, TariffID: e.sale.ID, Provider: Stars, PromoCode: pc.Code})
	must(t, err)
	e.now = e.now.Add(31 * time.Second)
	e.tg.refundErr = errors.New("Telegram unavailable")
	if err := e.s.StarsPaid(ctx, 555, p.Payload, "retry-charge", p.Currency, p.Amount); err == nil {
		t.Fatal("the first refund attempt unexpectedly succeeded")
	}
	failed := e.payment(p.ID)
	if failed.Error != "promo_late_refund_failed" || failed.ExternalID.String != "retry-charge" {
		t.Fatalf("payment after failed refund: %+v", failed)
	}
	r, err := e.s.d.Promo.GetPaymentRedemption(ctx, p.ID)
	if err != nil || r.RefundStartedAt.Valid {
		t.Fatalf("failed refund claim remains set: %+v err=%v", r.RefundStartedAt, err)
	}
	e.tg.refundErr = nil
	e.s.Reconcile(ctx)
	if got := e.payment(p.ID); got.Status != "refunded" || got.Error != "" {
		t.Fatalf("reconciled payment status=%q error=%q", got.Status, got.Error)
	}
	e.tg.mu.Lock()
	refunds := append([]string(nil), e.tg.refunds...)
	e.tg.mu.Unlock()
	if len(refunds) != 1 || refunds[0] != "retry-charge" {
		t.Fatalf("refunds=%v", refunds)
	}
}

func TestStarsPaymentReleasedDuringPaymentTransitionGetsRefunded(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	pc, err := e.st.Q.CreatePromoCode(ctx, db.CreatePromoCodeParams{Code: "NOEXPIRY", Type: "percent", Value: 10,
		Currency: "XTR", PerUserLimit: 1, TariffIds: "[]", Enabled: 1, CreatedAt: e.now.Unix()})
	must(t, err)
	e.s.d.Promo = promo.New(e.st, func() time.Time { return e.now })
	p, err := e.s.Invoice(ctx, InvoiceRequest{TgID: 555, TariffID: e.sale.ID, Provider: Stars, PromoCode: pc.Code})
	must(t, err)
	if _, err := e.st.Q.SetPaymentStatus(ctx, db.SetPaymentStatusParams{NewStatus: "expired", ID: p.ID, OldStatus: "pending"}); err != nil {
		t.Fatal(err)
	}
	e.now = e.now.Add(31 * time.Minute)
	baseNow := e.s.d.Now
	released := false
	e.s.d.Now = func() time.Time {
		if !released {
			released = true
			if err := e.s.d.Promo.ReleasePayment(ctx, p.ID); err != nil {
				t.Errorf("release during paid transition: %v", err)
			}
		}
		return baseNow()
	}
	if err := e.s.StarsPaid(ctx, 555, p.Payload, "race-charge", p.Currency, p.Amount); err != nil {
		t.Fatalf("late payment race should be refunded: %v", err)
	}
	if got := e.payment(p.ID); got.Status != "refunded" || !got.RefundedAt.Valid || got.Error != "" {
		t.Fatalf("payment after release race = %q refunded_at=%+v error=%q", got.Status, got.RefundedAt, got.Error)
	}
	e.tg.mu.Lock()
	refunds := append([]string(nil), e.tg.refunds...)
	e.tg.mu.Unlock()
	if len(refunds) != 1 || refunds[0] != "race-charge" {
		t.Fatalf("refunds = %v", refunds)
	}
	if e.users() != 0 {
		t.Fatal("late payment created a subscription")
	}
}

// A new buyer pays in Stars: one subscription, linked to the account, told once — however
// many times or how concurrently Telegram reports the payment.
func TestStarsNewSubscription(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	before := e.users()
	p := e.invoice(555, 0, Stars)
	if p.Amount != 150 || p.Currency != "XTR" || p.Kind != "new" {
		t.Fatalf("invoice: %+v", p)
	}
	// Asking again within minutes shows the same invoice.
	if again := e.invoice(555, 0, Stars); again.ID != p.ID {
		t.Fatalf("a second invoice for the same purchase: %d", again.ID)
	}
	for _, c := range []struct {
		name         string
		tg           int64
		payload, cur string
		amount       int64
	}{
		{"other account", 556, p.Payload, "XTR", 150},
		{"other amount", 555, p.Payload, "XTR", 1},
		{"other currency", 555, p.Payload, "USD", 150},
		{"unknown payload", 555, "nope", "XTR", 150},
	} {
		if err := e.s.PreCheckout(ctx, c.tg, c.payload, c.cur, c.amount); err == nil {
			t.Errorf("pre-checkout %s accepted", c.name)
		}
		if err := e.s.StarsPaid(ctx, c.tg, c.payload, "ch-x", c.cur, c.amount); err == nil {
			t.Errorf("payment %s accepted", c.name)
		}
	}
	must(t, e.s.PreCheckout(ctx, 555, p.Payload, "XTR", 150))

	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = e.s.StarsPaid(ctx, 555, p.Payload, "ch-1", "XTR", 150)
		}()
	}
	wg.Wait()
	// A second charge for the same invoice gives nothing and goes back to the buyer.
	must(t, e.s.StarsPaid(ctx, 555, p.Payload, "ch-2", "XTR", 150))
	must(t, e.s.StarsPaid(ctx, 555, p.Payload, "ch-1", "XTR", 150)) // the first one again: nothing
	if len(e.tg.refunds) != 1 || e.tg.refunds[0] != "ch-2" {
		t.Fatalf("a second charge was not refunded: %v", e.tg.refunds)
	}
	got := e.payment(p.ID)
	if got.Status != "applied" || got.ExternalID.String != "ch-1" || !got.UserID.Valid {
		t.Fatalf("payment: %+v", got)
	}
	if e.users() != before+1 || e.tg.told() != 1 {
		t.Fatalf("users %d (was %d), told %d", e.users(), before, e.tg.told())
	}
	link, err := e.st.Q.GetTgLink(ctx, got.UserID.Int64)
	if err != nil || link.TgID != 555 {
		t.Fatalf("link: %+v %v", link, err)
	}
	u, _ := e.st.Q.GetUser(ctx, got.UserID.Int64)
	if u.Name != "@buyer" || time.Unix(u.ExpiresAt.Int64, 0).Sub(e.now) != 30*24*time.Hour {
		t.Fatalf("user: %+v", u)
	}
	if u.Source != domain.UserFromBot {
		t.Fatalf("a subscription bought in the bot came from %q", u.Source)
	}
	// Paid once: the invoice cannot be paid again.
	if err := e.s.PreCheckout(ctx, 555, p.Payload, "XTR", 150); err == nil {
		t.Fatal("an applied invoice passed pre-checkout")
	}
	// Refund: Stars go back through the bot, the payment says so.
	if _, err := e.s.Refund(ctx, p.ID); err != nil {
		t.Fatal(err)
	}
	if e.payment(p.ID).Status != "refunded" || len(e.tg.refunds) != 2 || e.tg.refunds[1] != "ch-1" {
		t.Fatalf("refund: %+v %v", e.payment(p.ID), e.tg.refunds)
	}
	if off, _ := e.st.Q.GetUser(ctx, got.UserID.Int64); off.Status != "disabled" {
		t.Fatalf("the refunded subscription is %q", off.Status)
	}
	if _, err := e.s.Refund(ctx, p.ID); !errors.Is(err, ErrNotRefunable) {
		t.Fatalf("second refund: %v", err)
	}
}

// Renewal: only the owner renews, the term goes on from the current end, the traffic
// period starts anew.
func TestRenewal(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	clock := func() time.Time { return e.now }
	u, err := domain.NewUsers(e.st, domain.NewPool(e.st, clock), noChanges{}, clock).Create(ctx, domain.CreateInput{Name: "a", TariffID: e.sale.ID})
	must(t, err)
	must(t, e.st.Q.LinkTg(ctx, db.LinkTgParams{UserID: u.ID, TgID: 555, CreatedAt: e.now.Unix()}))
	_, err = e.st.DB.ExecContext(ctx, "UPDATE users SET used_up = 1000, used_down = 2000 WHERE id = $1", u.ID)
	must(t, err)

	if _, err := e.s.Invoice(ctx, InvoiceRequest{TgID: 777, UserID: u.ID, TariffID: e.sale.ID, Provider: Stars}); !errors.Is(err, ErrNotYours) {
		t.Fatalf("someone else's subscription: %v", err)
	}
	p := e.invoice(555, u.ID, Stars)
	must(t, e.s.StarsPaid(ctx, 555, p.Payload, "ch-r", "XTR", 150))
	after, _ := e.st.Q.GetUser(ctx, u.ID)
	if after.ExpiresAt.Int64 != u.ExpiresAt.Int64+30*24*3600 || after.UsedUp+after.UsedDown != 0 || after.Status != "active" {
		t.Fatalf("renewed: expires %d (was %d), used %d", after.ExpiresAt.Int64, u.ExpiresAt.Int64, after.UsedUp+after.UsedDown)
	}
	if e.payment(p.ID).UserID.Int64 != u.ID {
		t.Fatal("payment not tied to the renewed user")
	}
	// A renewal of a subscription the admin made does not make it a purchase of the bot.
	if after.Source != domain.UserFromAdmin {
		t.Fatalf("a renewed user came from %q, want the admin", after.Source)
	}
}

func TestInvoiceRefusals(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	ts, _ := e.st.Q.ListTariffs(ctx)
	if _, err := e.s.Invoice(ctx, InvoiceRequest{TgID: 555, TariffID: ts[0].ID, Provider: Stars}); !errors.Is(err, ErrNotForSale) {
		t.Fatalf("not on sale: %v", err)
	}
	if _, err := e.s.Invoice(ctx, InvoiceRequest{TgID: 555, TariffID: e.sale.ID, Provider: "paypal"}); !errors.Is(err, ErrProviderOff) {
		t.Fatalf("unknown provider: %v", err)
	}
	must(t, settings.Set(ctx, e.s.d.Settings, KeyConfig, Config{Enabled: true, Stars: true, AllowNew: false}))
	if _, err := e.s.Invoice(ctx, InvoiceRequest{TgID: 555, TariffID: e.sale.ID, Provider: "addon:yookassa"}); !errors.Is(err, ErrProviderOff) {
		t.Fatalf("an adapter that is not installed: %v", err)
	}
	if _, err := e.s.Invoice(ctx, InvoiceRequest{TgID: 555, TariffID: e.sale.ID, Provider: Stars}); !errors.Is(err, ErrNewOff) {
		t.Fatalf("new buyers off: %v", err)
	}
	must(t, settings.Set(ctx, e.s.d.Settings, KeyConfig, Config{Enabled: true, Stars: true, AllowNew: true}))
	for i := range 5 {
		u, err := e.s.d.Users.Create(ctx, domain.CreateInput{Name: "x" + strconv.Itoa(i), TariffID: e.sale.ID})
		must(t, err)
		must(t, e.st.Q.LinkTg(ctx, db.LinkTgParams{UserID: u.ID, TgID: 900, CreatedAt: 1}))
	}
	if _, err := e.s.Invoice(ctx, InvoiceRequest{TgID: 900, TariffID: e.sale.ID, Provider: Stars}); err == nil {
		t.Fatal("a sixth subscription for one account")
	}
	// One account cannot open invoices without end.
	for tariff := range maxPerHour + 1 {
		_, err := e.st.Q.CreatePayment(ctx, db.CreatePaymentParams{Provider: Stars, Payload: "p" + strconv.Itoa(tariff), TgID: 321, Kind: "new",
			TariffID: sql.NullInt64{Int64: e.sale.ID, Valid: true}, TariffName: "x", Amount: 1, Currency: "XTR", CreatedAt: e.now.Unix()})
		must(t, err)
	}
	if _, err := e.s.Invoice(ctx, InvoiceRequest{TgID: 321, TariffID: e.sale.ID, Provider: Stars}); !errors.Is(err, ErrTooMany) {
		t.Fatalf("invoice flood: %v", err)
	}
}

// Taps at once: the same purchase gives one invoice, and different ones together cannot
// pass the hourly limit, also when two panels (services) share the database.
func TestConcurrentInvoices(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	other := New(e.s.d)
	other.SetTelegram(e.tg)
	other.SetNotifier(e.tg)
	// Tap i goes through services[i % len]: across services only the database's lock holds.
	taps := func(n int, tariff func(i int) int64, services ...*Service) []error {
		var wg sync.WaitGroup
		errs := make([]error, n)
		for i := range n {
			wg.Add(1)
			go func() {
				defer wg.Done()
				_, errs[i] = services[i%len(services)].Invoice(ctx, InvoiceRequest{TgID: 555, TariffID: tariff(i), Provider: Stars})
			}()
		}
		wg.Wait()
		return errs
	}
	for _, err := range taps(5, func(int) int64 { return e.sale.ID }, e.s) {
		must(t, err)
	}
	all, err := e.st.Q.CountRecentInvoices(ctx, db.CountRecentInvoicesParams{TgID: 555, CreatedAt: 0})
	if err != nil || all != 1 {
		t.Fatalf("a double tap made %d invoices (%v), want 1", all, err)
	}

	// Different tariffs, so none is reused: only what is left of the hour's limit goes in.
	var tariffs []int64
	for i := range maxPerHour + 5 {
		tr, err := e.st.Q.CreateTariff(ctx, db.CreateTariffParams{Name: "t" + strconv.Itoa(i), DurationDays: 30, ResetStrategy: "none",
			PriceStars: sql.NullInt64{Int64: 10, Valid: true}, OnSale: 1, CreatedAt: e.now.Unix()})
		must(t, err)
		tariffs = append(tariffs, tr.ID)
	}
	tooMany := 0
	for _, err := range taps(len(tariffs), func(i int) int64 { return tariffs[i] }, e.s, other) {
		switch {
		case errors.Is(err, ErrTooMany):
			tooMany++
		case err != nil:
			t.Fatal(err)
		}
	}
	all, _ = e.st.Q.CountRecentInvoices(ctx, db.CountRecentInvoicesParams{TgID: 555, CreatedAt: 0})
	if all != maxPerHour || tooMany != len(tariffs)-(maxPerHour-1) {
		t.Fatalf("%d invoices and %d refused, want %d and %d", all, tooMany, maxPerHour, len(tariffs)-(maxPerHour-1))
	}
}

// With the reset off a renewal only adds the term: the traffic counter runs on.
func TestRenewalKeepsTraffic(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	must(t, settings.Set(ctx, e.s.d.Settings, KeyConfig, Config{Enabled: true, Stars: true, AllowNew: true, RenewResetsTraffic: false}))
	clock := func() time.Time { return e.now }
	u, err := domain.NewUsers(e.st, domain.NewPool(e.st, clock), noChanges{}, clock).Create(ctx, domain.CreateInput{Name: "a", TariffID: e.sale.ID})
	must(t, err)
	must(t, e.st.Q.LinkTg(ctx, db.LinkTgParams{UserID: u.ID, TgID: 555, CreatedAt: e.now.Unix()}))
	_, err = e.st.DB.ExecContext(ctx, "UPDATE users SET used_up = 1000, used_down = 2000 WHERE id = $1", u.ID)
	must(t, err)
	p := e.invoice(555, u.ID, Stars)
	must(t, e.s.StarsPaid(ctx, 555, p.Payload, "ch-k", "XTR", 150))
	after, _ := e.st.Q.GetUser(ctx, u.ID)
	if after.ExpiresAt.Int64 != u.ExpiresAt.Int64+30*24*3600 || after.UsedUp+after.UsedDown != 3000 {
		t.Fatalf("renewed: expires %d, used %d", after.ExpiresAt.Int64, after.UsedUp+after.UsedDown)
	}
}

// Selling off: nothing on offer and no new invoices, but an invoice opened before still
// turns into the subscription once it is paid — nobody pays for nothing.
func TestSalesOff(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	p := e.invoice(555, 0, Stars)
	c := e.s.Config(ctx)
	c.Enabled = false
	must(t, settings.Set(ctx, e.s.d.Settings, KeyConfig, c))

	if o, av, err := e.s.Offers(ctx); err != nil || len(o) != 0 || av.Any() {
		t.Fatalf("offers with selling off: %v %+v %v", o, av, err)
	}
	for _, prov := range []string{Stars, "addon:yookassa"} {
		if _, err := e.s.Invoice(ctx, InvoiceRequest{TgID: 556, TariffID: e.sale.ID, Provider: prov}); !errors.Is(err, ErrProviderOff) {
			t.Fatalf("invoice %s with selling off: %v", prov, err)
		}
	}
	must(t, e.s.PreCheckout(ctx, 555, p.Payload, "XTR", 150))
	must(t, e.s.StarsPaid(ctx, 555, p.Payload, "ch-off", "XTR", 150))
	if got := e.payment(p.ID); got.Status != "applied" || !got.UserID.Valid {
		t.Fatalf("an invoice opened before selling went off: %+v", got)
	}
}

// Payment settings saved before the switch existed keep selling; a panel that never saved
// them starts with selling off.
func TestSalesDefault(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	_, err := e.st.DB.ExecContext(ctx, "DELETE FROM settings WHERE key = $1", KeyConfig)
	must(t, err)
	if e.s.Config(ctx).Enabled || e.s.Available(ctx).Any() {
		t.Fatal("selling on in a panel that never set it up")
	}
	must(t, settings.Set(ctx, e.s.d.Settings, KeyConfig, map[string]any{"stars": true, "allow_new": true}))
	if c := e.s.Config(ctx); !c.Enabled || !c.Stars {
		t.Fatalf("settings from 0.4.1 lost selling: %+v", c)
	}
}

// A database that does not answer is not "no such invoice": the bot offers the payment
// again, and once the database answers it is applied.
func TestStarsPaidLookupFailureIsRetried(t *testing.T) {
	e := newEnv(t)
	p := e.invoice(555, 0, Stars)
	down, cancel := context.WithCancel(context.Background())
	cancel()
	err := e.s.StarsPaid(down, 555, p.Payload, "ch-1", "XTR", 150)
	if err == nil || errors.Is(err, ErrBadPayment) || errors.Is(err, ErrRecorded) {
		t.Fatalf("a failed lookup answered %v", err)
	}
	must(t, e.s.StarsPaid(context.Background(), 555, p.Payload, "ch-1", "XTR", 150))
	if got := e.payment(p.ID); got.Status != "applied" {
		t.Fatalf("payment after the retry: %q", got.Status)
	}
}

// A payment saved with its charge but not applied says so: the reconcile pass finishes
// it, the report of the payment is not needed again.
func TestStarsPaidSavedButNotApplied(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	p := e.invoice(555, 0, Stars)
	// The payment settings turn unreadable: applying fails after the payment is saved.
	_, err := e.st.DB.ExecContext(ctx, "UPDATE settings SET value = '{' WHERE key = $1", KeyConfig)
	must(t, err)
	err = e.s.StarsPaid(ctx, 555, p.Payload, "ch-1", "XTR", 150)
	if !errors.Is(err, ErrRecorded) {
		t.Fatalf("an unapplied saved payment answered %v", err)
	}
	if got := e.payment(p.ID); got.Status != "paid" || got.ExternalID.String != "ch-1" {
		t.Fatalf("payment: %+v", got)
	}
	// Paid and never applied: the admin can still give the Stars back.
	if _, err := e.s.Refund(ctx, p.ID); err != nil {
		t.Fatalf("refund of a paid payment: %v", err)
	}
	if got := e.payment(p.ID); got.Status != "refunded" || len(e.tg.refunds) != 1 || e.tg.refunds[0] != "ch-1" {
		t.Fatalf("after the refund: %+v %v", got, e.tg.refunds)
	}
}
