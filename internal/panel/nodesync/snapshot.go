package nodesync

import (
	"context"
	"database/sql"
	"maps"
	"strconv"
	"strings"

	"mikan/internal/panel/domain"
	"mikan/internal/panel/exitdns"
	"mikan/internal/panel/filters"
	"mikan/internal/panel/settings"
	"mikan/internal/panel/store/db"
	"mikan/internal/panel/torrent"
)

// snapshot is what the nodes' states and policies are built from: the same tables for
// every node, read once and shared by the syncers, in one REPEATABLE READ transaction. The
// users' counters and the nodes' counters positions come from the same moment: a quota
// "as of BaseSeq" then holds exactly the batches up to BaseSeq, none twice, none missing.
type snapshot struct {
	changes uint64           // Manager.changes when it was read
	batches map[int64]uint64 // Manager.batches when it was read

	users    []db.User
	owners   []db.ListSlotUsersRow
	inbounds []db.Inbound
	slots    []db.Slot
	grants   domain.GrantsLeft
	pools    []db.UserPool
	servers  map[int64]domain.Servers // the tariffs that include only chosen servers
	counters map[int64]counterPos
	torrent  torrent.Config
	bans     map[int64]int64 // user → when the torrent blocker's ban ends (unix)
	filters  filters.Config
	exitDNS  exitdns.Config
	detail   bool // the nodes sum the bytes by site
}

type counterPos struct {
	epoch string
	seq   int64
}

// snapshot returns the shared snapshot for node's syncer, reading a new one when anything
// it holds may have changed: a change the API or the upkeep announced, or a traffic batch
// of this node stored since. A batch of another node does not matter here: that node's
// traffic reaches this node's quotas with the next snapshot, as it always did.
func (m *Manager) snapshot(ctx context.Context, node int64) (*snapshot, error) {
	m.snapMu.Lock()
	defer m.snapMu.Unlock()
	if s := m.snap; s != nil && s.changes == m.changes.Load() && s.batches[node] == m.batchesOf(node) {
		return s, nil
	}
	// Taken before the read: what moves during it makes the next caller read again.
	changes, batches := m.changes.Load(), m.batchesCopy()
	s, err := m.readSnapshot(ctx)
	if err != nil {
		return nil, err
	}
	s.changes, s.batches = changes, batches
	m.snap = s
	return s, nil
}

func (m *Manager) readSnapshot(ctx context.Context) (s *snapshot, err error) {
	tx, err := m.st.DB.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	q := m.st.Q.WithTx(tx)
	s = &snapshot{counters: map[int64]counterPos{}, bans: map[int64]int64{}}
	if s.users, err = q.ListUsers(ctx); err != nil {
		return nil, err
	}
	if s.owners, err = q.ListSlotUsers(ctx); err != nil {
		return nil, err
	}
	if s.inbounds, err = q.ListInbounds(ctx); err != nil {
		return nil, err
	}
	if s.slots, err = q.ListSlots(ctx); err != nil {
		return nil, err
	}
	if s.grants, err = domain.LoadGrantsLeft(ctx, q, m.now()); err != nil {
		return nil, err
	}
	if s.pools, err = q.ListAllUserPools(ctx); err != nil {
		return nil, err
	}
	if s.servers, err = domain.AllTariffServers(ctx, q); err != nil {
		return nil, err
	}
	if s.torrent, err = torrent.Load(ctx, settings.New(q)); err != nil {
		return nil, err
	}
	if s.filters, err = filters.Load(ctx, settings.New(q)); err != nil {
		return nil, err
	}
	if s.exitDNS, err = exitdns.Load(ctx, settings.New(q)); err != nil {
		return nil, err
	}
	if s.detail, err = settings.New(q).On(ctx, settings.TrafficDetail); err != nil {
		return nil, err
	}
	if s.torrent.Enabled {
		bans, err := q.ActiveTorrentBans(ctx, m.now().Unix())
		if err != nil {
			return nil, err
		}
		for _, b := range bans {
			s.bans[b.UserID] = b.BannedUntil
		}
	}
	rows, err := q.CountersPositions(ctx)
	if err != nil {
		return nil, err
	}
	for _, r := range rows {
		name, rawID, _ := strings.Cut(r.Key, "/")
		id, err := strconv.ParseInt(rawID, 10, 64)
		if err != nil {
			continue
		}
		pos := s.counters[id]
		switch name {
		case "counters_epoch":
			pos.epoch = r.Value
		case "counters_seq":
			pos.seq, _ = strconv.ParseInt(r.Value, 10, 64)
		}
		s.counters[id] = pos
	}
	return s, tx.Commit()
}

// noteBatch records that a traffic batch of node was stored: its next policies need a
// snapshot that holds it.
func (m *Manager) noteBatch(node int64) {
	m.batchMu.Lock()
	m.batches[node]++
	m.batchMu.Unlock()
}

func (m *Manager) batchesOf(node int64) uint64 {
	m.batchMu.Lock()
	defer m.batchMu.Unlock()
	return m.batches[node]
}

func (m *Manager) batchesCopy() map[int64]uint64 {
	m.batchMu.Lock()
	defer m.batchMu.Unlock()
	return maps.Clone(m.batches)
}
