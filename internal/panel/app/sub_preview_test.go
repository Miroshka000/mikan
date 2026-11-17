package app

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"mikan/internal/panel/domain"
)

// subPreview is the part of a preview's answer the tests read.
type subPreview struct {
	Status  int    `json:"status"`
	Outcome string `json:"outcome"`
	Rule    string `json:"rule"`
	Format  string `json:"format"`
	Stub    string `json:"stub"`
	Device  string `json:"device"`
	Proxies []struct {
		Name   string `json:"name"`
		Server string `json:"server"`
	} `json:"proxies"`
	Headers []struct {
		Name  string `json:"name"`
		Value string `json:"value"`
	} `json:"headers"`
	Body string `json:"body"`
}

// The preview of a client's subscription over HTTP (#290): what the app of the headers the
// admin writes gets, and nothing changes for it: no device is bound, neither the fetch nor
// the request is recorded, the keys are placeholders and the link's token is hidden. An
// operator of support who is not shown the subscribers gets none of it.
func TestSubPreviewOverHTTP(t *testing.T) {
	s := newSupportHarness(t)
	ctx := context.Background()
	q := s.st.Q
	tariffs, _ := q.ListTariffs(ctx)
	clock := func() time.Time { return s.now }
	u, err := domain.NewUsers(s.st, domain.NewPool(s.st, clock), noChanges{}, clock).Create(ctx, domain.CreateInput{Name: "anna", TariffID: tariffs[1].ID})
	if err != nil {
		t.Fatal(err)
	}
	path := s.api + "/users/" + strconv.FormatInt(u.ID, 10) + "/sub-preview"
	ask := func(body map[string]any) subPreview {
		t.Helper()
		resp, raw := s.do(http.MethodPost, path, body, s.csrf)
		var p subPreview
		if resp.StatusCode != http.StatusOK || json.Unmarshal(raw, &p) != nil {
			t.Fatalf("preview %v: %d %s", body, resp.StatusCode, raw)
		}
		return p
	}
	count := func(table string) int {
		t.Helper()
		var n int
		if err := s.st.DB.QueryRowContext(ctx, "SELECT count(*) FROM "+table+" WHERE user_id = $1", u.ID).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	slot, err := q.GetSlot(ctx, u.SlotID.Int64)
	if err != nil {
		t.Fatal(err)
	}
	phone := []map[string]string{{"name": "X-Hwid", "value": "phone-0123456789"}}

	p := ask(map[string]any{"user_agent": "mihomo/1.19.31", "headers": phone})
	if p.Status != http.StatusOK || p.Outcome != "served" || p.Format != "clash" || p.Device != "new" || len(p.Proxies) == 0 || p.Body == "" {
		t.Fatalf("a new phone: %+v", p)
	}
	for _, h := range p.Headers {
		if strings.Contains(h.Value, u.SubToken) {
			t.Errorf("the token in %s: %s", h.Name, h.Value)
		}
	}
	if strings.Contains(p.Body, u.SubToken) || strings.Contains(p.Body, slot.Uuid) || !strings.Contains(p.Body, "00000000-0000-0000-0000-000000000000") {
		t.Errorf("the body has the token or real keys, or not the preview's:\n%.600s", p.Body)
	}
	// The links come undone from base64; every client's server is in the answer.
	links := ask(map[string]any{"user_agent": "v2rayNG/1.10.20", "format": "uri", "headers": phone})
	if links.Format != "uri" || !strings.Contains(links.Body, "://") || strings.Contains(links.Body, u.SubToken) {
		t.Errorf("links: %+v", links)
	}
	if n, m, f := count("bound_devices"), count("sub_log"), count("sub_fetches"); n != 0 || m != 0 || f != 0 {
		t.Fatalf("the preview wrote: %d devices, %d log rows, %d fetches", n, m, f)
	}

	// A real fetch of the same phone binds it and is recorded: the preview tells it known now.
	if resp, _ := s.do(http.MethodGet, "/"+subPath+"/"+u.SubToken, nil, map[string]string{"User-Agent": "mihomo/1.19.31", "X-Hwid": "phone-0123456789"}); resp.StatusCode != http.StatusOK {
		t.Fatalf("fetch: %d", resp.StatusCode)
	}
	if n, m, f := count("bound_devices"), count("sub_log"), count("sub_fetches"); n != 1 || m != 1 || f != 1 {
		t.Fatalf("the fetch wrote: %d devices, %d log rows, %d fetches", n, m, f)
	}
	if p := ask(map[string]any{"user_agent": "mihomo/1.19.31", "headers": phone}); p.Device != "known" {
		t.Errorf("the bound phone: %q", p.Device)
	}
	if n, m, f := count("bound_devices"), count("sub_log"), count("sub_fetches"); n != 1 || m != 1 || f != 1 {
		t.Fatalf("the preview wrote: %d devices, %d log rows, %d fetches", n, m, f)
	}

	// A header that is no header is refused; a client that is gone is not found.
	if resp, raw := s.do(http.MethodPost, path, map[string]any{"user_agent": "x", "headers": []map[string]string{{"name": "bad name", "value": "1"}}}, s.csrf); resp.StatusCode != http.StatusUnprocessableEntity {
		t.Errorf("a bad header: %d %s", resp.StatusCode, raw)
	}
	if resp, raw := s.do(http.MethodPost, s.api+"/users/999999/sub-preview", map[string]any{"user_agent": "x"}, s.csrf); resp.StatusCode != http.StatusNotFound {
		t.Errorf("no such client: %d %s", resp.StatusCode, raw)
	}

	// Support sees it only with the subscribers shown, as a diagnosis even at the view level.
	for _, show := range []bool{false, true} {
		g, code, out := s.grant(map[string]any{"hours": 2, "level": "view", "show_people": show})
		if code != http.StatusCreated {
			t.Fatalf("grant: %d %s", code, out)
		}
		op := s.newOperator()
		if resp, body := op.enter(g.Token); resp.StatusCode != http.StatusOK {
			t.Fatalf("enter: %d %s", resp.StatusCode, body)
		}
		resp, body := op.write(http.MethodPost, "/users/"+strconv.FormatInt(u.ID, 10)+"/sub-preview", map[string]any{"user_agent": "mihomo/1.19.31"})
		switch {
		case !show && (resp.StatusCode != http.StatusForbidden || detail(body) != "support_denied"):
			t.Errorf("an operator without the subscribers: %d %s", resp.StatusCode, body)
		case show && resp.StatusCode != http.StatusOK:
			t.Errorf("an operator with the subscribers: %d %s", resp.StatusCode, body)
		}
	}
}
