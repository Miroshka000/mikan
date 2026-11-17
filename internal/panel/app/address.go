package app

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"time"

	"mikan/internal/pace"
	"mikan/internal/panel/acme"
	"mikan/internal/panel/domain"
	"mikan/internal/panel/nodesync"
	"mikan/internal/panel/settings"
	"mikan/internal/panel/tgate"
	"mikan/internal/panel/tlscert"
)

// addressFollower keeps what hangs on the panel's address (settings.Address) in step with
// it, whoever changed it: the settings page, the CLI, the panel's own look at the address
// its server has. The names left behind keep their certificates (acme.Formers), the
// panel's own certificate and the subscription domain's follow the new names, the panel's
// own node shows its self-steal site under the new domain, the bot's Mini App button opens
// the new address, and the nodes get their states again: the cascades into the panel's own
// node, the API locks. What was followed last is kept (settings.KeyAddressApplied), so a
// change made while the panel was down is followed when it starts.
type addressFollower struct {
	set      *settings.Settings
	formers  *acme.Formers     // nil in development
	certs    *acme.Manager     // the panel's certificate; nil in development
	subCerts *acme.Manager     // the subscription domain's; nil in development
	names    *tlscert.BySNI    // nil where the panel runs no server
	nodes    *nodesync.Manager // nil without nodes
	inbounds *domain.Inbounds
	gateway  *tgate.Gateway
	log      *slog.Logger
	now      func() time.Time

	mu sync.Mutex
	// menu is the move of the bot's Mini App button Telegram did not take: tried again, no
	// sooner than menuRetry, until it goes through or there is nothing left to move. A later
	// change of the address does not wait for it. Under mu.
	menu      *menuMove
	menuRetry time.Time
}

// menuMove points the Mini App button opening one subscription address at another.
type menuMove struct{ from, to string }

// addressEvery is how often the address is looked at: the CLI's changes are followed this
// soon.
const addressEvery = 5 * time.Second

// A move of the Mini App button that failed is tried again this often, each try this long
// at most: it holds the follower, which a save of the settings waits for.
const (
	menuRetryEvery   = time.Minute
	menuRetryTimeout = 10 * time.Second
)

func (a *addressFollower) Run(ctx context.Context) {
	pace.Tick(ctx, addressEvery, func(ctx context.Context) {
		if err := a.Check(ctx); err != nil && ctx.Err() == nil {
			a.log.Warn("address: not followed", "err", err)
		}
	})
}

// Check follows the address when it is not the one followed last.
func (a *addressFollower) Check(ctx context.Context) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	cur, err := a.set.Address(ctx)
	if err != nil {
		return err
	}
	applied, ok, err := settings.Get[settings.Address](ctx, a.set, settings.KeyAddressApplied)
	if err != nil {
		return err
	}
	if ok && applied.Same(cur) {
		a.retryMenuButton(ctx)
		return nil
	}
	if ok {
		a.follow(ctx, applied, cur)
	}
	// The first start with this: the address as it is was followed already.
	return settings.Set(ctx, a.set, settings.KeyAddressApplied, cur)
}

func (a *addressFollower) follow(ctx context.Context, prev, next settings.Address) {
	a.log.Info("address: the panel's address changed", "domain", next.Domain, "was", prev.Domain, "public_host", next.PublicHost, "was_host", prev.PublicHost,
		"sub_domain", next.SubDomain, "was_sub", prev.SubDomain)
	// The settings page and the CLI keep the former names with the change; one made another
	// way gets them here.
	if err := settings.UpdateFormers(ctx, a.set, prev, a.now()); err != nil {
		a.log.Warn("address: former names not saved", "err", err)
	}
	// The names left behind take their certificates before the panel's own move on.
	if a.formers != nil {
		a.formers.Reconcile(ctx, false)
		a.formers.Wake()
	}
	if !strings.EqualFold(prev.Identifier(), next.Identifier()) && a.certs != nil {
		a.certs.Renew()
	}
	if prev.SubExternal != next.SubExternal || !strings.EqualFold(prev.SubDomain, next.SubDomain) {
		if a.names != nil {
			name := next.SubDomain
			if next.SubExternal {
				name = ""
			}
			a.names.SetName(name)
		}
		if a.subCerts != nil {
			a.subCerts.Renew()
		}
	}
	// A panel left without a domain keeps the old one as a former name, served under it:
	// the self-steal site still works under that name.
	if prev.Domain != "" && next.Domain != "" && !strings.EqualFold(prev.Domain, next.Domain) && a.inbounds != nil {
		changed, err := a.inbounds.FollowPanelDomain(ctx, prev.Domain, next.Domain)
		if err != nil {
			a.log.Warn("address: the self-steal sites of the panel's node not moved", "err", err)
		}
		for _, in := range changed {
			a.log.Info("address: a self-steal site moved to the new domain", "inbound", in.Name, "domain", next.Domain)
		}
	}
	a.moveMenuButton(ctx, prev, next)
	if a.nodes != nil {
		a.nodes.AddressChanged()
	}
}

// moveMenuButton points the bot's Mini App button at the subscriptions' new address.
func (a *addressFollower) moveMenuButton(ctx context.Context, prev, next settings.Address) {
	if a.gateway == nil {
		return
	}
	// Old saved snapshots did not contain ports; adopt current ports once on upgrade.
	if prev.PanelPort == 0 {
		prev.PanelPort, prev.SubPort = next.PanelPort, next.SubPort
	}
	from, to := prev.SubEndpoint(), next.SubEndpoint()
	if strings.EqualFold(from.Host, to.Host) && from.Port == to.Port {
		return
	}
	paths, err := a.set.Paths(ctx)
	if err != nil || paths.Sub == "" {
		return
	}
	base := func(ep settings.Endpoint) string {
		if ep.URL() == "" {
			return ""
		}
		return ep.URL() + "/" + paths.Sub
	}
	m := menuMove{from: base(from), to: base(to)}
	// A move Telegram did not take left the button at the address before that one.
	if a.menu != nil {
		m.from = a.menu.from
	}
	a.tryMenuMove(ctx, m)
}

// retryMenuButton tries again the move of the Mini App button Telegram did not take.
func (a *addressFollower) retryMenuButton(ctx context.Context) {
	if a.gateway == nil || a.menu == nil || a.now().Before(a.menuRetry) {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, menuRetryTimeout)
	defer cancel()
	a.tryMenuMove(ctx, *a.menu)
}

// tryMenuMove moves the Mini App button; a move that fails stays to be tried again.
func (a *addressFollower) tryMenuMove(ctx context.Context, m menuMove) {
	moved, err := a.gateway.MoveMenuButton(ctx, m.from, m.to)
	if err != nil {
		if a.menu == nil || *a.menu != m {
			a.log.Warn("address: the bot's Mini App button not moved, it is tried again", "err", err)
		}
		a.menu, a.menuRetry = &m, a.now().Add(menuRetryEvery)
		return
	}
	a.menu = nil
	if moved {
		a.log.Info("address: the bot's Mini App button opens the new address")
	}
}

// formerDropped follows a former name let go: the panel's own node shows its self-steal
// site under it no more, and the nodes take the change.
func (a *addressFollower) formerDropped(ctx context.Context, name string) {
	if a.inbounds != nil {
		if _, err := a.inbounds.ForgetPanelName(ctx, name); err != nil {
			a.log.Warn("address: a former name not taken off the self-steal sites", "name", name, "err", err)
		}
	}
	if a.nodes != nil {
		a.nodes.SlotsChanged()
	}
}
