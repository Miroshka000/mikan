package infraalerts

import (
	"context"
	"fmt"
	"html"
	"slices"
	"sort"
	"strconv"
	"strings"

	"mikan/internal/panel/hysteresis"
	"mikan/internal/panel/store/db"
)

// publicStatus keeps the public channel's side of the state in step with the servers: the text
// of the status message, a change message for each server whose level moved, and what is queued
// for the channel. Nothing here talks to Telegram; the queue is delivered by deliver.
func (m *Monitor) publicStatus(ctx context.Context, st *persistentState, cfg AlertsConfig, nodes []db.Node,
	inbounds map[int64][]db.Inbound, lang string) {
	if !cfg.PublicEnabled {
		st.PublicText = ""
		st.PublicLevels = map[int64]hysteresis.Level{}
		dropPending(st, targetSummary, targetPublicChange)
		return
	}
	if st.PublicLevels == nil {
		st.PublicLevels = map[int64]hysteresis.Level{}
	}
	t := textsFor(lang)
	type row struct {
		id    int64
		name  string
		level hysteresis.Level
	}
	var rows []row
	for _, n := range nodes {
		if n.Enabled == 0 || strings.TrimSpace(n.PublicName) == "" {
			continue
		}
		level := publicNodeLevel(ctx, st, m.nodeWarp, n.ID, inbounds[n.ID])
		rows = append(rows, row{n.ID, n.PublicName, level})
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].id < rows[j].id })
	lines := []string{t.publicTitle}
	if len(rows) == 0 {
		lines = append(lines, t.publicNone)
	}
	for _, r := range rows {
		lines = append(lines, publicLine(t, html.EscapeString(r.name), r.level))
	}
	text := strings.Join(lines, "\n")
	for _, r := range rows {
		previous, seen := st.PublicLevels[r.id]
		if seen && previous != r.level && previous != hysteresis.Unknown && r.level != hysteresis.Unknown && cfg.PublicChanges {
			change := publicLine(t, html.EscapeString(r.name), r.level)
			st.Pending = appendPending(st.Pending, delivery{Key: fmt.Sprintf("public/%d/%d/%d", r.id, previous, r.level), Target: targetPublicChange, Text: change})
		}
		st.PublicLevels[r.id] = r.level
	}
	for id := range st.PublicLevels {
		if !slices.ContainsFunc(nodes, func(n db.Node) bool { return n.ID == id }) {
			delete(st.PublicLevels, id)
		}
	}
	if st.PublicTarget != cfg.PublicChannel {
		st.PublicTarget, st.PublicMessage, st.PublicText, st.PublicPinned, st.PublicPinAttempted = cfg.PublicChannel, 0, "", false, false
		dropPending(st, targetSummary, targetPublicChange)
	}
	if !cfg.PublicSummary {
		st.PublicText = ""
		dropPending(st, targetSummary)
	} else if text != st.PublicText || st.PublicMessage > 0 && !st.PublicPinned && !st.PublicPinAttempted {
		st.PublicText = text
		updated := false
		for i := range st.Pending {
			if st.Pending[i].Target == targetSummary {
				st.Pending[i].Text = text
				updated = true
				break
			}
		}
		if !updated {
			st.Pending = append(st.Pending, delivery{Key: "summary/current", Target: targetSummary, Text: text})
		}
	}
}

// warp is the node's WARP row: the round's own cache (Monitor.nodeWarp), read once a
// round, not once per inbound.
func publicNodeLevel(ctx context.Context, st *persistentState, warp func(context.Context, int64) (db.NodeWarp, error), nodeID int64, inbounds []db.Inbound) hysteresis.Level {
	node := stateLevel(st, "node/"+strconv.FormatInt(nodeID, 10))
	if node == hysteresis.Unknown {
		return hysteresis.Unknown
	}
	if node == hysteresis.Unavailable {
		return hysteresis.Unavailable
	}
	total, working, unknown := 0, 0, false
	for _, in := range inbounds {
		if in.Enabled == 0 {
			continue
		}
		total++
		level := stateLevel(st, "inbound/"+strconv.FormatInt(in.ID, 10))
		if level == hysteresis.Unknown {
			unknown = true
			continue
		}
		if level != hysteresis.Healthy {
			continue
		}
		switch in.Outbound {
		case "warp":
			w, err := warp(ctx, nodeID)
			if err == nil && w.Enabled != 0 {
				out := stateLevel(st, "warp/"+strconv.FormatInt(nodeID, 10))
				if out == hysteresis.Unknown {
					unknown = true
					continue
				}
				if out != hysteresis.Healthy {
					continue
				}
			}
		case "node":
			if in.ExitNodeID.Valid {
				out := stateLevel(st, fmt.Sprintf("exit/%d/%d", nodeID, in.ExitNodeID.Int64))
				if out == hysteresis.Unknown {
					unknown = true
					continue
				}
				if out != hysteresis.Healthy {
					continue
				}
			}
		}
		working++
	}
	if total == 0 {
		return hysteresis.Unavailable
	}
	if working == total {
		return hysteresis.Healthy
	}
	if working > 0 {
		return hysteresis.Degraded
	}
	if unknown {
		return hysteresis.Unknown
	}
	return hysteresis.Unavailable
}

func stateLevel(st *persistentState, key string) hysteresis.Level {
	if s, ok := st.Samples[key]; ok {
		return s.Level
	}
	return hysteresis.Unknown
}

// PublicText is a server's line in the public status, in Russian.
func PublicText(name string, level hysteresis.Level) string { return publicLine(&ruTexts, name, level) }

// PublicTextEN is a server's line in the public status, in English.
func PublicTextEN(name string, level hysteresis.Level) string {
	return publicLine(&enTexts, name, level)
}

// publicLine is the icon, the server's name (already escaped) and the word for its level.
func publicLine(t *texts, name string, level hysteresis.Level) string {
	icon, label := "⚪", t.levelChecking
	switch level {
	case hysteresis.Healthy:
		icon, label = "🟢", t.levelWorking
	case hysteresis.Degraded:
		icon, label = "🟡", t.levelIssues
	case hysteresis.Unavailable:
		icon, label = "🔴", t.levelUnavailable
	}
	return icon + " " + name + " — " + label
}
