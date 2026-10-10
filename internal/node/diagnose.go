package node

import (
	"context"
	"net/http"
	"sync"
	"time"

	"mikan/internal/diag"
	"mikan/internal/nodeapi"
)

// diagnosing keeps one look at the server at a time, and its answer for a little while: a
// page the admin keeps reloading must not make the node dial out again and again.
var diagnosing struct {
	run  sync.Mutex
	mu   sync.Mutex
	last nodeapi.Diagnosis
}

const diagFresh = 15 * time.Second

// diagOptions are the real targets; tests put local stand-ins in their place.
var diagOptions = func(e *Engine) diag.Options { return diag.Options{DataDir: e.dataDir} }

// Diagnose looks at the node's server: DNS, the internet, GitHub and GHCR, its clock, disk
// (where its data are) and memory. The targets are diag's fixed ones.
func (e *Engine) Diagnose(ctx context.Context) nodeapi.Diagnosis {
	diagnosing.run.Lock()
	defer diagnosing.run.Unlock()
	diagnosing.mu.Lock()
	last := diagnosing.last
	diagnosing.mu.Unlock()
	if !last.At.IsZero() && time.Since(last.At) < diagFresh {
		return last
	}
	d := diag.Run(ctx, diagOptions(e))
	diagnosing.mu.Lock()
	diagnosing.last = d
	diagnosing.mu.Unlock()
	return d
}

func diagnoseHandler(e *Engine) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
		defer cancel()
		writeJSON(w, http.StatusOK, e.Diagnose(ctx))
	}
}
