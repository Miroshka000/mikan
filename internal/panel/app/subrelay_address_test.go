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

// menuBot is a Bot API that keeps the chats' Mini App button, starting at initial; while
// refuse is set it does not let the button change. It returns its URL and the button.
func menuBot(t *testing.T, initial string, refuse *atomic.Bool) (string, *atomic.Value) {
	t.Helper()
	var menu atomic.Value
	menu.Store(initial)
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
			if refuse != nil && refuse.Load() {
				json.NewEncoder(w).Encode(map[string]any{"ok": false, "error_code": 400, "description": "Bad Request: try again later"})
				return
			}
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
	t.Cleanup(telegram.Close)
	return telegram.URL, &menu
}

// menuPanel is a panel with its bot on menuBot at api, the address it has followed already:
// sub.example on the panel's port 2053.
func menuPanel(t *testing.T, api string) (*harness, *settings.Settings) {
	t.Helper()
	h := newHarness(t, func(o *Options) { o.TelegramAPI = api })
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
	t.Cleanup(func() { cancel(); <-done })
	for deadline := time.Now().Add(5 * time.Second); h.p.Gateway.Client() == nil; time.Sleep(10 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("gateway did not start")
		}
	}
	return h, set
}

func TestAddressFollowerMovesMenuOnPortAndExternalModeChanges(t *testing.T) {
	api, menu := menuBot(t, "https://sub.example:2053/"+subPath+"/tg?start=1", nil)
	h, set := menuPanel(t, api)
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

// A move Telegram refuses is not lost once the address is followed: a later look at the
// address tries it again, and a further change starts from where the button still is.
func TestAddressFollowerRetriesRefusedMenuMove(t *testing.T) {
	var refuse atomic.Bool
	old := "https://sub.example:2053/" + subPath + "/tg?start=1"
	api, menu := menuBot(t, old, &refuse)
	h, set := menuPanel(t, api)
	check := func(want, why string) {
		t.Helper()
		if err := h.p.address.Check(t.Context()); err != nil {
			t.Fatal(err)
		}
		if got := menu.Load().(string); got != want {
			t.Fatalf("%s: menu=%s want=%s", why, got, want)
		}
	}
	refuse.Store(true)
	if err := settings.Set(t.Context(), set, settings.KeySubPort, 2054); err != nil {
		t.Fatal(err)
	}
	check(old, "refused move")
	refuse.Store(false)
	check(old, "retried before its time")
	h.advance(menuRetryEvery)
	check("https://sub.example:2054/"+subPath+"/tg?start=1", "retry")

	refuse.Store(true)
	if err := settings.Set(t.Context(), set, settings.KeySubPort, 2055); err != nil {
		t.Fatal(err)
	}
	check("https://sub.example:2054/"+subPath+"/tg?start=1", "second refused move")
	refuse.Store(false)
	if err := settings.Set(t.Context(), set, settings.KeySubExternal, true); err != nil {
		t.Fatal(err)
	}
	check("https://sub.example/"+subPath+"/tg?start=1", "change while a move waits")
	h.advance(menuRetryEvery)
	check("https://sub.example/"+subPath+"/tg?start=1", "finished move retried")
}
