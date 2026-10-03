package tgbot

import (
	"context"
	"errors"
	"fmt"
	"html"
	"strconv"
	"strings"
	"time"

	"mikan/internal/panel/billing"
	"mikan/internal/panel/store/db"
)

// Callback data of the shop: b buy a new subscription, tn:<tariff> its tariff, pn:<tariff>:<p>
// pay for it; r renew the shown one, t:<tariff>, py:<tariff>:<p>. <p> is s (Stars) or
// a-<id> (a marketplace adapter); y and c are YooKassa's and CryptoBot's, short, and as
// messages sent before 0.4.4 carry them for the built-in providers those adapters replaced.

var providerCodes = map[string]string{"s": billing.Stars, "y": billing.AddonPrefix + "yookassa", "c": billing.AddonPrefix + "cryptobot"}

// addonCode is an adapter's code on a pay button.
func addonCode(id string) string {
	for code, p := range providerCodes {
		if p == billing.AddonPrefix+id {
			return code
		}
	}
	return "a-" + id
}

// providerOf is the provider a button's code names.
func providerOf(code string) (string, bool) {
	if p, ok := providerCodes[code]; ok {
		return p, true
	}
	id, ok := strings.CutPrefix(code, "a-")
	if !ok || billing.AddonID(billing.AddonPrefix+id) == "" {
		return "", false
	}
	return billing.AddonPrefix + id, true
}

// addonName names adapters on the buttons in the bot's language.
func (b *Bot) addonName(ctx context.Context) func(id string) string {
	lang := b.lang(ctx)
	return func(id string) string { return b.d.Billing.AddonName(ctx, id, lang) }
}

// offers lists what the chat can buy; nil without payments.
func (b *Bot) offers(ctx context.Context) ([]billing.Offer, billing.Available) {
	if b.d.Billing == nil {
		return nil, billing.Available{}
	}
	o, av, err := b.d.Billing.Offers(ctx)
	if err != nil {
		b.d.Log.Warn("telegram: offers", "err", err)
		return nil, av
	}
	return o, av
}

// canBuyNew: people without a subscription may buy one here.
func (b *Bot) canBuyNew(ctx context.Context) bool {
	if b.d.Billing == nil || !b.d.Billing.Config(ctx).AllowNew {
		return false
	}
	o, _ := b.offers(ctx)
	return len(o) > 0
}

// price: "⭐ 150" or "199 ₽" / "199,50 ₽".
func (w *words) price(amount int64, currency string) string {
	if currency == "XTR" {
		return "⭐ " + strconv.FormatInt(amount, 10)
	}
	s := strconv.FormatInt(amount/100, 10)
	if k := amount % 100; k != 0 {
		sep := ","
		if w == &en {
			sep = "."
		}
		s += fmt.Sprintf("%s%02d", sep, k)
	}
	return s + " ₽"
}

// shopList: the tariffs on sale as buttons; prefix is "t" (renew) or "tn" (new).
func (b *Bot) shopList(ctx context.Context, w *words, title, prefix, notice string, back []Button) (string, *Keyboard) {
	offers, _ := b.offers(ctx)
	lang := b.lang(ctx)
	lines := []string{"<b>" + html.EscapeString(title) + "</b>", ""}
	if notice != "" {
		lines = append([]string{html.EscapeString(notice), ""}, lines...)
	}
	rows := [][]Button{}
	for _, o := range offers {
		lines = append(lines, "• <b>"+html.EscapeString(o.Tariff.Name)+"</b> — "+html.EscapeString(billing.Describe(o.Tariff, lang)))
		rows = append(rows, []Button{{Text: o.Tariff.Name + " · " + w.cheapest(o), CallbackData: prefix + ":" + strconv.FormatInt(o.Tariff.ID, 10)}})
	}
	if len(offers) == 0 {
		lines = append(lines, html.EscapeString(w.payUnavailable))
	}
	return strings.Join(lines, "\n"), &Keyboard{append(rows, back)}
}

// cheapest is the price a list shows: rubles when sold for them, else Stars.
func (w *words) cheapest(o billing.Offer) string {
	if o.Rub > 0 {
		return w.price(o.Rub, "RUB")
	}
	return w.price(o.Stars, "XTR")
}

// shopTariff: one tariff and a button per way to pay; prefix "py" (renew) or "pn" (new).
func (b *Bot) shopTariff(ctx context.Context, w *words, id int64, prefix string, back []Button) (string, *Keyboard) {
	offers, av := b.offers(ctx)
	for _, o := range offers {
		if o.Tariff.ID != id {
			continue
		}
		text := "<b>" + html.EscapeString(o.Tariff.Name) + "</b>\n" + html.EscapeString(billing.Describe(o.Tariff, b.lang(ctx))) + "\n\n" + w.payHow
		rows := w.payButtons(prefix+":"+strconv.FormatInt(id, 10)+":", o.Stars, o.Rub, av, b.addonName(ctx))
		return text, &Keyboard{append(rows, back)}
	}
	return html.EscapeString(w.notForSale), &Keyboard{[][]Button{back}}
}

// shopInvoice opens the invoice and shows its pay button. It runs when the chat's screen
// is drawn, off the update loop: the provider may take seconds to answer.
func (b *Bot) shopInvoice(ctx context.Context, w *words, chat, userID int64, arg string, back []Button) (string, *Keyboard) {
	idStr, code, _ := strings.Cut(arg, ":")
	id, _ := strconv.ParseInt(idStr, 10, 64)
	provider, ok := providerOf(code)
	if !ok || b.d.Billing == nil {
		return html.EscapeString(w.payUnavailable), &Keyboard{[][]Button{back}}
	}
	p, err := b.d.Billing.Invoice(ctx, billing.InvoiceRequest{TgID: chat, UserID: userID, TariffID: id, Provider: provider})
	if err != nil {
		return html.EscapeString(w.payError(err)), &Keyboard{[][]Button{back}}
	}
	done := w.payNew
	if userID != 0 {
		done = w.payRenew
	}
	text := fmt.Sprintf(w.invoice, html.EscapeString(p.TariffName), html.EscapeString(w.price(p.Amount, p.Currency)), done)
	return text, &Keyboard{[][]Button{{{Text: fmt.Sprintf(w.payButton, w.price(p.Amount, p.Currency)), URL: p.PayUrl}}, back}}
}

func (w *words) payError(err error) string {
	switch {
	case errors.Is(err, billing.ErrNotForSale):
		return w.notForSale
	case errors.Is(err, billing.ErrTooMany):
		return w.tooManyInvoices
	case errors.Is(err, billing.ErrTooManySubs):
		return fmt.Sprintf(w.linkLimit, MaxLinks)
	case errors.Is(err, billing.ErrNotYours):
		return w.noSub
	}
	return w.payUnavailable
}

func (b *Bot) lang(ctx context.Context) string { return b.Config(ctx).Lang }

// preCheckout answers Telegram's last question before a Stars payment; it has ten seconds,
// so it runs off the update loop with a deadline of its own: whatever else the loop is
// busy with, the answer goes out in time or the buyer's payment fails.
func (b *Bot) preCheckout(ctx context.Context, c *Client, q *PreCheckoutQuery) {
	ctx, cancel := context.WithTimeout(ctx, preCheckoutDeadline)
	defer cancel()
	if b.d.Billing == nil {
		_ = c.AnswerPreCheckout(ctx, q.ID, false, wordsFor(b.lang(ctx)).payUnavailable)
		return
	}
	err := b.d.Billing.PreCheckout(ctx, q.From.ID, q.InvoicePayload, q.Currency, q.TotalAmount)
	reason := ""
	if err != nil {
		reason = wordsFor(b.lang(ctx)).payStale
	}
	if err := c.AnswerPreCheckout(ctx, q.ID, err == nil, reason); err != nil {
		b.d.Log.Warn("telegram: pre-checkout answer", "err", errText(err))
	}
}

// preCheckoutDeadline is the whole of a pre-checkout answer, the lookup and the call.
const preCheckoutDeadline = 9 * time.Second

// starsPaid takes Telegram's successful_payment: the billing applies it and calls Paid.
// An error that may go away (the database busy) is returned: the update is then not
// acknowledged to Telegram and comes again, so a payment is not left unapplied. A payment
// that matches no invoice never will, and is only logged.
func (b *Bot) starsPaid(ctx context.Context, m *Message) error {
	sp := m.SuccessfulPayment
	if m.From == nil {
		return nil
	}
	pay := b.d.stars
	if pay == nil && b.d.Billing != nil {
		pay = b.d.Billing.StarsPaid
	}
	if pay == nil {
		return nil
	}
	err := pay(ctx, m.From.ID, sp.InvoicePayload, sp.ChargeID, sp.Currency, sp.TotalAmount)
	switch {
	case err == nil:
		return nil
	case errors.Is(err, billing.ErrBadPayment):
		b.d.Log.Error("telegram: stars payment is not an invoice of ours", "tg", m.From.ID, "charge", sp.ChargeID, "amount", sp.TotalAmount)
		return nil
	}
	b.d.Log.Error("telegram: stars payment", "err", err, "charge", sp.ChargeID)
	return err
}

// InvoiceLink implements billing.Telegram.
func (b *Bot) InvoiceLink(ctx context.Context, title, description, payload string, stars int64) (string, error) {
	c := b.client.Load()
	if c == nil {
		return "", ErrOff
	}
	return c.InvoiceLink(ctx, title, description, payload, stars)
}

// RefundStars implements billing.Telegram.
func (b *Bot) RefundStars(ctx context.Context, tgID int64, chargeID string) error {
	c := b.client.Load()
	if c == nil {
		return ErrOff
	}
	return c.RefundStars(ctx, tgID, chargeID)
}

// BotURL implements billing.Telegram.
func (b *Bot) BotURL(context.Context) string {
	st := b.Status()
	if !st.Running || st.Bot.Username == "" {
		return ""
	}
	return "https://t.me/" + st.Bot.Username
}

// Paid implements billing.Telegram: the buyer learns the subscription is ready (with the
// link for a new one) or the traffic package is added, and gets a fresh menu that shows it.
func (b *Bot) Paid(ctx context.Context, p db.Payment, u db.User, created bool) {
	out := b.out.Load()
	if out == nil {
		return
	}
	w := wordsFor(b.lang(ctx))
	var text string
	switch {
	case p.Kind == billing.KindPackage:
		text = fmt.Sprintf(w.paidPackage, html.EscapeString(p.TariffName), html.EscapeString(u.Name))
	case created:
		text = fmt.Sprintf(w.paidNew, html.EscapeString(u.Name), html.EscapeString(p.TariffName), html.EscapeString(b.subURL(ctx, u)))
	default:
		until := w.forever
		if u.ExpiresAt.Valid {
			until = w.date(time.Unix(u.ExpiresAt.Int64, 0).UTC())
		}
		text = fmt.Sprintf(w.paidRenew, html.EscapeString(u.Name), html.EscapeString(until))
	}
	chat := p.TgID
	_ = b.d.Store.Q.SetTgCurrent(ctx, db.SetTgCurrentParams{Current: u.ID, TgID: chat})
	out.Notice(chat, func(ctx context.Context, c *Client) error {
		_, err := c.Send(ctx, chat, text, nil, false)
		return err
	}, nil)
	b.freshMenu(out, chat, "")
}

// takeTrial gives the chat its free trial and says what it got; a chat that had one (or
// is a customer) is told why not.
func (b *Bot) takeTrial(ctx context.Context, w *words, chat int64) (string, *Keyboard) {
	home := &Keyboard{[][]Button{{{Text: w.back, CallbackData: "m"}}}}
	if b.d.Billing == nil {
		return html.EscapeString(w.trialOff), home
	}
	u, err := b.d.Billing.Trial(ctx, chat)
	switch {
	case errors.Is(err, billing.ErrTrialUsed):
		return html.EscapeString(w.trialUsed), home
	case errors.Is(err, billing.ErrTrialOff):
		return html.EscapeString(w.trialOff), home
	case err != nil:
		b.d.Log.Warn("telegram: trial", "tg", chat, "err", err)
		return html.EscapeString(w.payUnavailable), home
	}
	what := u.Name
	if t, err := b.d.Store.Q.GetTariff(ctx, u.TariffID.Int64); err == nil {
		what = billing.Describe(t, b.lang(ctx))
	}
	return fmt.Sprintf(html.EscapeString(w.trialDone), html.EscapeString(what)), &Keyboard{[][]Button{{{Text: w.trialOpen, CallbackData: "m"}}}}
}
