package domain

import (
	"context"
	"database/sql"
	"errors"

	"mikan/internal/panel/store/db"
)

// Traffic pools (GitHub issue #6): inbounds in a pool count to it, with a limit per user
// apart from the main quota. Users get their pool limits from the tariff, like the main
// limit, and the admin may change them per user.

// ApplyTariffPools gives the user the tariff's pool limits and the pools it leaves out:
// pools the tariff does not list become unlimited. What the user used stays.
func ApplyTariffPools(ctx context.Context, q *db.Queries, userID, tariffID int64) error {
	if err := q.ClearUserPoolLimits(ctx, userID); err != nil {
		return err
	}
	ps, err := q.ListTariffPools(ctx, tariffID)
	if err != nil {
		return err
	}
	for _, p := range ps {
		limit := sql.NullInt64{Int64: p.TrafficLimit, Valid: !p.Excluded}
		if err := q.SetUserPoolLimit(ctx, db.SetUserPoolLimitParams{UserID: userID, PoolID: p.PoolID, TrafficLimit: limit, Excluded: p.Excluded}); err != nil {
			return err
		}
	}
	return nil
}

// ApplyTariffPoolsToAll gives every user of the tariff its pool limits now, as a renewal
// would: the tariff's limits and closed pools replace the users' own, what they used
// stays. Changing a tariff leaves the subscriptions given out alone; this brings them up
// to it, so that its traffic packages are offered to them too. It returns how many users
// it changed; ErrNotFound for no such tariff.
func (s *Users) ApplyTariffPoolsToAll(ctx context.Context, tariffID int64) (int, error) {
	n := 0
	err := s.st.Tx(ctx, func(q *db.Queries) error {
		n = 0
		if _, err := q.GetTariff(ctx, tariffID); errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		} else if err != nil {
			return err
		}
		ids, err := q.ListUserIDsOfTariff(ctx, sql.NullInt64{Int64: tariffID, Valid: true})
		if err != nil {
			return err
		}
		for _, id := range ids {
			if err := ApplyTariffPools(ctx, q, id, tariffID); err != nil {
				return err
			}
			n++
		}
		return nil
	})
	if err == nil && n > 0 {
		s.changes.PoliciesChanged() // the nodes count the pools' new limits
	}
	return n, err
}

// PoolExhausted says whether the user's pool has no traffic left: its base quota is used
// and nothing is left of its grants (GrantsLeft.Pool).
func PoolExhausted(p db.UserPool, grants int64) bool {
	return TrafficLeft(p.TrafficLimit, p.UsedUp+p.UsedDown, grants) == 0
}

// ExhaustedPools are the user's pools that do not serve: nothing left, or left out of the
// user's tariff, by pool id. left is UserGrantsLeft of the user, which the caller has read.
func ExhaustedPools(ctx context.Context, q *db.Queries, userID int64, left GrantsLeft) (map[int64]bool, error) {
	ps, err := q.ListUserPools(ctx, userID)
	if err != nil {
		return nil, err
	}
	out := map[int64]bool{}
	for _, p := range ps {
		if p.Excluded || PoolExhausted(p, left.Pool(userID, p.PoolID)) {
			out[p.PoolID] = true
		}
	}
	return out, nil
}
