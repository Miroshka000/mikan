package domain

import (
	"context"
	"database/sql"

	"mikan/internal/panel/store/db"
)

// BulkSampleSize is how many of the users a plan changes it shows one by one.
const BulkSampleSize = 10

// BulkPlan is what Bulk would do to a list of users now, worked out before it is done, so
// the admin sees it and confirms: of the users asked for (each once) how many are still
// there (Bulk applies the action to them all), how many of those it changes at all (one
// already off is not turned off again), the first of the changed in id order with what
// changes, and, for a deletion, what the users deleted would take with them.
type BulkPlan struct {
	Asked, Found, Changed int
	// Active is how many of the users found can connect now: what a deletion or a switch-off
	// cuts off.
	Active int
	Sample []BulkChange
	// AtStake is for BulkDelete alone (nil otherwise): how many of the users found hold what
	// the removal of inactive ones never takes (InactiveUser.Keep): money on the linked
	// account's balance, a payment not applied yet or a top-up waiting, traffic packages
	// still running.
	AtStake *db.CountUsersAtStakeRow
}

// BulkChange is one user of a plan as it is, and what the action makes of it.
type BulkChange struct {
	User  db.User
	State string // the user's State now
	// Expires is the term's end after BulkExtend (the user's own otherwise).
	Expires sql.NullInt64
}

// BulkPlan works out Bulk(ids, action, opt) without doing it: the same rules, the same
// refusals. A user deleted between the plan and the action is skipped by the action, one
// changed meanwhile is changed as it is then.
func (s *Users) BulkPlan(ctx context.Context, ids []int64, action string, opt BulkOpt) (BulkPlan, error) {
	if err := checkBulk(action, opt); err != nil {
		return BulkPlan{}, err
	}
	want := bulkIDs(ids)
	users, err := s.st.Q.ListUsersByIDs(ctx, want)
	if err != nil {
		return BulkPlan{}, err
	}
	now := s.now()
	p := BulkPlan{Asked: len(want), Found: len(users)}
	found := make([]int64, len(users))
	for i, u := range users {
		found[i] = u.ID
	}
	grants, err := GrantsLeftOf(ctx, s.st.Q, found, now)
	if err != nil {
		return BulkPlan{}, err
	}
	for _, u := range users {
		c := BulkChange{User: u, State: State(u, grants.Main(u.ID), now), Expires: u.ExpiresAt}
		if CanConnect(c.State) {
			p.Active++
		}
		if !bulkChanges(u, action, opt) {
			continue
		}
		if action == BulkExtend {
			c.Expires = sql.NullInt64{Int64: opt.extension().until(now, u.ExpiresAt, u.BillingDay).Unix(), Valid: true}
		}
		p.Changed++
		if len(p.Sample) < BulkSampleSize {
			p.Sample = append(p.Sample, c)
		}
	}
	if action == BulkDelete && len(found) > 0 {
		stake, err := s.st.Q.CountUsersAtStake(ctx, db.CountUsersAtStakeParams{Now: now.Unix(), Ids: found})
		if err != nil {
			return BulkPlan{}, err
		}
		p.AtStake = &stake
	}
	return p, nil
}

// bulkChanges says whether action changes u at all. An extension, a new traffic period and a
// deletion change everyone; the rest leave alone who is that way already.
func bulkChanges(u db.User, action string, opt BulkOpt) bool {
	switch action {
	case BulkDisable:
		return u.Status != "disabled"
	case BulkEnable:
		return u.Status == "disabled"
	case BulkHide:
		return u.Hidden == 0
	case BulkUnhide:
		return u.Hidden != 0
	case BulkMove:
		return u.FolderID != NullInt(opt.Folder)
	}
	return true
}
