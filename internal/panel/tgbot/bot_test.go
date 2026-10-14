package tgbot

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"mikan/internal/panel/billing"
	"mikan/internal/panel/campaigns"
	"mikan/internal/panel/domain"
	"mikan/internal/panel/events"
	"mikan/internal/panel/referral"
	"mikan/internal/panel/settings"
	"mikan/internal/panel/store"
	"mikan/internal/panel/store/db"
	"mikan/internal/panel/store/storetest"
	"mikan/internal/panel/tgate"
)

// fakeTelegram is the Bot API for tests: it hands out queued updates and records calls.
type fakeTelegram struct {
	mu      sync.Mutex
	updates []tgate.Update // not yet confirmed by an offset past them, as at Telegram
	seq     int64
	offsets []int64 // the offset of each getUpdates
	calls   []call
	nextID  int64
	blocked map[int64]bool // chats that blocked the bot
	broken  map[string]bool
	stall   map[string]time.Duration
	// conflict: another program polls the token, so getUpdates gets Telegram's 409
	conflict bool
	// The channel: who is in it (account → status; the bot is its admin unless notAdmin),
	// and notAdmin, the bot is not, so getChatMember is refused.
	members  map[int64]string
	notAdmin bool
}

// join puts an account in the channel ("" takes it out).
func (f *fakeTelegram) join(id int64, status string) {
	f.mu.Lock()
	if f.members == nil {
		f.members = map[int64]string{}
	}
	f.members[id] = status
	f.mu.Unlock()
}

// set breaks (answers with something that is no Bot API reply) or fixes a method.
func (f *fakeTelegram) set(method string, broken bool) {
	f.mu.Lock()
	if f.broken == nil {
		f.broken = map[string]bool{}
	}
	f.broken[method] = broken
	f.mu.Unlock()
}

type call struct {
	method string
	body   map[string]any
}

func (f *fakeTelegram) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	method := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
	var body map[string]any
	_ = json.NewDecoder(r.Body).Decode(&body)
	ok := func(result any) { _ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": result}) }
	switch method {
	case "getMe":
		ok(tgate.User{ID: 1, IsBot: true, FirstName: "Mikan", Username: "mikan_test_bot"})
		return
	case "getUpdates":
		f.mu.Lock()
		conflict := f.conflict
		f.mu.Unlock()
		if conflict {
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "error_code": 409,
				"description": "Conflict: terminated by other getUpdates request; make sure that only one bot instance is running"})
			return
		}
		off, _ := body["offset"].(float64)
		f.mu.Lock()
		f.offsets = append(f.offsets, int64(off))
		f.mu.Unlock()
		deadline := time.Now().Add(200 * time.Millisecond)
		for time.Now().Before(deadline) {
			f.mu.Lock()
			keep := f.updates[:0]
			for _, u := range f.updates {
				if u.UpdateID >= int64(off) {
					keep = append(keep, u)
				}
			}
			f.updates = keep
			ups := append([]tgate.Update(nil), keep...)
			f.mu.Unlock()
			if len(ups) > 0 {
				ok(ups)
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
		ok([]tgate.Update{})
		return
	}
	f.mu.Lock()
	f.calls = append(f.calls, call{method, body})
	chat, _ := body["chat_id"].(float64)
	blocked := f.blocked[int64(chat)]
	f.nextID++
	id := f.nextID
	broken, stall := f.broken[method], f.stall[method]
	f.mu.Unlock()
	if stall > 0 {
		select {
		case <-time.After(stall):
		case <-r.Context().Done():
		}
	}
	if broken {
		_, _ = w.Write([]byte("<html>bad gateway</html>"))
		return
	}
	if blocked {
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "error_code": 403, "description": "Forbidden: bot was blocked by the user"})
		return
	}
	switch method {
	case "createInvoiceLink":
		ok("https://t.me/$inv" + strconv.FormatInt(id, 10))
	case "sendMessage":
		ok(tgate.Message{MessageID: 1000 + id, Chat: tgate.Chat{ID: int64(chat), Type: "private"}})
	case "getChatMember":
		user, _ := body["user_id"].(float64)
		f.mu.Lock()
		status, notAdmin := f.members[int64(user)], f.notAdmin
		f.mu.Unlock()
		switch {
		case notAdmin:
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "error_code": 400, "description": "Bad Request: member list is inaccessible"})
			return
		case int64(user) == 1:
			status = "administrator"
		case status == "":
			status = "left"
		}
		ok(tgate.ChatMember{Status: status, IsMember: status == "restricted"})
	default:
		ok(true)
	}
}

func (f *fakeTelegram) push(u tgate.Update) {
	f.mu.Lock()
	f.seq++
	u.UpdateID = 1000 + f.seq
	f.updates = append(f.updates, u)
	f.mu.Unlock()
}

// wait returns the calls made after the n-th, once one of them is method.
func (f *fakeTelegram) wait(t *testing.T, from int, method string) []call {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		f.mu.Lock()
		got := append([]call(nil), f.calls[min(from, len(f.calls)):]...)
		f.mu.Unlock()
		for _, c := range got {
			if c.method == method {
				time.Sleep(50 * time.Millisecond) // let the rest of the handler's calls land
				f.mu.Lock()
				got = append([]call(nil), f.calls[min(from, len(f.calls)):]...)
				f.mu.Unlock()
				return got
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("no %s after call %d: %+v", method, from, f.calls)
	return nil
}

// until waits for the calls after the n-th to satisfy ok.
func (f *fakeTelegram) until(t *testing.T, from int, ok func([]call) bool) []call {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		f.mu.Lock()
		got := append([]call(nil), f.calls[min(from, len(f.calls)):]...)
		f.mu.Unlock()
		if ok(got) {
			return got
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("not reached after call %d: %+v", from, f.calls[from:])
	return nil
}

func (f *fakeTelegram) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

func find(calls []call, method string) (call, bool) {
	for _, c := range calls {
		if c.method == method {
			return c, true
		}
	}
	return call{}, false
}

func text(c call) string { s, _ := c.body["text"].(string); return s }

// buttons flattens a keyboard: text → callback data, url or web app url.
func buttons(c call) map[string]string {
	out := map[string]string{}
	kb, _ := c.body["reply_markup"].(map[string]any)
	rows, _ := kb["inline_keyboard"].([]any)
	for _, r := range rows {
		for _, b := range r.([]any) {
			m := b.(map[string]any)
			v, _ := m["callback_data"].(string)
			if u, ok := m["url"].(string); ok {
				v = u
			}
			if wa, ok := m["web_app"].(map[string]any); ok {
				v = "webapp:" + wa["url"].(string)
			}
			out[m["text"].(string)] = v
		}
	}
	return out
}

type env struct {
	// refs is the referral programme the fork's tests turned on (withReferral), for the
	// calls those tests make themselves.
	refs  *referral.Service
	t     *testing.T
	ctx   context.Context
	st    *store.Store
	set   *settings.Settings
	tg    *fakeTelegram
	gate  *tgate.Gateway
	bot   *Bot   // the addon's side: the screens, on the updates and the events
	panel *Panel // the panel's side: the notices and the broadcasts
	now   time.Time
	mu    sync.Mutex
	user  db.User
	devs  *domain.Devices
	users *domain.Users
	msgID int64
	// seen is the last event of the panel's feed the addon has been handed.
	seen int64
}

func (e *env) clock() time.Time {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.now
}

// later moves the clock past the bot's cooldown.
func (e *env) later() {
	e.mu.Lock()
	e.now = e.now.Add(2 * time.Second)
	e.mu.Unlock()
}

type noChanges struct{}

func (noChanges) PoliciesChanged() {}
func (noChanges) SlotsChanged()    {}

// wiring is what setup builds the gateway and the bot from.
type wiring struct {
	Store    *store.Store
	Settings *settings.Settings
	Devices  *domain.Devices
	Users    *domain.Users
	SubBase  func(ctx context.Context) string
	MiniApp  func() bool
	Log      *slog.Logger
	Now      func() time.Time
	Billing  *billing.Service
	// The fork's own: the referral programme and the ad campaign tags the screens read
	// through the Backend.
	ReferralProgram *referral.Service
	Campaigns       *campaigns.Service
	Limits          tgate.Limits
	// RPC: the screens see the panel through Remote over HTTP, as the bot addon does.
	RPC bool
}

// setup starts a bot and its gateway on a fake Bot API; with may change what they are
// built from before they start.
func setup(t *testing.T, with ...func(e *env, d *wiring)) *env {
	t.Helper()
	e := &env{t: t, now: time.Unix(1_800_000_000, 0), tg: &fakeTelegram{blocked: map[int64]bool{}}}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	e.ctx = ctx
	var err error
	if e.st, err = storetest.Open(ctx, t.TempDir()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { e.st.Close() })
	if err := domain.Seed(ctx, e.st, e.now); err != nil {
		t.Fatal(err)
	}
	e.set = settings.New(e.st.Q)
	for k, v := range map[string]any{tgate.KeyEnabled: true, tgate.KeyToken: "123:test", "brand": "Mikan VPN", "support_url": "https://t.me/mikan_support"} {
		if err := settings.Set(ctx, e.set, k, v); err != nil {
			t.Fatal(err)
		}
	}
	pool := domain.NewPool(e.st, e.clock)
	tariffs, _ := e.st.Q.ListTariffs(ctx)
	e.users = domain.NewUsers(e.st, pool, noChanges{}, e.clock)
	if e.user, err = e.users.Create(ctx, domain.CreateInput{Name: "Анна", TariffID: tariffs[1].ID}); err != nil {
		t.Fatal(err)
	}
	e.devs = domain.NewDevices(e.st, pool, noChanges{}, e.clock)
	srv := httptest.NewServer(e.tg)
	t.Cleanup(srv.Close)
	w := wiring{Store: e.st, Settings: e.set, Devices: e.devs, Users: e.users, Now: e.clock,
		SubBase: func(context.Context) string { return "https://vpn.example.com:21355/sub" },
		MiniApp: func() bool { return true }, Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Limits: fast}
	// Selling is off until a test turns it on, but the shop is always there.
	w.Billing = billing.New(billing.Deps{Store: e.st, Settings: e.set, Users: e.users, Log: w.Log, Now: e.clock, MaxLinks: tgate.MaxLinks})
	for _, f := range with {
		f(e, &w)
	}
	e.gate = tgate.New(tgate.Deps{Store: w.Store, Settings: w.Settings, API: srv.URL, Log: w.Log, Now: w.Now, Limits: w.Limits, Billing: w.Billing})
	local := &Local{Store: w.Store, Settings: w.Settings, Binder: w.Devices, Users: w.Users, Billing: w.Billing, Gateway: e.gate,
		SubBase: w.SubBase, MiniApp: w.MiniApp, Now: w.Now,
		ReferralProgram: w.ReferralProgram, Campaigns: w.Campaigns}
	var backend Backend = local
	if w.RPC {
		mux := http.NewServeMux()
		mux.Handle("POST /bot/{method}", RPCHandler(local))
		rpc := httptest.NewServer(mux)
		t.Cleanup(rpc.Close)
		backend = NewRemote(rpc.URL, "token", nil)
	}
	go e.gate.Run(ctx)
	deadline := time.Now().Add(3 * time.Second)
	for !e.gate.Status().Running {
		if time.Now().After(deadline) {
			t.Fatalf("the bot did not start: %+v", e.gate.Status())
		}
		time.Sleep(10 * time.Millisecond)
	}
	// The addon's screens on the gateway's client and outbox; the panel tells them its
	// events directly, and the updates come to them as the proxy hands them out.
	e.bot = New(Deps{Backend: backend, Client: e.gate.Client(), Outbox: e.gate.Outbox(), Go: e.gate.Go, Log: w.Log, Now: w.Now})
	e.panel = NewPanel(PanelDeps{Backend: local, Outbox: e.gate.Outbox, Emit: e.emit, Log: w.Log, Now: w.Now})
	e.bot.Started(ctx)
	e.gate.SetHandler(screens{e})
	return e
}

// screens hands the gateway's updates to the addon's bot.
type screens struct{ e *env }

func (s screens) Update(ctx context.Context, up tgate.Update) error {
	s.e.bot.Update(ctx, up)
	return nil
}

// emit is the panel's feed of events as the addon reads it, without the table between:
// the event is what the addon would pull, and its handling is the bot's.
func (e *env) emit(ctx context.Context, typ string, userID, tgID int64, data any, _ string) error {
	raw, err := json.Marshal(data)
	if err != nil {
		return err
	}
	return e.bot.HandleEvent(ctx, events.Event{Type: typ, UserID: userID, TgID: tgID, Data: raw})
}

// feed is what billing tells the buyers' messages through, as in the panel: billing writes
// its events in the transaction of the change, and the addon, which reads the feed, says what
// is new in it once the call returns. Only the types the addon asks for are handed on.
type feed struct{ e *env }

func (f feed) Paid(context.Context, db.Payment, db.User, bool)     { f.drain() }
func (f feed) Refunded(context.Context, db.Payment, db.User, bool) { f.drain() }
func (f feed) TrialNotGiven(context.Context, db.Payment)           { f.drain() }

func (f feed) drain() {
	e := f.e
	e.t.Helper()
	for {
		rows, err := e.st.Q.ListAddonEvents(e.ctx, db.ListAddonEventsParams{After: e.seen, Types: EventTypes, Lim: 100})
		if err != nil {
			e.t.Fatal(err)
		}
		if len(rows) == 0 {
			return
		}
		for _, r := range rows {
			e.seen = r.ID
			ev := events.Event{ID: r.ID, Type: r.Type, UserID: r.UserID.Int64, TgID: r.TgID.Int64, Data: json.RawMessage(r.Data)}
			if err := e.bot.HandleEvent(e.ctx, ev); err != nil {
				e.t.Fatalf("event %s: %v", r.Type, err)
			}
		}
	}
}

// notifications is the notifier billing is given in these tests: the panel's own feed, read
// as the bot addon does.
func (e *env) notifications() billing.Notifier { return feed{e} }

// code is the /start code of the subscription page's link to the bot for user.
func (e *env) code(user int64) string {
	e.t.Helper()
	link := e.gate.LinkURL(e.ctx, user)
	code, ok := strings.CutPrefix(link, "https://t.me/mikan_test_bot?start=")
	if !ok {
		e.t.Fatalf("the link to the bot: %q", link)
	}
	return code
}

// say sends a message from the Telegram user 555.
func (e *env) say(from int64, s string) {
	e.msgID++
	e.tg.push(tgate.Update{Message: &tgate.Message{MessageID: e.msgID, From: &tgate.User{ID: from, FirstName: "Anna", Username: "anna"}, Chat: tgate.Chat{ID: from, Type: "private"}, Text: s}})
}

func (e *env) press(from, msg int64, data string) {
	e.tg.push(tgate.Update{CallbackQuery: &tgate.CallbackQuery{ID: "cb" + data, From: tgate.User{ID: from}, Message: &tgate.Message{MessageID: msg, Chat: tgate.Chat{ID: from, Type: "private"}}, Data: data}})
}

func TestBot(t *testing.T) { testBot(t) }

// The same screens, seeing the panel only over the Panel API as the bot addon does.
func TestBotOverRPC(t *testing.T) { testBot(t, func(_ *env, w *wiring) { w.RPC = true }) }

func testBot(t *testing.T, with ...func(*env, *wiring)) {
	e := setup(t, with...)
	const anna, other = 555, 777

	// The Mini App's button beside the input field is one short word: on a phone a long
	// one takes the whole row and there is nowhere to type.
	e.tg.until(t, 0, func(cs []call) bool {
		for _, c := range cs {
			if c.method == "setChatMenuButton" {
				btn, _ := c.body["menu_button"].(map[string]any)
				if btn["type"] != "web_app" || btn["text"] != "Подписка" {
					t.Fatalf("menu button: %v", btn)
				}
				return true
			}
		}
		return false
	})

	// A stranger gets the welcome with the support button.
	n := e.tg.count()
	e.say(anna, "привет")
	calls := e.tg.wait(t, n, "sendMessage")
	send, _ := find(calls, "sendMessage")
	if !strings.Contains(text(send), "Mikan VPN") || buttons(send)["🎧 Поддержка"] != "https://t.me/mikan_support" {
		t.Fatalf("welcome: %q %v", text(send), buttons(send))
	}

	// The subscription page's link ties the subscription to the account.
	e.later()
	n = e.tg.count()
	e.say(anna, "/start "+e.code(e.user.ID))
	calls = e.tg.wait(t, n, "sendMessage")
	send, _ = find(calls, "sendMessage")
	menu := buttons(send)
	if !strings.Contains(text(send), "Подписка «Анна» подключена") || !strings.Contains(text(send), "Анна") || menu["👤 Подписка"] != "s" ||
		menu["📲 Открыть страницу подписки"] != "webapp:https://vpn.example.com:21355/sub/tg" {
		t.Fatalf("linked menu: %q %v", text(send), menu)
	}
	cleaned := false
	for _, c := range calls {
		cleaned = cleaned || c.method == "deleteMessage" && c.body["message_id"] == float64(e.msgID)
	}
	if !cleaned {
		t.Fatal("the /start message is cleaned up")
	}

	// Anything else brings a fresh menu to the bottom and removes the old one.
	e.later()
	n = e.tg.count()
	e.say(anna, "где моя подписка?")
	calls = e.tg.wait(t, n, "sendMessage")
	send, _ = find(calls, "sendMessage")
	deleted := 0
	for _, c := range calls {
		if c.method == "deleteMessage" {
			deleted++
		}
	}
	if !strings.Contains(text(send), "📦") || deleted != 2 {
		t.Fatalf("any text: a new menu, the old menu and the message deleted (%d): %q", deleted, text(send))
	}
	chat, _ := e.st.Q.GetTgChat(e.ctx, anna)
	menuMsg := chat.MenuMsgID

	// Buttons edit that message in place. Every tap's spinner stops at once; quick taps end
	// on the last one's screen, with no error for the user.
	e.later()
	n = e.tg.count()
	e.press(anna, menuMsg, "s")
	e.press(anna, menuMsg, "d")
	calls = e.tg.until(t, n, func(cs []call) bool {
		answers, last := 0, ""
		for _, c := range cs {
			switch c.method {
			case "answerCallbackQuery":
				answers++
			case "editMessageText":
				last = text(c)
			}
		}
		return answers == 2 && strings.Contains(last, "<b>Устройства</b>")
	})
	for _, c := range calls {
		if c.method == "editMessageText" && c.body["message_id"] != float64(menuMsg) {
			t.Fatalf("the menu message is edited, nothing new is sent: %+v", c)
		}
		if c.method == "sendMessage" {
			t.Fatalf("no new message for a tap: %+v", c)
		}
	}

	// Devices: listed with a button each; unbinding asks first, then once a day.
	u, _ := e.st.Q.GetUser(e.ctx, e.user.ID)
	for _, id := range []string{"phone-0123456789", "laptop-0123456789"} {
		if _, err := e.devs.Bind(e.ctx, u, domain.DeviceInfo{HWID: id, Model: map[string]string{"phone-0123456789": "Pixel 9"}[id], OS: "Windows", App: "Happ/3.4.1"}, false); err != nil {
			t.Fatal(err)
		}
	}
	bound, _ := e.st.Q.ListBoundDevices(e.ctx, e.user.ID)
	e.later()
	n = e.tg.count()
	e.press(anna, menuMsg, "d")
	edit, _ := find(e.tg.wait(t, n, "editMessageText"), "editMessageText")
	if !strings.Contains(text(edit), "Pixel 9") || buttons(edit)["❌ Pixel 9"] != "dc:"+strconv.FormatInt(bound[0].ID, 10) {
		t.Fatalf("devices: %q %v", text(edit), buttons(edit))
	}
	// The rules under the list: what is left of the limit, and the pause of an unbound device.
	if !strings.Contains(text(edit), "Можно отвязать ещё 1 из 1 за сутки.") || !strings.Contains(text(edit), "не сможет подключиться снова 1 день") {
		t.Fatalf("the rules: %q", text(edit))
	}
	// Asking first says to close the app, or it comes back.
	e.later()
	n = e.tg.count()
	e.press(anna, menuMsg, "dc:"+strconv.FormatInt(bound[0].ID, 10))
	edit, _ = find(e.tg.wait(t, n, "editMessageText"), "editMessageText")
	if !strings.Contains(text(edit), "Отвязать «Pixel 9»?") || !strings.Contains(text(edit), "только через 1 день: сначала закройте на нём приложение") {
		t.Fatalf("confirmation: %q", text(edit))
	}
	e.later()
	n = e.tg.count()
	e.press(anna, menuMsg, "du:"+strconv.FormatInt(bound[0].ID, 10))
	edit, _ = find(e.tg.wait(t, n, "editMessageText"), "editMessageText")
	if !strings.Contains(text(edit), "«Pixel 9» отвязано") || strings.Contains(text(edit), "1. Pixel 9") {
		t.Fatalf("unbound: %q", text(edit))
	}
	e.later()
	n = e.tg.count()
	e.press(anna, menuMsg, "du:"+strconv.FormatInt(bound[1].ID, 10))
	edit, _ = find(e.tg.wait(t, n, "editMessageText"), "editMessageText")
	if !strings.Contains(text(edit), "Следующее устройство можно отвязать") {
		t.Fatalf("second unbind the same day: %q", text(edit))
	}
	// While none is left the list has no buttons to tap, only the line with the moment.
	e.later()
	n = e.tg.count()
	e.press(anna, menuMsg, "d")
	edit, _ = find(e.tg.wait(t, n, "editMessageText"), "editMessageText")
	if !strings.Contains(text(edit), "<b>⏳ Следующее устройство можно отвязать") {
		t.Fatalf("the wait on top: %q", text(edit))
	}
	for label := range buttons(edit) {
		if strings.HasPrefix(label, "❌") {
			t.Fatalf("an unbind button while none is left: %v", buttons(edit))
		}
	}
	// Unbinding off: no buttons, the note says whom to ask, a stale tap is refused.
	if err := settings.Set(e.ctx, settings.New(e.st.Q), settings.KeyUnbindRules, domain.UnbindRules{Off: true, Limit: 1, Days: 1}); err != nil {
		t.Fatal(err)
	}
	e.later()
	n = e.tg.count()
	e.press(anna, menuMsg, "d")
	edit, _ = find(e.tg.wait(t, n, "editMessageText"), "editMessageText")
	if !strings.Contains(text(edit), "Отвязать устройство может только администратор") || strings.Contains(text(edit), "Можно отвязать ещё") {
		t.Fatalf("off: %q", text(edit))
	}
	for label := range buttons(edit) {
		if strings.HasPrefix(label, "❌") {
			t.Fatalf("an unbind button with unbinding off: %v", buttons(edit))
		}
	}
	if err := settings.Set(e.ctx, settings.New(e.st.Q), settings.KeyUnbindRules, domain.DefaultUnbindRules); err != nil {
		t.Fatal(err)
	}
	// A confirmation asked for before stays closed: the list again.
	e.later()
	n = e.tg.count()
	e.press(anna, menuMsg, "dc:"+strconv.FormatInt(bound[1].ID, 10))
	edit, _ = find(e.tg.wait(t, n, "editMessageText"), "editMessageText")
	if strings.Contains(text(edit), "Отвязать «") {
		t.Fatalf("a confirmation past the limit: %q", text(edit))
	}

	// Somebody else's device id does nothing.
	e.later()
	n = e.tg.count()
	e.press(anna, menuMsg, "du:999")
	edit, _ = find(e.tg.wait(t, n, "editMessageText"), "editMessageText")
	if strings.Contains(text(edit), "отвязано") {
		t.Fatalf("an unknown device: %q", text(edit))
	}

	// Notifications go once per term.
	exp := e.clock().Add(2 * 24 * time.Hour).Unix()
	if _, err := e.st.DB.ExecContext(e.ctx, "UPDATE users SET expires_at = $1 WHERE id = $2", exp, e.user.ID); err != nil {
		t.Fatal(err)
	}
	n = e.tg.count()
	e.panel.Notify(e.ctx)
	send, _ = find(e.tg.wait(t, n, "sendMessage"), "sendMessage")
	if !strings.Contains(text(send), "заканчивается") || send.body["chat_id"] != float64(anna) {
		t.Fatalf("expiring notice: %q", text(send))
	}
	n = e.tg.count()
	e.panel.Notify(e.ctx)
	time.Sleep(100 * time.Millisecond)
	if e.tg.count() != n {
		t.Fatal("the same notice twice")
	}

	// The Mini App signs in with Telegram's initData.
	vals := url.Values{"auth_date": {strconv.FormatInt(e.clock().Unix(), 10)}, "user": {`{"id":555,"first_name":"Anna"}`}}
	_, subs, err := e.gate.MiniAppUser(e.ctx, signed("123:test", vals))
	if err != nil || len(subs) != 1 || subs[0].ID != e.user.ID {
		t.Fatalf("mini app: %v %v", subs, err)
	}

	// A broadcast reaches every account with a subscription.
	n = e.tg.count()
	if k, err := e.panel.Broadcast(e.ctx, "Плановые работы в {brand} ночью"); err != nil || k != 1 {
		t.Fatalf("broadcast: %d %v", k, err)
	}
	send, _ = find(e.tg.wait(t, n, "sendMessage"), "sendMessage")
	if text(send) != "Плановые работы в Mikan VPN ночью" {
		t.Fatalf("broadcast text: %q", text(send))
	}
	if p := e.panel.Progress(); p.Total != 1 || p.Sent != 1 || p.Active() {
		t.Fatalf("broadcast progress: %+v", p)
	}

	// Somebody else holding the link does not take the subscription: its owner is asked.
	// A refusal keeps it, and the same account is not let ask again at once.
	owner := func() int64 { l, _ := e.st.Q.GetTgLink(e.ctx, e.user.ID); return l.TgID }
	uid := strconv.FormatInt(e.user.ID, 10)
	ask := func() []call {
		e.later()
		n := e.tg.count()
		e.say(other, "https://vpn.example.com:21355/sub/"+e.user.SubToken)
		// The answer to the one who asks and the question to the owner go out by different
		// priorities, in either order: wait for both.
		return e.tg.until(t, n, func(cs []call) bool {
			told, asked := false, false
			for _, c := range cs {
				if c.method != "sendMessage" {
					continue
				}
				told = told || c.body["chat_id"] == float64(other) && strings.Contains(text(c), "Эта подписка уже подключена")
				asked = asked || c.body["chat_id"] == float64(anna) && strings.Contains(text(c), "хотят подключить")
			}
			return told && asked
		})
	}
	calls = ask()
	var question call
	for _, c := range calls {
		if c.method == "sendMessage" && c.body["chat_id"] == float64(anna) && strings.Contains(text(c), "хотят подключить") {
			question = c
		}
	}
	if question.method == "" || owner() != anna || !strings.Contains(text(question), "Анна") {
		t.Fatalf("the owner is asked and keeps the subscription: %v", calls)
	}
	if b := buttons(question); b["✅ Разрешить"] != "ta:"+uid+":777" || b["❌ Отказать"] != "tx:"+uid+":777" {
		t.Fatalf("the question's buttons: %v", b)
	}
	// Only the owner's chat answers: the account that asks cannot press its own request through.
	e.later()
	e.press(other, 4242, "ta:"+uid+":777")
	time.Sleep(150 * time.Millisecond)
	if owner() != anna {
		t.Fatal("the requester allowed it for itself")
	}
	n = e.tg.count()
	e.press(anna, 4242, "tx:"+uid+":777")
	e.tg.until(t, n, func(cs []call) bool {
		for _, c := range cs {
			if c.method == "sendMessage" && c.body["chat_id"] == float64(other) && strings.Contains(text(c), "не разрешил") {
				return true
			}
		}
		return false
	})
	if owner() != anna {
		t.Fatal("a refusal keeps the subscription")
	}
	n = e.tg.count()
	e.later()
	e.say(other, "https://vpn.example.com:21355/sub/"+e.user.SubToken)
	again := e.tg.wait(t, n, "sendMessage")
	for _, c := range again {
		if c.method == "sendMessage" && c.body["chat_id"] == float64(anna) {
			t.Fatalf("a refused account asks again at once and the owner is bothered: %q", text(c))
		}
	}

	// An owner who allows it gives the subscription away: the other account gets it.
	e.bot.mu.Lock()
	e.bot.refused = map[string]time.Time{}
	e.bot.mu.Unlock()
	ask()
	e.later()
	e.press(anna, 4243, "ta:"+uid+":777")
	deadline := time.Now().Add(3 * time.Second)
	for owner() != other {
		if time.Now().After(deadline) {
			t.Fatalf("the owner allowed it, the subscription is with %d", owner())
		}
		time.Sleep(10 * time.Millisecond)
	}
	// The same button pressed again does nothing: the request is spent.
	e.later()
	e.press(anna, 4243, "ta:"+uid+":777")
	time.Sleep(150 * time.Millisecond)
	if owner() != other {
		t.Fatal("a spent request moved the subscription again")
	}

	// A user who blocked the bot is not written to again.
	e.tg.mu.Lock()
	e.tg.blocked[other] = true
	e.tg.mu.Unlock()
	e.later()
	n = e.tg.count()
	e.say(other, "ещё")
	e.tg.wait(t, n, "sendMessage")
	deadline = time.Now().Add(2 * time.Second)
	for ch, _ := e.st.Q.GetTgChat(e.ctx, other); ch.Blocked != 1; ch, _ = e.st.Q.GetTgChat(e.ctx, other) {
		if time.Now().After(deadline) {
			t.Fatalf("blocked chat: %+v", ch)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if targets, _ := e.st.Q.BroadcastTargets(e.ctx); len(targets) != 0 {
		t.Fatalf("broadcast targets: %v", targets)
	}
}

func TestConfigSavedBefore(t *testing.T) {
	ctx := context.Background()
	st, err := storetest.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	b := &Local{Store: st, Settings: settings.New(st.Q)}
	if c := b.Config(ctx); !c.QuietNight || len(c.Buttons) != 7 {
		t.Fatalf("a fresh bot: %+v", c)
	}
	// As 0.3.6 saved it: no quiet_night yet, a menu of two buttons.
	old := `{"lang":"ru","buttons":[{"id":"sub","action":"sub","label":"Моя подписка","on":true,"row":false},` +
		`{"id":"renew","action":"renew","label":"Продлить","on":true,"row":false}],` +
		`"texts":{"welcome":"","main":"Привет","renew":"","expiring":"","expired":"","traffic_90":"","traffic_end":""},` +
		`"notify":{"expire_3d":true,"expire_1d":false,"expired":true,"traffic_90":true,"traffic_100":true},"mini_app":true,"clean_chat":false}`
	if err := st.Q.SetSetting(ctx, db.SetSettingParams{Key: tgate.KeyConfig, Value: old}); err != nil {
		t.Fatal(err)
	}
	c := b.Config(ctx)
	if !c.QuietNight || !c.PromoButton {
		t.Fatal("an option added later keeps its default")
	}
	if c.CleanChat || c.Notify.Expire1d || c.Texts.Main != "Привет" || len(c.Buttons) != 2 || c.Buttons[1].Label != "Продлить" || c.Buttons[1].Row {
		t.Fatalf("what was saved stays: %+v", c)
	}
}

func TestConfigValidate(t *testing.T) {
	c := Default("ru")
	c.Buttons = append(c.Buttons, MenuButton{Action: "url", Label: "Канал", URL: "https://t.me/mikan", On: true},
		MenuButton{Action: "page", Label: "Правила", Text: "Не делитесь ссылкой", On: true})
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	if c.Buttons[6].ID == "" || c.Buttons[7].ID == "" || c.Buttons[6].ID == c.Buttons[7].ID {
		t.Fatalf("custom buttons get ids: %+v", c.Buttons[6:])
	}
	for name, bad := range map[string]MenuButton{
		"javascript link": {Action: "url", Label: "x", URL: "javascript:alert(1)"},
		"http link":       {Action: "url", Label: "x", URL: "http://example.com"},
		"empty page":      {Action: "page", Label: "x", Text: " "},
		"empty label":     {Action: "page", Label: " ", Text: "x"},
		"twice sub":       {Action: "sub", Label: "x"},
		"unknown action":  {Action: "pay", Label: "x"},
	} {
		c := Default("ru")
		c.Buttons = append(c.Buttons, bad)
		if err := c.Validate(); err == nil {
			t.Errorf("%s must be refused", name)
		}
	}
}

// A subscription can be taken off the Telegram account after a confirmation: only its link
// goes, the subscription keeps working, and nobody else can remove it.
func TestRemoveSubscriptionFromTheBot(t *testing.T) {
	e := setup(t)
	const anna, other = 555, 777
	n := e.tg.count()
	e.say(anna, "/start "+e.code(e.user.ID))
	e.tg.wait(t, n, "sendMessage")
	chat, _ := e.st.Q.GetTgChat(e.ctx, anna)
	menu := chat.MenuMsgID
	uid := strconv.FormatInt(e.user.ID, 10)

	tap := func(from int64, data string) call {
		t.Helper()
		e.later()
		n := e.tg.count()
		e.press(from, menu, data)
		calls := e.tg.until(t, n, func(cs []call) bool { _, ok := find(cs, "editMessageText"); return ok })
		edit, _ := find(calls, "editMessageText")
		return edit
	}
	if got := buttons(tap(anna, "s"))["🗑 Убрать из бота"]; got != "rm" {
		t.Fatalf("the subscription screen offers removing it: %q", got)
	}
	confirm := tap(anna, "rm")
	if !strings.Contains(text(confirm), "Убрать подписку «Анна»") || buttons(confirm)["✅ Да, убрать"] != "rd:"+uid || buttons(confirm)["↩️ Отмена"] != "s" {
		t.Fatalf("confirm: %q %v", text(confirm), buttons(confirm))
	}

	// Someone else's tap with the same data changes nothing.
	e.later()
	e.press(other, menu, "rd:"+uid)
	time.Sleep(100 * time.Millisecond)
	if l, err := e.st.Q.GetTgLink(e.ctx, e.user.ID); err != nil || l.TgID != anna {
		t.Fatalf("another account removed the link: %+v %v", l, err)
	}

	done := tap(anna, "rd:"+uid)
	if !strings.Contains(text(done), "Подписка «Анна» убрана из бота") || !strings.Contains(text(done), "пришлите сюда ссылку на подписку") {
		t.Fatalf("after removing: %q", text(done))
	}
	if _, err := e.st.Q.GetTgLink(e.ctx, e.user.ID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("the link stays: %v", err)
	}
	if u, err := e.st.Q.GetUser(e.ctx, e.user.ID); err != nil || u.Status != e.user.Status || u.SubToken != e.user.SubToken {
		t.Fatalf("the subscription itself changed: %+v %v", u, err)
	}
}

// {link} is the subscriber's own subscription link, for an admin's menu text.
func TestBotLinkVariable(t *testing.T) {
	e := setup(t)
	v := e.bot.vars(e.ctx, wordsFor("ru"), e.user, e.now)
	if !strings.HasPrefix(v["link"], "https://vpn.example.com:21355/sub/") || !strings.Contains(v["link"], e.user.SubToken) {
		t.Fatalf("link %q", v["link"])
	}
}

// signed builds initData the way Telegram does.
func signed(token string, vals url.Values) string {
	keys := make([]string, 0, len(vals))
	for k := range vals {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	lines := make([]string, len(keys))
	for i, k := range keys {
		lines[i] = k + "=" + vals.Get(k)
	}
	secret := hmac.New(sha256.New, []byte("WebAppData"))
	secret.Write([]byte(token))
	m := hmac.New(sha256.New, secret.Sum(nil))
	m.Write([]byte(strings.Join(lines, "\n")))
	vals.Set("hash", hex.EncodeToString(m.Sum(nil)))
	return vals.Encode()
}
