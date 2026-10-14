package main

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"mikan/internal/panel/addons"
	"mikan/internal/panel/addons/panelapi"
	"mikan/internal/panel/domain"
	"mikan/internal/panel/events"
	"mikan/internal/panel/settings"
	"mikan/internal/panel/store/db"
	"mikan/internal/panel/store/storetest"
	"mikan/internal/panel/tgate"
	"mikan/internal/panel/tgbot"
)

// telegram is Telegram for the panel's gateway: it hands out the updates pushed into it
// and records what the bot sent.
type telegram struct {
	mu      sync.Mutex
	updates []map[string]any
	seq     int64
	sent    []string // method: text
}

func (f *telegram) push(chat int64, text string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.seq++
	f.updates = append(f.updates, map[string]any{"update_id": f.seq, "message": map[string]any{
		"message_id": f.seq, "date": 1, "text": text,
		"chat": map[string]any{"id": chat, "type": "private"}, "from": map[string]any{"id": chat, "first_name": "Anna"}}})
}

func (f *telegram) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	method := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
	var body map[string]any
	_ = json.NewDecoder(r.Body).Decode(&body)
	ok := func(result any) { _ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": result}) }
	switch method {
	case "getMe":
		ok(map[string]any{"id": 1, "is_bot": true, "first_name": "Mikan", "username": "mikan_test_bot"})
	case "getUpdates":
		off, _ := body["offset"].(float64)
		for range 20 {
			f.mu.Lock()
			var ups []map[string]any
			for _, u := range f.updates {
				if u["update_id"].(int64) >= int64(off) {
					ups = append(ups, u)
				}
			}
			f.mu.Unlock()
			if len(ups) > 0 {
				ok(ups)
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
		ok([]any{})
	default:
		text, _ := body["text"].(string)
		f.mu.Lock()
		f.sent = append(f.sent, method+": "+text)
		f.mu.Unlock()
		ok(map[string]any{"message_id": 99, "date": 1, "chat": map[string]any{"id": body["chat_id"], "type": "private"}})
	}
}

func (f *telegram) said() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.sent...)
}

// The official bot end to end: Telegram → the panel's gateway → the buffer → the Bot API
// proxy → this addon's screens, which ask the panel over the Panel API → the proxy →
// Telegram; and a payment message told through the feed of events.
func TestAddonEndToEnd(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	st, err := storetest.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	now := func() time.Time { return time.Now() }
	if err := domain.Seed(ctx, st, now()); err != nil {
		t.Fatal(err)
	}
	set := settings.New(st.Q)
	tg := &telegram{}
	tgSrv := httptest.NewServer(tg)
	defer tgSrv.Close()
	for k, v := range map[string]any{tgate.KeyEnabled: true, tgate.KeyToken: "123:REAL", "brand": "Mikan VPN", panelapi.SlotKey: "telegram"} {
		if err := settings.Set(ctx, set, k, v); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.Q.UpsertAddonGrant(ctx, db.UpsertAddonGrantParams{AddonID: "telegram", Version: "1", Permissions: `["bot","users:read","users:write","billing:invoice","events","kv"]`, ApprovedAt: 1}); err != nil {
		t.Fatal(err)
	}
	token := strings.Repeat("t", 48)
	dir := t.TempDir()
	state, _ := json.Marshal(addons.State{Adapters: map[string]addons.Installed{"telegram": {Protocol: 2, Kind: addons.KindBot, Mode: addons.ModeClient, PanelToken: token, Token: "x", Status: "running"}}})
	_ = os.MkdirAll(filepath.Join(dir, "addons"), 0o755)
	_ = os.WriteFile(filepath.Join(dir, "addons", "state.json"), state, 0o600)

	gate := tgate.New(tgate.Deps{Store: st, Settings: set, API: tgSrv.URL, Log: log, Now: now})
	subBase := func(context.Context) string { return "https://vpn.example.com:21355/sub" }
	backend := &tgbot.Local{Store: st, Settings: set, Gateway: gate, SubBase: subBase, Now: now, MiniApp: func() bool { return false }}
	hub := events.NewHub()
	api := panelapi.New(panelapi.Deps{Addons: addons.New(dir, "", "dev", log, now), Store: st, Hub: hub, Log: log, Now: now,
		Telegram: gate, Settings: set, SubBase: subBase, Bot: tgbot.RPCHandler(backend)})
	gate.SetHandler(api.BotSlot())
	apiSrv := httptest.NewServer(api)
	defer apiSrv.Close()

	a := newAddon(apiSrv.URL+"/panel/v2", token, apiSrv.URL, "0:"+token, log)
	a.updatesWait = time.Second
	done := make(chan struct{})
	go func() { a.Run(ctx); close(done) }()
	// The gateway comes up after the addon, as after a restart of the panel: the addon sets up
	// the chats once the proxy reaches Telegram.
	time.Sleep(300 * time.Millisecond)
	go gate.Run(ctx)

	until := func(what string, ok func([]string) bool) []string {
		t.Helper()
		for start := time.Now(); time.Since(start) < 20*time.Second; time.Sleep(50 * time.Millisecond) {
			if s := tg.said(); ok(s) {
				return s
			}
		}
		t.Fatalf("%s: Telegram got %q", what, tg.said())
		return nil
	}
	// The addon sets up the chats through the proxy.
	until("the commands", func(s []string) bool { return strings.Contains(strings.Join(s, "\n"), "setMyCommands") })
	tg.push(555, "/start")
	said := until("the welcome", func(s []string) bool {
		for _, m := range s {
			if strings.HasPrefix(m, "sendMessage: ") && strings.Contains(m, "Mikan VPN") {
				return true
			}
		}
		return false
	})
	_ = said

	// A payment message comes as an event; the bot tells the buyer, once: the deprecated bot.*
	// twin of the same payment, which the panel still writes, is not read.
	u, err := domain.NewUsers(st, domain.NewPool(st, now), noChanges{}, now).Create(ctx, domain.CreateInput{Name: "Anna", TariffID: firstTariff(t, st.Q)})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Q.LinkTg(ctx, db.LinkTgParams{UserID: u.ID, TgID: 555, CreatedAt: now().Unix()}); err != nil {
		t.Fatal(err)
	}
	emit := func(typ string, data any) {
		t.Helper()
		if err := st.Tx(ctx, func(q *db.Queries) error { return events.Emit(ctx, q, now(), typ, u.ID, 555, data, typ+":1") }); err != nil {
			t.Fatal(err)
		}
		hub.Kick()
	}
	pay := db.Payment{ID: 1, TgID: 555, Kind: "new", Provider: "stars", Amount: 100, Currency: "XTR", TariffName: "Month", Status: "applied"}
	told := func(s []string) (n int) {
		for _, m := range s {
			if strings.HasPrefix(m, "sendMessage: ") && strings.Contains(m, "готова") {
				n++
			}
		}
		return n
	}
	emit(events.BotPaid, tgbot.PaymentMessage{Payment: pay, User: u, Created: true})
	emit(events.PaymentPaid, events.Payment{PaymentID: 1, Kind: "new", Provider: "stars", Amount: 100, Currency: "XTR", TariffName: "Month", Created: true})
	until("the payment message", func(s []string) bool { return told(s) > 0 })
	time.Sleep(500 * time.Millisecond)
	if n := told(tg.said()); n != 1 {
		t.Fatalf("the payment was told %d times: %q", n, tg.said())
	}

	cancel()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("the addon did not stop")
	}
}

func firstTariff(t *testing.T, st interface {
	ListTariffs(context.Context) ([]db.Tariff, error)
}) int64 {
	t.Helper()
	ts, err := st.ListTariffs(context.Background())
	if err != nil || len(ts) == 0 {
		t.Fatalf("tariffs: %v", err)
	}
	return ts[0].ID
}

type noChanges struct{}

func (noChanges) PoliciesChanged() {}
func (noChanges) SlotsChanged()    {}
