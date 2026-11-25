package serve

import (
	"net/http"
	"time"

	"mikan/internal/sub/apps"
)

// Outcome is how a request for a subscription ended.
type Outcome string

const (
	OutcomeServed  Outcome = "served"  // the profile, or the subscription page, went out
	OutcomeStub    Outcome = "stub"    // one placeholder entry went out in place of the servers
	OutcomeBlocked Outcome = "blocked" // a rule turned the request away with a status or a page
	OutcomeDropped Outcome = "dropped" // a rule closed the connection without an answer
)

// MaxAgent bounds the User-Agent a log keeps: a client may send what it likes.
const MaxAgent = 512

// LogEntry is one request as the request log keeps it. It holds nothing of the link the
// request came to, so the log has no token, and nothing of what the answer carried, so it
// has no key.
type LogEntry struct {
	At    time.Time
	IP    string
	Agent string // the User-Agent
	// App is the id of the known app that asked (apps.Catalog); empty for one the list does
	// not know.
	App string
	// Rule is what decided (apps.Decision.Rule); empty when nothing did.
	Rule string
	// Format is the format of the answer; empty for a request a rule turned away.
	Format  string
	Outcome Outcome
}

// record writes the request in the log of the subscription. A HEAD is left out: it only
// peeks at the traffic headers, and the fetch that follows is what the log is about.
func (h *Handler) record(r *http.Request, sub Subscription, ex *exchange, outcome Outcome) {
	if outcome == "" || r.Method == http.MethodHead {
		return
	}
	sub.Record(r.Context(), LogEntry{At: h.now(), IP: h.ip(r), Agent: cut(r.Header.Get("User-Agent"), MaxAgent), App: ex.dec.App.ID,
		Rule: cut(ex.dec.Rule, apps.MaxName), Format: ex.format, Outcome: outcome})
}
