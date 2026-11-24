package nodesync

import (
	"time"

	"mikan/internal/nodeapi"
	"mikan/internal/panel/domain"
	"mikan/internal/panel/store/db"
)

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
