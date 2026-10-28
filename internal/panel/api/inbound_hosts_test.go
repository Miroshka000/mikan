package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"mikan/internal/panel/domain"
	"mikan/internal/panel/store/db"
	"mikan/internal/panel/support"
)

// The hosts of an inbound over the API: listed by whoever may read the inbound, changed only
// in the admin's own session (as the client endpoint of update-inbound), the default one in
// the inbound's template and never deleted, a refusal named by the host's field.
func TestInboundHostsAPI(t *testing.T) {
	f := newHostFixture(t, false, func(d *Deps) {
		d.Inbounds, d.Nodes = domain.NewInbounds(d.Store, nil, d.Now), nil
	})
	code, body := f.send(http.MethodPost, "/api/v1/inbounds", `{"preset": "vless_tls_ws"}`)
	if code != http.StatusCreated {
		t.Fatalf("create: %d %s", code, body)
	}
	var in InboundView
	if err := json.Unmarshal([]byte(body), &in); err != nil {
		t.Fatal(err)
	}
	hostsPath := "/api/v1/inbounds/" + strconv.FormatInt(in.ID, 10) + "/hosts"
	list := func(as string) []HostView {
		t.Helper()
		code, body := f.do(http.MethodGet, hostsPath, as)
		if code != http.StatusOK {
			t.Fatalf("list as %q: %d %s", as, code, body)
		}
		var out []HostView
		if err := json.Unmarshal([]byte(body), &out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	hosts := list("")
	if len(hosts) != 1 || !hosts[0].Default || hosts[0].Address != "" {
		t.Fatalf("a new inbound's hosts: %+v", hosts)
	}
	if got := list("read"); len(got) != 1 {
		t.Fatalf("a read key's list: %+v", got)
	}
	def := hosts[0]

	// A key, full or read, changes no host.
	const host = `{"address": "b.example.com", "port": 8443, "sni": "", "host": "", "fingerprint": ""}`
	for _, key := range []string{"mk_FullKeyFullKeyFullKeyFullKey0123456789", "mk_ReadKeyReadKeyReadKeyReadKey0123456789"} {
		for _, r := range []struct{ method, path, body string }{
			{http.MethodPost, hostsPath, host},
			{http.MethodPut, hostsPath + "/" + strconv.FormatInt(def.ID, 10), host},
			{http.MethodDelete, hostsPath + "/" + strconv.FormatInt(def.ID, 10), ""},
		} {
			req := httptest.NewRequest(r.method, r.path, strings.NewReader(r.body))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Authorization", "Bearer "+key)
			rec := httptest.NewRecorder()
			f.handler.ServeHTTP(rec, req)
			if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "session_only") {
				t.Errorf("%s %s with %s: %d %s", r.method, r.path, key[:10], rec.Code, rec.Body.String())
			}
		}
	}
	// Nor does an operator without the extended mode; the handler says so itself.
	op := context.WithValue(context.Background(), keySupport, supportCtx{grant: db.SupportGrant{Level: support.LevelChange}})
	if _, err := (&handlers{}).addInboundHost(op, &addHostInput{ID: in.ID}); err == nil {
		t.Fatal("an operator added a host")
	} else if status, _, code := refusalOf(err); status != http.StatusForbidden || code != "support_extended" {
		t.Fatalf("an operator: %d %s", status, code)
	}
	key := context.WithValue(context.Background(), keyAPIKey, db.ApiKey{Scope: "full"})
	if _, err := (&handlers{}).deleteInboundHost(key, &hostIDInput{ID: in.ID, HostID: def.ID}); err == nil {
		t.Fatal("a key deleted a host")
	}
	if got := list(""); len(got) != 1 {
		t.Fatalf("refused changes wrote hosts: %+v", got)
	}

	code, body = f.send(http.MethodPost, hostsPath, host)
	if code != http.StatusCreated {
		t.Fatalf("add: %d %s", code, body)
	}
	var added HostView
	_ = json.Unmarshal([]byte(body), &added)
	if added.Default || added.Address != "b.example.com" || added.Port != 8443 {
		t.Fatalf("added: %+v", added)
	}
	if code, body := f.send(http.MethodPost, hostsPath, `{"address": "bad host", "port": 0, "sni": "", "host": "", "fingerprint": ""}`); code != http.StatusUnprocessableEntity ||
		!strings.Contains(body, "body.address") || !strings.Contains(body, "config_client_server") {
		t.Fatalf("a bad address: %d %s", code, body)
	}
	addedPath := hostsPath + "/" + strconv.FormatInt(added.ID, 10)
	if code, body := f.send(http.MethodPut, addedPath, `{"address": "c.example.com", "port": 0, "sni": "s.example.com", "host": "h.example.com", "fingerprint": "ios"}`); code != http.StatusOK ||
		!strings.Contains(body, `"c.example.com"`) || !strings.Contains(body, `"ios"`) {
		t.Fatalf("update: %d %s", code, body)
	}

	// The default host is the inbound's client endpoint.
	defPath := hostsPath + "/" + strconv.FormatInt(def.ID, 10)
	if code, body := f.send(http.MethodPut, defPath, `{"address": "edge.example.com", "port": 2053, "sni": "", "host": "", "fingerprint": ""}`); code != http.StatusOK {
		t.Fatalf("the default host: %d %s", code, body)
	}
	code, body = f.do(http.MethodGet, "/api/v1/inbounds", "")
	if code != http.StatusOK || !strings.Contains(body, `"client":{"server":"edge.example.com","port":2053`) {
		t.Fatalf("the inbound after its default host: %d %s", code, body)
	}
	if code, body := f.send(http.MethodDelete, defPath, ""); code != http.StatusConflict || !strings.Contains(body, "default_host") {
		t.Fatalf("deleting the default host: %d %s", code, body)
	}
	if code, body := f.send(http.MethodDelete, addedPath, ""); code != http.StatusNoContent {
		t.Fatalf("delete: %d %s", code, body)
	}
	if code, _ := f.send(http.MethodDelete, addedPath, ""); code != http.StatusNotFound {
		t.Fatalf("deleting it again: %d", code)
	}
	if code, _ := f.do(http.MethodGet, "/api/v1/inbounds/999/hosts", ""); code != http.StatusNotFound {
		t.Fatalf("the hosts of no inbound: %d", code)
	}
	for action, want := range map[string]int{"inbound.host_add": 1, "inbound.host_update": 2, "inbound.host_delete": 1} {
		if got := f.audited(action); got != want {
			t.Errorf("%s audited %d times, want %d", action, got, want)
		}
	}
}
