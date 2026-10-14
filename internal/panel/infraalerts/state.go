package infraalerts

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"mikan/internal/panel/hysteresis"
	"mikan/internal/panel/store/db"
)

const stateKey = "monitor"

type sampleState struct {
	hysteresis.Tracker
	Checked time.Time `json:"checked,omitempty"`
}

// persistentState is the monitor's memory between rounds, stored as one JSON text under
// stateKey. Its shape is the on-disk format: a field is renamed or retyped only together with
// a migration of what is already saved.
type persistentState struct {
	Samples            map[string]sampleState     `json:"samples"`
	Stuck              map[string]string          `json:"stuck"`
	AutoCursor         int64                      `json:"auto_cursor"`
	TorrentCursor      int64                      `json:"torrent_cursor,omitempty"`
	TLSBad             bool                       `json:"tls_bad"`
	UpdateAt           string                     `json:"update_at"`
	NodeUpdateAt       map[string]int64           `json:"node_update_at,omitempty"`
	PublicTarget       string                     `json:"public_target"`
	PublicMessage      int64                      `json:"public_message"`
	PublicText         string                     `json:"public_text"`
	PublicLevels       map[int64]hysteresis.Level `json:"public_levels"`
	PublicPinned       bool                       `json:"public_pinned"`
	PublicPinAttempted bool                       `json:"public_pin_attempted"`
	Pending            []delivery                 `json:"pending"`
}

// storedState is the state's text as last read or written, kept in memory so a round does not
// read the database again to compare (under Monitor.roundMu). known is false until the text has
// been read, and again after a write that failed: it may have committed after all.
type storedState struct {
	text  string
	known bool
}

func (m *Monitor) load(ctx context.Context) (persistentState, error) {
	var st persistentState
	raw, err := m.store.Q.GetInfrastructureAlertState(ctx, stateKey)
	if errors.Is(err, sql.ErrNoRows) {
		st.AutoCursor, err = m.store.Q.MaxInboundEventID(ctx)
		if err == nil {
			st.TorrentCursor, err = m.store.Q.LastTorrentHitID(ctx)
		}
	} else if err == nil {
		m.stored = storedState{text: raw, known: true}
		err = json.Unmarshal([]byte(raw), &st)
	}
	if st.Samples == nil {
		st.Samples = map[string]sampleState{}
	}
	if st.Stuck == nil {
		st.Stuck = map[string]string{}
	}
	return st, err
}

// save writes st unless it differs from the stored state only in when samples were
// checked. The stored text is the one load read or save wrote (both under roundMu), not
// read again every five seconds.
func (m *Monitor) save(ctx context.Context, st persistentState) error {
	old, found := m.stored.text, m.stored.known
	if !found {
		var err error
		old, err = m.store.Q.GetInfrastructureAlertState(ctx, stateKey)
		switch {
		case err == nil:
			found = true
		case !errors.Is(err, sql.ErrNoRows):
			return err
		}
	}
	if found {
		unchanged, err := sameStateIgnoringSampleChecks(old, st)
		if err != nil {
			return err
		}
		if unchanged {
			return nil
		}
	}
	raw, err := json.Marshal(st)
	if err != nil {
		return err
	}
	m.stored.known = false // read again if the write fails: it may have committed after all
	if err := m.store.Q.SetInfrastructureAlertState(ctx, db.SetInfrastructureAlertStateParams{Key: stateKey, Value: string(raw), UpdatedAt: m.now().Unix()}); err != nil {
		return err
	}
	m.stored = storedState{text: string(raw), known: true}
	return nil
}

func sameStateIgnoringSampleChecks(oldRaw string, next persistentState) (bool, error) {
	var previous persistentState
	if err := json.Unmarshal([]byte(oldRaw), &previous); err != nil {
		return false, err
	}
	normalize := func(state persistentState) persistentState {
		if state.Samples != nil {
			samples := make(map[string]sampleState, len(state.Samples))
			for key, sample := range state.Samples {
				sample.Checked = time.Time{}
				samples[key] = sample
			}
			state.Samples = samples
		}
		return state
	}
	previousRaw, err := json.Marshal(normalize(previous))
	if err != nil {
		return false, err
	}
	nextRaw, err := json.Marshal(normalize(next))
	if err != nil {
		return false, err
	}
	return string(previousRaw) == string(nextRaw), nil
}
