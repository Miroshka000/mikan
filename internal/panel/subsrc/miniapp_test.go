package subsrc

import (
	"errors"
	"fmt"
	"net/http"
	"testing"

	"mikan/internal/panel/billing"
	"mikan/internal/panel/promo"
)

// What the buyer can act on is told as it is; a provider that fails or a database that is
// busy is not "payment is off", and a cause nobody has logged yet is flagged for the log.
func TestInvoiceFailure(t *testing.T) {
	for _, c := range []struct {
		err         error
		status      int
		code        string
		unexplained bool
	}{
		{billing.ErrNotForSale, http.StatusConflict, "not_for_sale", false},
		{fmt.Errorf("wrapped: %w", billing.ErrTooMany), http.StatusConflict, "too_many_invoices", false},
		{billing.ErrNotYours, http.StatusConflict, "not_yours", false},
		{billing.ErrProviderOff, http.StatusConflict, "provider_off", false},
		{promo.ErrRefundUnsupported, http.StatusConflict, "promo_unavailable", false},
		{promo.ErrAlreadyApplied, http.StatusConflict, "promo_unavailable", false},
		{fmt.Errorf("invoice: %w", promo.ErrReservationExpired), http.StatusConflict, "promo_unavailable", false},
		{fmt.Errorf("%w: yookassa_unreachable", billing.ErrProviderOff), http.StatusBadGateway, "invoice_failed", false}, // billing logged it
		{errors.New("database is locked"), http.StatusBadGateway, "invoice_failed", true},
	} {
		status, code, unexplained := invoiceFailure(c.err)
		if status != c.status || code != c.code || unexplained != c.unexplained {
			t.Errorf("%v: %d %s %v, want %d %s %v", c.err, status, code, unexplained, c.status, c.code, c.unexplained)
		}
	}
}

func TestPromoUnavailableDoesNotRevealCodeExistence(t *testing.T) {
	for _, err := range []error{promo.ErrNotFound, promo.ErrInactive, promo.ErrExpired, promo.ErrLimit, promo.ErrUserLimit, promo.ErrTariff} {
		if got := promoAttemptCode(err); got != "promo_unavailable" {
			t.Errorf("%v leaked as %q", err, got)
		}
	}
}

func TestPromoAttemptErrorsDoNotRevealEligibility(t *testing.T) {
	for _, err := range []error{promo.ErrNotFound, promo.ErrInactive, promo.ErrExpired, promo.ErrLimit, promo.ErrUserLimit,
		promo.ErrTariff, promo.ErrMinimum, promo.ErrNewUser, promo.ErrFirstPurchase, promo.ErrCurrency, promo.ErrNotDiscount} {
		if got := promoAttemptCode(err); got != "promo_unavailable" {
			t.Errorf("%v leaked as %q", err, got)
		}
	}
}

func TestPromoAttemptInternalErrorIsNotHidden(t *testing.T) {
	if got := promoAttemptCode(errors.New("database is down")); got != "" {
		t.Errorf("an internal error answered as %q", got)
	}
	if got := promoAttemptCode(fmt.Errorf("redeem: %w", promo.ErrNotBonus)); got != "promo_unavailable" {
		t.Errorf("a wrapped promo error answered as %q", got)
	}
}

func TestPromoErrorsAreGenericAtCheckout(t *testing.T) {
	for _, err := range []error{promo.ErrNotFound, promo.ErrTariff, promo.ErrMinimum, promo.ErrCurrency, promo.ErrRefundUnsupported} {
		if !promoError(err) || promoAttemptCode(err) != "promo_unavailable" {
			t.Errorf("%v is not handled as a generic promo error", err)
		}
	}
	if promoError(billing.ErrProviderOff) {
		t.Fatal("provider error was classified as a promo error")
	}
}
