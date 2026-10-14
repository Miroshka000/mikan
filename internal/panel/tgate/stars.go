package tgate

import (
	"context"
	"errors"
	"time"

	"mikan/internal/panel/billing"
)

// Telegram Stars: the invoice links, Telegram's questions and reports about a payment, and
// refunds. The gateway handles them itself, whatever the bot's screens are, so a payment
// never waits for them.

// preCheckout answers Telegram's last question before a Stars payment; it has ten seconds,
// so it runs off the update loop with a deadline of its own: whatever else the loop is
// busy with, the answer goes out in time or the buyer's payment fails.
func (g *Gateway) preCheckout(ctx context.Context, c *Client, q *PreCheckoutQuery) {
	ctx, cancel := context.WithTimeout(ctx, preCheckoutDeadline)
	defer cancel()
	err := g.d.Billing.PreCheckout(ctx, q.From.ID, q.InvoicePayload, q.Currency, q.TotalAmount)
	reason := ""
	if err != nil {
		reason = wordsFor(g.Lang(ctx)).payStale
	}
	if err := c.AnswerPreCheckout(ctx, q.ID, err == nil, reason); err != nil {
		g.d.Log.Warn("telegram: pre-checkout answer", "err", errText(err))
	}
}

// preCheckoutDeadline is the whole of a pre-checkout answer, the lookup and the call.
const preCheckoutDeadline = 9 * time.Second

// starsPaid takes Telegram's successful_payment: the billing applies it and calls Paid.
// An error that may go away (the database busy) is returned: the update is then not
// acknowledged to Telegram and comes again, so a payment is not left unapplied. A payment
// that matches no invoice never will, and is only logged.
func (g *Gateway) starsPaid(ctx context.Context, m *Message) error {
	sp := m.SuccessfulPayment
	if m.From == nil {
		return nil
	}
	pay := g.d.stars
	if pay == nil {
		pay = g.d.Billing.StarsPaid
	}
	err := pay(ctx, m.From.ID, sp.InvoicePayload, sp.ChargeID, sp.Currency, sp.TotalAmount)
	switch {
	case err == nil:
		return nil
	case errors.Is(err, billing.ErrBadPayment):
		g.d.Log.Error("telegram: stars payment is not an invoice of ours", "tg", m.From.ID, "charge", sp.ChargeID, "amount", sp.TotalAmount)
		return nil
	case errors.Is(err, billing.ErrRecorded):
		// Saved with its charge: the reconcile pass applies it, the update is done.
		g.d.Log.Error("telegram: stars payment saved, not applied yet", "err", err, "charge", sp.ChargeID)
		return nil
	}
	g.d.Log.Error("telegram: stars payment", "err", err, "charge", sp.ChargeID)
	return err
}

// starsRefunded takes Telegram's refunded_payment: the billing takes back what the payment
// gave and calls Refunded. A refund that is not a payment of ours is only logged; an error
// that may go away is returned, as for starsPaid, so the update comes again.
func (g *Gateway) starsRefunded(ctx context.Context, m *Message) error {
	rp := m.RefundedPayment
	// The private chat is the buyer's: its id is their Telegram id.
	if err := g.d.Billing.StarsRefunded(ctx, m.Chat.ID, rp.ChargeID); err != nil {
		g.d.Log.Error("telegram: stars refund", "err", err, "charge", rp.ChargeID)
		return err
	}
	return nil
}

// InvoiceLink implements billing.Telegram.
func (g *Gateway) InvoiceLink(ctx context.Context, title, description, payload string, stars int64) (string, error) {
	c := g.client.Load()
	if c == nil {
		return "", ErrOff
	}
	return c.InvoiceLink(ctx, title, description, payload, stars)
}

// RefundStars implements billing.Telegram.
func (g *Gateway) RefundStars(ctx context.Context, tgID int64, chargeID string) error {
	c := g.client.Load()
	if c == nil {
		return ErrOff
	}
	return c.RefundStars(ctx, tgID, chargeID)
}

// BotURL implements billing.Telegram.
func (g *Gateway) BotURL(context.Context) string {
	st := g.Status()
	if !st.Running || st.Bot.Username == "" {
		return ""
	}
	return "https://t.me/" + st.Bot.Username
}
