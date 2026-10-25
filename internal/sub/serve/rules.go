package serve

import (
	"net/http"
	"strings"

	"mikan/internal/frontpage"
	"mikan/internal/sub/apps"
	"mikan/internal/textutil"
)

// The response rules (apps.Responder) go first in Serve: before the page, before the
// user is read, and before a device is given keys. This file is what the handler does with
// what they decided: the answers that end the request, the format they may name and the
// stub of an app that is turned away.

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

// Resolve is the format of the answer to a request with the headers h and the query's
// ?format=: the one the deciding rule names, else the one Format picks. An app that runs
// whole xray configs (apps.XrayJSON) gets them in place of the links Format picks for it
// while xrayJSON (Config.XrayJSON) is on, unless the request names the links itself. The
// admin panel's tester asks it too, so that it shows what Serve does.
func Resolve(h http.Header, query string, dec apps.Decision, xrayJSON bool) string {
	if f := dec.Action.Format; f != "" {
		return f
	}
	ua := h.Get("User-Agent")
	f := Format(ua, h.Get("Accept"), query)
	if f == FormatURI && queryFormat(query) == "" && xrayJSON && apps.TakesXrayJSON(ua) {
		return FormatXray
	}
	return f
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

// unsupportedError is why a request gets a stub that says its app is not supported.
type unsupportedError struct{ text string }

func (e *unsupportedError) Error() string { return "unsupported: " + e.text }

// StubText is the words of the entry an app that is turned away gets in place of the
// servers: the admin's own, else the built-in ones, which send the visitor to the apps the
// admin has not blocked. The admin panel's tester shows it too.
func StubText(lang string, d apps.Decision) string {
	if t := strings.TrimSpace(d.Action.Text); t != "" {
		return fillTitle(t, nil)
	}
	en := textutil.Lang(lang).En()
	switch {
	case len(d.Install) == 0 && en:
		return "⛔ This app is not supported — open the subscription link in a browser"
	case len(d.Install) == 0:
		return "⛔ Это приложение не поддерживается — откройте ссылку подписки в браузере"
	case en:
		return "⛔ This app is not supported — install " + listed(d.Install, "or")
	}
	return "⛔ Это приложение не поддерживается — установите " + listed(d.Install, "или")
}

// listed is "A", "A or B" and "A, B or C".
func listed(names []string, or string) string {
	if len(names) < 2 {
		return strings.Join(names, "")
	}
	return strings.Join(names[:len(names)-1], ", ") + " " + or + " " + names[len(names)-1]
}
