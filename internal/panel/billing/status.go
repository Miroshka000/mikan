package billing

// The statuses of a payment (payments.status). An invoice is pending until it is paid or
// expires (failed: the provider would not open it); paid becomes applied in the
// transaction that gives what was bought, and a refund ends either in refunded.
const (
	PaymentPending  = "pending"
	PaymentPaid     = "paid"
	PaymentApplied  = "applied"
	PaymentExpired  = "expired"
	PaymentFailed   = "failed"
	PaymentRefunded = "refunded"
)
