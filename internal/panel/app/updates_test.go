package app

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"mikan/internal/panel/updates"
	"mikan/internal/release"
)

// The admin sees a new release, switches automatic updates for the host updater and asks
// it to update now; the panel itself never touches Docker.
func TestUpdatesForTheHost(t *testing.T) {
	dir := t.TempDir()
	latest := "0.0.1"
	var (
		mu       sync.Mutex
		channels []string
	)
	lastChannel := func() string {
		mu.Lock()
		defer mu.Unlock()
		return channels[len(channels)-1]
	}
	h := newHarness(t, func(o *Options) {
		o.DataDir = dir
		o.Releases = func(_ context.Context, q updates.Query) (updates.Found, error) {
			mu.Lock()
			channels = append(channels, q.Channel)
			mu.Unlock()
			return updates.Found{Manifest: release.Manifest{Version: latest, Published: time.Unix(1_800_000_000, 0), Image: "ghcr.io/o0hhs/mikan",
				Digest: "sha256:" + strings.Repeat("a", 64), Notes: map[string]string{"en": "- faster", "ru": "- быстрее"}}}, nil
		}
	})
	if resp, _ := h.login(password, ""); resp.StatusCode != http.StatusOK {
		t.Fatalf("login: %d", resp.StatusCode)
	}
	csrf := map[string]string{"X-CSRF-Token": h.csrf}
	api := "/" + adminPath + "/api/v1/updates"
	type view struct {
		Current     string            `json:"current"`
		Latest      string            `json:"latest"`
		Available   bool              `json:"available"`
		Auto        bool              `json:"auto"`
		Channel     string            `json:"channel"`
		Notes       map[string]string `json:"notes"`
		RequestedAt int64             `json:"requested_at"`
		Host        *struct {
			State string `json:"state"`
		} `json:"host"`
	}
	read := func(resp *http.Response, body []byte) view {
		t.Helper()
		var v view
		if resp.StatusCode/100 != 2 || json.Unmarshal(body, &v) != nil {
			t.Fatalf("updates: %d %s", resp.StatusCode, body)
		}
		return v
	}
	if v := read(h.do(http.MethodGet, api, nil, nil)); v.Current != "test" || v.Latest != "" || v.Available || v.Auto || v.Channel != "stable" {
		t.Fatalf("before a check: %+v", v)
	}
	// A development build ("test") is older than any release.
	if v := read(h.do(http.MethodPost, api+"/check", nil, csrf)); v.Latest != "0.0.1" || !v.Available || v.Notes["ru"] != "- быстрее" {
		t.Fatalf("after a check: %+v", v)
	}
	if v := read(h.do(http.MethodPatch, api, map[string]any{"auto": true}, csrf)); !v.Auto {
		t.Fatalf("auto: %+v", v)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "update", "policy.json")); string(b) != `{"auto":true,"channel":"stable"}` {
		t.Fatalf("the host reads the switch from policy.json: %s", b)
	}
	// The channel goes to the host beside the switch, and the panel looks again on it.
	if v := read(h.do(http.MethodPatch, api, map[string]any{"channel": "beta"}, csrf)); !v.Auto || v.Channel != "beta" || lastChannel() != "beta" {
		t.Fatalf("channel: %+v, checked on %q", v, lastChannel())
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "update", "policy.json")); string(b) != `{"auto":true,"channel":"beta"}` {
		t.Fatalf("the host reads the channel from policy.json: %s", b)
	}
	if resp, body := h.do(http.MethodPatch, api, map[string]any{"channel": "nightly"}, csrf); resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("an unknown channel: %d %s", resp.StatusCode, body)
	}
	if v := read(h.do(http.MethodPatch, api, map[string]any{"channel": "stable"}, csrf)); v.Channel != "stable" || !v.Available {
		t.Fatalf("back to stable: %+v", v)
	}
	if v := read(h.do(http.MethodPost, api+"/request", nil, csrf)); v.RequestedAt == 0 {
		t.Fatalf("request: %+v", v)
	}
	if _, err := os.Stat(filepath.Join(dir, "update", "request")); err != nil {
		t.Fatalf("the host's path unit waits for the request file: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "update", "status.json"), []byte(`{"state":"ok","version":"0.0.1","from":"test"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if v := read(h.do(http.MethodGet, api, nil, nil)); v.Host == nil || v.Host.State != "ok" {
		t.Fatalf("the host's report: %+v", v)
	}
	// Nothing newer: no request.
	latest = "dev"
	read(h.do(http.MethodPost, api+"/check", nil, csrf))
	if resp, body := h.do(http.MethodPost, api+"/request", nil, csrf); resp.StatusCode != http.StatusConflict || !strings.Contains(string(body), "update_none") {
		t.Fatalf("no update to request: %d %s", resp.StatusCode, body)
	}
	if resp, _ := h.do(http.MethodPost, api+"/request", nil, nil); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("without CSRF: %d", resp.StatusCode)
	}
}

// The goals of the release index reach the updates card, and the admin can hide them.
func TestUpdatesGoals(t *testing.T) {
	h := newHarness(t, func(o *Options) {
		o.DataDir = t.TempDir()
		o.Releases = func(context.Context, updates.Query) (updates.Found, error) {
			return updates.Found{Manifest: release.Manifest{Version: "0.0.1", Published: time.Unix(1_800_000_000, 0), Image: "ghcr.io/o0hhs/mikan", Digest: "sha256:" + strings.Repeat("a", 64)},
				Goals: release.Goals{Donate: "https://web.tribute.tg/d/REA", Items: []release.Goal{{ID: "utm", Title: map[string]string{"ru": "Метки"}, Target: 150, Raised: 40, Currency: "USD", Status: release.GoalOpen}}}}, nil
		}
	})
	if resp, _ := h.login(password, ""); resp.StatusCode != http.StatusOK {
		t.Fatalf("login: %d", resp.StatusCode)
	}
	csrf := map[string]string{"X-CSRF-Token": h.csrf}
	api := "/" + adminPath + "/api/v1/updates"
	type view struct {
		Goals []struct {
			ID     string `json:"id"`
			Raised int    `json:"raised"`
		} `json:"goals"`
		Donate    string `json:"donate"`
		ShowGoals bool   `json:"show_goals"`
	}
	read := func(resp *http.Response, body []byte) view {
		t.Helper()
		var v view
		if resp.StatusCode/100 != 2 || json.Unmarshal(body, &v) != nil {
			t.Fatalf("updates: %d %s", resp.StatusCode, body)
		}
		return v
	}
	if v := read(h.do(http.MethodGet, api, nil, nil)); v.Goals == nil || len(v.Goals) != 0 || !v.ShowGoals {
		t.Fatalf("before a check: %+v", v)
	}
	if v := read(h.do(http.MethodPost, api+"/check", nil, csrf)); len(v.Goals) != 1 || v.Goals[0].Raised != 40 || v.Donate != "https://web.tribute.tg/d/REA" {
		t.Fatalf("after a check: %+v", v)
	}
	if v := read(h.do(http.MethodPatch, api, map[string]any{"show_goals": false}, csrf)); v.ShowGoals {
		t.Fatalf("hidden: %+v", v)
	}
	if v := read(h.do(http.MethodGet, api, nil, nil)); v.ShowGoals {
		t.Fatalf("the switch is kept: %+v", v)
	}
}
