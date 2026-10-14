package infraalerts

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"mikan/internal/panel/acme"
	"mikan/internal/panel/hysteresis"
	"mikan/internal/panel/settings"
	"mikan/internal/panel/store"
	"mikan/internal/panel/store/db"
	"mikan/internal/panel/store/storetest"
)

func testMonitorStore(t *testing.T) (*store.Store, context.Context) {
	t.Helper()
	ctx := context.Background()
	st, err := storetest.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st, ctx
}

func TestFreshMonitorStartsAfterExistingAutotuneEvents(t *testing.T) {
	st, ctx := testMonitorStore(t)
	if _, err := st.DB.ExecContext(ctx, `INSERT INTO inbounds(id,node_id,name,preset,port,settings,config,created_at,updated_at) VALUES(1,1,'in','vless-reality',443,'{}','',1,1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB.ExecContext(ctx, `INSERT INTO inbound_events(inbound_id,node_id,kind,network,old_value,new_value,reason,created_at) VALUES(1,1,'port','tcp','1','2','blocked',1)`); err != nil {
		t.Fatal(err)
	}
	m := New(st, settings.New(st.Q), nil, nil, nil, nil, nil, slog.Default(), time.Now)
	state, err := m.load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if state.AutoCursor != 1 {
		t.Fatalf("fresh cursor = %d, want 1", state.AutoCursor)
	}
}

func TestDisabledBotDoesNotKeepPendingAlerts(t *testing.T) {
	st, ctx := testMonitorStore(t)
	cfg := Default()
	cfg.AdminEnabled = true
	if err := settings.Set(ctx, settings.New(st.Q), KeyConfig, cfg); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	m := New(st, settings.New(st.Q), nil, nil, func() acme.Status { return acme.Status{Error: "broken", CheckedAt: now} }, nil, nil, slog.Default(), time.Now)
	m.round(ctx)
	state, err := m.load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Pending) != 0 {
		t.Fatalf("disabled bot retained %d pending alerts", len(state.Pending))
	}
}

func TestDeliveryQueueBoundsRetries(t *testing.T) {
	d := delivery{CreatedAt: 100}
	if !retryDelivery(&d, 100, context.DeadlineExceeded) || d.Attempts != 1 || d.NextAttemptAt != 105 {
		t.Fatalf("first retry = %+v", d)
	}
	d.Attempts = 7
	if retryDelivery(&d, 100, context.DeadlineExceeded) {
		t.Fatal("queue item exceeded the retry limit")
	}
}

func TestMergeDeliveryResultsPreservesNewItems(t *testing.T) {
	before := []delivery{{Key: "done", Target: "admin"}, {Key: "retry", Target: "admin"}}
	after := []delivery{{Key: "retry", Target: "admin", Attempts: 2, NextAttemptAt: 300}}
	latest := []delivery{{Key: "done", Target: "admin"}, {Key: "retry", Target: "admin", Text: "new text"}, {Key: "new", Target: "admin"}}
	got := mergeDeliveryResults(latest, before, after)
	if len(got) != 2 || got[0].Key != "retry" || got[0].Text != "new text" || got[0].Attempts != 2 || got[1].Key != "new" {
		t.Fatalf("merged queue = %+v", got)
	}
}

// A round that rewrote the summary while the old text was being sent keeps the new text
// queued: the channel would show the old one until a level changes again.
func TestMergeDeliveryResultsKeepsRewrittenSummary(t *testing.T) {
	before := []delivery{{Key: "summary/current", Target: targetSummary, Text: "A"}}
	latest := []delivery{{Key: "summary/current", Target: targetSummary, Text: "B"}}
	got := mergeDeliveryResults(latest, before, nil)
	if len(got) != 1 || got[0].Text != "B" {
		t.Fatalf("merged queue = %+v", got)
	}
	if got := mergeDeliveryResults(before, before, nil); len(got) != 0 {
		t.Fatalf("the sent text stays queued: %+v", got)
	}
}

func TestObserveDeduplicatesSampleAfterStateRoundTrip(t *testing.T) {
	st, ctx := testMonitorStore(t)
	now := time.Unix(1_800_000_000, 0).UTC()
	clock := now
	m := New(st, settings.New(st.Q), nil, nil, nil, nil, nil, slog.Default(), func() time.Time { return clock })
	state := persistentState{Samples: map[string]sampleState{"warp/1": {
		Tracker: hysteresis.Tracker{Level: hysteresis.Healthy, BadRounds: 1}, Checked: now,
	}}}
	if err := m.save(ctx, state); err != nil {
		t.Fatal(err)
	}
	var beforeUpdatedAt int64
	if err := st.DB.QueryRowContext(ctx, `SELECT updated_at FROM infrastructure_alert_state WHERE key = $1`, stateKey).Scan(&beforeUpdatedAt); err != nil {
		t.Fatal(err)
	}
	clock = clock.Add(time.Second)
	state.Samples["warp/1"] = sampleState{Tracker: state.Samples["warp/1"].Tracker, Checked: now.Add(time.Minute)}
	if err := m.save(ctx, state); err != nil {
		t.Fatal(err)
	}
	var afterUpdatedAt int64
	if err := st.DB.QueryRowContext(ctx, `SELECT updated_at FROM infrastructure_alert_state WHERE key = $1`, stateKey).Scan(&afterUpdatedAt); err != nil {
		t.Fatal(err)
	}
	if afterUpdatedAt != beforeUpdatedAt {
		t.Fatalf("CheckedAt-only change rewrote state: updated_at %d -> %d", beforeUpdatedAt, afterUpdatedAt)
	}
	loaded, err := m.load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got := loaded.Samples["warp/1"].Checked; !got.Equal(now) {
		t.Fatalf("persisted CheckedAt = %v", got)
	}
	before := loaded.Samples["warp/1"].BadRounds
	if m.observe(&loaded, "warp/1", hysteresis.Degraded, now, 2, 2, "") {
		t.Fatal("same probe emitted a new transition")
	}
	if got := loaded.Samples["warp/1"].BadRounds; got != before {
		t.Fatalf("duplicate probe advanced bad rounds: %d -> %d", before, got)
	}
}

func TestFailedPublicPinDoesNotRequeueUnchangedSummary(t *testing.T) {
	st, ctx := testMonitorStore(t)
	m := New(st, settings.New(st.Q), nil, nil, nil, nil, nil, slog.Default(), time.Now)
	const channel = "@mikan_status"
	text := strings.Join([]string{"🌐 <b>Состояние серверов</b>", "Публичные серверы пока не настроены."}, "\n")
	state := persistentState{PublicTarget: channel, PublicMessage: 42, PublicText: text, PublicPinAttempted: true, PublicLevels: map[int64]hysteresis.Level{}}
	cfg := Default()
	cfg.PublicEnabled, cfg.PublicChannel = true, channel
	m.publicStatus(ctx, &state, cfg, nil, map[int64][]db.Inbound{}, "ru")
	if len(state.Pending) != 0 {
		t.Fatalf("unchanged summary was requeued after failed pin: %+v", state.Pending)
	}
}

func TestPublicPinFailureIsRecordedAndLoggedOnce(t *testing.T) {
	var logs bytes.Buffer
	m := &Monitor{log: slog.New(slog.NewTextHandler(&logs, nil))}
	state := persistentState{PublicMessage: 42}
	pinErr := errors.New("bot is not an administrator")
	m.recordPublicPinResult(&state, pinErr)
	m.recordPublicPinResult(&state, pinErr)
	if !state.PublicPinAttempted || state.PublicPinned {
		t.Fatalf("pin state after failure: %+v", state)
	}
	if got := strings.Count(logs.String(), "pin public status failed"); got != 1 {
		t.Fatalf("pin failure logged %d times", got)
	}
}

func TestCheckedSampleIsPersistedInStateJSON(t *testing.T) {
	checked := time.Unix(1_800_000_000, 0).UTC()
	raw, err := json.Marshal(sampleState{Tracker: hysteresis.Tracker{Level: hysteresis.Degraded}, Checked: checked})
	if err != nil {
		t.Fatal(err)
	}
	var restored sampleState
	if err := json.Unmarshal(raw, &restored); err != nil {
		t.Fatal(err)
	}
	if !restored.Checked.Equal(checked) {
		t.Fatalf("CheckedAt after JSON roundtrip = %v", restored.Checked)
	}
}
