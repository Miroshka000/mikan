package billing

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"mikan/internal/panel/addons"
	"mikan/internal/panel/promo"
	"mikan/internal/panel/store/db"
)

const adapterSecret = "sk_live_ADAPTERsecret0"

// fakeAdapter is a payment adapter of protocol v1 for a provider that signs its webhooks
// with a header.
type fakeAdapter struct {
	id          string // what /v1/info answers as; "fake" when empty
	noRefund    bool
	settings    string // the "settings" array of /v1/info; the default three when empty
	mu          sync.Mutex
	status      map[string]addons.Status
	invoices    []addons.InvoiceRequest
	refunds     []string
	refundCalls []string
}

func (f *fakeAdapter) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Authorization") != "Bearer adapter-token" {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	var in struct {
		addons.InvoiceRequest
		ExternalID string              `json:"external_id"`
		Headers    map[string][]string `json:"headers"`
		Body       string              `json:"body"`
	}
	_ = json.NewDecoder(r.Body).Decode(&in)
	fail := func(status int, code string) {
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(map[string]string{"code": code, "message": "refused"})
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	switch r.URL.Path {
	case "/v1/info":
		id := f.id
		if id == "" {
			id = "fake"
		}
		caps := []string{"webhook"}
		if !f.noRefund {
			caps = append(caps, "refund")
		}
		capsJSON, _ := json.Marshal(caps)
		fields := f.settings
		if fields == "" {
			fields = `[{"key":"shop_id","type":"string","required":true,"pattern":"^[0-9]+$"},{"key":"secret_key","type":"string","secret":true,"required":true},{"key":"testnet","type":"bool"}]`
		}
		_, _ = io.WriteString(w, `{"id":"`+id+`","protocol":1,"version":"1.0.0","name":{"en":"Fake"},"currencies":["RUB"],"capabilities":`+string(capsJSON)+`,
			"settings":`+fields+`}`)
		return
	}
	if in.Settings["secret_key"] != adapterSecret {
		fail(http.StatusUnprocessableEntity, "bad_credentials")
		return
	}
	switch r.URL.Path {
	case "/v1/check":
		_, _ = io.WriteString(w, `{}`)
	case "/v1/invoices":
		f.invoices = append(f.invoices, in.InvoiceRequest)
		ext := "fk-" + strconv.FormatInt(in.PaymentID, 10)
		f.status[ext] = addons.Status{Status: "pending", Amount: in.Amount, Currency: in.Currency}
		_ = json.NewEncoder(w).Encode(addons.Invoice{ExternalID: ext, PayURL: "https://pay.example/" + ext})
	case "/v1/status":
		st, ok := f.status[in.ExternalID]
		if !ok {
			fail(http.StatusNotFound, "not_found")
			return
		}
		_ = json.NewEncoder(w).Encode(st)
	case "/v1/refund":
		f.refundCalls = append(f.refundCalls, in.IdempotencyKey)
		if f.status["refund:"+in.IdempotencyKey].Status == "" {
			f.refunds = append(f.refunds, in.ExternalID)
			f.status["refund:"+in.IdempotencyKey] = addons.Status{Status: "refunded"}
		}
	case "/v1/webhook":
		if len(in.Headers["X-Sig"]) != 1 || in.Headers["X-Sig"][0] != "ok" {
			fail(http.StatusBadRequest, "bad_request")
			return
		}
		body, _ := base64.StdEncoding.DecodeString(in.Body)
		var n struct {
			ID string `json:"id"`
		}
		_ = json.Unmarshal(body, &n)
		_ = json.NewEncoder(w).Encode(map[string]string{"external_id": n.ID})
	}
}

func TestExpiredDiscountedAddonPaymentIsRefunded(t *testing.T) {
	e, fa := addonEnv(t)
	ctx := context.Background()
	if _, err := e.s.SetAddonConfig(ctx, "fake", true, addons.Settings{"shop_id": "12", "secret_key": adapterSecret}); err != nil {
		t.Fatal(err)
	}
	pc, err := e.st.Q.CreatePromoCode(ctx, db.CreatePromoCodeParams{Code: "SHORT", Type: "percent", Value: 10,
		Currency: "RUB", PerUserLimit: 1, DiscountTtl: 60, TariffIds: "[]", Enabled: 1, CreatedAt: e.now.Unix()})
	must(t, err)
	e.s.d.Promo = promo.New(e.st, func() time.Time { return e.now })
	p, err := e.s.Invoice(ctx, InvoiceRequest{TgID: 555, TariffID: e.sale.ID, Provider: "addon:fake", PromoCode: pc.Code})
	must(t, err)
	if p.Amount >= e.sale.PriceRub.Int64 {
		t.Fatalf("promo discount was not applied: amount %d", p.Amount)
	}
	e.now = e.now.Add(8 * 24 * time.Hour)
	if _, err := e.st.Q.SetPaymentStatus(ctx, db.SetPaymentStatusParams{NewStatus: "expired", ID: p.ID, OldStatus: "pending"}); err != nil {
		t.Fatal(err)
	}
	if err := e.s.d.Promo.ReleasePayment(ctx, p.ID); err != nil {
		t.Fatal(err)
	}
	fa.set(p.ExternalID.String, func(s *addons.Status) { s.Status, s.Amount = "paid", p.Amount })
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	workers := []*Service{e.s, New(e.s.d)} // distinct process-local locks share only SQLite
	for _, worker := range workers {
		wg.Add(1)
		go func(s *Service) {
			defer wg.Done()
			errs <- s.checkAddon(ctx, p.Provider, p.ExternalID.String)
		}(worker)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("late paid invoice should be safely refunded: %v", err)
		}
	}
	if got := e.payment(p.ID); got.Status != "refunded" || !got.RefundedAt.Valid || got.RefundedAt.Int64 != e.now.Unix() {
		t.Fatalf("payment status/refunded_at = %q/%+v, want refunded with current timestamp", got.Status, got.RefundedAt)
	}
	if len(fa.refunds) != 1 || fa.refunds[0] != p.ExternalID.String {
		t.Fatalf("refund calls = %v", fa.refunds)
	}
	if len(fa.refundCalls) != 1 || fa.refundCalls[0] != "mikan-promo-late-"+strconv.FormatInt(p.ID, 10) {
		t.Fatalf("refund idempotency keys = %v", fa.refundCalls)
	}
	if e.users() != 0 {
		t.Fatalf("late payment created a subscription")
	}
	r, err := e.s.d.Promo.GetPaymentRedemption(ctx, p.ID)
	if err != nil || r.Status != "released" {
		t.Fatalf("reservation status = %q, err=%v; want released", r.Status, err)
	}
}

func TestDiscountedAddonRequiresRefundCapability(t *testing.T) {
	e, fa := addonEnv(t)
	fa.noRefund = true
	ctx := context.Background()
	if _, err := e.s.SetAddonConfig(ctx, "fake", true, addons.Settings{"shop_id": "12", "secret_key": adapterSecret}); err != nil {
		t.Fatal(err)
	}
	pc, err := e.st.Q.CreatePromoCode(ctx, db.CreatePromoCodeParams{Code: "NOREFUND", Type: "percent", Value: 10,
		Currency: "RUB", PerUserLimit: 1, TariffIds: "[]", Enabled: 1, CreatedAt: e.now.Unix()})
	must(t, err)
	e.s.d.Promo = promo.New(e.st, func() time.Time { return e.now })
	if _, err := e.s.Invoice(ctx, InvoiceRequest{TgID: 555, TariffID: e.sale.ID, Provider: "addon:fake", PromoCode: pc.Code}); !errors.Is(err, promo.ErrRefundUnsupported) {
		t.Fatalf("discounted invoice accepted without refund support: %v", err)
	}
	var n int
	if err := e.st.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM payments`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("unsupported provider created %d payment(s)", n)
	}
}

func TestCanceledDiscountedAddonReleasesPromoReservation(t *testing.T) {
	e, fa := addonEnv(t)
	ctx := context.Background()
	if _, err := e.s.SetAddonConfig(ctx, "fake", true, addons.Settings{"shop_id": "12", "secret_key": adapterSecret}); err != nil {
		t.Fatal(err)
	}
	pc, err := e.st.Q.CreatePromoCode(ctx, db.CreatePromoCodeParams{Code: "CANCEL", Type: "percent", Value: 10,
		Currency: "RUB", PerUserLimit: 1, DiscountTtl: 60, TariffIds: "[]", Enabled: 1, CreatedAt: e.now.Unix()})
	must(t, err)
	e.s.d.Promo = promo.New(e.st, func() time.Time { return e.now })
	p, err := e.s.Invoice(ctx, InvoiceRequest{TgID: 555, TariffID: e.sale.ID, Provider: "addon:fake", PromoCode: pc.Code})
	must(t, err)
	fa.set(p.ExternalID.String, func(s *addons.Status) { s.Status = "canceled" })
	if err := e.s.checkAddon(ctx, p.Provider, p.ExternalID.String); err != nil {
		t.Fatal(err)
	}
	if got := e.payment(p.ID); got.Status != "failed" {
		t.Fatalf("payment status = %q, want failed", got.Status)
	}
	r, err := e.s.d.Promo.GetPaymentRedemption(ctx, p.ID)
	if err != nil || r.Status != "released" {
		t.Fatalf("reservation status = %q, err=%v; want released", r.Status, err)
	}
	stored, err := e.st.Q.GetPromoCodeByCode(ctx, pc.Code)
	if err != nil || stored.UsedCount != 0 {
		t.Fatalf("promo uses = %d, err=%v; want reservation released", stored.UsedCount, err)
	}
}

func (f *fakeAdapter) set(ext string, fn func(*addons.Status)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	st := f.status[ext]
	fn(&st)
	f.status[ext] = st
}

// addonEnv is newEnv with the adapter "fake" installed and running.
func addonEnv(t *testing.T) (*env, *fakeAdapter) {
	e, fa, dir := adapterEnv(t, "fake")
	runAdapter(t, dir, "fake", fa)
	return e, fa
}

// adapterEnv is newEnv with the marketplace on a data directory where nothing runs yet,
// and an adapter answering as id for runAdapter to start.
func adapterEnv(t *testing.T, id string) (*env, *fakeAdapter, string) {
	e := newEnv(t)
	fa := &fakeAdapter{id: id, status: map[string]addons.Status{}}
	dir := t.TempDir()
	e.s.d.Addons = addons.New(dir, "", "0.4.3", slog.New(slog.NewTextHandler(io.Discard, nil)), func() time.Time { return e.now })
	e.s.d.SubBase = func(context.Context) string { return "https://panel.example:2053/sub" }
	return e, fa, dir
}

// runAdapter is the host having installed id: its state lists it running.
func runAdapter(t *testing.T, dir, id string, fa *fakeAdapter) {
	srv := httptest.NewServer(fa)
	t.Cleanup(srv.Close)
	state, _ := json.Marshal(addons.State{Adapters: map[string]addons.Installed{id: {Version: "1.0.0", Digest: "sha256:1", Status: "running",
		Listen: strings.TrimPrefix(srv.URL, "http://"), Token: "adapter-token"}}})
	must(t, os.MkdirAll(filepath.Join(dir, "addons"), 0o755))
	must(t, os.WriteFile(filepath.Join(dir, "addons", "state.json"), state, 0o600))
}

// The adapter's settings are checked against its own description and with the provider;
// the secret is write-only and kept when left empty.
func TestAddonSettings(t *testing.T) {
	e, _ := addonEnv(t)
	ctx := context.Background()
	if av := e.s.Available(ctx); len(av.Addons) != 0 {
		t.Fatalf("offered before it was set up: %v", av.Addons)
	}
	var se *AddonSettingError
	if _, err := e.s.SetAddonConfig(ctx, "fake", false, addons.Settings{"shop_id": "12"}); err != nil {
		t.Fatalf("half filled in, off: %v", err)
	}
	if _, err := e.s.SetAddonConfig(ctx, "fake", true, nil); !errors.As(err, &se) || se.Key != "secret_key" {
		t.Fatalf("on without the secret: %v", err)
	}
	if _, err := e.s.SetAddonConfig(ctx, "fake", true, addons.Settings{"shop_id": "12a", "secret_key": adapterSecret}); !errors.As(err, &se) || se.Key != "shop_id" {
		t.Fatalf("pattern: %v", err)
	}
	if _, err := e.s.SetAddonConfig(ctx, "fake", true, addons.Settings{"shop_id": "12", "testnet": "yes", "secret_key": adapterSecret}); !errors.As(err, &se) || se.Key != "testnet" {
		t.Fatalf("bool as a string: %v", err)
	}
	var ae *addons.Error
	if _, err := e.s.SetAddonConfig(ctx, "fake", true, addons.Settings{"shop_id": "12", "secret_key": "wrong"}); !errors.As(err, &ae) || ae.Code != "bad_credentials" {
		t.Fatalf("refused by the provider: %v", err)
	}
	if _, err := e.s.SetAddonConfig(ctx, "fake", true, addons.Settings{"shop_id": "12", "secret_key": adapterSecret, "extra": "x"}); err != nil {
		t.Fatal(err)
	}
	c, err := e.s.SetAddonConfig(ctx, "fake", true, addons.Settings{"shop_id": "34", "secret_key": ""})
	if err != nil || c.Values["secret_key"] != adapterSecret || c.Values["shop_id"] != "34" || c.Values["extra"] != nil {
		t.Fatalf("kept secret: %+v %v", c, err)
	}
	if av := e.s.Available(ctx); len(av.Addons) != 1 || av.Addons[0] != "fake" || !av.Rub() {
		t.Fatalf("available: %+v", av)
	}
	if _, err := e.s.SetAddonConfig(ctx, "ghost", true, nil); !errors.Is(err, addons.ErrNotInstalled) {
		t.Fatalf("not installed: %v", err)
	}
	// Switched off, it is not offered; its settings stay without a check.
	if _, err := e.s.SetAddonConfig(ctx, "fake", false, addons.Settings{}); err != nil {
		t.Fatal(err)
	}
	if av := e.s.Available(ctx); len(av.Addons) != 0 {
		t.Fatalf("off but offered: %v", av.Addons)
	}
	if _, err := e.s.Invoice(ctx, InvoiceRequest{TgID: 555, TariffID: e.sale.ID, Provider: "addon:fake"}); !errors.Is(err, ErrProviderOff) {
		t.Fatalf("invoice while off: %v", err)
	}
}

// A payment through an adapter: the webhook only makes the panel ask, the status the
// adapter reports must match the invoice, and the payment applies once.
func TestAddonPayment(t *testing.T) {
	e, fa := addonEnv(t)
	ctx := context.Background()
	if _, err := e.s.SetAddonConfig(ctx, "fake", true, addons.Settings{"shop_id": "12", "secret_key": adapterSecret}); err != nil {
		t.Fatal(err)
	}
	p := e.invoice(555, 0, "addon:fake")
	if p.Amount != 19900 || p.Currency != "RUB" || p.PayUrl != "https://pay.example/"+p.ExternalID.String {
		t.Fatalf("invoice: %+v", p)
	}
	if inv := fa.invoices[0]; inv.WebhookURL != "https://panel.example:2053/sub/pay/addon/fake/"+e.token || inv.IdempotencyKey != "mikan-"+strconv.FormatInt(p.ID, 10) ||
		inv.ReturnURL != "https://t.me/mikan_test_bot" {
		t.Fatalf("invoice request: %+v", inv)
	}
	if again := e.invoice(555, 0, "addon:fake"); again.ID != p.ID {
		t.Fatal("a second invoice for the same purchase")
	}
	note := []byte(`{"id":"` + p.ExternalID.String + `"}`)
	ok := map[string]string{"X-Sig": "ok"}
	if code := e.hook("addon/fake", "wrong-token-wrong-token-wrong-tok", "203.0.113.9", note, ok); code != http.StatusNotFound {
		t.Fatalf("wrong token: %d", code)
	}
	if code := e.hook("addon/../fake", e.token, "203.0.113.9", note, ok); code != http.StatusNotFound {
		t.Fatalf("bad id: %d", code)
	}
	if code := e.hook("addon/fake", e.token, "203.0.113.9", note, nil); code != http.StatusForbidden {
		t.Fatalf("forged: %d", code)
	}
	if code := e.hook("addon/fake", e.token, "203.0.113.9", note, ok); code != http.StatusOK || e.payment(p.ID).Status != "pending" {
		t.Fatalf("unconfirmed: %d %s", code, e.payment(p.ID).Status)
	}
	fa.set(p.ExternalID.String, func(s *addons.Status) { s.Status, s.Amount = "paid", 100 })
	e.hook("addon/fake", e.token, "203.0.113.9", note, ok)
	if e.payment(p.ID).Status != "pending" {
		t.Fatal("a smaller amount was accepted")
	}
	fa.set(p.ExternalID.String, func(s *addons.Status) { s.Amount = 19900 })
	before := e.users()
	for range 3 {
		if code := e.hook("addon/fake", e.token, "203.0.113.9", note, ok); code != http.StatusOK {
			t.Fatalf("webhook: %d", code)
		}
	}
	if e.payment(p.ID).Status != "applied" || e.users() != before+1 || e.tg.told() != 1 {
		t.Fatalf("applied: %s users %d→%d told %d", e.payment(p.ID).Status, before, e.users(), e.tg.told())
	}
	if code := e.hook("addon/fake", e.token, "203.0.113.9", []byte(`{"id":"fk-999"}`), ok); code != http.StatusOK {
		t.Fatalf("unknown invoice: %d", code)
	}

	// Without a webhook the reconcile pass finds it; canceled ones fail.
	q := e.invoice(556, 0, "addon:fake")
	c := e.invoice(557, 0, "addon:fake")
	fa.set(q.ExternalID.String, func(s *addons.Status) { s.Status = "paid" })
	fa.set(c.ExternalID.String, func(s *addons.Status) { s.Status = "canceled" })
	e.s.Reconcile(ctx)
	if e.payment(q.ID).Status != "applied" || e.payment(c.ID).Status != "failed" {
		t.Fatalf("reconcile: %s %s", e.payment(q.ID).Status, e.payment(c.ID).Status)
	}
	if strings.Contains(e.logs.String(), adapterSecret) {
		t.Fatal("the adapter's secret is in the log")
	}
}

// A provider that adds its fee on top for the buyer (Platega adds 5% once a method is
// picked) reports more than the invoice: that is paid. Less, or another currency, is not.
func TestAddonPaymentWithBuyerFee(t *testing.T) {
	e, fa := addonEnv(t)
	ctx := context.Background()
	if _, err := e.s.SetAddonConfig(ctx, "fake", true, addons.Settings{"shop_id": "12", "secret_key": adapterSecret}); err != nil {
		t.Fatal(err)
	}
	short := e.invoice(555, 0, "addon:fake")
	foreign := e.invoice(556, 0, "addon:fake")
	fee := e.invoice(557, 0, "addon:fake")
	fa.set(short.ExternalID.String, func(s *addons.Status) { s.Status, s.Amount = "paid", 19899 })
	fa.set(foreign.ExternalID.String, func(s *addons.Status) { s.Status, s.Amount, s.Currency = "paid", 20895, "USD" })
	fa.set(fee.ExternalID.String, func(s *addons.Status) { s.Status, s.Amount = "paid", 20895 })
	e.s.Reconcile(ctx)
	if e.payment(short.ID).Status != "pending" || e.payment(foreign.ID).Status != "pending" {
		t.Fatalf("a short or foreign payment was accepted: %s %s", e.payment(short.ID).Status, e.payment(foreign.ID).Status)
	}
	if e.payment(fee.ID).Status != "applied" {
		t.Fatalf("with the buyer's fee: %s", e.payment(fee.ID).Status)
	}
	if !strings.Contains(e.logs.String(), "adapter amount differs") {
		t.Fatal("a refused amount is not logged")
	}
}

// Each type of a setting takes what its description allows and nothing else; a type the
// panel does not know takes nothing.
func TestAddonFieldValues(t *testing.T) {
	one, ten := int64(1), int64(10)
	sel := addons.Field{Type: "select", Options: []addons.Option{{Value: "a"}, {Value: "b"}}}
	num := addons.Field{Type: "number", Min: &one, Max: &ten}
	free := addons.Field{Type: "number"}
	str := addons.Field{Type: "string", Pattern: "^[a-z]+$"}
	txt := addons.Field{Type: "text", Pattern: "^[a-z]+$"} // a pattern is for string only
	code := addons.Field{Type: "code"}
	for _, c := range []struct {
		name string
		f    addons.Field
		in   any
		want any
		ok   bool
	}{
		{"bool true", addons.Field{Type: "bool"}, true, true, true},
		{"bool nil", addons.Field{Type: "bool"}, nil, nil, true},
		{"bool string", addons.Field{Type: "bool"}, "yes", nil, false},

		{"select listed", sel, "b", "b", true},
		{"select unset", sel, "", nil, true},
		{"select nil", sel, nil, nil, true},
		{"select foreign", sel, "c", nil, false},
		{"select case", sel, "A", nil, false},
		{"select number", sel, 1.0, nil, false},
		{"select without options", addons.Field{Type: "select"}, "a", nil, false},

		{"number in range", num, 5.0, int64(5), true},
		{"number low edge", num, 1.0, int64(1), true},
		{"number high edge", num, 10.0, int64(10), true},
		{"number below", num, 0.0, nil, false},
		{"number above", num, 11.0, nil, false},
		{"number fraction", num, 2.5, nil, false},
		{"number string", num, "5", nil, false},
		{"number bool", num, true, nil, false},
		{"number unset", num, "", nil, true},
		{"number nil", num, nil, nil, true},
		{"number negative without bounds", free, -7.0, int64(-7), true},
		{"number too big for a float", free, 1e300, nil, false},
		{"number infinity", free, math.Inf(1), nil, false},
		{"number NaN", free, math.NaN(), nil, false},
		{"number json.Number", free, json.Number("42"), int64(42), true},
		{"number json.Number fraction", free, json.Number("4.2"), nil, false},

		{"string pattern ok", str, " abc ", "abc", true},
		{"string pattern no", str, "ABC", nil, false},
		{"string unset", str, "  ", nil, true},
		{"string at the limit", str, strings.Repeat("a", 4096), strings.Repeat("a", 4096), true},
		{"string over the limit", str, strings.Repeat("a", 4097), nil, false},
		{"string number", str, 5.0, nil, false},

		{"text lines", txt, "Hello\nWorld", "Hello\nWorld", true},
		{"text ignores the pattern", txt, "ABC 1\nDEF", "ABC 1\nDEF", true},
		{"text characters not bytes", txt, strings.Repeat("я", 4096), strings.Repeat("я", 4096), true},
		{"text over the limit", txt, strings.Repeat("я", 4097), nil, false},
		{"text unset", txt, "\n", nil, true},
		{"text bool", txt, true, nil, false},

		{"code keeps its indentation", code, "  if x:\n    y()\n", "  if x:\n    y()\n", true},
		{"code at 64 KiB", code, strings.Repeat("x", addons.MaxCode), strings.Repeat("x", addons.MaxCode), true},
		{"code over 64 KiB", code, strings.Repeat("x", addons.MaxCode+1), nil, false},
		{"code blank", code, " \n\t", nil, true},
		{"code number", code, 1.0, nil, false},
		{"code not utf-8", code, "a\xffb", nil, false},

		{"unknown type", addons.Field{Type: "color"}, "#fff", nil, false},
		{"unknown type unset", addons.Field{Type: "color"}, nil, nil, false},
		{"no type", addons.Field{}, "x", nil, false},
	} {
		got, ok := fieldValue(c.f, c.in)
		if ok != c.ok || (ok && got != c.want) {
			t.Errorf("%s: fieldValue(%v) = %v, %v; want %v, %v", c.name, c.in, got, ok, c.want, c.ok)
		}
	}
}

const allTypesSettings = `[
	{"key":"secret_key","type":"string","secret":true,"required":true},
	{"key":"mode","type":"select","options":[{"value":"a","label":{"en":"A"}},{"value":"b","label":{"en":"B"}}],"default":"a","help":{"en":"How"}},
	{"key":"limit","type":"number","min":1,"max":10,"default":3},
	{"key":"greeting","type":"text"},
	{"key":"script","type":"code"},
	{"key":"token","type":"text","secret":true},
	{"key":"color","type":"color"}]`

// A form with every type is saved as the admin filled it in; a secret text field is
// write-only like a secret string; a field of an unknown type is never saved but does not
// stop the others.
func TestAddonSettingsOfEveryType(t *testing.T) {
	e, fa, dir := adapterEnv(t, "fake")
	fa.settings = allTypesSettings
	runAdapter(t, dir, "fake", fa)
	ctx := context.Background()
	bad := func(values addons.Settings, key string) {
		t.Helper()
		var se *AddonSettingError
		if _, err := e.s.SetAddonConfig(ctx, "fake", false, values); !errors.As(err, &se) || se.Key != key {
			t.Fatalf("%v: want a refusal of %s, got %v", values, key, err)
		}
	}
	bad(addons.Settings{"mode": "c"}, "mode")
	bad(addons.Settings{"limit": 11.0}, "limit")
	bad(addons.Settings{"limit": 0.0}, "limit")
	bad(addons.Settings{"limit": 2.5}, "limit")
	bad(addons.Settings{"limit": "3"}, "limit")
	bad(addons.Settings{"greeting": strings.Repeat("x", 4097)}, "greeting")
	bad(addons.Settings{"script": strings.Repeat("x", addons.MaxCode+1)}, "script")
	bad(addons.Settings{"script": 5.0}, "script")
	bad(addons.Settings{"color": "#fff"}, "color")

	script := "def main():\n    return 1\n"
	c, err := e.s.SetAddonConfig(ctx, "fake", false, addons.Settings{"secret_key": adapterSecret, "mode": "b", "limit": 7.0, "greeting": " hi\nthere ", "script": script, "token": "t0k", "color": nil})
	if err != nil {
		t.Fatal(err)
	}
	if c.Values["mode"] != "b" || c.Values["limit"] != int64(7) || c.Values["greeting"] != "hi\nthere" || c.Values["script"] != script || c.Values["token"] != "t0k" {
		t.Fatalf("saved: %+v", c.Values)
	}
	if _, ok := c.Values["color"]; ok {
		t.Fatalf("a field of an unknown type was kept: %+v", c.Values)
	}
	// What was saved comes back from the store as it went in (numbers are JSON there).
	back, err := e.s.AddonConfig(ctx, "fake")
	if err != nil || back.Values["limit"] != float64(7) || back.Values["script"] != script {
		t.Fatalf("stored: %+v %v", back.Values, err)
	}
	// Left out, a setting keeps nothing it was not given: the secret text stays when empty,
	// the others are cleared when emptied.
	c, err = e.s.SetAddonConfig(ctx, "fake", false, addons.Settings{"token": "", "mode": "", "limit": "", "script": "  ", "greeting": ""})
	if err != nil {
		t.Fatal(err)
	}
	if c.Values["token"] != "t0k" || c.Values["secret_key"] != adapterSecret {
		t.Fatalf("secrets not kept: %+v", c.Values)
	}
	for _, k := range []string{"mode", "limit", "script", "greeting"} {
		if _, ok := c.Values[k]; ok {
			t.Fatalf("%s not cleared: %+v", k, c.Values)
		}
	}
	// Switching on works around the unknown field while it is not required, and the
	// provider sees the settings of the known ones.
	if _, err := e.s.SetAddonConfig(ctx, "fake", true, addons.Settings{"mode": "a"}); err != nil {
		t.Fatalf("on: %v", err)
	}
}

// A required field of an unknown type cannot be filled in, so the addon cannot be switched on.
func TestAddonRequiredFieldOfUnknownType(t *testing.T) {
	e, fa, dir := adapterEnv(t, "fake")
	fa.settings = `[{"key":"secret_key","type":"string","secret":true,"required":true},{"key":"color","type":"color","required":true}]`
	runAdapter(t, dir, "fake", fa)
	var se *AddonSettingError
	if _, err := e.s.SetAddonConfig(context.Background(), "fake", true, addons.Settings{"secret_key": adapterSecret}); !errors.As(err, &se) || se.Key != "color" {
		t.Fatalf("on with an unknown required field: %v", err)
	}
	if _, err := e.s.SetAddonConfig(context.Background(), "fake", false, addons.Settings{"secret_key": adapterSecret}); err != nil {
		t.Fatalf("saved off: %v", err)
	}
}
