package subsrc

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"slices"
	"time"

	"mikan/internal/panel/settings"
	"mikan/internal/panel/store"
	"mikan/internal/panel/store/db"
	"mikan/internal/sub/apps"
	"mikan/internal/sub/serve"
)

// What the admin decided about the requests for subscriptions (internal/sub/apps) and the
// log of the requests, as the panel keeps them: the response rules and the blocked apps in
// their tables, the rest in the settings.

// The request log is kept DefaultLogDays unless the admin says otherwise, at most
// MaxLogDays; 0 days turns it off. maxLogPerMinute bounds what one subscription may write
// in a minute: a link that is hammered must not fill the table.
const (
	DefaultLogDays  = 14
	MaxLogDays      = 90
	maxLogPerMinute = 30
)

// LogDays is how many days the request log is kept; 0: there is no log.
func LogDays(ctx context.Context, set *settings.Settings) int {
	days, ok, err := settings.Get[int](ctx, set, settings.KeySubLogDays)
	if err != nil || !ok || days < 0 || days > MaxLogDays {
		return DefaultLogDays
	}
	return days
}

// RuleOpts is what the response rules are checked against: the formats the handler can
// write, and the ids of the templates there are (TemplateIDs) for a rule to name.
func RuleOpts(templates []int64) apps.CheckOpts {
	return apps.CheckOpts{Formats: serve.Formats, Template: func(id int64) bool { return slices.Contains(templates, id) }}
}

// LoadRules reads the admin's response rules in their order.
func LoadRules(ctx context.Context, q *db.Queries) ([]apps.Rule, error) {
	rows, err := q.ListSubRules(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]apps.Rule, 0, len(rows))
	for _, r := range rows {
		rule, err := ruleOf(r)
		if err != nil {
			return nil, fmt.Errorf("rule %d: %w", r.Position, err)
		}
		out = append(out, rule)
	}
	return out, nil
}

func ruleOf(r db.SubRule) (apps.Rule, error) {
	rule := apps.Rule{Name: r.Name, Enabled: r.Enabled}
	if err := json.Unmarshal([]byte(r.Cond), &rule.When); err != nil {
		return rule, err
	}
	if err := json.Unmarshal([]byte(r.Action), &rule.Do); err != nil {
		return rule, err
	}
	return rule.Clean(), nil
}

// SaveRules replaces the response rules with these, in this order; the caller's
// transaction makes it one change. They must have passed apps.CheckRules.
func SaveRules(ctx context.Context, q *db.Queries, rules []apps.Rule) error {
	if err := q.DeleteSubRules(ctx); err != nil {
		return err
	}
	for i, r := range rules {
		r = r.Clean()
		cond, err := json.Marshal(r.When)
		if err != nil {
			return err
		}
		action, err := json.Marshal(r.Do)
		if err != nil {
			return err
		}
		if err := q.InsertSubRule(ctx, db.InsertSubRuleParams{Position: int64(i), Name: r.Name, Enabled: r.Enabled, Cond: string(cond), Action: string(action)}); err != nil {
			return err
		}
	}
	return nil
}

// LoadBlocked reads the ids of the blocked apps.
func LoadBlocked(ctx context.Context, q *db.Queries) ([]string, error) {
	return q.ListBlockedApps(ctx)
}

// SaveBlocked replaces the blocked apps with these ids, which must be known ones, in the
// caller's transaction.
func SaveBlocked(ctx context.Context, q *db.Queries, ids []string) error {
	if err := q.DeleteBlockedApps(ctx); err != nil {
		return err
	}
	for _, id := range slices.Compact(slices.Sorted(slices.Values(ids))) {
		if err := q.InsertBlockedApp(ctx, id); err != nil {
			return err
		}
	}
	return nil
}

// LoadPolicy is what the admin decided about requests, as the profiles are served by it.
// A rule that cannot be read or that the check refuses (kept by a panel of another
// version, edited by hand) is left out and logged: one bad row must not stop every
// subscription.
func LoadPolicy(ctx context.Context, q *db.Queries, set *settings.Settings, log *slog.Logger) (apps.Policy, error) {
	var p apps.Policy
	var err error
	if p.Blocked, err = LoadBlocked(ctx, q); err != nil {
		return p, err
	}
	// An id the catalog dropped blocks nothing.
	p.Blocked = slices.DeleteFunc(p.Blocked, func(id string) bool { return !apps.ValidID(id) })
	if p.Text, err = set.String(ctx, settings.KeySubBlockedText); err != nil {
		return p, err
	}
	on, err := set.On(ctx, settings.SubBuiltin)
	if err != nil {
		return p, err
	}
	p.NoBuiltin = !on
	rows, err := q.ListSubRules(ctx)
	if err != nil {
		return p, err
	}
	templates, err := TemplateIDs(ctx, q)
	if err != nil {
		return p, err
	}
	for _, r := range rows {
		rule, err := ruleOf(r)
		if err == nil {
			err = rule.Check("rule", RuleOpts(templates))
		}
		if err != nil {
			if log != nil {
				log.Warn("subscription: a response rule is left out", "position", r.Position, "name", r.Name, "err", err)
			}
			continue
		}
		p.Rules = append(p.Rules, rule)
	}
	return p, nil
}

// Record writes a request of the subscription in the log (serve.Subscription). It is
// best effort and takes its own time, so that a client that hung up after its answer does
// not leave it unwritten.
func (s *subscription) Record(ctx context.Context, e serve.LogEntry) {
	if s.cfg.LogDays <= 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
	defer cancel()
	_, err := s.h.st.Q.AddSubLog(ctx, db.AddSubLogParams{UserID: s.u.ID, At: e.At.Unix(), Ip: e.IP, Agent: e.Agent, App: e.App, Rule: e.Rule,
		Format: e.Format, Outcome: string(e.Outcome), MaxPerMinute: maxLogPerMinute})
	if err != nil {
		s.h.warn("sub_log", "subscription: the request log is not written", "err", err)
	}
}

// LogItem is one request as the user's card lists it.
type LogItem struct {
	At      time.Time
	IP      string
	Agent   string
	App     string
	Rule    string
	Format  string
	Outcome string
}

// UserLog is the latest requests of a user's subscription, newest first.
func UserLog(ctx context.Context, q *db.Queries, userID int64, limit int) ([]LogItem, error) {
	rows, err := q.ListSubLog(ctx, db.ListSubLogParams{UserID: userID, Limit: int32(limit)})
	if err != nil {
		return nil, err
	}
	out := make([]LogItem, 0, len(rows))
	for _, r := range rows {
		out = append(out, LogItem{At: store.Time(r.At), IP: r.Ip, Agent: r.Agent, App: r.App, Rule: r.Rule, Format: r.Format, Outcome: r.Outcome})
	}
	return out, nil
}

// PruneLog forgets the requests older than the retention, when there is one.
func PruneLog(ctx context.Context, q *db.Queries, set *settings.Settings, now time.Time) (int64, error) {
	days := LogDays(ctx, set)
	// With the log off what was written before goes with the next sweep too: the admin who
	// turned it off wants it gone, not kept for the days it was on.
	return q.PruneSubLog(ctx, now.AddDate(0, 0, -days).Unix())
}
