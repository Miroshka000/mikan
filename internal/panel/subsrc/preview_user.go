package subsrc

import (
	"bytes"
	"context"
	"net/http"
	"net/url"
	"strings"

	"mikan/internal/panel/store/db"
	"mikan/internal/sub/serve"
)

// PreviewInput is a request of a subscription as the admin writes it for a preview: the
// headers an app sends (User-Agent among them; Host names the address it asks, the
// subscriptions' own by default) and the format a link may name (?format=).
type PreviewInput struct {
	Headers http.Header
	Format  string
}

// UserPreview is what the app of a request would get of a user's subscription (PreviewUser).
type UserPreview struct {
	serve.Preview
	Group   int64 // the group the user is in (domain.GroupOf); 0: none
	ByGroup bool  // the user stands on its group's own servers
	Own     bool  // the protocols of the user's card decide what it may use
	// Device is what the device of the request would get (domain.Admit*); "" when its keys
	// were not asked: a rule, the page or a stub answered first.
	Device string
	// Left are the servers of the panel the subscription leaves out, and the addresses of its
	// servers the user is not shown, with why.
	Left []Left
}

// maskedToken stands for the link's token wherever a preview's answer names it.
const maskedToken = "•••"

// PreviewUser answers in for u's subscription as a fetch of it is answered
// (serve.Handler.Preview) and changes nothing: no device takes a place, u is placed in no
// balance group, neither the fetch nor the request is recorded, and the profile carries the
// preview's keys. The link's token is masked wherever the answer names it.
func (h *Handler) PreviewUser(ctx context.Context, u db.User, in PreviewInput) (UserPreview, error) {
	cfg, err := h.cfg(ctx)
	if err != nil {
		return UserPreview{}, err
	}
	sub, err := h.subscriptionOf(ctx, u, cfg)
	if err != nil {
		return UserPreview{}, err
	}
	sub.dry = true
	target := "/" + u.SubToken
	if in.Format != "" {
		target += "?format=" + url.QueryEscape(in.Format)
	}
	r, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://preview"+target, nil)
	if err != nil {
		return UserPreview{}, err
	}
	r.Header = in.Headers.Clone()
	if r.Header == nil {
		r.Header = http.Header{}
	}
	r.Host = r.Header.Get("Host")
	r.Header.Del("Host")
	if r.Host == "" {
		r.Host = subHost(cfg.SubBase)
	}
	r.RemoteAddr = "127.0.0.1:1"
	p := h.profiles.Preview(sub, r)
	// What the user may use is told whatever answered: a rule or a stub asks for none of it.
	if sub.reach.all == nil {
		if _, err := sub.Inbounds(ctx); err != nil {
			return UserPreview{}, err
		}
	}
	out := UserPreview{Preview: p, Group: sub.reach.group, ByGroup: sub.reach.byGroup, Own: sub.reach.access.Own(), Device: sub.admitted, Left: sub.reach.left}
	out.Body = bytes.ReplaceAll(out.Body, []byte(u.SubToken), []byte(maskedToken))
	for name, vs := range out.Header {
		for i, v := range vs {
			vs[i] = strings.ReplaceAll(v, u.SubToken, maskedToken)
		}
		out.Header[name] = vs
	}
	return out, nil
}

// subHost is the host (and port) of the subscriptions' address base; "localhost" without one.
func subHost(base string) string {
	if u, err := url.Parse(base); err == nil && u.Host != "" {
		return u.Host
	}
	return "localhost"
}
