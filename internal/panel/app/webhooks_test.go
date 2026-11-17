package app

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"

	"mikan/internal/panel/events"
	"mikan/internal/panel/store/db"
	"mikan/internal/panel/webhooks"
)

// webhookEndpoint is an address of the API's answers.
type webhookEndpoint struct {
	ID         int64    `json:"id"`
	Name       string   `json:"name"`
	URL        string   `json:"url"`
	Events     []string `json:"events"`
	Enabled    bool     `json:"enabled"`
	AllowLocal bool     `json:"allow_local"`
	SecretHint string   `json:"secret_hint"`
	Secret     string   `json:"secret"`
	Waiting    int64    `json:"waiting"`
	Last       *struct {
		Status string `json:"status"`
		Error  string `json:"error"`
	} `json:"last"`
}

// refusal is the shape of a 422: the code of each refused field.
type refusal struct {
	Detail string `json:"detail"`
	Errors []struct {
		Message  string `json:"message"`
		Location string `json:"location"`
		Value    any    `json:"value"`
	} `json:"errors"`
}

func (r refusal) code(location string) string {
	for _, e := range r.Errors {
		if e.Location == location {
			return e.Message
		}
	}
	return ""
}

// The addresses events are sent to are the admin's session alone: a key, even a full one, can
// neither read them (an address may hold a token) nor point the events of the panel at its own
// server, and a request without the page's CSRF header is refused too.
func TestWebhooksAreTheSessionsAlone(t *testing.T) {
	k := newKeyHarness(t)
	created := mustWebhook(t, k, map[string]any{"name": "mine", "url": "https://hooks.example.com/mikan"})
	id := strconv.FormatInt(created.ID, 10)
	for name, c := range map[string]struct {
		method, path string
		body         any
	}{
		"list":       {http.MethodGet, "/webhooks", nil},
		"events":     {http.MethodGet, "/webhooks/events", nil},
		"create":     {http.MethodPost, "/webhooks", map[string]any{"name": "x", "url": "https://attacker.example/steal"}},
		"update":     {http.MethodPatch, "/webhooks/" + id, map[string]any{"url": "https://attacker.example/steal"}},
		"delete":     {http.MethodDelete, "/webhooks/" + id, nil},
		"secret":     {http.MethodPost, "/webhooks/" + id + "/secret", nil},
		"test":       {http.MethodPost, "/webhooks/" + id + "/test", nil},
		"deliveries": {http.MethodGet, "/webhooks/" + id + "/deliveries", nil},
	} {
		for _, key := range []string{k.full, k.read} {
			resp, body := k.asKey(key, c.method, c.path, c.body)
			if resp.StatusCode != http.StatusForbidden || !strings.Contains(string(body), "session_only") {
				t.Errorf("%s with a key: %d %s, want 403 session_only", name, resp.StatusCode, body)
			}
		}
		// A session that does not carry the CSRF header may not change anything.
		if c.method != http.MethodGet {
			if resp, _ := k.do(c.method, k.api+c.path, c.body, nil); resp.StatusCode != http.StatusForbidden {
				t.Errorf("%s without the CSRF header: %d", name, resp.StatusCode)
			}
		}
	}
	// Nobody signed in sees nothing.
	k.client.Jar = nil
	if resp, _ := k.do(http.MethodGet, k.api+"/webhooks", nil, nil); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("anonymous list: %d", resp.StatusCode)
	}
	// Nothing the keys tried left a trace.
	rows, err := k.st.Q.ListWebhooks(context.Background())
	if err != nil || len(rows) != 1 || rows[0].Url != "https://hooks.example.com/mikan" || rows[0].Secret != created.Secret {
		t.Fatalf("the endpoints after the refused requests: %+v %v", rows, err)
	}
}

func mustWebhook(t *testing.T, k *keyHarness, body map[string]any) webhookEndpoint {
	t.Helper()
	resp, out := k.do(http.MethodPost, k.api+"/webhooks", body, k.csrf)
	var ep webhookEndpoint
	if resp.StatusCode != http.StatusCreated || json.Unmarshal(out, &ep) != nil {
		t.Fatalf("create %v: %d %s", body, resp.StatusCode, out)
	}
	return ep
}

// Making, changing and removing an address through the API: what is refused (and the code of
// each field), what is given back and what is not, the secret shown once, the audit.
func TestWebhooksAPI(t *testing.T) {
	k := newKeyHarness(t)
	ctx := context.Background()
	call := func(method, path string, body any) (*http.Response, []byte) {
		t.Helper()
		return k.do(method, k.api+path, body, k.csrf)
	}

	// The events an address can be told of are the panel's own list.
	resp, out := call(http.MethodGet, "/webhooks/events", nil)
	var types struct {
		Types []string `json:"types"`
	}
	if resp.StatusCode != http.StatusOK || json.Unmarshal(out, &types) != nil || !slices.Equal(types.Types, events.Types()) {
		t.Fatalf("events: %d %s", resp.StatusCode, out)
	}
	if resp, out = call(http.MethodGet, "/webhooks", nil); !strings.Contains(string(out), `"items":[]`) || !strings.Contains(string(out), `"limit":20`) {
		t.Fatalf("an empty list: %d %s", resp.StatusCode, out)
	}

	// What is refused, with the code of the field.
	good := func(change func(map[string]any)) map[string]any {
		m := map[string]any{"name": "billing", "url": "https://hooks.example.com/mikan"}
		change(m)
		return m
	}
	for name, c := range map[string]struct {
		body     map[string]any
		location string
		code     string
		value    any
	}{
		"a blank name":      {good(func(m map[string]any) { m["name"] = "   " }), "body.name", "name_blank", nil},
		"ftp":               {good(func(m map[string]any) { m["url"] = "ftp://files.example.com/x" }), "body.url", "webhook_url_scheme", nil},
		"no scheme":         {good(func(m map[string]any) { m["url"] = "hooks.example.com/mikan" }), "body.url", "webhook_url_scheme", nil},
		"credentials":       {good(func(m map[string]any) { m["url"] = "https://user:pass@hooks.example.com/" }), "body.url", "webhook_url_invalid", nil},
		"a port of none":    {good(func(m map[string]any) { m["url"] = "https://hooks.example.com:70000/" }), "body.url", "webhook_url_port", nil},
		"localhost":         {good(func(m map[string]any) { m["url"] = "http://localhost:8080/x" }), "body.url", "webhook_url_local", nil},
		"a private address": {good(func(m map[string]any) { m["url"] = "http://192.168.1.5/x" }), "body.url", "webhook_url_local", nil},
		"loopback":          {good(func(m map[string]any) { m["url"] = "http://127.0.0.1:9000/x" }), "body.url", "webhook_url_local", nil},
		"the metadata":      {good(func(m map[string]any) { m["url"] = "http://169.254.169.254/latest/meta-data/" }), "body.url", "webhook_url_blocked", nil},
		"the metadata, local targets allowed": {good(func(m map[string]any) {
			m["url"], m["allow_local"] = "http://169.254.169.254/latest/", true
		}), "body.url", "webhook_url_blocked", nil},
		"an event that is none": {good(func(m map[string]any) { m["events"] = []string{"payment.paid", "payment.payd"} }), "body.events", "webhook_event_unknown", "payment.payd"},
	} {
		resp, out := call(http.MethodPost, "/webhooks", c.body)
		var r refusal
		_ = json.Unmarshal(out, &r)
		if resp.StatusCode != http.StatusUnprocessableEntity || r.code(c.location) != c.code {
			t.Errorf("%s: %d %s, want %s at %s", name, resp.StatusCode, out, c.code, c.location)
			continue
		}
		if c.value != nil && r.Errors[0].Value != c.value {
			t.Errorf("%s: value %v, want %v", name, r.Errors[0].Value, c.value)
		}
	}
	// The ones the schema refuses before the handler sees them.
	for name, body := range map[string]map[string]any{
		"no name":           {"url": "https://hooks.example.com/x"},
		"an empty url":      {"name": "x", "url": ""},
		"a long name":       {"name": strings.Repeat("n", 61), "url": "https://hooks.example.com/x"},
		"a long url":        {"name": "x", "url": "https://hooks.example.com/" + strings.Repeat("a", 2100)},
		"many events":       {"name": "x", "url": "https://hooks.example.com/x", "events": slices.Repeat([]string{"payment.paid"}, 65)},
		"events not a list": {"name": "x", "url": "https://hooks.example.com/x", "events": "payment.paid"},
	} {
		if resp, out := call(http.MethodPost, "/webhooks", body); resp.StatusCode != http.StatusUnprocessableEntity {
			t.Errorf("%s: %d %s", name, resp.StatusCode, out)
		}
	}
	if n, _ := k.st.Q.CountWebhooks(ctx); n != 0 {
		t.Fatalf("%d endpoints after refusals", n)
	}

	// A good one: on by default, told of every event, with a secret that is shown now.
	ep := mustWebhook(t, k, map[string]any{"name": " billing ", "url": "https://hooks.example.com/mikan/TOKEN-IN-THE-PATH", "events": []string{"node.up", "payment.paid", "node.up"}})
	if ep.Name != "billing" || !ep.Enabled || ep.AllowLocal || !slices.Equal(ep.Events, []string{"payment.paid", "node.up"}) || ep.Waiting != 0 || ep.Last != nil {
		t.Errorf("created %+v", ep)
	}
	if !strings.HasPrefix(ep.Secret, "whsec_") || ep.SecretHint != ep.Secret[len(ep.Secret)-4:] {
		t.Errorf("secret %q, hint %q", ep.Secret, ep.SecretHint)
	}
	off := mustWebhook(t, k, map[string]any{"name": "paused", "url": "https://hooks.example.com/other", "enabled": false})
	if off.Enabled || len(off.Events) != 0 {
		t.Errorf("a paused one: %+v", off)
	}

	// The list gives the hint, never the secret.
	resp, out = call(http.MethodGet, "/webhooks", nil)
	var list struct {
		Items []webhookEndpoint `json:"items"`
	}
	if resp.StatusCode != http.StatusOK || json.Unmarshal(out, &list) != nil || len(list.Items) != 2 {
		t.Fatalf("list: %d %s", resp.StatusCode, out)
	}
	if strings.Contains(string(out), ep.Secret) || strings.Contains(string(out), off.Secret) || strings.Contains(string(out), `"secret":`) {
		t.Fatalf("the list gives out a secret: %s", out)
	}
	if list.Items[0].ID != ep.ID || list.Items[0].SecretHint != ep.SecretHint || list.Items[1].ID != off.ID {
		t.Errorf("list %+v", list.Items)
	}

	// A change is only what is sent; it is judged as it will be; the answer has no secret.
	path := "/webhooks/" + strconv.FormatInt(ep.ID, 10)
	resp, out = call(http.MethodPatch, path, map[string]any{"name": "billing 2", "enabled": false})
	var patched webhookEndpoint
	if resp.StatusCode != http.StatusOK || json.Unmarshal(out, &patched) != nil || patched.Name != "billing 2" || patched.Enabled || patched.URL != ep.URL || !slices.Equal(patched.Events, ep.Events) || patched.Secret != "" {
		t.Fatalf("patch: %d %s", resp.StatusCode, out)
	}
	resp, out = call(http.MethodPatch, path, map[string]any{"events": []string{}})
	if json.Unmarshal(out, &patched) != nil || resp.StatusCode != http.StatusOK || len(patched.Events) != 0 {
		t.Fatalf("every event: %d %s", resp.StatusCode, out)
	}
	for name, body := range map[string]map[string]any{
		"a bad url":      {"url": "gopher://x.example/"},
		"the metadata":   {"url": "http://169.254.169.254/"},
		"a local one":    {"url": "http://10.0.0.5/x"},
		"a blank name":   {"name": " "},
		"an unknown one": {"events": []string{"nope"}},
	} {
		resp, out := call(http.MethodPatch, path, body)
		if resp.StatusCode != http.StatusUnprocessableEntity {
			t.Errorf("%s: %d %s", name, resp.StatusCode, out)
		}
	}
	if resp, out := call(http.MethodPatch, "/webhooks/999999", map[string]any{"name": "x"}); resp.StatusCode != http.StatusNotFound {
		t.Errorf("patch of an address that is not there: %d %s", resp.StatusCode, out)
	}
	if now, _ := k.st.Q.GetWebhook(ctx, ep.ID); now.Url != ep.URL || now.Name != "billing 2" {
		t.Fatalf("a refused patch changed the row: %+v", now)
	}
	// A local address needs the switch, and the switch is judged with the address.
	resp, out = call(http.MethodPatch, path, map[string]any{"url": "http://10.0.0.5:5678/hook", "allow_local": true})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("a local address with the switch: %d %s", resp.StatusCode, out)
	}
	resp, out = call(http.MethodPatch, path, map[string]any{"allow_local": false})
	var r refusal
	_ = json.Unmarshal(out, &r)
	if resp.StatusCode != http.StatusUnprocessableEntity || r.code("body.url") != "webhook_url_local" {
		t.Fatalf("the switch taken from a local address: %d %s", resp.StatusCode, out)
	}

	// A new secret: shown now, the hint follows it, the old one is gone.
	resp, out = call(http.MethodPost, path+"/secret", nil)
	var rotated webhookEndpoint
	if resp.StatusCode != http.StatusOK || json.Unmarshal(out, &rotated) != nil || rotated.Secret == ep.Secret || !strings.HasPrefix(rotated.Secret, "whsec_") || rotated.SecretHint != rotated.Secret[len(rotated.Secret)-4:] {
		t.Fatalf("rotate: %d %s", resp.StatusCode, out)
	}
	if row, _ := k.st.Q.GetWebhook(ctx, ep.ID); row.Secret != rotated.Secret {
		t.Fatal("the new secret is not the one kept")
	}
	if resp, _ := call(http.MethodPost, "/webhooks/999999/secret", nil); resp.StatusCode != http.StatusNotFound {
		t.Errorf("rotate of an address that is not there: %d", resp.StatusCode)
	}

	// The journal says what happened, never the secret nor the path of an address.
	audit, err := k.st.Q.ListAudit(ctx, db.ListAuditParams{BeforeID: 1 << 62, Lim: 100})
	if err != nil {
		t.Fatal(err)
	}
	actions := map[string]bool{}
	for _, e := range audit {
		if !strings.HasPrefix(e.Action, "webhook.") {
			continue
		}
		actions[e.Action] = true
		for _, secret := range []string{ep.Secret, rotated.Secret, off.Secret, "TOKEN-IN-THE-PATH", "whsec_"} {
			if strings.Contains(e.Details.String, secret) {
				t.Errorf("%s writes %q to the journal: %s", e.Action, secret, e.Details.String)
			}
		}
	}
	for _, want := range []string{"webhook.create", "webhook.update", "webhook.rotate"} {
		if !actions[want] {
			t.Errorf("no %s in the journal (%v)", want, actions)
		}
	}

	// Gone with its log; a second delete and everything after find nothing.
	if resp, _ := call(http.MethodDelete, path, nil); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("delete: %d", resp.StatusCode)
	}
	for _, c := range []struct{ method, path string }{{http.MethodDelete, path}, {http.MethodPost, path + "/test"}, {http.MethodGet, path + "/deliveries"}, {http.MethodPost, path + "/secret"}} {
		if resp, _ := call(c.method, c.path, nil); resp.StatusCode != http.StatusNotFound {
			t.Errorf("%s %s after the delete: %d", c.method, c.path, resp.StatusCode)
		}
	}
}

// The panel holds no more than twenty addresses, and says so.
func TestWebhooksLimit(t *testing.T) {
	k := newKeyHarness(t)
	for i := range webhooks.MaxEndpoints {
		mustWebhook(t, k, map[string]any{"name": "n" + strconv.Itoa(i), "url": "https://hooks.example.com/" + strconv.Itoa(i)})
	}
	resp, out := k.do(http.MethodPost, k.api+"/webhooks", map[string]any{"name": "one more", "url": "https://hooks.example.com/more"}, k.csrf)
	var r refusal
	_ = json.Unmarshal(out, &r)
	if resp.StatusCode != http.StatusConflict || r.Detail != "webhooks_limit" {
		t.Fatalf("the twenty-first: %d %s", resp.StatusCode, out)
	}
}

// The test button sends the event, tells how it went and writes it to the log, which pages
// newest first and filters by status; a receiver that refuses is told as a failure, and the
// log is the session's alone like the rest.
func TestWebhookTestAndLog(t *testing.T) {
	k := newKeyHarness(t)
	var mu sync.Mutex
	var statuses []int
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = append(seen, r.Header.Get("X-Mikan-Event")+" "+r.URL.Path)
		status := http.StatusNoContent
		if len(statuses) > 0 {
			status, statuses = statuses[0], statuses[1:]
		}
		mu.Unlock()
		w.WriteHeader(status)
	}))
	t.Cleanup(srv.Close)
	ep := mustWebhook(t, k, map[string]any{"name": "local", "url": srv.URL + "/hook", "allow_local": true, "events": []string{"node.down"}})
	path := "/webhooks/" + strconv.FormatInt(ep.ID, 10)
	type result struct {
		OK         bool   `json:"ok"`
		Status     int    `json:"status"`
		Error      string `json:"error"`
		DurationMS int64  `json:"duration_ms"`
	}
	test := func() result {
		t.Helper()
		resp, out := k.do(http.MethodPost, k.api+path+"/test", nil, k.csrf)
		var r result
		if resp.StatusCode != http.StatusOK || json.Unmarshal(out, &r) != nil {
			t.Fatalf("test: %d %s", resp.StatusCode, out)
		}
		return r
	}
	if r := test(); !r.OK || r.Status != 204 || r.Error != "" {
		t.Fatalf("test: %+v", r)
	}
	mu.Lock()
	statuses = []int{http.StatusServiceUnavailable}
	mu.Unlock()
	if r := test(); r.OK || r.Status != 503 || r.Error != "status" {
		t.Fatalf("test of a receiver that refuses: %+v", r)
	}
	if got := []string{"webhook.test /hook", "webhook.test /hook"}; !slices.Equal(seen, got) {
		t.Fatalf("the receiver saw %v", seen)
	}

	type page struct {
		Items []struct {
			ID           int64  `json:"id"`
			Type         string `json:"type"`
			EventID      *int64 `json:"event_id"`
			Status       string `json:"status"`
			Attempts     int64  `json:"attempts"`
			ResponseCode int64  `json:"response_code"`
			Error        string `json:"error"`
		} `json:"items"`
		Next int64 `json:"next"`
	}
	get := func(query string) page {
		t.Helper()
		resp, out := k.do(http.MethodGet, k.api+path+"/deliveries"+query, nil, nil)
		var p page
		if resp.StatusCode != http.StatusOK || json.Unmarshal(out, &p) != nil {
			t.Fatalf("log %s: %d %s", query, resp.StatusCode, out)
		}
		return p
	}
	all := get("")
	if len(all.Items) != 2 || all.Next != 0 {
		t.Fatalf("log %+v", all)
	}
	failed, delivered := all.Items[0], all.Items[1] // newest first
	if failed.Status != "failed" || failed.ResponseCode != 503 || failed.Error != "status" || failed.EventID != nil || failed.Type != "webhook.test" || failed.Attempts != 1 {
		t.Errorf("the failed test: %+v", failed)
	}
	if delivered.Status != "delivered" || delivered.ResponseCode != 204 || delivered.Error != "" {
		t.Errorf("the delivered test: %+v", delivered)
	}
	if only := get("?status=failed"); len(only.Items) != 1 || only.Items[0].ID != failed.ID {
		t.Errorf("only the failed: %+v", only)
	}
	first := get("?limit=1")
	if len(first.Items) != 1 || first.Items[0].ID != failed.ID || first.Next != failed.ID {
		t.Errorf("a page of one: %+v", first)
	}
	if second := get("?limit=1&before=" + strconv.FormatInt(first.Next, 10)); len(second.Items) != 1 || second.Items[0].ID != delivered.ID {
		t.Errorf("the page after: %+v", second)
	}
	for _, bad := range []string{"?status=sent", "?limit=0", "?limit=101", "?before=-1", "?limit=x"} {
		if resp, _ := k.do(http.MethodGet, k.api+path+"/deliveries"+bad, nil, nil); resp.StatusCode != http.StatusUnprocessableEntity {
			t.Errorf("%s accepted: %d", bad, resp.StatusCode)
		}
	}
	// The log of the test is not in the journal with the address: its rows carry no address.
	_, out := k.do(http.MethodGet, k.api+path+"/deliveries", nil, nil)
	if strings.Contains(string(out), srv.URL) || strings.Contains(string(out), "/hook") {
		t.Errorf("the log holds the address: %s", out)
	}

	// An address the endpoint may not reach: the test is refused when it connects, and says so.
	row, err := k.st.Q.GetWebhook(context.Background(), ep.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := k.st.DB.Exec("UPDATE webhook_endpoints SET allow_local = false WHERE id = $1", row.ID); err != nil {
		t.Fatal(err)
	}
	if r := test(); r.OK || r.Status != 0 || r.Error != "address_local" {
		t.Fatalf("test of a local address that is not allowed: %+v", r)
	}
	mu.Lock()
	n := len(seen)
	mu.Unlock()
	if n != 2 {
		t.Fatalf("the receiver was reached %d times", n)
	}
}
