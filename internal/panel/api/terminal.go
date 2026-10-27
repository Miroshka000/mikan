package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/danielgtaylor/huma/v2"

	"mikan/internal/hostchan"
	"mikan/internal/nodeapi"
	"mikan/internal/panel/audit"
	"mikan/internal/panel/server"
	"mikan/internal/panel/store/db"
	"mikan/internal/panel/support"
	"mikan/internal/textutil"
)

// The support terminal. The operator of a grant of the terminal level opens a WebSocket to
// GET /api/v1/support/terminal?target=panel|node:<id>; the panel opens the host's agent for
// that server (hostchan, or through the node's API for a remote node) and passes frames both
// ways. Nothing here is reachable without a live grant that names the target: the admin's own
// session and API keys have no terminal (supportTerminalAllowed), and every session is
// registered under its grant so that the end of the grant (a revocation, a replacement, the
// time) closes it at once. The agent on the host ends it too (the end of the grant, a
// revocation marker, the closed stream) and records all of it.
//
// The WebSocket's messages: binary is the terminal's bytes both ways; the browser may send a
// text {"type":"resize","cols":N,"rows":N}; the panel sends a text {"type":"ready",
// "recording":"..","expires_at":".."} when the shell is up, {"type":"error","code":".."} when
// it could not be opened and {"type":"end","reason":".."} when it ended, then closes: 1000,
// or 4001 when the grant ended (revoked, expired).

const (
	// wsClosedGrantEnded is the close code when the grant ended under a session.
	wsClosedGrantEnded websocket.StatusCode = 4001
	// terminalReadLimit bounds one message from the browser.
	terminalReadLimit = 256 << 10
	// terminalCloseWait bounds the closing handshake with the browser.
	terminalCloseWait = 3 * time.Second
)

// Why a session ended, besides what the agent says (closed, idle, expired, revoked, exit,
// recording, error).
var (
	errTerminalRevoked = errors.New("revoked")
	errTerminalExpired = errors.New("expired")
)

// terminalSession is one open session, held by the hub for its grant.
type terminalSession struct {
	grantID int64
	target  string
	cancel  context.CancelCauseFunc
}

// terminalHub is the open sessions: one per server, each under its grant.
type terminalHub struct {
	mu       sync.Mutex
	sessions map[*terminalSession]struct{}
}

// add registers the session; false when the server has one already.
func (t *terminalHub) add(s *terminalSession) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	for o := range t.sessions {
		if o.target == s.target {
			return false
		}
	}
	if t.sessions == nil {
		t.sessions = map[*terminalSession]struct{}{}
	}
	t.sessions[s] = struct{}{}
	return true
}

func (t *terminalHub) remove(s *terminalSession) {
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.sessions, s)
}

// endGrant ends every session of the grant, now; the sessions clean up after themselves.
func (t *terminalHub) endGrant(id int64, cause error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	for s := range t.sessions {
		if s.grantID == id {
			s.cancel(cause)
		}
	}
}

// grantEnded is what runs when a support grant ends for any reason: its terminal sessions
// close, and the hosts' agents are told so that they end theirs even where the stream cannot
// be closed.
func (h *handlers) grantEnded(id int64) {
	// A grant that was not revoked ended by its time.
	cause := errTerminalRevoked
	if g, err := h.d.Store.Q.GetSupportGrant(context.Background(), id); err == nil && !g.RevokedAt.Valid {
		cause = errTerminalExpired
	}
	h.terms.endGrant(id, cause)
	if h.d.Host != nil {
		if err := h.d.Host.RevokeTerminal(id); err != nil {
			h.d.Log.Warn("terminal revocation marker", "grant", id, "err", err)
		}
	}
}

func writeProblem(w http.ResponseWriter, status int, code string) {
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"title": http.StatusText(status), "status": status, "detail": code})
}

// registerTerminal puts the terminal's WebSocket on the API's mux and the recordings on huma.
func (h *handlers) registerTerminal(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/support/terminal", h.terminalWS)
	admin := []string{supportAdminTag}
	huma.Register(h.api, huma.Operation{OperationID: "list-support-recordings", Method: http.MethodGet, Path: "/api/v1/support/recordings", Summary: "Записи сеансов терминала поддержки",
		Description: "Каждый сеанс терминала записывается на сервере целиком (ввод и вывод, формат asciicast v2) и хранится 30 дней. source=panel или node:<id>. Только админ из сессии: оператору поддержки и ключам закрыто.",
		Tags:        admin, Metadata: sessionOnly, Extensions: sessionOnlyExt}, h.listRecordings)
	huma.Register(h.api, huma.Operation{OperationID: "get-support-recording", Method: http.MethodGet, Path: "/api/v1/support/recordings/{name}", Summary: "Запись сеанса терминала",
		Description: "Файл asciicast v2 (application/x-asciicast), до 8 МиБ. source=panel или node:<id>. Только админ из сессии.",
		Tags:        admin, Metadata: sessionOnly, Extensions: sessionOnlyExt}, h.getRecording)
}

// terminalSource is where a session runs: its server's host channel, or a remote node.
type terminalSource struct {
	host   *hostchan.Channel // the panel's own server (target panel, or the panel's own node)
	nodeID int64             // a remote node
}

// terminalSourceOf resolves "panel" or "node:<id>" to the server; an error is a refusal for
// the caller (status and code).
func (h *handlers) terminalSourceOf(ctx context.Context, target string) (terminalSource, int, string) {
	if target == support.TargetPanel {
		if h.d.Host == nil {
			return terminalSource{}, http.StatusConflict, "host_unavailable"
		}
		return terminalSource{host: h.d.Host}, 0, ""
	}
	id, _ := strconv.ParseInt(strings.TrimPrefix(target, "node:"), 10, 64)
	n, err := h.getNode(ctx, id)
	if err != nil {
		var se huma.StatusError
		if errors.As(err, &se) {
			return terminalSource{}, se.GetStatus(), se.Error()
		}
		h.d.Log.Error("terminal node lookup", "err", err)
		return terminalSource{}, http.StatusInternalServerError, "terminal_failed"
	}
	// The panel's own node runs on the panel's server: its terminal is the server's.
	if n.Address == "" {
		if h.d.Host == nil {
			return terminalSource{}, http.StatusConflict, "host_unavailable"
		}
		return terminalSource{host: h.d.Host}, 0, ""
	}
	if h.d.Nodes == nil {
		return terminalSource{}, http.StatusConflict, "nodes_disabled"
	}
	return terminalSource{nodeID: n.ID}, 0, ""
}

// terminalOpenError turns what opening a session said into the code the browser is told.
func (h *handlers) terminalOpenError(err error, target string) string {
	var ne *nodeapi.Error
	var se *nodeapi.StatusError
	switch {
	case errors.Is(err, hostchan.ErrTerminalBusy):
		return "terminal_busy"
	case errors.Is(err, hostchan.ErrTerminalTimeout):
		return "terminal_timeout"
	case errors.Is(err, hostchan.ErrUnavailable):
		return "host_unavailable"
	case errors.As(err, &ne):
		switch ne.Code {
		case nodeapi.CodeTerminalBusy:
			return "terminal_busy"
		case nodeapi.CodeTerminalTimeout:
			return "terminal_timeout"
		case nodeapi.CodeHostUnavailable:
			return "host_unavailable"
		}
	case errors.As(err, &se) && se.Status == http.StatusNotFound:
		return "node_too_old"
	case errors.Is(err, context.Canceled):
		return "terminal_closed"
	}
	h.d.Log.Warn("terminal open", "target", target, "err", err)
	return "node_no_answer"
}

// terminalWS is the operator's way into a terminal.
func (h *handlers) terminalWS(w http.ResponseWriter, r *http.Request) {
	// A page of another site may not drive a session with the operator's cookie.
	if server.FetchSite(r.Header, r.Host) == server.SiteCross {
		writeProblem(w, http.StatusForbidden, "csrf")
		return
	}
	target := r.URL.Query().Get("target")
	if !hostchan.ValidTarget(target) {
		writeProblem(w, http.StatusUnprocessableEntity, "bad_target")
		return
	}
	g, ok, err := h.supportFromRequest(r)
	if err != nil {
		h.d.Log.Error("support grant lookup", "err", err)
		writeProblem(w, http.StatusInternalServerError, "internal error")
		return
	}
	if !ok {
		writeProblem(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	via := "browser"
	if r.Header.Get("Authorization") != "" {
		via = "api"
	}
	ip := server.ClientIP(r.Header, r.RemoteAddr, h.d.TrustProxy)
	// What a request through the middleware carries: who it is for the audit, and the grant.
	rctx := withSupportGrant(context.WithValue(r.Context(), keyClient, client{IP: ip, UserAgent: r.UserAgent()}), g)
	if !supportTerminalAllowed(rctx, target) {
		writeProblem(w, http.StatusForbidden, "terminal_not_allowed")
		return
	}
	src, status, code := h.terminalSourceOf(rctx, target)
	if status != 0 {
		writeProblem(w, status, code)
		return
	}
	// Only a WebSocket handshake goes on: a plain GET opens no session.
	c, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	c.SetReadLimit(terminalReadLimit)
	h.runTerminal(rctx, c, g, target, src, via)
}

// runTerminal opens the session and serves it until it ends; the grant has been checked.
func (h *handlers) runTerminal(rctx context.Context, c *websocket.Conn, g db.SupportGrant, target string, src terminalSource, via string) {
	// sctx ends with the grant; the WebSocket is used with rctx, since the library closes a
	// connection whose context is cancelled and the operator is owed the reason first.
	sctx, cancel := context.WithCancelCause(rctx)
	defer cancel(nil)
	sess := &terminalSession{grantID: g.ID, target: target, cancel: cancel}
	// The server is let go before the operator hears why the session did not open: the closing
	// handshake takes up to terminalCloseWait, and one who tries again at once must not find the
	// failed session still holding the server (terminal_busy).
	fail := func(code string) {
		h.terms.remove(sess)
		writeWS(rctx, c, map[string]any{"type": "error", "code": code})
		_ = closeWS(c, websocket.StatusInternalError, code)
	}
	if !h.terms.add(sess) {
		fail("terminal_busy")
		return
	}
	defer h.terms.remove(sess)
	// The grant may have ended between the check and now; from here on the hub ends it.
	if _, err := h.d.Support.Get(sctx, g.ID); err != nil {
		fail("grant_ended")
		return
	}
	req := hostchan.TerminalRequest{GrantID: g.ID, Target: target, ExpiresAt: support.Expires(g)}
	var host net.Conn
	var err error
	if src.host != nil {
		host, err = src.host.OpenTerminal(sctx, req)
	} else {
		host, err = h.d.Nodes.Terminal(sctx, src.nodeID, nodeapi.TerminalOpen{GrantID: g.ID, Target: target, ExpiresAt: req.ExpiresAt.Unix()})
	}
	if err != nil {
		if cause := context.Cause(sctx); cause != nil && errors.Is(err, context.Canceled) {
			fail("grant_ended")
			return
		}
		fail(h.terminalOpenError(err, target))
		return
	}
	defer host.Close()
	// The grant may have ended while the host came up.
	if _, err := h.d.Support.Get(sctx, g.ID); err != nil || context.Cause(sctx) != nil {
		fail("grant_ended")
		return
	}

	started := h.d.Now()
	timer := time.AfterFunc(max(0, support.Expires(g).Sub(started)), func() { cancel(errTerminalExpired) })
	defer timer.Stop()
	h.audit(rctx, 0, "support.terminal.start", audit.TargetSupportGrant, strconv.FormatInt(g.ID, 10), map[string]any{"target": target, "via": via})
	h.d.Support.NotifyTerminal(context.WithoutCancel(rctx), g, target, false, "", 0)

	var (
		recording string
		recMu     sync.Mutex
	)
	// What the agent says, to the browser; the agent's end is the session's.
	agentDone := make(chan string, 1)
	go func() {
		for {
			kind, payload, err := hostchan.ReadFrame(host)
			if err != nil {
				agentDone <- "closed"
				return
			}
			switch kind {
			case hostchan.FrameHello:
				var hi hostchan.TerminalHello
				if json.Unmarshal(payload, &hi) == nil && hostchan.ValidRecordingName(hi.Recording) {
					recMu.Lock()
					recording = hi.Recording
					recMu.Unlock()
					writeWS(rctx, c, map[string]any{"type": "ready", "recording": hi.Recording, "expires_at": support.Expires(g).Format(time.RFC3339)})
				}
			case hostchan.FrameData:
				if err := c.Write(rctx, websocket.MessageBinary, payload); err != nil {
					agentDone <- "closed"
					return
				}
			case hostchan.FrameExit:
				var end hostchan.TerminalEnd
				_ = json.Unmarshal(payload, &end)
				agentDone <- cleanReason(end.Reason)
				return
			}
		}
	}()
	// What the browser sends, to the agent.
	browserDone := make(chan struct{}, 1)
	go func() {
		defer func() { browserDone <- struct{}{} }()
		for {
			typ, data, err := c.Read(rctx)
			if err != nil {
				return
			}
			if typ == websocket.MessageBinary {
				for len(data) > 0 {
					n := min(len(data), 32<<10)
					if err := hostchan.WriteFrame(host, hostchan.FrameData, data[:n]); err != nil {
						return
					}
					data = data[n:]
				}
				continue
			}
			var m struct {
				Type string `json:"type"`
				Cols int    `json:"cols"`
				Rows int    `json:"rows"`
			}
			if json.Unmarshal(data, &m) == nil && m.Type == "resize" {
				if err := hostchan.WriteFrame(host, hostchan.FrameResize, hostchan.ResizePayload(m.Cols, m.Rows)); err != nil {
					return
				}
			}
		}
	}()

	var reason string
	select {
	case reason = <-agentDone:
	case <-browserDone:
		reason = "closed"
	case <-sctx.Done():
	}
	// The grant's end wins over whatever followed from it (the browser's read failing).
	switch cause := context.Cause(sctx); {
	case errors.Is(cause, errTerminalExpired):
		reason = "expired"
	case errors.Is(cause, errTerminalRevoked):
		reason = "revoked"
	}
	// Closing the stream is what ends the shell when the agent has not. The server is let go
	// with it, before the operator hears of the end and before the journal's line: one who opens
	// the next session at once must not find this one still holding the server (terminal_busy).
	host.Close()
	h.terms.remove(sess)
	secs := int64(h.d.Now().Sub(started).Seconds())
	recMu.Lock()
	rec := recording
	recMu.Unlock()
	writeWS(rctx, c, map[string]any{"type": "end", "reason": reason})
	code := websocket.StatusNormalClosure
	if reason == "revoked" || reason == "expired" {
		code = wsClosedGrantEnded
	}
	_ = closeWS(c, code, reason)
	h.audit(rctx, 0, "support.terminal.end", audit.TargetSupportGrant, strconv.FormatInt(g.ID, 10),
		map[string]any{"target": target, "seconds": secs, "reason": reason, "recording": rec})
	h.d.Support.NotifyTerminal(context.WithoutCancel(rctx), g, target, true, reason, secs)
}

// cleanReason keeps what the agent says of the end within the words it uses.
func cleanReason(r string) string {
	switch r {
	case "closed", "idle", "expired", "revoked", "exit", "recording", "error":
		return r
	}
	return "error"
}

func writeWS(ctx context.Context, c *websocket.Conn, v any) {
	raw, err := json.Marshal(v)
	if err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, terminalCloseWait)
	defer cancel()
	_ = c.Write(ctx, websocket.MessageText, raw)
}

func closeWS(c *websocket.Conn, code websocket.StatusCode, reason string) error {
	// A close reason is at most 123 bytes.
	return c.Close(code, textutil.Clip(reason, 100))
}

// ---- recordings ---------------------------------------------------------------------

type recordingsInput struct {
	Source string `query:"source" default:"panel" maxLength:"24" doc:"Чьи записи: panel или node:<id> ноды"`
}

type recordingInput struct {
	Name   string `path:"name" maxLength:"64" pattern:"^[0-9]{1,18}-[0-9]{1,12}\\.cast$" doc:"Имя записи из списка"`
	Source string `query:"source" default:"panel" maxLength:"24" doc:"Чья запись: panel или node:<id> ноды"`
}

// RecordingsView is the recordings of one server.
type RecordingsView struct {
	Source     string                  `json:"source"`
	Recordings []nodeapi.RecordingInfo `json:"recordings" doc:"От новых к старым"`
}

type recordingsOutput struct{ Body RecordingsView }

type recordingOutput struct {
	ContentType string `header:"Content-Type"`
	Body        []byte
}

// recordingSource resolves ?source= to the server whose recordings are asked for.
func (h *handlers) recordingSource(ctx context.Context, source string) (terminalSource, error) {
	if source != support.TargetPanel && !hostchan.ValidTarget(source) {
		return terminalSource{}, fieldErr("query.source", "bad_source")
	}
	src, status, code := h.terminalSourceOf(ctx, source)
	if status != 0 {
		return terminalSource{}, huma.NewError(status, code)
	}
	return src, nil
}

func (h *handlers) listRecordings(ctx context.Context, in *recordingsInput) (*recordingsOutput, error) {
	if err := requireOwner(ctx, "source"); err != nil {
		return nil, err
	}
	src, err := h.recordingSource(ctx, in.Source)
	if err != nil {
		return nil, err
	}
	out := &recordingsOutput{Body: RecordingsView{Source: in.Source, Recordings: []nodeapi.RecordingInfo{}}}
	if src.host != nil {
		list, err := src.host.Recordings()
		if err != nil {
			return nil, h.hostError(err, "recordings")
		}
		out.Body.Recordings = list
		return out, nil
	}
	list, err := h.d.Nodes.Recordings(ctx, src.nodeID)
	if err != nil {
		return nil, h.hostNodeError(err, "recordings", src.nodeID)
	}
	// A node is a server somebody else may run: what it lists is kept to what the agent makes.
	for _, rec := range list.Recordings[:min(len(list.Recordings), 1000)] {
		if hostchan.ValidRecordingName(rec.Name) && rec.Size >= 0 {
			out.Body.Recordings = append(out.Body.Recordings, rec)
		}
	}
	return out, nil
}

func (h *handlers) getRecording(ctx context.Context, in *recordingInput) (*recordingOutput, error) {
	if err := requireOwner(ctx, "name"); err != nil {
		return nil, err
	}
	if !hostchan.ValidRecordingName(in.Name) {
		return nil, huma.Error404NotFound("not_found")
	}
	src, err := h.recordingSource(ctx, in.Source)
	if err != nil {
		return nil, err
	}
	out := &recordingOutput{ContentType: "application/x-asciicast"}
	if src.host != nil {
		f, size, err := src.host.OpenRecording(in.Name)
		if errors.Is(err, hostchan.ErrNoRecording) {
			return nil, huma.Error404NotFound("not_found")
		}
		if err != nil {
			return nil, h.hostError(err, "recording")
		}
		defer f.Close()
		out.Body = make([]byte, size)
		if _, err := io.ReadFull(f, out.Body); err != nil {
			return nil, huma.Error500InternalServerError("host_failed")
		}
		return out, nil
	}
	raw, err := h.d.Nodes.Recording(ctx, src.nodeID, in.Name)
	var ne *nodeapi.Error
	if errors.As(err, &ne) && ne.Code == nodeapi.CodeNoRecording {
		return nil, huma.Error404NotFound("not_found")
	}
	if err != nil {
		return nil, h.hostNodeError(err, "recording", src.nodeID)
	}
	out.Body = raw
	return out, nil
}
