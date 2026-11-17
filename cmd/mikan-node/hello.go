package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"time"

	"mikan/internal/node"
	"mikan/internal/nodehello"
	"mikan/internal/nodetls"
	"mikan/internal/pace"
	"mikan/internal/panel/config"
)

// helloPause are the waits before each hello of a start: the panel may need a moment to
// see the node's port (a firewall rule that takes a while, the panel's own retry), and
// after about a minute the installer stops waiting for the answer.
var helloPause = []time.Duration{2 * time.Second, 5 * time.Second, 10 * time.Second, 15 * time.Second, 25 * time.Second}

// The hellos while the panel stays away: the first once it has not come for silentAfter,
// then at waits that double up to silentMax. A node whose server got a new address is found
// by them (the panel takes the address a hello comes from), and so is a panel that moved.
var (
	silentAfter = 3 * time.Minute
	silentMax   = 30 * time.Minute
	silentLook  = time.Minute
)

// panelNode is what the hellos need of the running node.
type panelNode interface {
	// PanelURL is the panel's address its last state named; "": none.
	PanelURL() string
	// PanelSeen is when the panel last reached the node; zero: not yet.
	PanelSeen() time.Time
}

// savedURL is the panel's address the node saved with its last state, for a hello made
// beside the running node.
type savedURL string

func (s savedURL) PanelURL() string   { return string(s) }
func (savedURL) PanelSeen() time.Time { return time.Time{} }

func savedPanel(dataDir string) panelNode { return savedURL(node.SavedPanelURL(dataDir)) }

// helloKey is the join key with the panel's address the node knows now: the panel sends it
// with every state, the key keeps the one it was issued with.
func helloKey(key nodetls.Key, n panelNode) nodetls.Key {
	if u := n.PanelURL(); u != "" && nodetls.ValidPanelURL(u) {
		key.PanelURL = u
	}
	return key
}

// helloLoop says hello to the panel after the node starts, until the panel answers for
// good, then again whenever the panel stays away; each answer is kept in the data
// directory for the installer and mikan doctor.
func helloLoop(ctx context.Context, key nodetls.Key, n panelNode, dataDir string, log *slog.Logger) {
	started := time.Now().UTC()
	hc := nodehello.Client()
	for i, pause := range helloPause {
		if !pace.Sleep(ctx, pause) {
			return
		}
		r := nodehello.Send(ctx, helloKey(key, n), hc, time.Now())
		final := nodehello.Final(r) || i == len(helloPause)-1
		s := nodehello.Status{Result: r, At: time.Now().UTC(), Started: started, Attempt: i + 1, Final: final}
		if err := nodehello.Write(dataDir, s); err != nil {
			log.Warn("hello: save the answer", "err", err)
		}
		if final {
			if r.OK {
				log.Info("hello: the panel reached the node")
			} else {
				log.Warn("hello: the panel did not reach the node", "code", r.Code)
			}
			break
		}
	}
	silentHellos(ctx, key, n, hc, dataDir, log, time.Now)
}

// silentHellos says hello while the panel stays away: first once it has not come for
// silentAfter since the start or its last visit, then at waits that double up to silentMax.
func silentHellos(ctx context.Context, key nodetls.Key, n panelNode, hc *http.Client, dataDir string, log *slog.Logger, now func() time.Time) {
	since := now()
	wait := silentAfter
	var next time.Time
	for pace.Sleep(ctx, silentLook) {
		t := now()
		last := n.PanelSeen()
		if last.Before(since) {
			last = since
		}
		if t.Sub(last) < silentAfter {
			wait, next = silentAfter, time.Time{}
			continue
		}
		if t.Before(next) {
			continue
		}
		r := nodehello.Send(ctx, helloKey(key, n), hc, t)
		s := nodehello.Status{Result: r, At: t.UTC(), Started: since.UTC(), Attempt: 1, Final: true}
		if err := nodehello.Write(dataDir, s); err != nil {
			log.Warn("hello: save the answer", "err", err)
		}
		log.Info("hello: the panel has not come for a while, said hello", "away", t.Sub(last).Round(time.Second), "ok", r.OK, "code", r.Code)
		next = t.Add(wait)
		wait = min(2*wait, silentMax)
	}
}

// helloOnce is `mikan-node hello`: one hello now (mikan doctor runs it), its answer saved
// and printed as JSON. It never fails: the answer says what went wrong.
func helloOnce() {
	now := time.Now().UTC()
	var s nodehello.Status
	raw, err := config.Secret("MIKAN_NODE_JOIN")
	var key nodetls.Key
	if err == nil {
		key, err = nodetls.DecodeKey(raw)
	}
	if err != nil {
		s = nodehello.Status{Result: nodehello.Result{Code: nodehello.NoPanelURL, Error: err.Error()}}
	} else {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		key = helloKey(key, savedPanel(envOr("MIKAN_DATA_DIR", config.DefaultDataDir)))
		s = nodehello.Status{Result: nodehello.Send(ctx, key, nodehello.Client(), now)}
	}
	s.At, s.Started, s.Attempt, s.Final = time.Now().UTC(), now, 1, true
	if err := nodehello.Write(envOr("MIKAN_DATA_DIR", config.DefaultDataDir), s); err != nil {
		fmt.Fprintln(os.Stderr, "mikan-node: save the answer:", err)
	}
	out, _ := json.Marshal(s)
	fmt.Println(string(out))
}
