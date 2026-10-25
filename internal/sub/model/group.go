package model

// Group is a group of proxies apart from any core: how the app picks one of its members.
// Each core writes it as its own kind of group, or as the nearest one it has with a Note.
type Group struct {
	Name string
	Type GroupType
	// Members are proxies, groups and the built-in policies (DIRECT, REJECT) by name; a
	// chain's are its hops in order, the entry first and the exit last.
	Members []string
	// URL, Interval (seconds) and Tolerance (milliseconds) are of the groups that test their
	// members; zero values are the core's own defaults.
	URL       string
	Interval  int
	Tolerance int
	// Strategy is a load-balance's: round-robin, consistent-hashing or sticky-sessions
	// (mihomo's names); "" is the core's default.
	Strategy string
	// Hidden keeps a group the app needs out of its list of groups.
	Hidden bool
}

// GroupType is how a group picks a member, by mihomo's names for it.
type GroupType string

const (
	Select      GroupType = "select"       // the user does
	URLTest     GroupType = "url-test"     // the fastest that answers
	Fallback    GroupType = "fallback"     // the first that answers, in order
	LoadBalance GroupType = "load-balance" // spread over the members
	// Chain is a cascade of hops (Citrus): every hop after the first is dialed through the
	// hop before it, and the group goes out of the last.
	Chain GroupType = "chain"
)

// Tests says whether the group tests its members: its URL, Interval and Tolerance apply.
func (t GroupType) Tests() bool { return t == URLTest || t == Fallback || t == LoadBalance }

// Note is what a core made of a group it cannot write as it is: the nearest form, which the
// admin panel's preview tells. Code names it for the panel's words.
type Note struct {
	Core  string `json:"core" enum:"mihomo,singbox,xray"`
	Group string `json:"group"`
	Code  string `json:"code" enum:"fallback_fastest,balance_fastest,balance_random,balance_flat,select_first,chain_group_hop,chain_dialer_taken"`
}
