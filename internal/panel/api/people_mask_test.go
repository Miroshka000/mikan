package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"mikan/internal/panel/store/db"
	"mikan/internal/panel/support"
)

func TestMaskPeople(t *testing.T) {
	in := `{"items":[{"id":7,"name":"Ivan Petrov","contact":"@ivan","note":"owes","sub_url":"https://h/s/tok","online_ips":["1.2.3.4"],"used_up":123456789012345678,` +
		`"telegram":{"id":99,"username":"ivan_tg","name":"Ivan"},"legacy":{"url":"https://old/tok"},"tags":["vip"]}],` +
		`"top_month":[{"id":3,"name":"Anna","bytes":5}],` +
		`"payment":{"user_id":7,"user_name":"Ivan Petrov","tg_id":99,"tg_username":"ivan_tg","tariff_name":"Month <b>"},` +
		`"hit":{"user_id":7,"ip":"5.6.7.8","user_name":"Ivan Petrov","node_name":"DE-1"},` +
		`"device":{"id":1,"name":"Ivan's iPhone","hwid":"abc","last_ip":"9.9.9.9","platform":"ios"},` +
		`"node":{"id":1,"name":"DE-1","host":"h","ip":"203.0.113.9","note":"fine"},"tariff":{"id":2,"name":"Month"},` +
		// The addresses a subscriber came from: a device list names no user beside them.
		`"devices":[{"ip":"7.7.7.7","first_seen":"a","last_seen":"b","online":true}],"probe":{"ok":true,"ip":"198.51.100.7","checked_at":"c"},` +
		// Where a subscriber is online now (GET /users/{id}/online): the node stays, the addresses go.
		`"live":[{"node_id":2,"node_name":"DE-2","device_id":1,"online_ips":["4.4.4.4"],"conns":3}]}`
	out := string(maskPeople([]byte(in)))
	for _, leak := range []string{"Ivan", "@ivan", "owes", "tok", "1.2.3.4", "99", "ivan_tg", "old", "Anna", "5.6.7.8", "abc", "9.9.9.9", "7.7.7.7", "4.4.4.4"} {
		// 99 also stands in "tg_id":99 only; used_up has no 99 run.
		if strings.Contains(out, leak) {
			t.Errorf("%q survives: %s", leak, out)
		}
	}
	for _, keep := range []string{`"name":"#7"`, `"name":"#3"`, `"user_name":"#7"`, `"tg_id":0`, `"used_up":123456789012345678`, `"tags":["vip"]`, `"online_ips":[]`,
		`"tariff_name":"Month <b>"`, `"node_name":"DE-1"`, `"ip":"203.0.113.9"`, `"ip":"198.51.100.7"`, `"online":true`, `"name":"DE-1"`, `"name":"Month"`, `"platform":"ios"`, `"bytes":5`, `"node_name":"DE-2"`, `"conns":3`} {
		if !strings.Contains(out, keep) {
			t.Errorf("%s is lost: %s", keep, out)
		}
	}
	// What is not an object of people passes untouched, and what is not JSON is not sent.
	if got := string(maskPeople([]byte(`[1,"a",null,true]`))); got != `[1,"a",null,true]` {
		t.Errorf("scalars: %s", got)
	}
	if got := string(maskPeople([]byte(`<html>`))); !strings.Contains(got, "masking_failed") {
		t.Errorf("garbage: %s", got)
	}
}

// The writer holds an answer only for a caller marked for masking, and sends the others
// as they were written.
func TestMaskResponses(t *testing.T) {
	body := `{"name":"Ivan","contact":"@ivan","sub_url":"x"}`
	handler := func(mark bool) http.Handler {
		return maskResponses(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if mark {
				r.Context().Value(keyMask).(*maskState).on.Store(true)
			}
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Content-Length", "999")
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(body))
		}))
	}
	plain := httptest.NewRecorder()
	handler(false).ServeHTTP(plain, httptest.NewRequest(http.MethodGet, "/", nil))
	if plain.Code != http.StatusCreated || plain.Body.String() != body {
		t.Fatalf("unmarked: %d %s", plain.Code, plain.Body)
	}
	masked := httptest.NewRecorder()
	handler(true).ServeHTTP(masked, httptest.NewRequest(http.MethodGet, "/", nil))
	if masked.Code != http.StatusCreated || strings.Contains(masked.Body.String(), "Ivan") || strings.Contains(masked.Body.String(), "@ivan") {
		t.Fatalf("marked: %d %s", masked.Code, masked.Body)
	}
	if masked.Header().Get("Content-Length") == "999" || masked.Header().Get("Content-Length") == "" {
		t.Fatalf("the length of the held answer is stale: %q", masked.Header().Get("Content-Length"))
	}
}

func TestSupportGate(t *testing.T) {
	op := func(id string, tags ...string) *huma.Operation { return &huma.Operation{OperationID: id, Tags: tags} }
	only := func(id string, tags ...string) *huma.Operation {
		return &huma.Operation{OperationID: id, Tags: tags, Metadata: sessionOnly}
	}
	view, change, terminal := db.SupportGrant{Level: support.LevelView}, db.SupportGrant{Level: support.LevelChange}, db.SupportGrant{Level: support.LevelTerminal}
	extended, extTerminal := db.SupportGrant{Level: support.LevelChange, Extended: true}, db.SupportGrant{Level: support.LevelTerminal, Extended: true}
	for _, c := range []struct {
		op       *huma.Operation
		mutating bool
		g        db.SupportGrant
		status   int
		code     string
	}{
		{op("list-users", "users"), false, view, 0, ""},
		{op("update-user", "users"), true, view, 403, "support_read_only"},
		{op("update-user", "users"), true, change, 0, ""},
		{op("update-user", "users"), true, terminal, 0, ""},
		{op("me", "auth"), false, change, 403, "support_denied"},
		{op("list-api-keys", "api-keys"), false, view, 403, "support_denied"},
		{op("create-support-grant", "support"), true, change, 403, "support_denied"},
		{op("list-audit", "settings"), false, view, 403, "support_denied"},
		{op("reset-admin-path", "settings"), true, change, 403, "support_denied"},
		// Closed before read-only: the answer for a write to a closed endpoint says closed.
		{op("reset-admin-path", "settings"), true, view, 403, "support_denied"},
		{op("support-whoami", supportOperatorTag), false, view, 0, ""},
		// A doctor run only reads, so view may call it; a restart writes and needs change.
		{&huma.Operation{OperationID: "doctor-server", Tags: []string{"settings"}, Metadata: supportDiagnostic}, true, view, 0, ""},
		{&huma.Operation{OperationID: "doctor-node", Tags: []string{"node"}, Metadata: supportDiagnostic}, true, view, 0, ""},
		{&huma.Operation{OperationID: "restart-server", Tags: []string{"settings"}, Metadata: sessionOnly}, true, view, 403, "support_read_only"},
		{&huma.Operation{OperationID: "restart-node", Tags: []string{"node"}, Metadata: sessionOnly}, true, view, 403, "support_read_only"},
		{&huma.Operation{OperationID: "restart-server", Tags: []string{"settings"}, Metadata: sessionOnly}, true, change, 0, ""},
		{&huma.Operation{OperationID: "restart-node", Tags: []string{"node"}, Metadata: sessionOnly}, true, terminal, 0, ""},
		// What outlasts the grant or moves money is the extended mode's, at every level.
		{only("create-node", "node"), true, change, 403, "support_extended"},
		{only("rekey-node", "node"), true, terminal, 403, "support_extended"},
		{only("update-payment-settings", "payments"), true, change, 403, "support_extended"},
		{only("get-telegram-whatever", "telegram"), false, view, 403, "support_extended"},
		{only("create-node", "node"), true, extended, 0, ""},
		{only("update-payment-settings", "payments"), true, extTerminal, 0, ""},
		// The mode adds operations, not writes: a view grant still only reads (the panel
		// refuses such a grant anyway).
		{only("create-node", "node"), true, db.SupportGrant{Level: support.LevelView, Extended: true}, 403, "support_read_only"},
		// What no operator reaches stays closed in the extended mode too.
		{only("update-telegram", "telegram"), true, extended, 403, "support_denied"},
		{only("reset-admin-path", "settings"), true, extTerminal, 403, "support_denied"},
		{only("list-audit", "settings"), false, extended, 403, "support_denied"},
		{op("me", "auth"), false, extended, 403, "support_denied"},
		{op("create-api-key", "api-keys"), true, extended, 403, "support_denied"},
		{only("create-support-grant", "support"), true, extTerminal, 403, "support_denied"},
	} {
		if status, code := supportGate(c.op, c.mutating, c.g); status != c.status || code != c.code {
			t.Errorf("%s mutating=%v level=%s extended=%v: %d %s, want %d %s", c.op.OperationID, c.mutating, c.g.Level, c.g.Extended, status, code, c.status, c.code)
		}
	}
}

// registeredOps are the panel's operations, by id, as the API registers them.
func registeredOps(t *testing.T) map[string]*huma.Operation {
	t.Helper()
	_, api, err := New(Deps{Now: time.Now})
	if err != nil {
		t.Fatal(err)
	}
	ops := map[string]*huma.Operation{}
	for _, item := range api.OpenAPI().Paths {
		for _, op := range []*huma.Operation{item.Get, item.Post, item.Put, item.Patch, item.Delete} {
			if op != nil {
				ops[op.OperationID] = op
			}
		}
	}
	return ops
}

// The operations registered for real: the diagnostics a view operator is promised (the
// UI, the docs and the MCP wrappers lean on it) pass the gate at the view level, the
// restarts do not; every sessionOnly operation outside supportSessionOps waits for the
// extended mode, and what no operator reaches is closed in it too.
func TestSupportGateOfTheRegisteredOperations(t *testing.T) {
	ops := registeredOps(t)
	view, change := db.SupportGrant{Level: support.LevelView}, db.SupportGrant{Level: support.LevelChange}
	extended := db.SupportGrant{Level: support.LevelChange, Extended: true}
	for id, want := range map[string]int{"check-node": 0, "check-node-russia": 0, "check-server": 0, "doctor-server": 0, "doctor-node": 0,
		"restart-server": http.StatusForbidden, "restart-node": http.StatusForbidden,
		"update-telegram": http.StatusForbidden, "telegram-infrastructure-connect": http.StatusForbidden, "telegram-infrastructure-disconnect": http.StatusForbidden} {
		op := ops[id]
		if op == nil {
			t.Errorf("%s is not registered", id)
			continue
		}
		if status, _ := supportGate(op, op.Method != http.MethodGet, view); status != want {
			t.Errorf("%s at view: %d, want %d", id, status, want)
		}
	}
	// The lists name operations that exist: a renamed one would drop out of them unseen.
	for _, id := range append(slices.Clone(supportDeniedOps), supportSessionOps...) {
		if ops[id] == nil {
			t.Errorf("%s is named in the support lists but not registered", id)
		}
	}
	for _, id := range supportSessionOps {
		if op := ops[id]; op != nil {
			if only, _ := op.Metadata["sessionOnly"].(bool); !only {
				t.Errorf("%s is in supportSessionOps but not sessionOnly: the list is for those alone", id)
			}
		}
	}
	for id, op := range ops {
		mutating := op.Method != http.MethodGet
		closed := slices.Contains(supportDeniedOps, id) || slices.ContainsFunc(op.Tags, func(tag string) bool { return slices.Contains(supportDeniedTags, tag) })
		only, _ := op.Metadata["sessionOnly"].(bool)
		status, code := supportGate(op, mutating, change)
		extStatus, extCode := supportGate(op, mutating, extended)
		switch {
		case closed:
			if code != "support_denied" || extCode != "support_denied" {
				t.Errorf("%s: %d %s, extended %d %s, want support_denied in both", id, status, code, extStatus, extCode)
			}
		case only && !slices.Contains(supportSessionOps, id):
			if code != "support_extended" || extStatus != 0 {
				t.Errorf("%s: %d %s, extended %d %s, want support_extended, then through", id, status, code, extStatus, extCode)
			}
		default:
			if status != 0 || extStatus != 0 {
				t.Errorf("%s at change: %d %s, extended %d %s, want through", id, status, code, extStatus, extCode)
			}
		}
	}
	// What hands out lasting access or moves money waits for the extended mode by name: the
	// review that led here found every one of these open to a change grant.
	for _, id := range []string{"create-node", "rekey-node", "import-node-warp", "set-certificate", "set-node-certificate", "set-sub-certificate",
		"import-run", "import-preview", "update-legacy-links", "update-payment-settings", "refund-payment", "mark-payment-refunded",
		"decide-topup", "adjust-balance", "set-bot-addon", "add-addon-source", "refresh-addon-source", "install-external-addon",
		"update-exit-dns", "put-traffic-detail", "update-sub-page", "put-sub-apps", "put-sub-rules"} {
		op := ops[id]
		if op == nil {
			t.Errorf("%s is not registered", id)
			continue
		}
		if _, code := supportGate(op, true, change); code != "support_extended" {
			t.Errorf("%s at change: %s, want support_extended", id, code)
		}
	}
}

// By field: an API key is refused, an operator unless its grant is extended; requireOwner
// refuses the extended mode too.
func TestRequireSessionAndOwner(t *testing.T) {
	grant := func(g db.SupportGrant) context.Context {
		return context.WithValue(context.Background(), keySupport, supportCtx{grant: g})
	}
	key := context.WithValue(context.Background(), keyAPIKey, db.ApiKey{Scope: "full"})
	change := grant(db.SupportGrant{Level: support.LevelChange})
	extended := grant(db.SupportGrant{Level: support.LevelChange, Extended: true})
	code := func(err error) string {
		if err == nil {
			return ""
		}
		var se huma.StatusError
		if !errors.As(err, &se) || se.GetStatus() != http.StatusForbidden {
			return "not 403: " + err.Error()
		}
		return se.Error()
	}
	for _, c := range []struct {
		name         string
		ctx          context.Context
		session, own string
	}{
		{"the admin", context.Background(), "", ""},
		{"a key", key, "session_only", "session_only"},
		{"an operator", change, "support_extended", "support_denied"},
		{"an operator in the extended mode", extended, "", "support_denied"},
	} {
		if got := code(requireSession(c.ctx, "f")); got != c.session {
			t.Errorf("%s: requireSession %q, want %q", c.name, got, c.session)
		}
		if got := code(requireOwner(c.ctx, "f")); got != c.own {
			t.Errorf("%s: requireOwner %q, want %q", c.name, got, c.own)
		}
	}
}

func TestSupportTerminalAllowed(t *testing.T) {
	grant := func(level string, targets ...string) context.Context {
		return context.WithValue(context.Background(), keySupport, supportCtx{grant: db.SupportGrant{Level: level, TerminalTargets: targets}})
	}
	for _, c := range []struct {
		name   string
		ctx    context.Context
		target string
		want   bool
	}{
		{"terminal on the panel", grant("terminal", "panel"), "panel", true},
		{"terminal on a named node", grant("terminal", "panel", "node:3"), "node:3", true},
		{"terminal on a node not named", grant("terminal", "panel", "node:3"), "node:4", false},
		{"change with targets in the row", grant("change", "panel"), "panel", false},
		{"view", grant("view"), "panel", false},
		{"empty target", grant("terminal", "panel"), "", false},
		{"the admin's own session", context.Background(), "panel", false},
		{"an API key", context.WithValue(context.Background(), keyAPIKey, db.ApiKey{Scope: "full"}), "panel", false},
	} {
		if got := supportTerminalAllowed(c.ctx, c.target); got != c.want {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
	}
}
