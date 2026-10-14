package events

// Every event type the panel emits and the shape of its data, in one place: PROTOCOL-v2.md
// (getmikan/marketplace) is written from Docs. An event's envelope is Event (id, type, at,
// user_id, tg_id, data); the data never holds a secret (subscription tokens, keys, the
// devices' ids, a provider's credentials) and no node address: an addon that needs more of a
// subscriber asks the Panel API with the permission for it.
//
// Delivery is at least once, by pull (GET /events) or push (POST /v2/events), in id order.
// A deleted subscription's devices are not told unbound (there is no event for the deletion
// itself): an addon that keeps a copy of a subscriber asks the Panel API when it needs it.

// Types of events.
const (
	PaymentPaid     = "payment.paid"
	PaymentRefunded = "payment.refunded"

	UserCreated  = "user.created"
	UserGranted  = "user.granted"
	UserExpiring = "user.expiring"
	UserExpired  = "user.expired"
	Traffic90    = "traffic.90"
	Traffic100   = "traffic.100"

	TrialStarted  = "trial.started"
	TrialNotGiven = "trial.not_given"

	DeviceBound   = "device.bound"
	DeviceUnbound = "device.unbound"

	NodeDown = "node.down"
	NodeUp   = "node.up"
)

// The bot's own messages (deprecated), written after the commit by tgbot.Notifications and
// carrying a whole payment and a whole subscription (so only the addon that holds the bot
// reads them). They stay for one release so the bot builds released before PaymentPaid,
// PaymentRefunded and TrialNotGiven carried what the bot needs keep working; the bot addon
// built with this panel does not read them.
const (
	BotPaid          = "bot.paid"
	BotRefunded      = "bot.refunded"
	BotTrialNotGiven = "bot.trial_not_given"
)

// Payment is the data of PaymentPaid and PaymentRefunded: enough to thank, notify or count,
// nothing of the provider's secrets. The envelope's user_id is the subscription the payment
// gave to (0 on a refund when it is gone), tg_id the buyer.
type Payment struct {
	PaymentID  int64  `json:"payment_id"`
	Kind       string `json:"kind"`     // new | renew | package | trial
	Provider   string `json:"provider"` // stars | addon:<id> | ...
	Amount     int64  `json:"amount"`   // minor units
	Currency   string `json:"currency"`
	TariffID   int64  `json:"tariff_id,omitempty"`
	TariffName string `json:"tariff_name,omitempty"` // as it was when the buyer paid
	PackageID  int64  `json:"package_id,omitempty"`
	TermDays   int64  `json:"term_days,omitempty"`
	Created    bool   `json:"created,omitempty"`  // PaymentPaid: the payment made the subscription
	Disabled   bool   `json:"disabled,omitempty"` // PaymentRefunded: the subscription was turned off
}

// Notice is the data of UserExpiring, UserExpired, Traffic90 and Traffic100: which
// subscription (the envelope's user_id, tg_id the account it is linked to) and what is due.
type Notice struct {
	Days       int   `json:"days,omitempty"`        // UserExpiring: 3 or 1
	ExpiresAt  int64 `json:"expires_at,omitempty"`  // unix seconds
	PeriodFrom int64 `json:"period_from,omitempty"` // traffic: the period it is about
}

// User is the data of UserCreated, for every way a subscription is made except an import
// (a bulk move from another panel has no one to welcome). The envelope's user_id is the new
// subscription; tg_id is the Telegram account that bought or took it, when there is one.
type User struct {
	Source   string `json:"source"` // admin | bot | trial | free
	TariffID int64  `json:"tariff_id,omitempty"`
}

// Granted is the data of UserGranted: an addon with users:grant gave a subscription days or
// traffic. The envelope's user_id is the subscription, tg_id the account linked to it. The
// reason is the admin's to read in the journal, not every addon's.
type Granted struct {
	Days   int64  `json:"days,omitempty"`
	Bytes  int64  `json:"bytes,omitempty"`
	PoolID int64  `json:"pool_id,omitempty"`
	Addon  string `json:"addon"`
}

// Trial is the data of TrialStarted: a subscription made as an account's trial (free, or
// bought: a paid trial also has its PaymentPaid, kind trial). The envelope's user_id is the
// new subscription, tg_id the account.
type Trial struct {
	TariffID  int64 `json:"tariff_id"`
	Paid      bool  `json:"paid,omitempty"`
	PaymentID int64 `json:"payment_id,omitempty"`
}

// Reasons and refunds of TrialNotGiven.
const (
	TrialUsed        = "trial_used" // the account had its trial or is a customer
	RefundedAuto     = "auto"       // the money went back; PaymentRefunded says so too
	RefundedManually = "manual"     // the payment method cannot refund: the admin does it by hand
)

// NotGiven is the data of TrialNotGiven: a trial an account paid for that the panel may no
// longer give. The envelope's tg_id is the buyer, user_id 0.
type NotGiven struct {
	PaymentID int64  `json:"payment_id"`
	Provider  string `json:"provider"`
	Amount    int64  `json:"amount"`
	Currency  string `json:"currency"`
	TariffID  int64  `json:"tariff_id,omitempty"`
	TermDays  int64  `json:"term_days,omitempty"`
	Reason    string `json:"reason"` // TrialUsed
	Refund    string `json:"refund"` // RefundedAuto | RefundedManually
}

// Device is the data of DeviceBound and DeviceUnbound. The envelope's user_id is the
// subscription (tg_id is not set: a device is not about a Telegram account). The device's id
// at its app (the HWID) is never told.
type Device struct {
	DeviceID int64  `json:"device_id"`
	Label    string `json:"label,omitempty"`  // its name, else what its app reported
	Shared   bool   `json:"shared,omitempty"` // an app that sends no id: the subscription's shared place
	// By (DeviceUnbound): subscriber | admin | ban | idle | reissue.
	By string `json:"by,omitempty"`
}

// Ways a device is unbound.
const (
	UnboundBySubscriber = "subscriber"
	UnboundByAdmin      = "admin"
	UnboundByBan        = "ban"
	UnboundIdle         = "idle"    // not seen for 90 days
	UnboundReissue      = "reissue" // the subscription got a new link
)

// Node is the data of NodeDown and NodeUp. Not tied to a subscriber: no user_id and no tg_id.
// A node is down after three failed health checks in a row (about 15 s), and up again at the
// first one that passes; each change is told once, though the checks go on every 5 s.
type Node struct {
	NodeID int64  `json:"node_id"`
	Name   string `json:"name"`
	Code   string `json:"code,omitempty"`       // NodeDown: why it did not answer (nodeapi.Link*)
	Since  int64  `json:"since,omitempty"`      // NodeDown: unix seconds, when the failures began
	DownAt int64  `json:"down_since,omitempty"` // NodeUp: unix seconds, when it went down
}

// Doc describes one event type for the protocol's documentation: Data is an example of its
// data as it goes on the wire.
type Doc struct {
	Type  string
	About string
	Data  any
}

// Docs lists every event type the panel emits (the deprecated BotPaid, BotRefunded and
// BotTrialNotGiven are for the bot addon alone and not listed).
var Docs = []Doc{
	{PaymentPaid, "A payment was applied: the subscription is made or extended, or the package credited.",
		Payment{PaymentID: 7, Kind: "new", Provider: "stars", Amount: 100, Currency: "XTR", TariffID: 2, TariffName: "Month", TermDays: 30, Created: true}},
	{PaymentRefunded, "A payment was refunded and what it gave is taken back (also a trial that could not be given and went back, or a payment refunded by hand: then user_id is 0).",
		Payment{PaymentID: 7, Kind: "renew", Provider: "stars", Amount: 100, Currency: "XTR", TariffID: 2, TariffName: "Month", TermDays: 30, Disabled: true}},
	{UserCreated, "A subscription was made: by the admin, bought, taken as a trial or as the free plan; not by an import.",
		User{Source: "bot", TariffID: 2}},
	{UserGranted, "An addon with users:grant gave a subscription days or traffic.", Granted{Days: 7, Addon: "referrals"}},
	{UserExpiring, "A subscription ends in 3 or 1 days (the admin's switches decide which are told).",
		Notice{Days: 3, ExpiresAt: 1_800_000_000}},
	{UserExpired, "A subscription ended.", Notice{ExpiresAt: 1_800_000_000}},
	{Traffic90, "A subscription used 90% of its traffic.", Notice{PeriodFrom: 1_790_000_000}},
	{Traffic100, "A subscription used all of its traffic.", Notice{PeriodFrom: 1_790_000_000}},
	{TrialStarted, "An account took its trial.", Trial{TariffID: 3}},
	{TrialNotGiven, "An account paid for a trial it may not have: nothing was made. With refund auto the money went back (PaymentRefunded follows); with manual the admin returns it by hand.",
		NotGiven{PaymentID: 8, Provider: "addon:yookassa", Amount: 100, Currency: "RUB", TariffID: 3, TermDays: 3, Reason: TrialUsed, Refund: RefundedManually}},
	{DeviceBound, "A device took a place on a subscription.", Device{DeviceID: 41, Label: "iPhone 15"}},
	{DeviceUnbound, "A device lost its place on a subscription.", Device{DeviceID: 41, Label: "iPhone 15", By: UnboundBySubscriber}},
	{NodeDown, "A node stopped answering the panel's health checks.", Node{NodeID: 2, Name: "de-1", Code: "timeout", Since: 1_800_000_000}},
	{NodeUp, "A node that was down answers again.", Node{NodeID: 2, Name: "de-1", DownAt: 1_800_000_000}},
}
