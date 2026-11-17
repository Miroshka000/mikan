package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"testing"

	"mikan/internal/panel/store/db"
	"mikan/internal/panel/support"
)

// The ports a node's users reach on the server's own address: checked, written one way, on
// the panel's own node as on any other, and read back with the node.
func TestNodeOwnPorts(t *testing.T) {
	f := newHostFixture(t, false, func(d *Deps) { d.Nodes = nil })
	ctx := context.Background()
	ports := func(id int64) []int64 {
		t.Helper()
		n, err := f.st.GetNode(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		return n.OwnPorts
	}
	for _, bad := range []string{`[0]`, `[-1]`, `[65536]`, `[53, 70000]`} {
		code, body := f.send(http.MethodPatch, "/api/v1/nodes/2", `{"own_ports": `+bad+`}`)
		if code != http.StatusUnprocessableEntity || !strings.Contains(body, "own_port_invalid") || !strings.Contains(body, "body.own_ports") {
			t.Errorf("%s: %d %s", bad, code, body)
		}
	}
	for _, bad := range []string{`["53"]`, `[1.5]`, `"53"`} {
		if code, body := f.send(http.MethodPatch, "/api/v1/nodes/2", `{"own_ports": `+bad+`}`); code != http.StatusUnprocessableEntity {
			t.Errorf("%s: %d %s", bad, code, body)
		}
	}
	many := make([]int, 17)
	for i := range many {
		many[i] = 1000 + i
	}
	raw, _ := json.Marshal(map[string]any{"own_ports": many})
	if code, body := f.send(http.MethodPatch, "/api/v1/nodes/2", string(raw)); code != http.StatusUnprocessableEntity {
		t.Fatalf("17 ports were taken: %d %s", code, body)
	}
	if got := ports(2); len(got) != 0 {
		t.Fatalf("a refused change was written: %v", got)
	}

	audited := f.audited("node.update")
	code, body := f.send(http.MethodPatch, "/api/v1/nodes/2", `{"own_ports": [853, 53, 853]}`)
	if code != http.StatusOK {
		t.Fatalf("%d %s", code, body)
	}
	var got NodeInfo
	if err := json.Unmarshal([]byte(body), &got); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got.OwnPorts, []int{53, 853}) {
		t.Fatalf("the answer: %v", got.OwnPorts)
	}
	if row := ports(2); !slices.Equal(row, []int64{53, 853}) {
		t.Fatalf("the row: %v", row)
	}
	if f.audited("node.update") != audited+1 {
		t.Fatal("the change is not in the audit log")
	}

	// The list goes whole, and a request about something else leaves it alone.
	if code, body := f.send(http.MethodPatch, "/api/v1/nodes/2", `{"enabled": true}`); code != http.StatusOK {
		t.Fatalf("%d %s", code, body)
	}
	if row := ports(2); !slices.Equal(row, []int64{53, 853}) {
		t.Fatalf("another field took the ports: %v", row)
	}
	if code, body := f.send(http.MethodPatch, "/api/v1/nodes/1", `{"own_ports": [53]}`); code != http.StatusOK {
		t.Fatalf("the panel's own node: %d %s", code, body)
	}
	if row := ports(1); !slices.Equal(row, []int64{53}) {
		t.Fatalf("the panel's own node: %v", row)
	}
	code, body = f.send(http.MethodPatch, "/api/v1/nodes/2", `{"own_ports": []}`)
	if code != http.StatusOK {
		t.Fatalf("%d %s", code, body)
	}
	if !strings.Contains(body, `"own_ports":[]`) || len(ports(2)) != 0 {
		t.Fatalf("an empty list takes them all away: %s, row %v", body, ports(2))
	}
}

// What a node's users reach on the server itself is the admin's alone: an API key does not
// get to change it, and an operator only in the extended mode. The form sends the list as it
// is, so an operator who edits something else is not refused for it.
func TestNodeOwnPortsAreTheAdmins(t *testing.T) {
	f := newHostFixture(t, false, func(d *Deps) { d.Nodes = nil })
	ctx := context.Background()
	if err := f.st.SetNodeOwnPorts(ctx, db.SetNodeOwnPortsParams{OwnPorts: []int64{53}, ID: 2}); err != nil {
		t.Fatal(err)
	}
	row := func() []int64 {
		t.Helper()
		n, err := f.st.GetNode(ctx, 2)
		if err != nil {
			t.Fatal(err)
		}
		return n.OwnPorts
	}

	req := httptest.NewRequest(http.MethodPatch, "/api/v1/nodes/2", strings.NewReader(`{"own_ports": [22]}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer mk_FullKeyFullKeyFullKeyFullKey0123456789")
	rec := httptest.NewRecorder()
	f.handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "session_only") {
		t.Fatalf("a full key: %d %s", rec.Code, rec.Body.String())
	}

	h := &handlers{d: f.deps}
	patch := func(ctx context.Context, ports ...int) error {
		in := &patchNodeInput{ID: 2}
		list := append([]int{}, ports...)
		in.Body.OwnPorts = &list
		_, err := h.updateNode(ctx, in)
		return err
	}
	grant := func(level string, extended bool) context.Context {
		return context.WithValue(ctx, keySupport, supportCtx{grant: db.SupportGrant{Level: level, Extended: extended}})
	}
	for name, try := range map[string][]int{"a new port": {22, 53}, "fewer ports": {}, "not a port": {0}} {
		err := patch(grant(support.LevelChange, false), try...)
		if status, location, code := refusalOf(err); status != http.StatusForbidden || location != "body.own_ports" || code != "support_extended" {
			t.Errorf("%s, by an operator: %d %q %q (%v)", name, status, location, code, err)
		}
	}
	if got := row(); !slices.Equal(got, []int64{53}) {
		t.Fatalf("a refused change was written: %v", got)
	}
	if err := patch(grant(support.LevelChange, false), 53, 53); err != nil {
		t.Fatalf("an operator saving the form with the list as it is: %v", err)
	}
	if err := patch(grant(support.LevelChange, true), 53, 853); err != nil {
		t.Fatalf("an operator in the extended mode: %v", err)
	}
	if got := row(); !slices.Equal(got, []int64{53, 853}) {
		t.Fatalf("the extended mode's change: %v", got)
	}
}

// statesChanged counts what the nodes are told to look at again.
type statesChanged struct {
	noChanges
	n *int
}

func (c statesChanged) SlotsChanged() { *c.n++ }

// A new subscription port goes to the nodes at once: the panel's own node keeps it open to
// its users, and until it knows of it a client going through it could not refresh.
func TestSubPortReachesTheNodes(t *testing.T) {
	changed := 0
	f := newHostFixture(t, false, func(d *Deps) {
		d.Nodes = nil
		d.Changes = statesChanged{n: &changed}
		d.SubPort = func(int) error { return nil }
	})
	for _, tc := range []struct {
		body string
		want int
	}{
		{`{"brand": "Mikan"}`, 0},
		{`{"sub_port": ` + strconv.Itoa(2083) + `}`, 1},
		{`{"sub_port": 0}`, 2},
	} {
		code, body := f.send(http.MethodPatch, "/api/v1/settings", tc.body)
		if code != http.StatusOK {
			t.Fatalf("%s: %d %s", tc.body, code, body)
		}
		if changed != tc.want {
			t.Fatalf("%s: the nodes were told %d times, want %d", tc.body, changed, tc.want)
		}
	}
}
