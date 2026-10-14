package billing

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"mikan/internal/panel/addons"
	"mikan/internal/panel/promo"
	"mikan/internal/panel/settings"
	"mikan/internal/panel/store/db"
)

// Payments through marketplace adapters (internal/panel/addons): a provider is
// "addon:<id>". The panel keeps the settings and trusts only the invoice status it asks
// for itself, compared with the payment it created, never a webhook alone.

// AddonPrefix starts the provider name of a payment made through an adapter.
const AddonPrefix = "addon:"

// AddonID is the adapter of a provider name, "" for a built-in provider.
func AddonID(provider string) string {
	id, ok := strings.CutPrefix(provider, AddonPrefix)
	if !ok || !addons.ValidID(id) {
		return ""
	}
	return id
}

// AddonConfig is what the admin set for an adapter: on or off, and its settings (secrets
// included; the API never sends those back).
type AddonConfig struct {
	Enabled bool            `json:"enabled"`
	Values  addons.Settings `json:"values"`
}

// AddonSettingError: a setting does not fit the adapter's description of it, or the
// adapter takes no rubles (Key "currency").
type AddonSettingError struct{ Key string }

func (e *AddonSettingError) Error() string { return "addon_setting: " + e.Key }

var errAddonOff = errors.New("addon_off")

func addonKey(id string) string { return "addon." + id }

func (s *Service) AddonConfig(ctx context.Context, id string) (AddonConfig, error) {
	c, _, err := settings.Get[AddonConfig](ctx, s.d.Settings, addonKey(id))
	if c.Values == nil {
		c.Values = addons.Settings{}
	}
	return c, err
}

// SetAddonConfig checks the settings against the adapter's own description and, to switch
// it on, that they are complete and the provider takes them; then keeps them. A secret left empty keeps the
// saved one, as the built-in providers do.
func (s *Service) SetAddonConfig(ctx context.Context, id string, enabled bool, values addons.Settings) (AddonConfig, error) {
	if s.d.Addons == nil {
		return AddonConfig{}, addons.ErrUnavailable
	}
	info, err := s.d.Addons.Info(ctx, id)
	if err != nil {
		return AddonConfig{}, err
	}
	old, err := s.AddonConfig(ctx, id)
	if err != nil {
		return AddonConfig{}, err
	}
	next := AddonConfig{Enabled: enabled, Values: addons.Settings{}}
	for _, f := range info.Settings {
		v, given := values[f.Key]
		if !addons.KnownField(f.Type) {
			// A type this panel cannot check is not saved. It is refused only if the
			// request sets it, so the rest of the form still saves; switching on still
			// needs it when it is required.
			if v != nil {
				return AddonConfig{}, &AddonSettingError{Key: f.Key}
			}
			continue
		}
		var ok bool
		if !given || f.Secret && v == "" {
			v = old.Values[f.Key]
		}
		if v, ok = fieldValue(f, v); !ok {
			return AddonConfig{}, &AddonSettingError{Key: f.Key}
		}
		if v != nil {
			next.Values[f.Key] = v
		}
	}
	if enabled {
		// Switched off, the settings may wait half filled in.
		for _, f := range info.Settings {
			if f.Required && next.Values[f.Key] == nil {
				return AddonConfig{}, &AddonSettingError{Key: f.Key}
			}
		}
		if !info.Takes("RUB") {
			return AddonConfig{}, &AddonSettingError{Key: "currency"}
		}
		c, err := s.d.Addons.Client(id)
		if err != nil {
			return AddonConfig{}, err
		}
		if err := c.Check(ctx, next.Values); err != nil {
			return AddonConfig{}, err
		}
	}
	return next, settings.Set(ctx, s.d.Settings, addonKey(id), next)
}

// fieldValue checks one setting by the adapter's description; nil is "not set". A type the
// panel does not know never passes: it could not say what the value may be.
func fieldValue(f addons.Field, v any) (any, bool) {
	switch f.Type {
	case addons.FieldBool:
		b, ok := v.(bool)
		if v == nil {
			return nil, true
		}
		if !ok {
			return nil, false
		}
		return b, true
	case addons.FieldSelect:
		str, ok := v.(string)
		if v != nil && !ok {
			return nil, false
		}
		if str == "" {
			return nil, true
		}
		if !slices.ContainsFunc(f.Options, func(o addons.Option) bool { return o.Value == str }) {
			return nil, false
		}
		return str, true
	case addons.FieldNumber:
		return numberValue(f, v)
	case addons.FieldString, addons.FieldText:
		str, ok := v.(string)
		if v != nil && !ok {
			return nil, false
		}
		str = strings.TrimSpace(str)
		switch {
		case str == "":
			return nil, true
		case utf8.RuneCountInString(str) > maxAddonText:
			return nil, false
		case f.Type == addons.FieldString && f.Pattern != "":
			re, err := regexp.Compile(f.Pattern)
			if err != nil || !re.MatchString(str) {
				return nil, false
			}
		}
		return str, true
	case addons.FieldCode:
		// Not trimmed: the indentation of a script is part of it.
		str, ok := v.(string)
		if v != nil && !ok {
			return nil, false
		}
		switch {
		case strings.TrimSpace(str) == "":
			return nil, true
		case len(str) > addons.MaxCode, !utf8.ValidString(str):
			return nil, false
		}
		return str, true
	}
	return nil, false
}

// maxAddonText is the longest value of a string or text setting, in characters.
const maxAddonText = 4096

// numberValue reads an integer setting: a JSON number without a fraction (or an empty
// string for "not set"), within the field's Min and Max. Stored as int64.
func numberValue(f addons.Field, v any) (any, bool) {
	var n int64
	switch x := v.(type) {
	case nil:
		return nil, true
	case string:
		if x == "" {
			return nil, true
		}
		return nil, false
	case float64:
		// 2^53 is where a float stops holding every integer.
		if x != math.Trunc(x) || math.Abs(x) > 1<<53 {
			return nil, false
		}
		n = int64(x)
	case int:
		n = int64(x)
	case int64:
		n = x
	case json.Number:
		i, err := x.Int64()
		if err != nil {
			return nil, false
		}
		n = i
	default:
		return nil, false
	}
	if f.Min != nil && n < *f.Min || f.Max != nil && n > *f.Max {
		return nil, false
	}
	return n, true
}

// availableAddons are the adapters that take rubles now: installed and running, switched
// on, with their required settings. Their descriptions are cached, so this asks nothing.
func (s *Service) availableAddons(ctx context.Context) []string {
	if s.d.Addons == nil {
		return nil
	}
	st, err := s.d.Addons.State()
	if err != nil {
		return nil
	}
	var out []string
	for id, a := range st.Of(addons.KindPayment) {
		if a.Status != "running" {
			continue
		}
		c, err := s.AddonConfig(ctx, id)
		if err != nil || !c.Enabled {
			continue
		}
		info, err := s.d.Addons.Info(ctx, id)
		if err != nil || !info.Takes("RUB") || !complete(info, c) {
			continue
		}
		out = append(out, id)
	}
	slices.Sort(out)
	return out
}

func complete(info addons.Info, c AddonConfig) bool {
	for _, f := range info.Settings {
		if f.Required && c.Values[f.Key] == nil {
			return false
		}
	}
	return true
}

func (s *Service) addonClient(ctx context.Context, id string) (*addons.Client, AddonConfig, error) {
	if s.d.Addons == nil {
		return nil, AddonConfig{}, errAddonOff
	}
	c, err := s.AddonConfig(ctx, id)
	if err != nil {
		return nil, c, err
	}
	cl, err := s.d.Addons.Client(id)
	return cl, c, err
}

// Discounted payments can arrive after their promo reservation expires. Accept them only
// from providers that advertise refunds, so Mikan can return money without issuing service
// for a discount that is no longer reserved.
func (s *Service) validatePromoProvider(ctx context.Context, provider string) error {
	if provider == Stars {
		return nil
	}
	id := AddonID(provider)
	if id == "" || s.d.Addons == nil {
		return promo.ErrRefundUnsupported
	}
	info, err := s.d.Addons.Info(ctx, id)
	if err != nil {
		return err
	}
	if !info.Can("refund") {
		return promo.ErrRefundUnsupported
	}
	return nil
}

// AddonName is the adapter's own name for buyers in lang; its id while it does not answer.
func (s *Service) AddonName(ctx context.Context, id, lang string) string {
	if s.d.Addons != nil {
		if info, err := s.d.Addons.Info(ctx, id); err == nil {
			if n := info.Name.In(lang); n != "" {
				return n
			}
		}
	}
	return id
}

// AddonWebhookURL is where the provider sends its notifications for an adapter: the admin
// enters it in the provider's dashboard, as for the built-in providers.
func (s *Service) AddonWebhookURL(ctx context.Context, id, subBase string) string {
	tok, err := s.WebhookToken(ctx)
	if err != nil || subBase == "" {
		return ""
	}
	return subBase + "/pay/addon/" + id + "/" + tok
}

func (s *Service) openAddonInvoice(ctx context.Context, p db.Payment, description string) (sql.NullString, string, error) {
	id := AddonID(p.Provider)
	cl, cfg, err := s.addonClient(ctx, id)
	if err != nil {
		return sql.NullString{}, "", err
	}
	// Where the buyer goes back after paying: the bot, or Telegram itself while it is off.
	ret, sub := "https://t.me", ""
	if tg := s.telegram(); tg != nil {
		if u := tg.BotURL(ctx); u != "" {
			ret = u
		}
	}
	if s.d.SubBase != nil {
		sub = s.d.SubBase(ctx)
	}
	inv, err := cl.CreateInvoice(ctx, addons.InvoiceRequest{Settings: cfg.Values, PaymentID: p.ID, IdempotencyKey: "mikan-" + strconv.FormatInt(p.ID, 10),
		Amount: p.Amount, Currency: p.Currency, Description: description, ReturnURL: ret, WebhookURL: s.AddonWebhookURL(ctx, id, sub)})
	if err != nil {
		return sql.NullString{}, "", err
	}
	return sql.NullString{String: inv.ExternalID, Valid: true}, inv.PayURL, nil
}

// addonWebhook hands the provider's notification to its adapter, then checks the invoice
// itself. A 200 goes back only once the payment is recorded, so the provider retries.
func (s *Service) addonWebhook(ctx context.Context, w http.ResponseWriter, r *http.Request, id string, body []byte) {
	cl, cfg, err := s.addonClient(ctx, id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	ext, err := cl.Webhook(ctx, cfg.Values, s.clientIP(r), r.Header, body)
	var ae *addons.Error
	switch {
	case errors.As(err, &ae) && ae.Status < 500:
		s.d.Log.Warn("billing: adapter refused a webhook", "addon", id, "code", ae.Code, "ip", s.clientIP(r))
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	case err != nil:
		http.Error(w, "retry", http.StatusServiceUnavailable)
		return
	}
	if ext != "" {
		if err := s.checkAddon(ctx, AddonPrefix+id, ext); err != nil && !errors.Is(err, ErrBadPayment) {
			http.Error(w, "retry", http.StatusServiceUnavailable)
			return
		}
	}
	w.WriteHeader(http.StatusOK)
}

// checkAddon asks the adapter about an invoice and records what it says: the currency must
// match the payment and the amount must cover it. More is the provider's fee charged to the
// buyer on top (Platega adds it once a method is picked); the invoice is ours, so it is paid.
// Only an invoice we do not have is ErrBadPayment: a database that failed to answer is
// returned as it is, so the webhook answers 503 and the provider sends it again.
func (s *Service) checkAddon(ctx context.Context, provider, ext string) error {
	pay, err := s.d.Store.Q.GetPaymentByExternal(ctx, db.GetPaymentByExternalParams{Provider: provider, ExternalID: sql.NullString{String: ext, Valid: true}})
	if errors.Is(err, sql.ErrNoRows) {
		return ErrBadPayment
	}
	if err != nil {
		return err
	}
	return s.checkAddonPayment(ctx, pay)
}

// checkAddonPayment is checkAddon for a payment already loaded.
func (s *Service) checkAddonPayment(ctx context.Context, pay db.Payment) error {
	provider, ext := pay.Provider, pay.ExternalID.String
	cl, cfg, err := s.addonClient(ctx, AddonID(provider))
	if err != nil {
		return err
	}
	st, err := cl.Status(ctx, cfg.Values, ext)
	if err != nil {
		return err
	}
	switch st.Status {
	case "paid":
		if st.Amount < pay.Amount || st.Currency != pay.Currency {
			s.d.Log.Error("billing: adapter amount differs", "payment", pay.ID, "provider", provider, "amount", st.Amount, "currency", st.Currency)
			return ErrBadPayment
		}
		if st.Amount > pay.Amount {
			s.d.Log.Info("billing: the buyer paid the provider's fee", "payment", pay.ID, "provider", provider, "amount", st.Amount, "invoice", pay.Amount)
		}
		if s.d.Promo != nil {
			r, redemptionErr := s.d.Promo.GetPaymentRedemption(ctx, pay.ID)
			late := redemptionErr == nil && promo.PaidLate(r, paymentTime(pay, s.d.Now().Unix()))
			if late {
				handled, err := s.refundLatePromoPayment(ctx, pay.ID, ext, func(current db.Payment) error {
					// This invoice already contains the discounted amount. The stable key
					// makes adapter retries idempotent.
					return cl.Refund(ctx, cfg.Values, ext, current.Amount, "mikan-promo-late-"+strconv.FormatInt(current.ID, 10))
				})
				if err != nil {
					return fmt.Errorf("refund expired promo payment %d: %w", pay.ID, err)
				}
				if handled {
					return nil
				}
			}
			if redemptionErr != nil && !errors.Is(redemptionErr, sql.ErrNoRows) {
				return redemptionErr
			}
		}
		return s.paid(ctx, pay, ext)
	case "canceled":
		_, err := s.d.Store.Q.SetPaymentStatus(ctx, db.SetPaymentStatusParams{NewStatus: PaymentFailed, ID: pay.ID, OldStatus: PaymentPending})
		if err != nil {
			return err
		}
		if s.d.Promo != nil && (pay.Status == PaymentPending || pay.Status == PaymentExpired || pay.Status == PaymentFailed) {
			return s.d.Promo.ReleasePayment(ctx, pay.ID)
		}
		return nil
	}
	return nil
}
