package serve

import (
	"errors"

	"mikan/internal/sub/model"
	"mikan/internal/sub/profile"
)

// trace is what a Preview keeps of the way answer went: the servers at each step, the
// template, why a stub went out. answer tells it as it goes; a fetch of a client has none,
// and every method of a nil trace does nothing, so the answer itself does not change.
type trace struct {
	inbounds, kept  []model.Inbound
	skipped         []Skipped
	proxies, served []model.Proxy
	tpl             model.Template
	failed          bool // the own template did not render: the built-in profile went out
	stub            string
	prof            profile.Profile // the profile the format's core is given
	profiled        bool
}

// gave are the inbounds the subscription gives (Subscription.Inbounds).
func (t *trace) gave(ins []model.Inbound) {
	if t != nil {
		t.inbounds = ins
	}
}

// keep are the inbounds of those the app takes (build.ForApp).
func (t *trace) keep(ins []model.Inbound) {
	if t != nil {
		t.kept = ins
	}
}

// skip is an inbound build.Proxies left out because it could not be rendered.
func (t *trace) skip(in model.Inbound, err error) {
	if t != nil {
		t.skipped = append(t.skipped, Skipped{Inbound: in, Err: err.Error()})
	}
}

// made are the servers made for the profile.
func (t *trace) made(ps []model.Proxy) {
	if t != nil {
		t.proxies = ps
	}
}

// template is the own template the profile is made of; the zero one is the built-in profile.
func (t *trace) template(tpl model.Template) {
	if t != nil {
		t.tpl = tpl
	}
}

// fellBack: the own template did not render for this user, and the built-in profile went out.
func (t *trace) fellBack() {
	if t != nil {
		t.failed = true
	}
}

// profile is the profile the format's core is given, and the servers the answer holds.
func (t *trace) profile(p profile.Profile, served []model.Proxy) {
	if t != nil {
		t.prof, t.profiled, t.served = p, true, served
	}
}

// stubbed is why a placeholder went out in place of the servers (stub).
func (t *trace) stubbed(reason error) {
	if t != nil {
		t.stub = stubReason(reason)
	}
}

// What a stub went out for, as a Preview tells it.
const (
	StubUnsupported = "unsupported"    // a rule or the list of blocked apps turned the app away
	StubDeviceLimit = "device_limit"   // the subscription's places for devices are taken
	StubNoHWID      = "no_hwid"        // the app sends no device id, and one is required
	StubBanned      = "device_banned"  // the admin banned the device
	StubUnbound     = "device_unbound" // the subscriber unbound the device a while ago
	StubInactive    = "inactive"       // the subscription cannot connect: its notice in place of the servers
	StubNoProxies   = "no_proxies"     // nothing the app can use
)

func stubReason(reason error) string {
	var ue *UnboundError
	var ie *inactiveError
	var un *unsupportedError
	switch {
	case errors.As(reason, &un):
		return StubUnsupported
	case errors.Is(reason, model.ErrNoProxies):
		return StubNoProxies
	case errors.As(reason, &ie):
		return StubInactive
	case errors.Is(reason, ErrDeviceBanned):
		return StubBanned
	case errors.As(reason, &ue):
		return StubUnbound
	case errors.Is(reason, ErrNoHWID):
		return StubNoHWID
	}
	return StubDeviceLimit
}
