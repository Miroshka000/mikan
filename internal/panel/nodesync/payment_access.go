package nodesync

import (
	"context"
	"time"

	"mikan/internal/nodeapi"
	"mikan/internal/panel/domain"
	"mikan/internal/panel/store/db"
)

// addPaymentTraffic stores a counters batch's renewal bytes, by user and expiry, in one
// statement.
func addPaymentTraffic(ctx context.Context, q *db.Queries, bytes map[[2]int64]int64) error {
	if len(bytes) == 0 {
		return nil
	}
	arg := db.AddPaymentAccessTrafficParams{
		UserIds: make([]int64, 0, len(bytes)), ExpiredAts: make([]int64, 0, len(bytes)), Bytes: make([]int64, 0, len(bytes)),
	}
	for key, n := range bytes {
		arg.UserIds, arg.ExpiredAts, arg.Bytes = append(arg.UserIds, key[0]), append(arg.ExpiredAts, key[1]), append(arg.Bytes, n)
	}
	return q.AddPaymentAccessTraffic(ctx, arg)
}

func (s *Syncer) paymentInbound(snap *snapshot) (db.Inbound, bool) {
	for _, in := range snap.inbounds {
		if in.NodeID == s.id && snap.payment.Matches(in) {
			return in, true
		}
	}
	return db.Inbound{}, false
}

func (s *Syncer) paymentAccess(snap *snapshot, inbound string, now time.Time) (*nodeapi.PaymentAccess, []nodeapi.Policy) {
	pos := snap.counters[s.id]
	state := snap.payment.NodeState(inbound, snap.paymentPage)
	secrets := make(map[int64]string, len(snap.slots))
	for _, slot := range snap.slots {
		secrets[slot.ID] = slot.Secret
	}
	var policies []nodeapi.Policy
	for _, u := range snap.users {
		until, ok := snap.payment.Window(u, now)
		secret := secrets[u.SlotID.Int64]
		if !ok || !u.SlotID.Valid || secret == "" {
			continue
		}
		slot := domain.PaymentSlot(u, secret)
		left := snap.payment.Remaining(snap.paymentUsed[[2]int64{u.ID, u.ExpiresAt.Int64}])
		if left == 0 {
			continue
		}
		state.Slots = append(state.Slots, slot)
		p := nodeapi.Policy{
			Slot: slot.Name, Allowed: true, ExpiresAt: until, Inbounds: []string{inbound}, DeviceLimit: 1,
			QuotaRemaining: left, BaseSeq: pos.seq, SpeedMbps: snap.payment.SpeedMbps,
			TorrentExempt: snap.torrent.IsExempt(u.ID),
		}
		if !p.TorrentExempt {
			p.BannedUntil = snap.bans[u.ID]
		}
		policies = append(policies, p)
	}
	return state, policies
}
