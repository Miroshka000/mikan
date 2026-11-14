package app

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	"mikan/internal/hostchan"
	"mikan/internal/hostchan/hosttest"
	"mikan/internal/panel/settings"
	"mikan/internal/panel/store/db"
	"mikan/internal/panel/tgate"
)

// terminalHarness is the whole panel with the host's directory and a fake agent in it.
type terminalHarness struct {
	*supportHarness
	dir   string
	agent *hosttest.Agent
}

func newTerminalHarness(t *testing.T, with ...func(*Options)) *terminalHarness {
	t.Helper()
	dir := t.TempDir()
	agent := hosttest.Start(t, dir)
	opts := append([]func(*Options){func(o *Options) { o.DataDir = dir }}, with...)
	return &terminalHarness{supportHarness: newSupportHarness(t, opts...), dir: dir, agent: agent}
}

// terminalGrant opens a grant of the terminal level for the panel (the admin's password is
// given: that is what the level asks).
func (s *supportHarness) terminalGrant(extra map[string]any) grantReply {
	s.t.Helper()
	body := map[string]any{"hours": 6, "level": "terminal", "terminal_targets": []string{"panel"}, "password": password}
	for k, v := range extra {
		body[k] = v
	}
	g, code, out := s.grant(body)
	if code != http.StatusCreated {
		s.t.Fatalf("terminal grant: %d %s", code, out)
	}
	return g
}

// term is one open terminal session of the operator.
type term struct {
	t    *testing.T
	c    *websocket.Conn
	text strings.Builder
	msgs []map[string]any
	err  error // what ended the stream
}

// dialTerminal opens the terminal's WebSocket; the response is returned when the handshake
// is refused.
func (s *supportHarness) dialTerminal(client *http.Client, hdr http.Header, target string) (*term, *http.Response, error) {
	s.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if client == nil {
		client = s.ts.Client()
	}
	c, resp, err := websocket.Dial(ctx, s.ts.URL+s.api+"/support/terminal?target="+target, &websocket.DialOptions{HTTPClient: client, HTTPHeader: hdr})
	if err != nil {
		return nil, resp, err
	}
	s.t.Cleanup(func() { _ = c.CloseNow() })
	return &term{t: s.t, c: c}, resp, nil
}

// next reads one message: terminal bytes into text, a control message into msgs.
func (m *term) next(wait time.Duration) (websocket.MessageType, bool) {
	m.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), wait)
	defer cancel()
	typ, data, err := m.c.Read(ctx)
	if err != nil {
		m.err = err
		return 0, false
	}
	if typ == websocket.MessageBinary {
		m.text.Write(data)
		return typ, true
	}
	var v map[string]any
	if json.Unmarshal(data, &v) == nil {
		m.msgs = append(m.msgs, v)
	}
	return typ, true
}

// until reads until the terminal has shown needle.
func (m *term) until(needle string) bool {
	m.t.Helper()
	for !strings.Contains(m.text.String(), needle) {
		if _, ok := m.next(10 * time.Second); !ok {
			return false
		}
	}
	return true
}

// control reads until a control message of the type came; nil when the stream ended first.
func (m *term) control(typ string) map[string]any {
	m.t.Helper()
	for {
		for _, v := range m.msgs {
			if v["type"] == typ {
				return v
			}
		}
		if _, ok := m.next(10 * time.Second); !ok {
			return nil
		}
	}
}

// closeCode reads to the end and returns the close code the panel gave.
func (m *term) closeCode() websocket.StatusCode {
	m.t.Helper()
	for {
		if _, ok := m.next(10 * time.Second); !ok {
			break
		}
	}
	return websocket.CloseStatus(m.err)
}

func (m *term) type_(s string) {
	m.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := m.c.Write(ctx, websocket.MessageBinary, []byte(s)); err != nil {
		m.t.Fatal(err)
	}
}

func (m *term) resize(cols, rows int) {
	m.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	raw, _ := json.Marshal(map[string]any{"type": "resize", "cols": cols, "rows": rows})
	if err := m.c.Write(ctx, websocket.MessageText, raw); err != nil {
		m.t.Fatal(err)
	}
}

// auditLines returns the journal's lines of an action, oldest first.
func auditLines(t *testing.T, h *harness, action string) []db.AuditLog {
	t.Helper()
	rows, err := h.st.Q.ListAudit(context.Background(), db.ListAuditParams{BeforeID: 1 << 62, Lim: 500})
	if err != nil {
		t.Fatal(err)
	}
	var out []db.AuditLog
	for i := len(rows) - 1; i >= 0; i-- {
		if rows[i].Action == action {
			out = append(out, rows[i])
		}
	}
	return out
}

// waitAudit waits for n lines of an action: a session writes its end after the socket closed.
func waitAudit(t *testing.T, h *harness, action string, n int) []db.AuditLog {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		lines := auditLines(t, h, action)
		if len(lines) >= n || time.Now().After(deadline) {
			if len(lines) != n {
				t.Fatalf("%s written %d times, want %d", action, len(lines), n)
			}
			return lines
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func bearerHeader(token string) http.Header { return http.Header{"Authorization": {"Bearer " + token}} }

// The operator of a grant of the terminal level gets a shell on the panel's server: with the
// browser session and with the token, typed text and window sizes go to the host's agent and
// its answer comes back, and the journal says that the session began and ended, whose it was,
// where and which recording holds it.
func TestSupportTerminalSession(t *testing.T) {
	s := newTerminalHarness(t)
	g := s.terminalGrant(nil)
	op := s.newOperator()
	if resp, body := op.enter(g.Token); resp.StatusCode != http.StatusOK {
		t.Fatalf("enter: %d %s", resp.StatusCode, body)
	}
	for i, how := range []struct {
		name string
		open func() (*term, *http.Response, error)
	}{
		{"browser", func() (*term, *http.Response, error) { return s.dialTerminal(op.client, nil, "panel") }},
		{"token", func() (*term, *http.Response, error) {
			return s.dialTerminal(s.newOperator().client, bearerHeader(g.Token), "panel")
		}},
	} {
		name := how.name
		m, resp, err := how.open()
		if err != nil {
			t.Fatalf("%s: %v (%v)", name, err, resp)
		}
		sess := s.agent.Next()
		if sess.Req.GrantID != g.Grant.ID || sess.Req.Target != "panel" {
			t.Fatalf("%s: the agent was asked %+v", name, sess.Req)
		}
		ready := m.control("ready")
		if ready == nil || ready["recording"] != sess.Recording {
			t.Fatalf("%s ready: %v", name, ready)
		}
		m.type_("uname -a\n")
		if !m.until("echo:uname -a\n") {
			t.Fatalf("%s: no answer: %q", name, m.text.String())
		}
		m.resize(132, 43)
		deadline := time.Now().Add(5 * time.Second)
		for len(sess.Sizes()) == 0 && time.Now().Before(deadline) {
			time.Sleep(5 * time.Millisecond)
		}
		if got := sess.Sizes(); len(got) != 1 || got[0] != [2]int{132, 43} {
			t.Errorf("%s: sizes %v", name, got)
		}
		// The operator leaves: the host's session ends with the stream.
		_ = m.c.Close(websocket.StatusNormalClosure, "bye")
		select {
		case <-sess.Closed():
		case <-time.After(5 * time.Second):
			t.Fatalf("%s: the agent's session stayed open", name)
		}
		// The panel frees the terminal after the agent sees the stream end, and writes the end
		// last: the next session opens after that line, or it would find the terminal busy.
		waitAudit(t, s.harness, "support.terminal.end", i+1)
	}

	starts := waitAudit(t, s.harness, "support.terminal.start", 2)
	ends := waitAudit(t, s.harness, "support.terminal.end", 2)
	for _, l := range append(starts, ends...) {
		if l.AdminID.Valid || l.TargetType.String != "support_grant" || l.TargetID.String != idOf(g.Grant.ID) {
			t.Errorf("line %s: admin %v, target %s/%s", l.Action, l.AdminID, l.TargetType.String, l.TargetID.String)
		}
		d := l.Details.String
		if !strings.Contains(d, `"actor":"support:`+idOf(g.Grant.ID)+`"`) || !strings.Contains(d, `"target":"panel"`) || strings.Contains(d, g.Token) {
			t.Errorf("details of %s: %s", l.Action, d)
		}
	}
	if !strings.Contains(starts[0].Details.String, `"via":"browser"`) || !strings.Contains(starts[1].Details.String, `"via":"api"`) {
		t.Errorf("who came how: %s / %s", starts[0].Details.String, starts[1].Details.String)
	}
	for _, l := range ends {
		d := l.Details.String
		if !strings.Contains(d, `"reason":"closed"`) || !strings.Contains(d, `"recording":"`+idOf(g.Grant.ID)+`-`) || !strings.Contains(d, `"seconds":`) {
			t.Errorf("end details: %s", d)
		}
	}
	// What the admin sees of the grant: the session lines are in its card.
	_, body := s.do(http.MethodGet, s.api+"/support/grants", nil, nil)
	if !strings.Contains(string(body), "support.terminal.start") || !strings.Contains(string(body), "support.terminal.end") {
		t.Errorf("the grant's card lacks the terminal's lines: %s", body)
	}
}

// Nobody gets a terminal without a live grant of the level that names the server, whatever
// else they hold; nothing is asked of the host for any of them.
func TestSupportTerminalBoundaries(t *testing.T) {
	s := newTerminalHarness(t)
	var node int64
	if err := s.st.DB.QueryRow("INSERT INTO nodes(name,address,created_at,updated_at) VALUES('remote','198.51.100.7:9000',1,1) RETURNING id").Scan(&node); err != nil {
		t.Fatal(err)
	}
	denied := func(name string, client *http.Client, hdr http.Header, target string, status int, code string) {
		t.Helper()
		m, resp, err := s.dialTerminal(client, hdr, target)
		if err == nil {
			_ = m.c.CloseNow()
			t.Errorf("%s: a terminal was given", name)
			return
		}
		if resp == nil || resp.StatusCode != status {
			t.Errorf("%s: %v, want %d", name, resp, status)
			return
		}
		body, _ := io.ReadAll(resp.Body)
		if detail(body) != code {
			t.Errorf("%s: %s, want %s", name, body, code)
		}
	}
	// Nothing at all; the admin's own session; a full and a read key.
	denied("anonymous", s.newOperator().client, nil, "panel", http.StatusUnauthorized, "unauthorized")
	denied("the admin's session", s.client, nil, "panel", http.StatusUnauthorized, "unauthorized")
	for _, key := range []string{s.full, s.read} {
		denied("a key", s.newOperator().client, bearerHeader(key), "panel", http.StatusUnauthorized, "unauthorized")
	}
	denied("a made-up token", s.newOperator().client, bearerHeader("ms_"+strings.Repeat("a", 40)), "panel", http.StatusUnauthorized, "unauthorized")

	// Grants of the lower levels, and of the terminal level for another server.
	for _, level := range []string{"view", "change"} {
		g, code, out := s.grant(map[string]any{"hours": 2, "level": level})
		if code != http.StatusCreated {
			t.Fatalf("%s grant: %d %s", level, code, out)
		}
		op := s.newOperator()
		if resp, _ := op.enter(g.Token); resp.StatusCode != http.StatusOK {
			t.Fatal("enter")
		}
		denied(level+" by session", op.client, nil, "panel", http.StatusForbidden, "terminal_not_allowed")
		denied(level+" by token", s.newOperator().client, bearerHeader(g.Token), "panel", http.StatusForbidden, "terminal_not_allowed")
	}
	g := s.terminalGrant(map[string]any{"terminal_targets": []string{"panel"}})
	denied("a server the grant does not name", s.newOperator().client, bearerHeader(g.Token), "node:"+idOf(node), http.StatusForbidden, "terminal_not_allowed")
	denied("a node that is not there", s.newOperator().client, bearerHeader(g.Token), "node:99999", http.StatusForbidden, "terminal_not_allowed")
	for _, bad := range []string{"", "root", "node:", "node:0", "node:01", "../panel", "panel%20"} {
		denied("target "+bad, s.newOperator().client, bearerHeader(g.Token), bad, http.StatusUnprocessableEntity, "bad_target")
	}
	// The way into the page of another site is shut: the cookie of the operator's browser
	// must not drive a session for a page that is not the panel's.
	op := s.newOperator()
	g2 := s.terminalGrant(nil)
	if resp, _ := op.enter(g2.Token); resp.StatusCode != http.StatusOK {
		t.Fatal("enter")
	}
	denied("another origin", op.client, http.Header{"Origin": {"https://evil.example"}}, "panel", http.StatusForbidden, "csrf")
	denied("cross-site", op.client, http.Header{"Sec-Fetch-Site": {"cross-site"}}, "panel", http.StatusForbidden, "csrf")
	denied("same-site is not same-origin", op.client, http.Header{"Sec-Fetch-Site": {"same-site"}}, "panel", http.StatusForbidden, "csrf")
	// A plain GET with a good grant opens no session either.
	req, _ := http.NewRequest(http.MethodGet, s.ts.URL+s.api+"/support/terminal?target=panel", nil)
	req.Header.Set("Authorization", "Bearer "+g2.Token)
	resp, err := s.newOperator().client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode < 400 {
		t.Errorf("a plain GET: %d", resp.StatusCode)
	}
	// An ended grant: revoked.
	if resp, _ := s.do(http.MethodDelete, s.api+"/support/grants/"+idOf(g2.Grant.ID), nil, s.csrf); resp.StatusCode != http.StatusNoContent {
		t.Fatal("revoke")
	}
	denied("a revoked grant by session", op.client, nil, "panel", http.StatusUnauthorized, "unauthorized")
	denied("a revoked grant by token", s.newOperator().client, bearerHeader(g2.Token), "panel", http.StatusUnauthorized, "unauthorized")

	if n := len(s.agent.Requests()); n != 0 {
		t.Errorf("the host was asked %d times for sessions nobody was let have", n)
	}
	if n := len(auditLines(t, s.harness, "support.terminal.start")); n != 0 {
		t.Errorf("%d sessions began", n)
	}
}

// The end of the grant closes the open session at once: a revocation, a replacement by a new
// grant and the time running out. The operator is told why, the host's agent is told too, the
// journal says how it ended, and nobody gets back in.
func TestSupportTerminalEndsWithTheGrant(t *testing.T) {
	for name, end := range map[string]struct {
		do     func(s *terminalHarness, g grantReply)
		reason string
	}{
		"revoked": {func(s *terminalHarness, g grantReply) {
			if resp, body := s.do(http.MethodDelete, s.api+"/support/grants/"+idOf(g.Grant.ID), nil, s.csrf); resp.StatusCode != http.StatusNoContent {
				s.t.Fatalf("revoke: %d %s", resp.StatusCode, body)
			}
		}, "revoked"},
		"replaced": {func(s *terminalHarness, g grantReply) {
			s.terminalGrant(nil)
		}, "revoked"},
		"expired": {func(s *terminalHarness, g grantReply) {
			s.advance(7 * time.Hour)
			if err := s.p.Support.Sweep(context.Background()); err != nil {
				s.t.Fatal(err)
			}
		}, "expired"},
	} {
		t.Run(name, func(t *testing.T) {
			s := newTerminalHarness(t)
			g := s.terminalGrant(nil)
			m, _, err := s.dialTerminal(s.newOperator().client, bearerHeader(g.Token), "panel")
			if err != nil {
				t.Fatal(err)
			}
			sess := s.agent.Next()
			m.type_("sleep 1000\n")
			if !m.until("echo:sleep 1000\n") {
				t.Fatal("no answer")
			}
			end.do(s, g)
			if e := m.control("end"); e == nil || e["reason"] != end.reason {
				t.Fatalf("the operator is told %v, want %s (%v, %v)", e, end.reason, m.msgs, m.err)
			}
			if code := m.closeCode(); code != 4001 {
				t.Errorf("close code %d, want 4001", code)
			}
			select {
			case <-sess.Closed():
			case <-time.After(5 * time.Second):
				t.Fatal("the agent's session stayed open")
			}
			// The host's agent is told as well: the marker names the grant.
			raw, err := os.ReadFile(filepath.Join(s.dir, "support/terminal/revoked"))
			if err != nil || !strings.Contains(string(raw), idOf(g.Grant.ID)) {
				t.Errorf("the revocation marker: %q %v", raw, err)
			}
			ends := waitAudit(t, s.harness, "support.terminal.end", 1)
			if !strings.Contains(ends[0].Details.String, `"reason":"`+end.reason+`"`) {
				t.Errorf("the journal: %s", ends[0].Details.String)
			}
			// No way back with the old token or browser.
			if _, resp, err := s.dialTerminal(s.newOperator().client, bearerHeader(g.Token), "panel"); err == nil || resp == nil || resp.StatusCode != http.StatusUnauthorized {
				t.Errorf("back in with an ended grant: %v %v", err, resp)
			}
		})
	}
}

// A server has one session at a time; a host that has no agent, or no place for one, answers
// with what is wrong and is not asked again for it; a session that did not open leaves no line
// of a session in the journal.
func TestSupportTerminalWhereItCannotOpen(t *testing.T) {
	old := hostchan.TerminalWait
	hostchan.TerminalWait = 300 * time.Millisecond
	t.Cleanup(func() { hostchan.TerminalWait = old })
	s := newTerminalHarness(t)
	g := s.terminalGrant(nil)
	open := func() *term {
		t.Helper()
		m, resp, err := s.dialTerminal(s.newOperator().client, bearerHeader(g.Token), "panel")
		if err != nil {
			t.Fatalf("%v %v", err, resp)
		}
		return m
	}
	first := open()
	s.agent.Next()
	if first.control("ready") == nil {
		t.Fatal("first session")
	}
	second := open()
	if e := second.control("error"); e == nil || e["code"] != "terminal_busy" {
		t.Fatalf("a second session: %v", e)
	}
	_ = second.closeCode()
	if n := len(s.agent.Requests()); n != 1 {
		t.Errorf("the second one asked the host: %d requests", n)
	}
	_ = first.c.Close(websocket.StatusNormalClosure, "")
	waitAudit(t, s.harness, "support.terminal.end", 1)

	// The unit is not there: the request waits for nobody.
	s.agent.Pause(true)
	third := open()
	if e := third.control("error"); e == nil || e["code"] != "terminal_timeout" {
		t.Fatalf("no agent: %v", e)
	}
	_ = third.closeCode()
	if _, err := os.Stat(filepath.Join(s.dir, "support/terminal/request")); err == nil {
		t.Error("the request waits for a unit that is not there")
	}
	s.agent.Pause(false)
	// The directory is not there at all (a server that was not updated).
	if err := os.RemoveAll(filepath.Join(s.dir, "support")); err != nil {
		t.Fatal(err)
	}
	fourth := open()
	if e := fourth.control("error"); e == nil || e["code"] != "host_unavailable" {
		t.Fatalf("no directory: %v", e)
	}
	_ = fourth.closeCode()
	if _, err := os.Stat(filepath.Join(s.dir, "support")); err == nil {
		t.Error("the panel made support/ itself")
	}
	if n := len(auditLines(t, s.harness, "support.terminal.start")); n != 1 {
		t.Errorf("%d sessions are in the journal, one began", n)
	}
}

func (s *supportHarness) putRecording(dir, name, text string) {
	s.t.Helper()
	d := filepath.Join(dir, "support", "recordings")
	if err := os.MkdirAll(d, 0o755); err != nil {
		s.t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(d, name), []byte(text), 0o640); err != nil {
		s.t.Fatal(err)
	}
}

// The recordings are the admin's: listed and read from their session, and for nobody else,
// whatever the level of the grant or the scope of the key.
func TestSupportRecordingsAreTheAdminsOnly(t *testing.T) {
	s := newTerminalHarness(t)
	cast := "{\"version\":2,\"width\":80,\"height\":24}\n[0.5,\"o\",\"root@host:~# \"]\n"
	s.putRecording(s.dir, "4-1800000000.cast", cast)
	s.putRecording(s.dir, "4-1800000900.cast", "{}\n")
	s.putRecording(s.dir, "notes.txt", "not a recording")
	list := s.api + "/support/recordings"

	resp, body := s.do(http.MethodGet, list, nil, nil)
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), `"4-1800000900.cast"`) || strings.Contains(string(body), "notes.txt") || strings.Index(string(body), "900.cast") > strings.Index(string(body), "000.cast") {
		t.Fatalf("list: %d %s", resp.StatusCode, body)
	}
	resp, body = s.do(http.MethodGet, list+"/4-1800000000.cast", nil, nil)
	if resp.StatusCode != http.StatusOK || string(body) != cast || !strings.HasPrefix(resp.Header.Get("Content-Type"), "application/x-asciicast") {
		t.Fatalf("get: %d %q %s", resp.StatusCode, body, resp.Header.Get("Content-Type"))
	}
	for _, p := range []string{"/9-9.cast", "/notes.txt", "/..%2f..%2fetc%2fpasswd", "/4-1800000000.cast%00"} {
		if resp, body := s.do(http.MethodGet, list+p, nil, nil); resp.StatusCode == http.StatusOK || strings.Contains(string(body), "root:") {
			t.Errorf("%s: %d %s", p, resp.StatusCode, body)
		}
	}
	if resp, _ := s.do(http.MethodGet, list+"?source=node:99999", nil, nil); resp.StatusCode != http.StatusNotFound {
		t.Errorf("a node that is not there: %d", resp.StatusCode)
	}
	if resp, _ := s.do(http.MethodGet, list+"?source=etc", nil, nil); resp.StatusCode != http.StatusUnprocessableEntity {
		t.Errorf("a source that is not one: %d", resp.StatusCode)
	}

	// Nobody else.
	if resp, _ := s.newOperator().do(http.MethodGet, "/support/recordings", nil, nil); resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("anonymous: %d", resp.StatusCode)
	}
	for name, key := range map[string]string{"full": s.full, "read": s.read} {
		for _, p := range []string{"/support/recordings", "/support/recordings/4-1800000000.cast"} {
			resp, body := s.asKey(key, http.MethodGet, p, nil)
			if resp.StatusCode != http.StatusForbidden || strings.Contains(string(body), "version") {
				t.Errorf("%s key %s: %d %s", name, p, resp.StatusCode, body)
			}
		}
	}
	for _, level := range []string{"view", "change", "terminal"} {
		body := map[string]any{"hours": 2, "level": level}
		if level == "terminal" {
			body["terminal_targets"], body["password"] = []string{"panel"}, password
		}
		g, code, out := s.grant(body)
		if code != http.StatusCreated {
			t.Fatalf("%s grant: %d %s", level, code, out)
		}
		op := s.newOperator()
		if resp, _ := op.enter(g.Token); resp.StatusCode != http.StatusOK {
			t.Fatal("enter")
		}
		for how, call := range map[string]func(p string) (*http.Response, []byte){
			"browser": func(p string) (*http.Response, []byte) { return op.do(http.MethodGet, p, nil, nil) },
			"token": func(p string) (*http.Response, []byte) {
				return s.newOperator().do(http.MethodGet, p, nil, bearerOf(g.Token))
			},
		} {
			for _, p := range []string{"/support/recordings", "/support/recordings/4-1800000000.cast"} {
				resp, body := call(p)
				if resp.StatusCode != http.StatusForbidden || detail(body) != "support_denied" || strings.Contains(string(body), "version") {
					t.Errorf("%s %s %s: %d %s", level, how, p, resp.StatusCode, body)
				}
			}
		}
	}
}

// The admin is told in Telegram when support opens a terminal and when it closes.
func TestSupportTerminalNotices(t *testing.T) {
	var mu sync.Mutex
	var sent []string
	tg := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		if strings.HasSuffix(r.URL.Path, "/sendMessage") {
			var m struct {
				ChatID int64  `json:"chat_id"`
				Text   string `json:"text"`
			}
			_ = json.Unmarshal(raw, &m)
			mu.Lock()
			sent = append(sent, strconv.FormatInt(m.ChatID, 10)+"|"+m.Text)
			mu.Unlock()
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"ok":true,"result":{"message_id":1}}`)
	}))
	t.Cleanup(tg.Close)
	s := newTerminalHarness(t, func(o *Options) { o.TelegramAPI = tg.URL })
	ctx := context.Background()
	set := settings.New(s.st.Q)
	for k, v := range map[string]any{tgate.KeyToken: "123:abc", tgate.KeyEnabled: true, tgate.KeyInfraAdminChat: int64(777)} {
		if err := settings.Set(ctx, set, k, v); err != nil {
			t.Fatal(err)
		}
	}
	g := s.terminalGrant(nil)
	m, _, err := s.dialTerminal(s.newOperator().client, bearerHeader(g.Token), "panel")
	if err != nil {
		t.Fatal(err)
	}
	s.agent.Next()
	if m.control("ready") == nil {
		t.Fatal("not open")
	}
	_ = m.c.Close(websocket.StatusNormalClosure, "")
	var got []string
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		mu.Lock()
		got = append([]string(nil), sent...)
		mu.Unlock()
		// the operator's first use of the token is told too ("came in"); the terminal's two follow
		if len(got) >= 3 {
			break
		}
	}
	var terminal []string
	for _, text := range got {
		if strings.Contains(text, "🖥") {
			terminal = append(terminal, text)
		}
	}
	if len(terminal) != 2 {
		t.Fatalf("messages: %q", got)
	}
	if !strings.HasPrefix(terminal[0], "777|") || !strings.Contains(terminal[0], "открыла терминал") || !strings.Contains(terminal[0], "Сервер: panel") {
		t.Errorf("the start notice: %q", terminal[0])
	}
	if !strings.Contains(terminal[1], "закрыт") || !strings.Contains(terminal[1], "терминал закрыт") || strings.Contains(terminal[1], "—") {
		t.Errorf("the end notice: %q", terminal[1])
	}
	for _, text := range got {
		if strings.Contains(text, g.Token) {
			t.Error("a token is in a message")
		}
	}
}
