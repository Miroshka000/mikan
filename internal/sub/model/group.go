package model

// Group is a group of proxies apart from any core: how the app picks one of its members.
type Group struct {
	Name string
	Type GroupType
	// Members are proxies, groups and the built-in policies (DIRECT, REJECT) by name.
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
)

// Tests says whether the group tests its members: its URL, Interval and Tolerance apply.
func (t GroupType) Tests() bool { return t == URLTest || t == Fallback || t == LoadBalance }
