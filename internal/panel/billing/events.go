package billing

import (
	"context"
	"strconv"
	"time"

	"mikan/internal/panel/events"
	"mikan/internal/panel/store/db"
)

// emitPayment writes a payment's event in the caller's transaction, once per payment and type.
// created and disabled are what the buyer's messages differ by (events.Payment).
func emitPayment(ctx context.Context, q *db.Queries, now time.Time, typ string, p db.Payment, userID int64, created, disabled bool) error {
	return events.Emit(ctx, q, now, typ, userID, p.TgID, events.Payment{PaymentID: p.ID, Kind: p.Kind, Provider: p.Provider, Amount: p.Amount,
		Currency: p.Currency, TariffID: p.TariffID.Int64, TariffName: p.TariffName, PackageID: p.PackageID.Int64, TermDays: p.TermDays.Int64,
		Created: created, Disabled: disabled},
		typ+":"+strconv.FormatInt(p.ID, 10))
}

// emitTrialNotGiven tells that the trial payment p cannot be given, once per payment. refund
// is events.RefundedAuto or events.RefundedManually.
func emitTrialNotGiven(ctx context.Context, q *db.Queries, now time.Time, p db.Payment, refund string) error {
	return events.Emit(ctx, q, now, events.TrialNotGiven, 0, p.TgID, events.NotGiven{PaymentID: p.ID, Provider: p.Provider, Amount: p.Amount,
		Currency: p.Currency, TariffID: p.TariffID.Int64, TermDays: p.TermDays.Int64, Reason: events.TrialUsed, Refund: refund},
		events.TrialNotGiven+":"+strconv.FormatInt(p.ID, 10))
}

// emitTrialStarted tells that tgID took its trial as subscription u; paid is the payment that
// bought it, 0 for the free trial. Once per subscription.
func emitTrialStarted(ctx context.Context, q *db.Queries, now time.Time, userID, tgID, tariffID, paid int64) error {
	return events.Emit(ctx, q, now, events.TrialStarted, userID, tgID, events.Trial{TariffID: tariffID, Paid: paid != 0, PaymentID: paid},
		events.TrialStarted+":"+strconv.FormatInt(userID, 10))
}
