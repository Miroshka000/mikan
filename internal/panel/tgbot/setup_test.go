package tgbot

import (
	"encoding/json"
	"mikan/internal/tgapi"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRemoteMenuFollowsAddressAndPreservesItDuringOutage(t *testing.T) {
	url := "https://panel.example:2053/sub/tg"
	down := false
	panel := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if down {
			http.Error(w, "offline", 503)
			return
		}
		var value any = Config{Lang: "ru", MiniApp: true}
		if strings.HasSuffix(r.URL.Path, "MiniAppURL") {
			value = url
		}
		json.NewEncoder(w).Encode(map[string]any{"results": []any{value}})
	}))
	defer panel.Close()
	var menus []string
	telegram := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "setChatMenuButton") {
			var in struct {
				Menu struct {
					WebApp struct {
						URL string `json:"url"`
					} `json:"web_app"`
				} `json:"menu_button"`
			}
			json.NewDecoder(r.Body).Decode(&in)
			menus = append(menus, in.Menu.WebApp.URL)
		}
		json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": true})
	}))
	defer telegram.Close()
	b := New(Deps{Backend: NewRemote(panel.URL, "key", nil), Client: tgapi.NewClient(telegram.URL, "token", nil)})
	if err := b.Started(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := b.RefreshSetup(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(menus) != 1 || menus[0] != url {
		t.Fatalf("initial menu %v", menus)
	}
	url = "https://edge.example/sub/tg"
	if err := b.RefreshSetup(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(menus) != 2 || menus[1] != url {
		t.Fatalf("stale menu %v", menus)
	}
	down = true
	if err := b.RefreshSetup(t.Context()); err == nil || len(menus) != 2 {
		t.Fatal("panel failure replaced valid Telegram menu")
	}
}
