package billing

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"net/mail"
	"strings"
	"time"

	"mikan/internal/panel/model"
	"mikan/internal/panel/store/db"
)

const (
	webPayloadPrefix  = "web_"
	webInvoiceTimeout = 30 * time.Second
	webInvoiceLease   = time.Minute
)

var (
	ErrCheckoutKey      = errors.New("invalid_checkout_key")
	ErrCheckoutEmail    = errors.New("invalid_email")
	ErrCheckoutConflict = errors.New("checkout_conflict")
	ErrCheckoutBusy     = errors.New("invoice_in_progress")
)

type WebOrder struct {
	Key      string
	SubToken string
	Email    string
	TariffID int64
	TermDays *int64
	Provider string
}

func tokenHash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func webPayload(key string) (string, error) {
	if len(key) != 64 || strings.ToLower(key) != key {
		return "", ErrCheckoutKey
	}
	if _, err := hex.DecodeString(key); err != nil {
		return "", ErrCheckoutKey
	}
	return webPayloadPrefix + tokenHash(key), nil
}

func (s *Service) webAvailable(ctx context.Context, cfg Config) Available {
	if !cfg.Enabled || !cfg.Website {
		return Available{}
	}
	return Available{Addons: s.availableAddons(ctx)}
}

func (s *Service) WebOffers(ctx context.Context, u db.User) ([]Offer, Available, error) {
	cfg := s.Config(ctx)
	if u.ID == 0 && !cfg.AllowNew {
		return nil, Available{}, nil
	}
	av := s.webAvailable(ctx, cfg)
	offers, err := s.offers(ctx, av)
	for i := range offers {
		o := &offers[i]
		_, extra := model.RenewalExtras(cfg.Model(), model.User(u), model.Tariff(o.Tariff))
		for j := range o.Terms {
			o.Terms[j].Rub += extra
		}
		o.Rub = o.Terms[0].Rub
	}
	return offers, av, err
}

// WebInvoice uses the subscription link for renewals and the checkout key for idempotency.
// The key's digest is stored as a payload and the key itself stays with the buyer.
func (s *Service) WebInvoice(ctx context.Context, order WebOrder) (db.Payment, error) {
	payload, err := webPayload(order.Key)
	if err != nil {
		return db.Payment{}, err
	}
	email := strings.TrimSpace(order.Email)
	if email != "" {
		addr, err := mail.ParseAddress(email)
		if len(email) > 254 || err != nil || addr.Address != email {
			return db.Payment{}, ErrCheckoutEmail
		}
	}
	if model.AddonID(order.Provider) == "" || s.d.CheckoutBase == nil {
		return db.Payment{}, ErrProviderOff
	}
	subBase := s.d.CheckoutBase(ctx)
	if subBase == "" {
		return db.Payment{}, ErrProviderOff
	}
	q := s.d.Store.Q
	kind := "new"
	var userID sql.NullInt64
	if order.SubToken != "" {
		u, err := q.GetUserBySubToken(ctx, order.SubToken)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				err = ErrNotYours
			}
			return db.Payment{}, err
		}
		kind, userID = "renew", sql.NullInt64{Int64: u.ID, Valid: true}
	}
	var p db.Payment
	cfg := s.Config(ctx)
	t, term, amount, currency, orderErr := s.tariffOrder(ctx, userID.Int64, order.TariffID, order.TermDays, order.Provider, s.webAvailable(ctx, cfg))
	if kind == "new" && !cfg.AllowNew {
		orderErr = ErrNewOff
	}
	var leaseUntil int64
	var receiptEmail string
	claimed := false
	err = s.d.Store.TxRC(ctx, func(q *db.Queries) error {
		claimed = false
		if err := q.LockWebCheckout(ctx, payload); err != nil {
			return err
		}
		now := s.d.Now()
		prev, err := q.GetPaymentByPayload(ctx, payload)
		switch {
		case err == nil:
			if prev.Kind != kind || prev.Provider != order.Provider || prev.TariffID.Int64 != order.TariffID ||
				kind == "renew" && prev.UserID != userID || order.TermDays != nil && prev.TermDays.Int64 != *order.TermDays {
				return ErrCheckoutConflict
			}
			p = prev
			incomplete := UnopenedWebInvoice(p)
			if p.Status != PaymentPending && !incomplete {
				return nil
			}
			checkout, err := q.GetWebCheckout(ctx, p.ID)
			if err != nil {
				return err
			}
			if email != "" && checkout.ReceiptEmail != email {
				// Applying a payment may clear its receipt email after the read above.
				p, err = q.GetPayment(ctx, p.ID)
				if err == nil && (p.Status == PaymentPending || UnopenedWebInvoice(p)) {
					return ErrCheckoutConflict
				}
				return err
			}
			if !incomplete {
				return nil
			}
			if p.CreatedAt <= now.Add(-pendingTTL).Unix() {
				if err := q.ExpireIncompleteWebInvoice(ctx, db.ExpireIncompleteWebInvoiceParams{PaymentID: p.ID, Now: now.Unix()}); err != nil {
					return err
				}
				p, err = q.GetPayment(ctx, p.ID)
				if err == nil && UnopenedWebInvoice(p) {
					return ErrCheckoutBusy
				}
				return err
			}
			if orderErr != nil {
				return orderErr
			}
			receiptEmail = checkout.ReceiptEmail
		case errors.Is(err, sql.ErrNoRows):
			if kind == "new" && email == "" {
				return ErrCheckoutEmail
			}
			receiptEmail = email
			if orderErr != nil {
				return orderErr
			}
			p, err = q.CreatePayment(ctx, db.CreatePaymentParams{Provider: order.Provider, Payload: payload, Kind: kind, UserID: userID,
				TariffID: sql.NullInt64{Int64: t.ID, Valid: true}, TariffName: t.Name, Amount: amount, Currency: currency,
				CreatedAt: now.Unix(), TermDays: sql.NullInt64{Int64: term.Days, Valid: true}})
			if err != nil {
				return err
			}
			if err := q.CreateWebCheckout(ctx, db.CreateWebCheckoutParams{PaymentID: p.ID, ReceiptEmail: email}); err != nil {
				return err
			}
		default:
			return err
		}
		// A lock wait or transaction retry must not consume the invoice lease.
		claimTime := s.d.Now()
		leaseUntil = claimTime.Add(webInvoiceLease).Unix()
		n, err := q.ClaimWebInvoice(ctx, db.ClaimWebInvoiceParams{PaymentID: p.ID, LeaseUntil: leaseUntil, Now: claimTime.Unix()})
		if err == nil && n == 0 {
			return ErrCheckoutBusy
		}
		claimed = err == nil
		return err
	})
	if err != nil || !claimed {
		return p, err
	}

	// Keep the payment and the provider's idempotency key through retries. The lease
	// keeps concurrent requests from opening invoices for the same payment.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), webInvoiceTimeout)
	defer cancel()
	defer func() {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		if err := q.ReleaseWebInvoice(cleanup, db.ReleaseWebInvoiceParams{PaymentID: p.ID, LeaseUntil: leaseUntil}); err != nil {
			s.d.Log.Warn("billing: release web invoice lease", "payment", p.ID, "err", err)
		}
	}()

	lang, _ := s.d.Settings.Lang(ctx)
	desc := model.Describe(model.Tariff(t), p.TermDays.Int64, lang)
	// Share a digest with the provider for selecting the browser's saved order.
	// The original key remains in the browser and is required for status and retries.
	returnURL := strings.TrimSuffix(subBase, "/") + "/shop?checkout=" + strings.TrimPrefix(p.Payload, webPayloadPrefix)
	ext, url, invoiceErr := s.openAddonInvoice(ctx, p, p.TariffName+" — "+desc, returnURL, receiptEmail)
	finish := db.FinishWebInvoiceParams{PaymentID: p.ID, LeaseUntil: leaseUntil, ExternalID: ext, PayUrl: url, Status: PaymentPending}
	if invoiceErr != nil {
		finish.Status, finish.Error = PaymentFailed, errCode(invoiceErr)
		s.d.Log.Warn("billing: invoice", "provider", p.Provider, "payment", p.ID, "err", invoiceErr)
	}
	saved, err := q.FinishWebInvoice(ctx, finish)
	if errors.Is(err, sql.ErrNoRows) {
		return db.Payment{}, ErrCheckoutBusy
	}
	if err != nil {
		return db.Payment{}, err
	}
	if invoiceErr != nil {
		return db.Payment{}, fmt.Errorf("%w: %s", ErrProviderOff, finish.Error)
	}
	return saved, nil
}

// WebReturnExists identifies a checkout return without releasing its status or link.
func (s *Service) WebReturnExists(ctx context.Context, digest string) bool {
	if len(digest) != 64 {
		return false
	}
	if _, err := hex.DecodeString(digest); err != nil {
		return false
	}
	_, err := s.d.Store.Q.GetPaymentByPayload(ctx, webPayloadPrefix+digest)
	return err == nil
}

// UnopenedWebInvoice reports whether the same checkout can retry opening an invoice.
func UnopenedWebInvoice(p db.Payment) bool {
	return p.PayUrl == "" && !p.ExternalID.Valid && (p.Status == PaymentPending || p.Status == PaymentFailed)
}

// The general sweep does not touch incomplete web invoices. An invoice claim and
// an expiration decision must share the lock and read the lease on its transaction.
func (s *Service) expireWebInvoices(ctx context.Context, now time.Time) {
	rows, err := s.d.Store.Q.ListExpiredWebInvoices(ctx, db.ListExpiredWebInvoicesParams{Cutoff: now.Add(-pendingTTL).Unix(), Now: now.Unix()})
	if err != nil {
		s.d.Log.Error("billing: list expired web invoices", "err", err)
		return
	}
	for _, row := range rows {
		err := s.d.Store.TxRC(ctx, func(q *db.Queries) error {
			if err := q.LockWebCheckout(ctx, row.Payload); err != nil {
				return err
			}
			return q.ExpireIncompleteWebInvoice(ctx, db.ExpireIncompleteWebInvoiceParams{PaymentID: row.ID, Now: s.d.Now().Unix()})
		})
		if err != nil {
			s.d.Log.Error("billing: expire web invoice", "payment", row.ID, "err", err)
		}
	}
}

// WebStatus only releases a link issued by this checkout, while it is still current.
func (s *Service) WebStatus(ctx context.Context, key string) (p db.Payment, token string, err error) {
	payload, err := webPayload(key)
	if err != nil {
		return p, "", err
	}
	q := s.d.Store.Q
	p, err = q.GetPaymentByPayload(ctx, payload)
	if err != nil || p.Status != PaymentApplied || !p.UserID.Valid {
		return p, "", err
	}
	checkout, err := q.GetWebCheckout(ctx, p.ID)
	if errors.Is(err, sql.ErrNoRows) {
		return p, "", nil
	}
	if err != nil || checkout.TokenHash == "" {
		return p, "", err
	}
	u, err := q.GetUser(ctx, p.UserID.Int64)
	if errors.Is(err, sql.ErrNoRows) {
		return p, "", nil
	}
	if err == nil && tokenHash(u.SubToken) == checkout.TokenHash {
		token = u.SubToken
	}
	return p, token, err
}
