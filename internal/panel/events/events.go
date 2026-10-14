// Package events is the panel's feed of events for addons of protocol 2 (permission events):
// what happened to payments, subscribers, devices and nodes (types.go lists them all), written
// in the same transaction as the change itself, so an event is never lost and never told about
// a change that rolled back.
package events

import (
	"context"
	"database/sql"
	"encoding/json"
	"sync"
	"time"

	"mikan/internal/panel/store/db"
)

// Retention: an event older than this is gone, read or not.
const Retention = 7 * 24 * time.Hour

// emitLock is the advisory lock emitters take till their commit: ids are then committed in
// the order they were given, and a reader that saw id N never finds N-1 later.
const emitLock = 0x6d696b616e657631 // "mikanev1"

// Event is one entry of the feed.
type Event struct {
	ID     int64           `json:"id"`
	Type   string          `json:"type"`
	At     time.Time       `json:"at"`
	UserID int64           `json:"user_id,omitempty"`
	TgID   int64           `json:"tg_id,omitempty"`
	Data   json.RawMessage `json:"data"`
}

// Hub wakes readers waiting for events of this process.
type Hub struct {
	mu   sync.Mutex
	wake chan struct{}
}

func NewHub() *Hub { return &Hub{wake: make(chan struct{})} }

// Kick wakes every reader waiting now; it may come before the commit, so a reader that
// finds nothing waits again.
func (h *Hub) Kick() {
	if h == nil {
		return
	}
	h.mu.Lock()
	close(h.wake)
	h.wake = make(chan struct{})
	h.mu.Unlock()
}

// Wait is closed by the next Kick; without a hub it never is.
func (h *Hub) Wait() <-chan struct{} {
	if h == nil {
		return nil
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.wake
}

// Emit writes an event in the caller's transaction; dedupe, when given, makes a second
// emit of the same thing (a retried transaction, a notice scheduler run twice) a no-op.
func Emit(ctx context.Context, q *db.Queries, now time.Time, typ string, userID, tgID int64, data any, dedupe string) error {
	raw, err := json.Marshal(data)
	if err != nil {
		return err
	}
	if err := q.LockAddonEvents(ctx, emitLock); err != nil {
		return err
	}
	return q.AddAddonEvent(ctx, db.AddAddonEventParams{Type: typ, UserID: nullID(userID), TgID: nullID(tgID), Data: string(raw),
		DedupeKey: sql.NullString{String: dedupe, Valid: dedupe != ""}, CreatedAt: now.Unix()})
}

func nullID(id int64) sql.NullInt64 { return sql.NullInt64{Int64: id, Valid: id != 0} }

// Prune drops what is older than Retention.
func Prune(ctx context.Context, q *db.Queries, now time.Time) (int64, error) {
	return q.PruneAddonEvents(ctx, now.Add(-Retention).Unix())
}
