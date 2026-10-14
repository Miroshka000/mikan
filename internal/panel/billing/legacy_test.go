package billing

import (
	"context"
	"database/sql"
	"net/http"
	"testing"

	"mikan/internal/panel/addons"
	"mikan/internal/panel/store/db"
)

// YooKassa notifies the URL its dashboard has had since 0.4.0; the adapter that took over
// the name answers it. A wrong token is still the bare 404.
func TestLegacyWebhookURL(t *testing.T) {
	e, fa, dir := adapterEnv(t, "yookassa")
	ctx := context.Background()
	runAdapter(t, dir, legacyYooKassa, fa)
	_, err := e.s.SetAddonConfig(ctx, legacyYooKassa, true, addons.Settings{"shop_id": "12", "secret_key": adapterSecret})
	must(t, err)
	p, err := e.st.Q.CreatePayment(ctx, db.CreatePaymentParams{Provider: AdapterOf(legacyYooKassa), Payload: "legacy-payload", TgID: 555, Kind: "new",
		TariffID: sql.NullInt64{Int64: e.sale.ID, Valid: true}, TariffName: e.sale.Name, Amount: 19900, Currency: "RUB", CreatedAt: e.now.Unix()})
	must(t, err)
	must(t, e.st.Q.SetPaymentInvoice(ctx, db.SetPaymentInvoiceParams{ExternalID: sql.NullString{String: "yk-1", Valid: true}, PayUrl: "https://yoomoney.ru/x", ID: p.ID}))

	fa.set("yk-1", func(s *addons.Status) { s.Status, s.Amount, s.Currency = "paid", 19900, "RUB" })
	if code := e.hook(legacyYooKassa, e.token, "185.71.76.5", []byte(`{"id":"yk-1"}`), map[string]string{"X-Sig": "ok"}); code != http.StatusOK {
		t.Fatalf("the old webhook URL: %d", code)
	}
	if got := e.payment(p.ID); got.Status != "applied" {
		t.Fatalf("the payment after the notification: %s", got.Status)
	}
	if code := e.hook(legacyCryptoBot, "wrong-token-wrong-token-wrong-tok", "203.0.113.9", nil, nil); code != http.StatusNotFound {
		t.Fatalf("the old URL with a wrong token: %d", code)
	}
}

func TestAdapterOf(t *testing.T) {
	if AdapterOf("yookassa") != "addon:yookassa" || AdapterOf(Stars) != Stars || AdapterOf("addon:x") != "addon:x" {
		t.Fatal("AdapterOf")
	}
}
