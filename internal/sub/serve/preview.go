package serve

import (
	"net/http"

	"mikan/internal/sub/apps"
	"mikan/internal/sub/model"
	"mikan/internal/sub/singbox"
	"mikan/internal/sub/xray"
)

// Preview is what a fetch of a subscription would be answered with, worked out by the very
// code that answers one (answer): the admin panel shows it as what a client gets and how an
// app sees it.
type Preview struct {
	// Status is the answer's status; 0 when a rule closes the connection without one.
	Status int
	Header http.Header
	// Body is the answer's body; nil for the subscription page and a closed connection.
	Body    []byte
	Outcome Outcome
	// Decision is what the response rules came to.
	Decision apps.Decision
	// Format is the format of the answer; "" when a rule turned the request away.
	Format string
	// Template is the own template the profile is made of; the zero one is the built-in
	// profile. TemplateFailed: it did not render for this user, the built-in profile went out.
	Template       model.Template
	TemplateFailed bool
	// Stub is why a placeholder went out in place of the servers (Stub*); "" for none.
	Stub string
	// Inbounds are what the subscription gives, Kept those of them the app takes, Skipped
	// those of these that could not be rendered.
	Inbounds, Kept []model.Inbound
	Skipped        []Skipped
	// Proxies are the servers made for the profile, Served the ones the answer holds (an own
	// Clash template's choice of them).
	Proxies, Served []model.Proxy
	// Notes are what the format's core writes in its own way or leaves out (sing-box, Xray).
	Notes []model.Note
}

// Skipped is an inbound left out of a profile because it could not be rendered.
type Skipped struct {
	Inbound model.Inbound
	Err     string
}

// Preview answers r for sub as Serve would, and keeps the answer instead of sending it.
// Serve's own traces are left out: the request is not logged and a connection a rule drops is
// only told. sub must be the source's subscription for a preview, which takes no device's
// place, places no client and records no fetch: what is kept of the answer is the source's.
func (h *Handler) Preview(sub Subscription, r *http.Request) Preview {
	cfg := sub.Config()
	// Go keeps the request's Host out of its headers; a rule on Host must see it, as in Serve.
	hdr := r.Header.Clone()
	hdr.Set("Host", r.Host)
	tr := &trace{}
	ex := &exchange{dec: cfg.Responses.Decide(hdr), trace: tr}
	rec := newRecorder()
	outcome := h.answer(rec, r, sub, cfg, ex)
	p := Preview{Status: rec.status, Header: rec.header, Body: rec.body.Bytes(), Outcome: outcome, Decision: ex.dec, Format: ex.format,
		Template: tr.tpl, TemplateFailed: tr.failed, Stub: tr.stub, Inbounds: tr.inbounds, Kept: tr.kept, Skipped: tr.skipped,
		Proxies: tr.proxies, Served: tr.served}
	switch {
	case outcome == OutcomeDropped:
		p.Status, p.Header, p.Body = 0, http.Header{}, nil
	case ex.format == FormatHTML:
		p.Body = nil // the subscription page, as a browser opens it
	}
	if tr.profiled {
		// What went out: the built-in profile in place of a template that did not render.
		t := tr.tpl
		if tr.failed {
			t = model.Template{}
		}
		switch ex.format {
		case FormatSingBox:
			p.Notes = append(singbox.Notes(tr.prof, cfg.Groups, cfg.Mode, t), singbox.Dropped(tr.prof, cfg.Groups, cfg.Mode, t)...)
		case FormatXray:
			p.Notes = append(xray.Notes(tr.prof, cfg.Groups, cfg.Mode, t), xray.Dropped(tr.prof, cfg.Groups, cfg.Mode, t)...)
		}
	}
	return p
}
