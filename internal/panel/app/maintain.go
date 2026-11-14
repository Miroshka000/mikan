package app

import (
	"context"
	"time"

	"mikan/internal/panel/domain"
	"mikan/internal/panel/settings"
	"mikan/internal/panel/subsrc"
)

// What only grows is cut here, every ten minutes with the rest of the housekeeping. The
// hourly traffic is cut by the node syncer, which writes it.
const (
	// trafficDailyKeep: the dashboard shows 30 days and the month's top; a year back is
	// there for the admin's own reports.
	trafficDailyKeep = 400 * 24 * time.Hour
	// auditKeep is how long the admin's journal reaches back.
	auditKeep = 180 * 24 * time.Hour
	// removalsPerRound bounds the inactive subscriptions one round removes: rules turned on
	// over years of them go over a few rounds, each removal a short transaction of its own.
	removalsPerRound = 200
)

// maintain forgets what is too old to matter (daily traffic, the subscriptions' request log
// by its retention setting, the audit journal, devices nobody has used for 90 days: their
// keys burn) and moves the subscriptions' lifecycle on.
func (p *Panel) maintain(ctx context.Context) {
	now := p.now()
	if err := p.st.Q.PruneTrafficDaily(ctx, now.Add(-trafficDailyKeep).Unix()/86400); err != nil {
		p.log.Error("prune daily traffic", "err", err)
	}
	if err := p.st.Q.PruneNodeTrafficDaily(ctx, now.Add(-trafficDailyKeep).Unix()/86400); err != nil {
		p.log.Error("prune daily node traffic", "err", err)
	}
	if _, err := p.st.Q.PruneUserSites(ctx, domain.SiteDay(now.AddDate(0, 0, -domain.DetailDays(ctx, settings.New(p.st.Q))))); err != nil {
		p.log.Error("prune user sites", "err", err)
	}
	if _, err := subsrc.PruneLog(ctx, p.st.Q, settings.New(p.st.Q), now); err != nil {
		p.log.Error("prune subscription request log", "err", err)
	}
	if _, err := p.st.Q.PruneAudit(ctx, now.Add(-auditKeep).Unix()); err != nil {
		p.log.Error("prune audit log", "err", err)
	}
	if n, err := p.devices.ForgetIdle(ctx); err != nil {
		p.log.Error("forget idle devices", "err", err)
	} else if n > 0 {
		p.log.Info("forgot idle devices", "count", n)
	}
	if n, err := p.devices.ExpireExtra(ctx); err != nil {
		p.log.Error("expire bought device places", "err", err)
	} else if n > 0 {
		p.log.Info("bought device places ended with their term", "subscriptions", n)
	}
	if err := p.devices.Prune(ctx); err != nil {
		p.log.Error("prune device pauses and unbinds", "err", err)
	}
	p.lifecycle(ctx)
}

// lifecycle is the housekeeping's round of the subscriptions' lifecycle (domain/lifecycle.go):
// the terms that wait for their first connection keep their clocks still, the next tariffs
// take over from the terms that end before the next round (domain.SwitchAhead: this round is
// ten minutes from it), and the subscriptions inactive long enough go by the admin's rules.
func (p *Panel) lifecycle(ctx context.Context) {
	if _, err := p.users.SlideWaiting(ctx); err != nil {
		p.log.Error("hold the waiting terms", "err", err)
	}
	if n, err := p.users.SwitchDue(ctx); err != nil {
		p.log.Error("switch to the next tariffs", "switched", n, "err", err)
	} else if n > 0 {
		p.log.Info("switched to the next tariffs", "subscriptions", n)
	}
	rules, err := domain.LoadLifecycle(ctx, p.Settings)
	if err != nil {
		p.log.Error("read the lifecycle rules", "err", err)
		return
	}
	if n, err := p.users.RemoveInactive(ctx, rules.Removal, removalsPerRound); err != nil {
		p.log.Error("remove inactive subscriptions", "removed", n, "err", err)
	} else if n > 0 {
		p.log.Info("removed inactive subscriptions", "count", n, "days", rules.Removal.Days)
	}
}
