package app

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"mikan/internal/panel/domain"
	"mikan/internal/panel/filters"
	"mikan/internal/panel/settings"
)

// The traffic filters from the admin's side: off by default with the mail preset ready,
// a wrong entry named, the lists put in order, each filter replaced on its own.
func TestFiltersAPI(t *testing.T) {
	h := newHarness(t)
	if resp, _ := h.login(password, ""); resp.StatusCode != http.StatusOK {
		t.Fatal("login")
	}
	csrf := map[string]string{"X-CSRF-Token": h.csrf}
	path := "/" + adminPath + "/api/v1/filters"
	type egress struct {
		Enabled  bool     `json:"enabled"`
		Mail     bool     `json:"mail"`
		Ports    []string `json:"ports"`
		Networks []string `json:"networks"`
		Domains  []string `json:"domains"`
	}
	type ingress struct {
		Enabled  bool     `json:"enabled"`
		Allow    bool     `json:"allow"`
		Networks []string `json:"networks"`
	}
	var v struct {
		Egress    egress   `json:"egress"`
		Ingress   ingress  `json:"ingress"`
		MailPorts []string `json:"mail_ports"`
	}
	resp, body := h.do(http.MethodGet, path, nil, nil)
	if resp.StatusCode != http.StatusOK || json.Unmarshal(body, &v) != nil || v.Egress.Enabled || !v.Egress.Mail || v.Ingress.Enabled ||
		v.Egress.Ports == nil || v.Ingress.Networks == nil || !slices.Equal(v.MailPorts, []string{"465", "587", "2525"}) {
		t.Fatalf("off by default, the mail preset ready: %d %s", resp.StatusCode, body)
	}

	bad := egress{Enabled: true, Mail: true, Ports: []string{"465"}, Networks: []string{}, Domains: []string{"ok.example", "no way"}}
	resp, body = h.do(http.MethodPatch, path, map[string]any{"egress": bad}, csrf)
	if resp.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(string(body), "filters_entry") || !strings.Contains(string(body), "body.egress.domains[1]") {
		t.Fatalf("a wrong domain: %d %s", resp.StatusCode, body)
	}

	good := egress{Enabled: true, Mail: true, Ports: []string{"6881-6889", "1194"}, Networks: []string{"203.0.113.7/24"}, Domains: []string{"*.Spam.Example"}}
	resp, body = h.do(http.MethodPatch, path, map[string]any{"egress": good}, csrf)
	if resp.StatusCode != http.StatusOK || json.Unmarshal(body, &v) != nil || !v.Egress.Enabled ||
		!slices.Equal(v.Egress.Ports, []string{"1194", "6881-6889"}) || v.Egress.Networks[0] != "203.0.113.0/24" || v.Egress.Domains[0] != "spam.example" {
		t.Fatalf("egress saved in order: %d %s", resp.StatusCode, body)
	}

	resp, body = h.do(http.MethodPatch, path, map[string]any{"ingress": ingress{Enabled: true, Allow: true, Networks: []string{"198.51.100.7"}}}, csrf)
	if resp.StatusCode != http.StatusOK || json.Unmarshal(body, &v) != nil || !v.Ingress.Allow || v.Ingress.Networks[0] != "198.51.100.7/32" || !v.Egress.Enabled || len(v.Egress.Ports) != 2 {
		t.Fatalf("ingress saved, egress kept: %d %s", resp.StatusCode, body)
	}
}

func TestScopedFiltersAPI(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	if err := domain.Seed(ctx, h.st, h.now); err != nil {
		t.Fatal(err)
	}
	clock := func() time.Time { return h.now }
	tariffs, err := h.st.Q.ListTariffs(ctx)
	if err != nil || len(tariffs) == 0 {
		t.Fatalf("list plans: %v", err)
	}
	clients := domain.NewUsers(h.st, domain.NewPool(h.st, clock), noChanges{}, clock)
	user, err := clients.Create(ctx, domain.CreateInput{Name: "client", TariffID: tariffs[0].ID})
	if err != nil {
		t.Fatal(err)
	}
	if resp, _ := h.login(password, ""); resp.StatusCode != http.StatusOK {
		t.Fatal("login")
	}
	csrf := map[string]string{"X-CSRF-Token": h.csrf}
	path := "/" + adminPath + "/api/v1/filters"
	var v struct {
		Egress filters.Egress
		Sites  []filters.Site
	}
	send := func(method, target string, input any) {
		t.Helper()
		resp, body := h.do(method, target, input, csrf)
		if resp.StatusCode != http.StatusOK || json.Unmarshal(body, &v) != nil {
			t.Fatalf("%s %s: %d %s", method, target, resp.StatusCode, body)
		}
	}

	egress := map[string]any{
		"enabled": true, "mail": false, "ports": []string{}, "networks": []string{}, "domains": []string{"example.com"},
		"rules": []filters.Rule{{Kind: "port", Value: "1000 - 2000", Scope: "only", Users: []int64{user.ID}}},
	}
	send(http.MethodPatch, path, map[string]any{"egress": egress})
	if len(v.Egress.Rules) != 1 || v.Egress.Rules[0].Value != "1000-2000" {
		t.Fatalf("scoped rule was not canonicalized: %+v", v.Egress.Rules)
	}
	site := map[string]any{"user_id": user.ID, "domain": "Open.Example.COM", "action": "allow"}
	send(http.MethodPut, path+"/site", site)
	if len(v.Sites) != 1 || !v.Sites[0].Allow || v.Sites[0].Domain != "open.example.com" {
		t.Fatalf("personal exception: %+v", v.Sites)
	}
	delete(egress, "rules")
	send(http.MethodPatch, path, map[string]any{"egress": egress})
	if len(v.Egress.Rules) != 1 || len(v.Sites) != 1 {
		t.Fatalf("legacy PATCH removed new rules: %+v", v)
	}
	site["action"] = "block"
	send(http.MethodPut, path+"/site", site)
	if len(v.Sites) != 1 || v.Sites[0].Allow {
		t.Fatalf("personal block: %+v", v.Sites)
	}
	site["action"] = "clear"
	send(http.MethodPut, path+"/site", site)
	if len(v.Sites) != 0 || len(v.Egress.Domains) != 1 {
		t.Fatalf("clearing the exception changed the global rule: %+v", v)
	}

	site["action"] = "block"
	send(http.MethodPut, path+"/site", site)
	if err := clients.Delete(ctx, user.ID); err != nil {
		t.Fatal(err)
	}
	send(http.MethodGet, path, nil)
	if len(v.Sites) != 0 {
		t.Fatalf("deleted client left site rules: %+v", v.Sites)
	}
}

// A save which commits while a bulk delete is dropping personal rules must retain its
// global rules. Hold the save open until the delete reaches its write to settings.
func TestBulkDeletePreservesConcurrentFilterSave(t *testing.T) {
	h := newHarness(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := domain.Seed(ctx, h.st, h.now); err != nil {
		t.Fatal(err)
	}
	tariffs, err := h.st.Q.ListTariffs(ctx)
	if err != nil || len(tariffs) == 0 {
		t.Fatalf("list plans: %v", err)
	}
	clock := func() time.Time { return h.now }
	clients := domain.NewUsers(h.st, domain.NewPool(h.st, clock), noChanges{}, clock)
	user, err := clients.Create(ctx, domain.CreateInput{Name: "client", TariffID: tariffs[0].ID})
	if err != nil {
		t.Fatal(err)
	}
	cfg := filters.Default()
	cfg.Egress.Sites = []filters.Site{{UserID: user.ID, Domain: "example.com"}}
	set := settings.New(h.st.Q)
	if err := settings.Set(ctx, set, filters.KeyConfig, cfg); err != nil {
		t.Fatal(err)
	}

	save, err := h.st.DB.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		t.Fatal(err)
	}
	defer save.Rollback()
	var writer int
	if err := save.QueryRowContext(ctx, "SELECT pg_backend_pid()").Scan(&writer); err != nil {
		t.Fatal(err)
	}
	cfg.Egress.Domains = []string{"new.example"}
	if err := settings.Set(ctx, settings.New(h.st.Q.WithTx(save)), filters.KeyConfig, cfg); err != nil {
		t.Fatal(err)
	}
	deleted := make(chan error, 1)
	go func() {
		n, err := clients.Bulk(ctx, []int64{user.ID}, domain.BulkDelete, domain.BulkOpt{})
		if err == nil && n != 1 {
			err = fmt.Errorf("bulk delete removed %d clients", n)
		}
		deleted <- err
	}()
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for {
		var blocked bool
		if err := h.st.DB.QueryRowContext(ctx, "SELECT EXISTS (SELECT 1 FROM pg_stat_activity WHERE $1 = ANY(pg_blocking_pids(pid)))", writer).Scan(&blocked); err != nil {
			t.Fatalf("wait for bulk delete to reach settings: %v", err)
		}
		if blocked {
			break
		}
		select {
		case err := <-deleted:
			t.Fatalf("bulk delete finished before reaching the concurrent settings write: %v", err)
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-tick.C:
		}
	}
	if err := save.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := <-deleted; err != nil {
		t.Fatal(err)
	}
	got, err := filters.Load(ctx, set)
	if err != nil || !slices.Equal(got.Egress.Domains, []string{"new.example"}) || len(got.Egress.Sites) != 0 {
		t.Fatalf("filter save lost or client rules remain: %+v, %v", got.Egress, err)
	}
}
