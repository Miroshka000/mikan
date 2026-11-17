package app

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"mikan/internal/panel/settings"
	"mikan/internal/panel/tgate"
)

func TestAddressFollowerMovesMenuOnPortAndExternalModeChanges(t *testing.T) {
	var menu atomic.Value
	menu.Store("https://sub.example:2053/" + subPath + "/tg?start=1")
	telegram := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var result any = true
		switch r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:] {
		case "getMe":
			result = map[string]any{"id": 1, "is_bot": true, "username": "test_bot", "first_name": "Test"}
		case "getUpdates":
			result = []any{}
			time.Sleep(10 * time.Millisecond)
		case "getChatMenuButton":
			result = map[string]any{"type": "web_app", "text": "Open", "web_app": map[string]any{"url": menu.Load().(string)}}
		case "setChatMenuButton":
			var in struct {
				Menu struct {
					App struct {
						URL string `json:"url"`
					} `json:"web_app"`
				} `json:"menu_button"`
			}
			json.NewDecoder(r.Body).Decode(&in)
			if in.Menu.App.URL != "" {
				menu.Store(in.Menu.App.URL)
			}
		}
		json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": result})
	}))
	defer telegram.Close()
	h := newHarness(t, func(o *Options) { o.TelegramAPI = telegram.URL })
	set := settings.New(h.st.Q)
	for key, value := range map[string]any{settings.KeyDomain: "panel.example", settings.KeySubDomain: "sub.example", settings.KeyPanelPort: 2053, tgate.KeyEnabled: true, tgate.KeyToken: "123:test"} {
		if err := settings.Set(t.Context(), set, key, value); err != nil {
			t.Fatal(err)
		}
	}
	old, err := set.Address(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := settings.Set(t.Context(), set, settings.KeyAddressApplied, old); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() { h.p.Gateway.Run(ctx); close(done) }()
	defer func() { cancel(); <-done }()
	for deadline := time.Now().Add(5 * time.Second); h.p.Gateway.Client() == nil; time.Sleep(10 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("gateway did not start")
		}
	}
	change := func(key string, value any, want string) {
		t.Helper()
		if err := settings.Set(t.Context(), set, key, value); err != nil {
			t.Fatal(err)
		}
		if err := h.p.address.Check(t.Context()); err != nil {
			t.Fatal(err)
		}
		if got := menu.Load().(string); got != want {
			t.Fatalf("%s: menu=%s want=%s", key, got, want)
		}
	}
	change(settings.KeySubPort, 2054, "https://sub.example:2054/"+subPath+"/tg?start=1")
	change(settings.KeySubExternal, true, "https://sub.example/"+subPath+"/tg?start=1")
	change(settings.KeySubExternal, false, "https://sub.example:2054/"+subPath+"/tg?start=1")
}
