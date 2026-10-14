package app

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"mikan/internal/panel/addons"
	"mikan/internal/panel/addons/panelapi"
	"mikan/internal/panel/settings"
	"mikan/internal/panel/store/db"
	"mikan/internal/panel/tgate"
)

// A panel whose bot is on installs the official bot addon from the catalog by itself, with
// the catalog's permissions, and hands it the bot once it runs; with the bot off, a host
// before protocol 2 or another addon holding the bot it does nothing.
func TestOfficialBotInstallsItself(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	entry := addons.Entry{ID: OfficialBot, Version: "0.5.1.0-rc.6", Protocol: 2, Kind: addons.KindBot, Mode: addons.ModeClient,
		Permissions: []string{"bot", "events", "kv"}, Image: "ghcr.io/getmikan/telegram", Digest: "sha256:" + strings.Repeat("b", 64)}
	index, _ := json.Marshal(addons.Catalog{Version: 1, Adapters: []addons.Entry{entry}})
	catalog := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/index.json":
			_, _ = w.Write(index)
		case "/index.json.sig":
			_, _ = io.WriteString(w, base64.StdEncoding.EncodeToString(ed25519.Sign(priv, index)))
		}
	}))
	t.Cleanup(catalog.Close)
	dir := t.TempDir()
	h := newHarness(t, func(o *Options) { o.DataDir, o.AddonsCatalog = dir, catalog.URL+"/index.json" })
	h.p.Addons.SetKey(pub)
	ctx := context.Background()
	host := func(st addons.State) {
		data, _ := json.Marshal(st)
		_ = os.MkdirAll(filepath.Join(dir, "addons"), 0o755)
		if err := os.WriteFile(filepath.Join(dir, "addons", "state.json"), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	b := &botAddon{p: h.p}
	queued := func() bool { return len(h.p.Addons.Pending()) > 0 }

	host(addons.State{Adapters: map[string]addons.Installed{}, HostVersion: "0.5.1.0"})
	b.check(ctx)
	if queued() {
		t.Fatal("installed with the bot off")
	}
	if err := settings.Set(ctx, h.p.Settings, tgate.KeyEnabled, true); err != nil {
		t.Fatal(err)
	}
	host(addons.State{Adapters: map[string]addons.Installed{}})
	b.check(ctx)
	if queued() {
		t.Fatal("asked a host before protocol 2")
	}
	host(addons.State{Adapters: map[string]addons.Installed{}, HostVersion: "0.5.1.0"})
	b.check(ctx)
	if q := h.p.Addons.Pending(); len(q) != 1 || q[0].ID != OfficialBot || q[0].Action != "install" {
		t.Fatalf("not asked: %+v", q)
	}
	g, err := h.st.Q.GetAddonGrant(ctx, OfficialBot)
	if err != nil || g.Permissions != `["bot","events","kv"]` {
		t.Fatalf("grant: %+v %v", g, err)
	}
	b.check(ctx) // a second round asks nothing more
	if len(h.p.Addons.Pending()) != 1 {
		t.Fatal("asked twice")
	}

	// The host ran it: the bot is the addon's now.
	_ = os.RemoveAll(filepath.Join(dir, "addons", "requests"))
	host(addons.State{HostVersion: "0.5.1.0", Adapters: map[string]addons.Installed{OfficialBot: {Protocol: 2, Kind: addons.KindBot, Status: "running", PanelToken: strings.Repeat("p", 48)}}})
	b.check(ctx)
	if h.p.AddonsAPI.Slot(ctx) != OfficialBot {
		t.Fatal("the bot was not handed over")
	}

	// The admin handed it to another addon: left alone.
	if err := h.st.Q.UpsertAddonGrant(ctx, db.UpsertAddonGrantParams{AddonID: "other", Version: "1", Permissions: `["bot"]`, ApprovedAt: 1}); err != nil {
		t.Fatal(err)
	}
	if err := settings.Set(ctx, h.p.Settings, panelapi.SlotKey, "other"); err != nil {
		t.Fatal(err)
	}
	b.check(ctx)
	if id, _, _ := settings.Get[string](ctx, h.p.Settings, panelapi.SlotKey); id != "other" {
		t.Fatalf("the admin's choice was overwritten: %q", id)
	}
}

// A bot addon the admin removed stays removed: the slot is freed and the panel does not put
// the official bot back by itself until the admin turns the bot on again or installs a bot
// from the catalog.
func TestRemovedBotStaysRemoved(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	digest := "sha256:" + strings.Repeat("b", 64)
	entry := addons.Entry{ID: OfficialBot, Version: "1.0.0", Protocol: 2, Kind: addons.KindBot, Mode: addons.ModeClient,
		Permissions: []string{"bot", "kv"}, Image: "ghcr.io/getmikan/telegram", Digest: digest}
	index, _ := json.Marshal(addons.Catalog{Version: 1, Adapters: []addons.Entry{entry}})
	catalog := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/index.json":
			_, _ = w.Write(index)
		case "/index.json.sig":
			_, _ = io.WriteString(w, base64.StdEncoding.EncodeToString(ed25519.Sign(priv, index)))
		}
	}))
	t.Cleanup(catalog.Close)
	// The gateway may start polling once the bot is on: a Telegram that knows nothing.
	telegram := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"ok":false,"error_code":404,"description":"Not Found"}`)
	}))
	t.Cleanup(telegram.Close)
	dir := t.TempDir()
	h := newHarness(t, func(o *Options) {
		o.DataDir, o.AddonsCatalog, o.TelegramAPI = dir, catalog.URL+"/index.json", telegram.URL
	})
	h.p.Addons.SetKey(pub)
	ctx := context.Background()
	if resp, _ := h.login(password, ""); resp.StatusCode != http.StatusOK {
		t.Fatal("login")
	}
	csrf := map[string]string{"X-CSRF-Token": h.csrf}
	host := func(st addons.State) {
		t.Helper()
		data, _ := json.Marshal(st)
		_ = os.RemoveAll(filepath.Join(dir, "addons", "requests"))
		_ = os.MkdirAll(filepath.Join(dir, "addons"), 0o755)
		if err := os.WriteFile(filepath.Join(dir, "addons", "state.json"), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	installs := func() bool {
		return slices.ContainsFunc(h.p.Addons.Pending(), func(r addons.Request) bool { return r.Action == "install" && r.ID == OfficialBot })
	}
	slot := func() (string, bool) {
		t.Helper()
		resp, body := h.do(http.MethodGet, "/"+adminPath+"/api/v1/telegram/bot-addon", nil, nil)
		var v struct {
			Addon string `json:"addon"`
			Auto  bool   `json:"auto"`
		}
		if resp.StatusCode != http.StatusOK || json.Unmarshal(body, &v) != nil {
			t.Fatalf("bot addon: %d %s", resp.StatusCode, body)
		}
		return v.Addon, v.Auto
	}
	for k, v := range map[string]any{tgate.KeyToken: tgToken, tgate.KeyEnabled: true, panelapi.SlotKey: OfficialBot} {
		if err := settings.Set(ctx, h.p.Settings, k, v); err != nil {
			t.Fatal(err)
		}
	}
	if err := h.st.Q.UpsertAddonGrant(ctx, db.UpsertAddonGrantParams{AddonID: OfficialBot, Version: "1.0.0", Permissions: `["bot","kv"]`, ApprovedAt: 1}); err != nil {
		t.Fatal(err)
	}
	host(addons.State{HostVersion: "0.5.1.0", Adapters: map[string]addons.Installed{OfficialBot: {Version: "1.0.0", Digest: digest,
		Protocol: 2, Kind: addons.KindBot, Status: "running", PanelToken: strings.Repeat("p", 48)}}})
	if id, auto := slot(); id != OfficialBot || !auto {
		t.Fatalf("before: %q %v", id, auto)
	}

	market := "/" + adminPath + "/api/v1/marketplace/" + OfficialBot
	if resp, body := h.do(http.MethodPost, market+"/remove", nil, csrf); resp.StatusCode/100 != 2 {
		t.Fatalf("remove: %d %s", resp.StatusCode, body)
	}
	if id, auto := slot(); id != "" || auto {
		t.Fatalf("after the removal: %q %v", id, auto)
	}
	host(addons.State{HostVersion: "0.5.1.0", Adapters: map[string]addons.Installed{}})
	b := &botAddon{p: h.p}
	b.check(ctx)
	if installs() {
		t.Fatal("the removed bot came back by itself")
	}

	// Turning the bot off and on again is asking for it.
	tg := "/" + adminPath + "/api/v1/telegram"
	for _, on := range []bool{false, true} {
		if resp, body := h.do(http.MethodPatch, tg, map[string]any{"enabled": on}, csrf); resp.StatusCode != http.StatusOK {
			t.Fatalf("enabled %v: %d %s", on, resp.StatusCode, body)
		}
	}
	if _, auto := slot(); !auto {
		t.Fatal("turning the bot on did not bring the official bot back")
	}
	b.check(ctx)
	if !installs() {
		t.Fatalf("not asked again: %+v", h.p.Addons.Pending())
	}

	// So is installing a bot from the catalog.
	host(addons.State{HostVersion: "0.5.1.0", Adapters: map[string]addons.Installed{}})
	if err := settings.Set(ctx, h.p.Settings, panelapi.AutoBotKey, false); err != nil {
		t.Fatal(err)
	}
	if resp, body := h.do(http.MethodPost, market+"/install", map[string]any{"permissions": []string{"bot", "kv"}}, csrf); resp.StatusCode/100 != 2 {
		t.Fatalf("install: %d %s", resp.StatusCode, body)
	}
	if _, auto := slot(); !auto {
		t.Fatal("a bot from the catalog left the panel off")
	}
}
