package serve

import (
	"net/http"

	"mikan/internal/frontpage"
	"mikan/internal/sub/apps"
)

// The response rules (apps.Responder) go first in Serve: before the page, before the
// user is read, and before a device is given keys. This file is what the handler does with
// what they decided: the answers that end the request and the format they may name. The
// stub of an app that is turned away is stub.go's.

// Formats are the formats a response rule may force on an answer, for apps.CheckOpts.
// A format the handler can write (Serve) is listed here to be named.
var Formats = []string{FormatClash, FormatSingBox, FormatURI, FormatXray}

// exchange is one request as the handler works it.
type exchange struct {
	dec apps.Decision
	// format is the format the answer takes; empty for a request a rule turned away.
	format string
	// abort: the connection could not be closed from inside the handler, so the handler
	// ends it by aborting once the request is logged.
	abort bool
}

// refuse answers a request a rule turned away: with the status or the page it names, or
// with nothing at all.
func (h *Handler) refuse(w http.ResponseWriter, r *http.Request, ex *exchange) Outcome {
	a := ex.dec.Action
	if a.Respond == apps.RespondDrop {
		if !drop(w) {
			ex.abort = true
		}
		return OutcomeDropped
	}
	hd := w.Header()
	hd.Set("Cache-Control", "no-store")
	ruleHeaders(hd, a.Headers)
	switch a.Respond {
	case apps.Respond403:
		http.Error(w, "403 Forbidden", http.StatusForbidden)
	case apps.Respond404:
		frontpage.NotFound(w)
	default:
		htmlHeaders(hd)
		if r.Method != http.MethodHead {
			_, _ = w.Write([]byte(a.HTML))
		}
	}
	return OutcomeBlocked
}

// drop closes the connection without a word; false when it cannot be taken over, which is
// the case of HTTP/2, where one connection carries many requests.
func drop(w http.ResponseWriter) bool {
	conn, _, err := http.NewResponseController(w).Hijack()
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}
