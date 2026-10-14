package app

import (
	"context"
	"time"
)

// What only grows is cut here, every ten minutes with the rest of the housekeeping. The
// hourly traffic is cut by the node syncer, which writes it.
const (
	// trafficDailyKeep: the dashboard shows 30 days and the month's top; a year back is
	// there for the admin's own reports.
	trafficDailyKeep = 400 * 24 * time.Hour
	// auditKeep is how long the admin's journal reaches back.
	auditKeep = 180 * 24 * time.Hour
)

// maintain forgets what is too old to matter: daily traffic, the audit journal and devices
// nobody has used for 90 days (their keys burn).
func (p *Panel) maintain(ctx context.Context) {
	now := p.now()
	if err := p.st.Q.PruneTrafficDaily(ctx, now.Add(-trafficDailyKeep).Unix()/86400); err != nil {
		p.log.Error("prune daily traffic", "err", err)
	}
	if err := p.st.Q.PruneNodeTrafficDaily(ctx, now.Add(-trafficDailyKeep).Unix()/86400); err != nil {
		p.log.Error("prune daily node traffic", "err", err)
	}
	if _, err := p.st.Q.PruneAudit(ctx, now.Add(-auditKeep).Unix()); err != nil {
		p.log.Error("prune audit log", "err", err)
	}
	if n, err := p.devices.ForgetIdle(ctx); err != nil {
		p.log.Error("forget idle devices", "err", err)
	} else if n > 0 {
		p.log.Info("forgot idle devices", "count", n)
	}
	if err := p.devices.Prune(ctx); err != nil {
		p.log.Error("prune device pauses and unbinds", "err", err)
	}
}
