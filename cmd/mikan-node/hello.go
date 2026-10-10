package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"time"

	"mikan/internal/nodehello"
	"mikan/internal/nodetls"
)

// helloPause are the waits before each hello of a start: the panel may need a moment to
// see the node's port (a firewall rule that takes a while, the panel's own retry), and
// after about a minute the installer stops waiting for the answer.
var helloPause = []time.Duration{2 * time.Second, 5 * time.Second, 10 * time.Second, 15 * time.Second, 25 * time.Second}

// helloLoop says hello to the panel after the node starts, until the panel answers for
// good, and keeps each answer in the data directory for the installer.
func helloLoop(ctx context.Context, key nodetls.Key, dataDir string, log *slog.Logger) {
	started := time.Now().UTC()
	hc := nodehello.Client()
	for i, pause := range helloPause {
		select {
		case <-ctx.Done():
			return
		case <-time.After(pause):
		}
		r := nodehello.Send(ctx, key, hc, time.Now())
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
			return
		}
	}
}

// helloOnce is `mikan-node hello`: one hello now (mikan doctor runs it), its answer saved
// and printed as JSON. It never fails: the answer says what went wrong.
func helloOnce() {
	now := time.Now().UTC()
	var s nodehello.Status
	key, err := nodetls.DecodeKey(os.Getenv("MIKAN_NODE_JOIN"))
	if err != nil {
		s = nodehello.Status{Result: nodehello.Result{Code: nodehello.NoPanelURL, Error: err.Error()}}
	} else {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		s = nodehello.Status{Result: nodehello.Send(ctx, key, nodehello.Client(), now)}
	}
	s.At, s.Started, s.Attempt, s.Final = time.Now().UTC(), now, 1, true
	if err := nodehello.Write(envOr("MIKAN_DATA_DIR", "/data"), s); err != nil {
		fmt.Fprintln(os.Stderr, "mikan-node: save the answer:", err)
	}
	raw, _ := json.Marshal(s)
	fmt.Println(string(raw))
}
