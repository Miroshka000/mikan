package node

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"slices"
	"strconv"
	"sync"
	"time"

	"mikan/internal/nodeapi"
	"mikan/internal/scan"
)

var scanning sync.Mutex

// Handler exposes the Node API: on the unix socket, whose file permissions are the
// access control, and on a remote node over TLS that only the panel's certificate opens.
func Handler(e *Engine, log *slog.Logger) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("PUT /v1/state", func(w http.ResponseWriter, r *http.Request) {
		var st nodeapi.DesiredState
		if !decode(w, r, &st) {
			return
		}
		res, err := e.Apply(st)
		if err != nil {
			fail(w, log, err)
			return
		}
		writeJSON(w, http.StatusOK, res)
	})
	mux.HandleFunc("POST /v1/validate", func(w http.ResponseWriter, r *http.Request) {
		var req nodeapi.ValidateRequest
		if !decode(w, r, &req) {
			return
		}
		if err := e.Validate(req); err != nil {
			writeJSON(w, http.StatusUnprocessableEntity, nodeapi.Error{Code: "invalid_config", Message: err.Error()})
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("PUT /v1/policies", func(w http.ResponseWriter, r *http.Request) {
		var req nodeapi.PoliciesRequest
		if !decode(w, r, &req) {
			return
		}
		e.SetPolicies(req)
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("GET /v1/counters", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, e.Reg.Counters())
	})
	mux.HandleFunc("POST /v1/counters/ack", func(w http.ResponseWriter, r *http.Request) {
		var req nodeapi.AckRequest
		if !decode(w, r, &req) {
			return
		}
		if !e.Reg.Ack(req.Epoch, req.Seq) {
			writeJSON(w, http.StatusConflict, nodeapi.Error{Code: "stale_ack", Message: "no outstanding batch with this epoch/seq"})
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("GET /v1/health", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, e.Health())
	})
	mux.HandleFunc("POST /v1/update", func(w http.ResponseWriter, r *http.Request) {
		var req nodeapi.UpdateRequest
		// A request is one short version: nothing here is as large as the other calls' bodies.
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, nodeapi.Error{Code: "bad_request", Message: err.Error()})
			return
		}
		switch err := e.RequestUpdate(req.Version); {
		case errors.Is(err, ErrBadVersion):
			writeJSON(w, http.StatusUnprocessableEntity, nodeapi.Error{Code: "bad_version", Message: "version must be a release version like 0.5.0.2"})
		case err != nil:
			log.Error("update request", "err", err)
			writeJSON(w, http.StatusInternalServerError, nodeapi.Error{Code: "update_failed", Message: "the request could not be written"})
		default:
			w.WriteHeader(http.StatusAccepted)
		}
	})
	mux.HandleFunc("GET /v1/warp", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 18*time.Second)
		defer cancel()
		writeJSON(w, http.StatusOK, e.WarpStatus(ctx, r.URL.Query().Get("force") == "1"))
	})
	mux.HandleFunc("GET /v1/probe", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 18*time.Second)
		defer cancel()
		res, ok := e.Probe(ctx, r.URL.Query().Get("proxy"))
		if !ok {
			writeJSON(w, http.StatusNotFound, nodeapi.Error{Code: "no_such_outbound", Message: "not an outbound of this node"})
			return
		}
		writeJSON(w, http.StatusOK, res)
	})
	mux.HandleFunc("POST /v1/diagnose", diagnoseHandler(e))
	mux.HandleFunc("POST /v1/speedtest", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
		defer cancel()
		res, err := e.SpeedTest(ctx)
		if errors.Is(err, ErrSpeedTestBusy) {
			writeJSON(w, http.StatusConflict, nodeapi.Error{Code: "speed_test_busy", Message: "a speed test is running"})
			return
		}
		writeJSON(w, http.StatusOK, res)
	})
	mux.HandleFunc("GET /v1/activity", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, e.Reg.Activity())
	})
	mux.HandleFunc("GET /v1/torrents", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		after, _ := strconv.ParseInt(q.Get("after"), 10, 64)
		writeJSON(w, http.StatusOK, e.Reg.TorrentHits(q.Get("epoch"), after))
	})
	mux.HandleFunc("POST /v1/targets/check", func(w http.ResponseWriter, r *http.Request) {
		var req nodeapi.TargetCheckRequest
		if !decode(w, r, &req) {
			return
		}
		if !e.TargetAllowed(req.Dest) {
			writeJSON(w, http.StatusUnprocessableEntity, nodeapi.Error{Code: "bad_target", Message: "dest must be a public host:port"})
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
		defer cancel()
		writeJSON(w, http.StatusOK, scan.Check(ctx, req.Dest, req.SNI, e.TargetOptions()))
	})
	mux.HandleFunc("POST /v1/targets/scan", func(w http.ResponseWriter, r *http.Request) {
		var req nodeapi.TargetScanRequest
		if !decode(w, r, &req) {
			return
		}
		// ~250 connections per scan: one at a time.
		if !scanning.TryLock() {
			writeJSON(w, http.StatusConflict, nodeapi.Error{Code: "scan_busy", Message: "a scan is running"})
			return
		}
		defer scanning.Unlock()
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()
		limit := req.Limit
		if limit <= 0 || limit > 32 {
			limit = 12
		}
		res, scanned, err := scan.Neighbors(ctx, req.IP, limit, e.TargetOptions())
		if err != nil && ctx.Err() == nil {
			writeJSON(w, http.StatusUnprocessableEntity, nodeapi.Error{Code: "bad_request", Message: err.Error()})
			return
		}
		if res == nil {
			res = []scan.Result{}
		}
		writeJSON(w, http.StatusOK, nodeapi.TargetScan{Scanned: scanned, Results: res})
	})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodConnect {
			connectTunnel(w, r)
			return
		}
		mux.ServeHTTP(w, r)
	})
}

// connectTunnel is HTTP CONNECT for the panel: the bot reaches Telegram through this node when
// the panel's own server cannot. Only nodeapi.TunnelHosts go through.
func connectTunnel(w http.ResponseWriter, r *http.Request) {
	if !slices.Contains(nodeapi.TunnelHosts, r.Host) {
		writeJSON(w, http.StatusForbidden, nodeapi.Error{Code: "tunnel_forbidden", Message: r.Host + " is not a tunnel host"})
		return
	}
	up, err := (&net.Dialer{Timeout: 10 * time.Second}).DialContext(r.Context(), "tcp", r.Host)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, nodeapi.Error{Code: "tunnel_unreachable", Message: err.Error()})
		return
	}
	hj, ok := w.(http.Hijacker)
	if !ok {
		up.Close()
		writeJSON(w, http.StatusInternalServerError, nodeapi.Error{Code: "tunnel_unsupported"})
		return
	}
	conn, buf, err := hj.Hijack()
	if err != nil {
		up.Close()
		return
	}
	// Long polling keeps the stream idle for most of a minute: no deadlines from here on.
	_ = conn.SetDeadline(time.Time{})
	if _, err := conn.Write([]byte("HTTP/1.1 200 Connection established\r\n\r\n")); err != nil {
		conn.Close()
		up.Close()
		return
	}
	done := make(chan struct{}, 2)
	go func() { _, _ = io.Copy(up, buf.Reader); done <- struct{}{} }()
	go func() { _, _ = io.Copy(conn, up); done <- struct{}{} }()
	<-done
	conn.Close()
	up.Close()
	<-done
}

// decode reads a request body of at most 64 MiB. Fields the node does not know are
// ignored: a panel newer than the node sends what the node cannot read yet, and refusing
// the request would leave the node without policies and state until it is updated. Only
// the panel reaches the API (the socket's permissions, or its pinned certificate), so no
// caller needs protecting from a mistyped field.
func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<20)).Decode(v); err != nil {
		writeJSON(w, http.StatusBadRequest, nodeapi.Error{Code: "bad_request", Message: err.Error()})
		return false
	}
	return true
}

func fail(w http.ResponseWriter, log *slog.Logger, err error) {
	var ne *nodeapi.Error
	if errors.As(err, &ne) {
		status := http.StatusInternalServerError
		if ne.Code == "invalid_state" {
			status = http.StatusBadRequest
		}
		writeJSON(w, status, ne)
		return
	}
	log.Error("node api", "err", err)
	writeJSON(w, http.StatusInternalServerError, nodeapi.Error{Code: "apply_failed", Message: err.Error()})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
