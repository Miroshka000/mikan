package panelapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"mikan/internal/panel/addons"
	"mikan/internal/panel/domain"
	"mikan/internal/panel/events"
	"mikan/internal/panel/store/db"
)

var (
	readerToken  = strings.Repeat("r", 48)
	granterToken = strings.Repeat("g", 48)
	bothToken    = strings.Repeat("o", 48)
	otherToken   = strings.Repeat("h", 48)
)

type changeCount struct{ n atomic.Int32 }

func (c *changeCount) PoliciesChanged() { c.n.Add(1) }
func (c *changeCount) SlotsChanged()    {}

// usersEnv is a panel with its tariffs, four addons that hold different permissions over the
// subscribers, and a clock to move.
type usersEnv struct {
	t       *testing.T
	s       *Server
	users   *domain.Users
	devs    *domain.Devices
	changes *changeCount
	now     *time.Time
	tariffs []db.Tariff
}

func newUsersEnv(t *testing.T) *usersEnv {
	t.Helper()
	st := state()
	for id, tok := range map[string]string{"reader": readerToken, "granter": granterToken, "both": bothToken, "other": otherToken} {
		st.Adapters[id] = addons.Installed{Protocol: addons.Protocol2, Kind: addons.KindBot, PanelToken: tok, Token: "t", Status: "running"}
	}
	srv, _ := world(t, st, grants{
		// The bot's own: users:read and users:write reach the subscribers it talks to, not all.
		"telegram": {Version: "1", Permissions: `["bot","events","kv","users:read","users:write"]`},
		"reader":   {Version: "1", Permissions: `["users:all"]`},
		"granter":  {Version: "1", Permissions: `["users:grant"]`},
		"both":     {Version: "1", Permissions: `["users:all","users:grant"]`},
		"other":    {Version: "1", Permissions: `["users:grant"]`},
	})
	now := time.Unix(1_800_000_000, 0)
	clock := func() time.Time { return now }
	srv.d.Now = clock
	ctx := context.Background()
	if err := domain.Seed(ctx, srv.d.Store, now); err != nil {
		t.Fatal(err)
	}
	ch := &changeCount{}
	pool := domain.NewPool(srv.d.Store, clock)
	srv.d.Users = domain.NewUsers(srv.d.Store, pool, ch, clock)
	srv.handleUsers()
	tariffs, err := srv.d.Store.Q.ListTariffs(ctx)
	if err != nil || len(tariffs) < 2 {
		t.Fatalf("tariffs: %v %v", tariffs, err)
	}
	return &usersEnv{t: t, s: srv, users: srv.d.Users, devs: domain.NewDevices(srv.d.Store, pool, ch, clock), changes: ch, now: &now, tariffs: tariffs}
}

func (e *usersEnv) user(name string) db.User {
	e.t.Helper()
	u, err := e.users.Create(context.Background(), domain.CreateInput{Name: name, TariffID: e.tariffs[1].ID})
	if err != nil {
		e.t.Fatal(err)
	}
	return u
}

func (e *usersEnv) reload(id int64) db.User {
	e.t.Helper()
	u, err := e.s.d.Store.Q.GetUser(context.Background(), id)
	if err != nil {
		e.t.Fatal(err)
	}
	return u
}

// do calls the API and decodes an object answer.
func (e *usersEnv) do(token, method, path, body string) (int, map[string]any) {
	e.t.Helper()
	r := httptest.NewRequest(method, "http://127.0.0.1:41990/panel/v2"+path, strings.NewReader(body))
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	e.s.ServeHTTP(w, r)
	var m map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &m)
	return w.Code, m
}

func (e *usersEnv) grant(token string, id int64, body string) (int, map[string]any) {
	e.t.Helper()
	return e.do(token, http.MethodPost, fmt.Sprintf("/users/%d/grant", id), body)
}

func code2(m map[string]any) string { c, _ := m["code"].(string); return c }

func (e *usersEnv) audit() []db.AuditLog {
	e.t.Helper()
	rows, err := e.s.d.Store.Q.ListAudit(context.Background(), db.ListAuditParams{BeforeID: 1 << 62, Lim: 100})
	if err != nil {
		e.t.Fatal(err)
	}
	return rows
}

// feed is the grants told to addons; the users the tests make are told too (user.created).
func (e *usersEnv) feed() []db.AddonEvent {
	e.t.Helper()
	rows, err := e.s.d.Store.Q.ListAddonEvents(context.Background(), db.ListAddonEventsParams{BotToo: true, Lim: 100})
	if err != nil {
		e.t.Fatal(err)
	}
	var granted []db.AddonEvent
	for _, r := range rows {
		if r.Type == events.UserGranted {
			granted = append(granted, r)
		}
	}
	return granted
}

// Each route needs its own permission; the bot's users:read and users:write, and the
// other of the two new ones, open none of them.
func TestUsersPermissions(t *testing.T) {
	e := newUsersEnv(t)
	u := e.user("anna")
	grantBody := `{"days":1,"reason":"test","key":"k-perm"}`
	list, card, give := "/users", fmt.Sprintf("/users/%d", u.ID), fmt.Sprintf("/users/%d/grant", u.ID)
	cases := []struct {
		name, token, method, path, body string
		status                          int
	}{
		{"no token lists", "", "GET", list, "", 401},
		{"no token grants", "", "POST", give, grantBody, 401},
		{"the bot's users:read and users:write list nothing", botToken, "GET", list, "", 403},
		{"the bot has no card either", botToken, "GET", card, "", 403},
		{"the bot grants to nobody", botToken, "POST", give, grantBody, 403},
		{"users:grant does not read", granterToken, "GET", list, "", 403},
		{"users:grant has no card", granterToken, "GET", card, "", 403},
		{"users:all does not grant", readerToken, "POST", give, grantBody, 403},
		{"users:all lists", readerToken, "GET", list, "", 200},
		{"users:all reads a card", readerToken, "GET", card, "", 200},
		{"users:grant grants", granterToken, "POST", give, grantBody, 200},
		{"both read", bothToken, "GET", list, "", 200},
	}
	for _, c := range cases {
		if status, m := e.do(c.token, c.method, c.path, c.body); status != c.status {
			t.Errorf("%s: %d %v, want %d", c.name, status, m, c.status)
		}
	}
	// Refused calls changed nothing: only the one grant that was let in left a mark.
	if got := len(e.feed()); got != 1 {
		t.Fatalf("events after the refusals: %d", got)
	}
	// Another method on a route is no route.
	if status, _ := e.do(bothToken, "DELETE", card, ""); status == 200 || status == 204 {
		t.Fatalf("delete: %d", status)
	}
}

type briefs struct {
	Users []struct {
		ID         int64   `json:"id"`
		Name       string  `json:"name"`
		State      string  `json:"state"`
		TariffName string  `json:"tariff_name"`
		TgID       *int64  `json:"tg_id"`
		ExpiresAt  *string `json:"expires_at"`
	} `json:"users"`
	Next *int64 `json:"next"`
}

func (e *usersEnv) list(query string) (int, briefs, string) {
	e.t.Helper()
	r := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:41990/panel/v2/users"+query, nil)
	r.Header.Set("Authorization", "Bearer "+readerToken)
	w := httptest.NewRecorder()
	e.s.ServeHTTP(w, r)
	var b briefs
	_ = json.Unmarshal(w.Body.Bytes(), &b)
	return w.Code, b, w.Body.String()
}

func (b briefs) ids() []int64 {
	out := []int64{}
	for _, u := range b.Users {
		out = append(out, u.ID)
	}
	return out
}

// The list is paged by a cursor of ids, searches by name, id and Telegram id, and shows no
// credential.
func TestUsersList(t *testing.T) {
	e := newUsersEnv(t)
	ctx := context.Background()
	names := []string{"Anna", "bob", "Борис", "annabel", "Carl"}
	var ids []int64
	for _, n := range names {
		ids = append(ids, e.user(n).ID)
	}
	// Carl is linked to Telegram account 777000111, bob too (a second subscription).
	for _, id := range []int64{ids[4], ids[1]} {
		if err := e.s.d.Store.Q.LinkTg(ctx, db.LinkTgParams{UserID: id, TgID: 777000111, CreatedAt: 1}); err != nil {
			t.Fatal(err)
		}
	}
	if err := e.s.d.Store.Q.UpsertTgChat(ctx, db.UpsertTgChatParams{TgID: 777000111, Username: "carl", FirstName: "Carl", CreatedAt: 1, UpdatedAt: 1}); err != nil {
		t.Fatal(err)
	}

	// Pages of two, by the cursor: every user once, in id order, the last page without next.
	var seen []int64
	query := "?limit=2"
	for pages := 0; ; pages++ {
		status, b, raw := e.list(query)
		if status != 200 || len(b.Users) == 0 || len(b.Users) > 2 || pages > 5 {
			t.Fatalf("page %d: %d %s", pages, status, raw)
		}
		seen = append(seen, b.ids()...)
		if b.Next == nil {
			if len(b.Users) > 2 {
				t.Fatal("the last page is too long")
			}
			break
		}
		if *b.Next != b.Users[len(b.Users)-1].ID {
			t.Fatalf("next %d is not the last id of the page %v", *b.Next, b.ids())
		}
		query = fmt.Sprintf("?limit=2&after=%d", *b.Next)
	}
	if fmt.Sprint(seen) != fmt.Sprint(ids) {
		t.Fatalf("paged %v, want %v", seen, ids)
	}
	// A page of exactly the rest has no next.
	if _, b, _ := e.list(fmt.Sprintf("?limit=2&after=%d", ids[2])); b.Next != nil || len(b.Users) != 2 {
		t.Fatalf("the rest: %+v", b)
	}
	// The cursor is stable: a user made later never moves the pages already read.
	late := e.user("zed")
	if _, b, _ := e.list(fmt.Sprintf("?limit=2&after=%d", ids[2])); fmt.Sprint(b.ids()) != fmt.Sprint(ids[3:]) || b.Next == nil || *b.Next != ids[4] {
		t.Fatalf("after a new user: %v next %v", b.ids(), b.Next)
	}
	if _, b, _ := e.list(fmt.Sprintf("?after=%d", ids[4])); fmt.Sprint(b.ids()) != fmt.Sprint([]int64{late.ID}) {
		t.Fatalf("the newcomer: %v", b.ids())
	}

	search := func(q string) []int64 {
		t.Helper()
		status, b, raw := e.list("?q=" + q)
		if status != 200 {
			t.Fatalf("search %q: %d %s", q, status, raw)
		}
		return b.ids()
	}
	for _, c := range []struct {
		q    string
		want []int64
	}{
		{"ANNA", []int64{ids[0], ids[3]}},       // a part of a name, any case
		{"%D0%B1%D0%BE%D1%80", []int64{ids[2]}}, // Cyrillic, lowercased by Go
		{fmt.Sprint(ids[1]), []int64{ids[1]}},   // an id
		{"777000111", []int64{ids[1], ids[4]}},  // a Telegram id: every subscription it owns
		{"nobody", []int64{}},                   // nothing
		{"%20anna%20", []int64{ids[0], ids[3]}}, // trimmed
		{fmt.Sprint(ids[4] + 1000), []int64{}},  // an id nobody has
	} {
		if got := search(c.q); fmt.Sprint(got) != fmt.Sprint(c.want) {
			t.Errorf("search %q: %v, want %v", c.q, got, c.want)
		}
	}
	// A search pages too: its cursor skips what does not match.
	status, b, _ := e.list("?q=anna&limit=1")
	if status != 200 || fmt.Sprint(b.ids()) != fmt.Sprint(ids[:1]) || b.Next == nil || *b.Next != ids[0] {
		t.Fatalf("search page 1: %d %+v", status, b)
	}
	if _, b, _ = e.list(fmt.Sprintf("?q=anna&limit=1&after=%d", *b.Next)); fmt.Sprint(b.ids()) != fmt.Sprint([]int64{ids[3]}) || b.Next != nil {
		t.Fatalf("search page 2: %+v", b)
	}

	// What a row says.
	_, all, raw := e.list("?limit=100")
	carl := all.Users[4]
	if carl.Name != "Carl" || carl.State != "active" || carl.TariffName != e.tariffs[1].Name || carl.TgID == nil || *carl.TgID != 777000111 || carl.ExpiresAt == nil {
		t.Fatalf("a row: %+v", carl)
	}
	if all.Users[0].TgID != nil {
		t.Fatalf("an unlinked user has a Telegram id: %+v", all.Users[0])
	}
	// The link of a subscription, its notes and its contact are not for addons.
	for _, secret := range []string{"sub_token", "sub_url", "contact", "note"} {
		if strings.Contains(raw, secret) {
			t.Fatalf("the list says %q: %s", secret, raw)
		}
	}

	// Refused input.
	for _, q := range []string{"?limit=0", "?limit=101", "?limit=x", "?after=-1", "?after=x", "?q=" + strings.Repeat("a", 101)} {
		if status, _, raw := e.list(q); status != 400 || !strings.Contains(raw, "bad_request") {
			t.Errorf("%s: %d %s", q, status, raw)
		}
	}
	if status, b, _ := e.list("?limit=100"); status != 200 || len(b.Users) != 6 {
		t.Fatalf("the biggest page: %d %d", status, len(b.Users))
	}
}

// The state a list shows counts the traffic packages, as the admin's does.
func TestUsersListState(t *testing.T) {
	e := newUsersEnv(t)
	ctx := context.Background()
	disabled, expired, limited, packed := e.user("off"), e.user("old"), e.user("full"), e.user("packed")
	yes, past, small := true, e.now.Add(-time.Hour), int64(1000)
	if _, err := e.users.Update(ctx, disabled.ID, domain.Patch{Disabled: &yes}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.users.Update(ctx, expired.ID, domain.Patch{ExpiresAt: &past}); err != nil {
		t.Fatal(err)
	}
	for _, id := range []int64{limited.ID, packed.ID} {
		if _, err := e.users.Update(ctx, id, domain.Patch{TrafficLimit: &small}); err != nil {
			t.Fatal(err)
		}
	}
	if err := e.s.d.Store.TxRC(ctx, func(q *db.Queries) error {
		return domain.CountTraffic(ctx, q, domain.TrafficBatch{Main: map[int64]domain.Bytes{limited.ID: {Down: 2000}, packed.ID: {Down: 2000}}}, *e.now)
	}); err != nil {
		t.Fatal(err)
	}
	// A package that outlasts what the quota lacks keeps the user going.
	if _, err := domain.GrantTx(ctx, e.s.d.Store.Q, e.reload(packed.ID), domain.GrantSpec{Bytes: 5000, Lifetime: domain.LifetimeUsed, Source: domain.SourceAdmin}, *e.now); err != nil {
		t.Fatal(err)
	}
	_, b, raw := e.list("")
	got := map[string]string{}
	for _, u := range b.Users {
		got[u.Name] = u.State
	}
	want := map[string]string{"off": "disabled", "old": "expired", "full": "limited", "packed": "active"}
	for n, st := range want {
		if got[n] != st {
			t.Errorf("%s is %q, want %q (%s)", n, got[n], st, raw)
		}
	}
}

// A card says what the addon needs about one subscriber, and 404s on one that is not there.
func TestUserCard(t *testing.T) {
	e := newUsersEnv(t)
	ctx := context.Background()
	u := e.user("Dina")
	gib := domain.GiB
	limit := 10 * gib
	if _, err := e.users.Update(ctx, u.ID, domain.Patch{TrafficLimit: &limit}); err != nil {
		t.Fatal(err)
	}
	if err := e.s.d.Store.TxRC(ctx, func(q *db.Queries) error {
		return domain.CountTraffic(ctx, q, domain.TrafficBatch{Main: map[int64]domain.Bytes{u.ID: {Up: gib, Down: 3 * gib}}}, *e.now)
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.users.Grant(ctx, u.ID, domain.GrantInput{Bytes: 2 * gib, Lifetime: domain.LifetimeUsed}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.devs.Bind(ctx, e.reload(u.ID), domain.DeviceInfo{HWID: "phone-0123456789", OS: "Android"}, false); err != nil {
		t.Fatal(err)
	}
	if err := e.s.d.Store.Q.LinkTg(ctx, db.LinkTgParams{UserID: u.ID, TgID: 4242, CreatedAt: 1}); err != nil {
		t.Fatal(err)
	}
	if err := e.s.d.Store.Q.UpsertTgChat(ctx, db.UpsertTgChatParams{TgID: 4242, Username: "dina", FirstName: "Dina", CreatedAt: 1, UpdatedAt: 1}); err != nil {
		t.Fatal(err)
	}

	status, m := e.do(readerToken, "GET", fmt.Sprintf("/users/%d", u.ID), "")
	if status != 200 {
		t.Fatalf("card: %d %v", status, m)
	}
	tr, _ := m["traffic"].(map[string]any)
	tg, _ := m["telegram"].(map[string]any)
	if m["id"] != float64(u.ID) || m["name"] != "Dina" || m["state"] != "active" || m["tariff_name"] != e.tariffs[1].Name || m["tariff_id"] != float64(e.tariffs[1].ID) ||
		tr["used"] != float64(4*gib) || tr["limit"] != float64(10*gib) || tr["left"] != float64(8*gib) ||
		m["packages_left"] != float64(2*gib) || m["devices"] != float64(1) || m["tg_id"] != float64(4242) {
		t.Fatalf("card: %v", m)
	}
	if tg["username"] != "dina" || tg["name"] != "Dina" {
		t.Fatalf("telegram: %v", m["telegram"])
	}
	if exp, _ := m["expires_at"].(string); exp == "" {
		t.Fatalf("a tariff with a term has an expiry: %v", m)
	}
	for _, secret := range []string{"sub_token", "sub_url", "contact", "note"} {
		if _, ok := m[secret]; ok {
			t.Fatalf("the card says %q: %v", secret, m)
		}
	}

	// No limit: nothing is left of nothing.
	if _, err := e.users.Update(ctx, u.ID, domain.Patch{ClearTrafficLimit: true}); err != nil {
		t.Fatal(err)
	}
	if _, m = e.do(readerToken, "GET", fmt.Sprintf("/users/%d", u.ID), ""); m["traffic"].(map[string]any)["limit"] != nil || m["traffic"].(map[string]any)["left"] != nil {
		t.Fatalf("unlimited: %v", m)
	}
	// A subscription with no owner has no link.
	bare := e.user("bare")
	if _, m = e.do(readerToken, "GET", fmt.Sprintf("/users/%d", bare.ID), ""); m["tg_id"] != nil || m["telegram"] != nil {
		t.Fatalf("unlinked: %v", m)
	}

	for path, want := range map[string]int{"/users/99999": 404, "/users/0": 400, "/users/-3": 400, "/users/abc": 400} {
		if status, m := e.do(readerToken, "GET", path, ""); status != want {
			t.Errorf("%s: %d %v, want %d", path, status, m, want)
		}
	}
}

func (e *usersEnv) trafficGrants(id int64) []db.TrafficGrant {
	e.t.Helper()
	gs, err := e.s.d.Store.Q.ListUserGrants(context.Background(), id)
	if err != nil {
		e.t.Fatal(err)
	}
	return gs
}

// Days go through the admin's extension, traffic through the admin's grant; both leave the
// audit lines of the addon and one event, and tell the nodes.
func TestGrantDaysAndTraffic(t *testing.T) {
	e := newUsersEnv(t)
	u := e.user("Eve")
	before := e.reload(u.ID)

	status, m := e.grant(granterToken, u.ID, `{"days":30,"reason":"loyalty bonus","key":"bonus:2026-10"}`)
	if status != 200 || m["repeat"] != false || m["days"] != float64(30) || m["user_id"] != float64(u.ID) {
		t.Fatalf("days: %d %v", status, m)
	}
	after := e.reload(u.ID)
	if after.ExpiresAt.Int64 != before.ExpiresAt.Int64+30*86400 {
		t.Fatalf("the term: %d -> %d", before.ExpiresAt.Int64, after.ExpiresAt.Int64)
	}
	if want := time.Unix(after.ExpiresAt.Int64, 0).UTC().Format(time.RFC3339); m["expires_at"] != want {
		t.Fatalf("answered %v, the term ends %s", m["expires_at"], want)
	}

	status, m = e.grant(granterToken, u.ID, `{"bytes":5368709120,"reason":"apology for the outage","key":"gift-1"}`)
	if status != 200 || m["bytes"] != float64(5*domain.GiB) || m["lifetime"] != "used" || m["grant_id"] == nil {
		t.Fatalf("traffic: %d %v", status, m)
	}
	gs := e.trafficGrants(u.ID)
	if len(gs) != 1 || gs[0].Bytes != 5*domain.GiB || gs[0].Remaining != 5*domain.GiB || gs[0].Note != "addon:granter: apology for the outage" ||
		gs[0].Lifetime != domain.LifetimeUsed || gs[0].PoolID.Valid || gs[0].ExpiresAt.Valid {
		t.Fatalf("grants: %+v", gs)
	}

	// Days and traffic in one call: the term is extended by days, the traffic lasts traffic_days.
	status, m = e.grant(granterToken, u.ID, `{"days":30,"bytes":1073741824,"lifetime":"days","traffic_days":7,"reason":"week pass","key":"week-1"}`)
	if status != 200 || m["days"] != float64(30) || m["bytes"] != float64(domain.GiB) || m["traffic_expires_at"] == nil {
		t.Fatalf("both: %d %v", status, m)
	}
	if gs = e.trafficGrants(u.ID); len(gs) != 2 {
		t.Fatalf("grants: %+v", gs)
	}
	if got := e.reload(u.ID).ExpiresAt.Int64; got != before.ExpiresAt.Int64+60*86400 {
		t.Fatalf("the term is 30 days (bonus) and 30 days (week pass) longer: %d -> %d", before.ExpiresAt.Int64, got)
	}
	var week db.TrafficGrant
	for _, g := range gs {
		if g.Lifetime == domain.LifetimeDays {
			week = g
		}
	}
	if !week.ExpiresAt.Valid || week.ExpiresAt.Int64 != e.now.Unix()+7*86400 {
		t.Fatalf("the traffic of a week: %+v", week)
	}

	// A pool.
	pool, err := e.s.d.Store.Q.CreateTrafficPool(context.Background(), db.CreateTrafficPoolParams{Name: "premium", CreatedAt: 1})
	if err != nil {
		t.Fatal(err)
	}
	if status, m = e.grant(granterToken, u.ID, fmt.Sprintf(`{"bytes":2147483648,"pool_id":%d,"reason":"pool gift","key":"pool-1"}`, pool.ID)); status != 200 || m["pool_id"] != float64(pool.ID) {
		t.Fatalf("pool: %d %v", status, m)
	}
	if gs = e.trafficGrants(u.ID); len(gs) != 3 {
		t.Fatalf("grants: %+v", gs)
	}

	// The journal names the addon, not an admin; the event tells what was given.
	var extend, grant int
	for _, a := range e.audit() {
		var d map[string]any
		_ = json.Unmarshal([]byte(a.Details.String), &d)
		if a.AdminID.Valid || a.TargetType.String != "user" || a.TargetID.String != fmt.Sprint(u.ID) || d["actor"] != "addon:granter" || d["reason"] == "" || d["key"] == "" {
			t.Errorf("audit line: %+v %v", a, d)
		}
		switch a.Action {
		case "user.extend":
			extend++
		case "user.grant":
			grant++
		}
	}
	if extend != 2 || grant != 3 {
		t.Fatalf("audit: %d extensions, %d grants", extend, grant)
	}
	evs := e.feed()
	if len(evs) != 4 {
		t.Fatalf("events: %d", len(evs))
	}
	var data map[string]any
	_ = json.Unmarshal([]byte(evs[2].Data), &data)
	if evs[2].Type != "user.granted" || evs[2].UserID.Int64 != u.ID || data["days"] != float64(30) ||
		data["bytes"] != float64(domain.GiB) || data["addon"] != "granter" {
		t.Fatalf("event: %+v %v", evs[2], data)
	}
	// The reason is the admin's: every addon with events reads the feed.
	if _, ok := data["reason"]; ok || strings.Contains(evs[2].Data, "week pass") {
		t.Fatalf("the event tells the reason: %s", evs[2].Data)
	}
	if got := e.changes.n.Load(); got < 4 {
		t.Fatalf("the nodes were told %d times", got)
	}
}

// What a grant refuses: nothing changes, the key stays free, no line or event is written.
func TestGrantRefusals(t *testing.T) {
	e := newUsersEnv(t)
	ctx := context.Background()
	u := e.user("Finn")
	disabled := e.user("Gus")
	yes := true
	if _, err := e.users.Update(ctx, disabled.ID, domain.Patch{Disabled: &yes}); err != nil {
		t.Fatal(err)
	}
	long := strings.Repeat("я", 201)
	cases := []struct {
		name, body string
		status     int
		code       string
	}{
		{"nothing to give", `{"reason":"x","key":"k"}`, 400, "bad_request"},
		{"a reason is needed", `{"days":1,"key":"k"}`, 400, "bad_request"},
		{"a blank reason", `{"days":1,"reason":"  ","key":"k"}`, 400, "bad_request"},
		{"a long reason", `{"days":1,"reason":"` + long + `","key":"k"}`, 400, "bad_request"},
		{"a key is needed", `{"days":1,"reason":"x"}`, 400, "bad_request"},
		{"a key with a slash", `{"days":1,"reason":"x","key":"a/b"}`, 400, "bad_request"},
		{"a key with a space", `{"days":1,"reason":"x","key":"a b"}`, 400, "bad_request"},
		{"a long key", `{"days":1,"reason":"x","key":"` + strings.Repeat("k", 65) + `"}`, 400, "bad_request"},
		{"zero days", `{"days":0,"reason":"x","key":"k"}`, 400, "bad_request"},
		{"negative days", `{"days":-5,"reason":"x","key":"k"}`, 400, "bad_request"},
		{"too many days", `{"days":3651,"reason":"x","key":"k"}`, 400, "bad_request"},
		{"too few bytes", `{"bytes":1073741823,"reason":"x","key":"k"}`, 400, "bad_request"},
		{"zero bytes", `{"bytes":0,"reason":"x","key":"k"}`, 400, "bad_request"},
		{"too many bytes", `{"bytes":109951162777601,"reason":"x","key":"k"}`, 400, "bad_request"},
		{"an unknown lifetime", `{"bytes":1073741824,"lifetime":"forever","reason":"x","key":"k"}`, 400, "bad_request"},
		{"days lifetime without traffic_days", `{"bytes":1073741824,"lifetime":"days","reason":"x","key":"k"}`, 400, "bad_request"},
		{"days lifetime with days only", `{"days":7,"bytes":1073741824,"lifetime":"days","reason":"x","key":"k"}`, 400, "bad_request"},
		{"traffic_days without lifetime days", `{"bytes":1073741824,"traffic_days":7,"reason":"x","key":"k"}`, 400, "bad_request"},
		{"traffic_days with another lifetime", `{"bytes":1073741824,"lifetime":"period","traffic_days":7,"reason":"x","key":"k"}`, 400, "bad_request"},
		{"traffic_days without traffic", `{"days":1,"traffic_days":7,"reason":"x","key":"k"}`, 400, "bad_request"},
		{"zero traffic_days", `{"bytes":1073741824,"lifetime":"days","traffic_days":0,"reason":"x","key":"k"}`, 400, "bad_request"},
		{"too many traffic_days", `{"bytes":1073741824,"lifetime":"days","traffic_days":3651,"reason":"x","key":"k"}`, 400, "bad_request"},
		{"a lifetime without traffic", `{"days":1,"lifetime":"used","reason":"x","key":"k"}`, 400, "bad_request"},
		{"a pool without traffic", `{"days":1,"pool_id":1,"reason":"x","key":"k"}`, 400, "bad_request"},
		{"pool zero", `{"bytes":1073741824,"pool_id":0,"reason":"x","key":"k"}`, 400, "bad_request"},
		{"a pool that is not there", `{"bytes":1073741824,"pool_id":9999,"reason":"x","key":"k"}`, 400, "bad_request"},
		{"a field of the old table", `{"traffic_bytes":1073741824,"reason":"x","key":"k"}`, 400, "bad_request"},
		{"not JSON", `days=1`, 400, "bad_request"},
		{"days of text", `{"days":"ten","reason":"x","key":"k"}`, 400, "bad_request"},
		{"empty", ``, 400, "bad_request"},
		{"days to a disabled user", `{"days":3,"reason":"x","key":"k"}`, 409, "user_disabled"},
	}
	for _, c := range cases {
		target := u.ID
		if c.code == "user_disabled" {
			target = disabled.ID
		}
		if status, m := e.grant(granterToken, target, c.body); status != c.status || code2(m) != c.code {
			t.Errorf("%s: %d %v", c.name, status, m)
		}
	}
	// A big body is cut off before it is read to its end.
	if status, _ := e.grant(granterToken, u.ID, `{"days":1,"reason":"`+strings.Repeat("x", 2<<20)+`","key":"k"}`); status != 400 {
		t.Errorf("a body of 2 MiB: %d", status)
	}
	// An unknown user, and ids that are not ids.
	if status, m := e.grant(granterToken, 99999, `{"days":1,"reason":"x","key":"nobody"}`); status != 404 || code2(m) != "not_found" {
		t.Errorf("unknown user: %d %v", status, m)
	}
	for _, path := range []string{"/users/0/grant", "/users/-1/grant", "/users/x/grant"} {
		if status, _ := e.do(granterToken, "POST", path, `{"days":1,"reason":"x","key":"k"}`); status != 400 {
			t.Errorf("%s: %d", path, status)
		}
	}
	// A disabled user still takes traffic: it does not turn the user on.
	if status, m := e.grant(granterToken, disabled.ID, `{"bytes":1073741824,"reason":"x","key":"gus-traffic"}`); status != 200 {
		t.Errorf("traffic to a disabled user: %d %v", status, m)
	}
	if e.reload(disabled.ID).Status != "disabled" {
		t.Error("a grant turned the user on")
	}

	// Nothing above (but the one traffic grant) left a key, a line, an event or days.
	if got := len(e.trafficGrants(u.ID)); got != 0 {
		t.Errorf("grants of the refused: %d", got)
	}
	if after := e.reload(u.ID); after.ExpiresAt != u.ExpiresAt {
		t.Errorf("the term moved: %v -> %v", u.ExpiresAt, after.ExpiresAt)
	}
	if n := len(e.feed()); n != 1 {
		t.Errorf("events: %d", n)
	}
	if n := len(e.audit()); n != 1 {
		t.Errorf("audit lines: %d", n)
	}
	if _, err := e.s.d.Store.Q.GetAddonGrantKey(ctx, db.GetAddonGrantKeyParams{AddonID: "granter", Key: "k"}); err == nil {
		t.Error("a refused grant kept its key")
	}
}

// Days that were applied are taken back with everything else when the traffic of the same
// call is refused, and the key is free for the call made right.
func TestGrantIsOneTransaction(t *testing.T) {
	e := newUsersEnv(t)
	u := e.user("Hal")
	before := e.reload(u.ID)
	if status, m := e.grant(granterToken, u.ID, `{"days":10,"bytes":1073741824,"pool_id":9999,"reason":"x","key":"half"}`); status != 400 {
		t.Fatalf("a pool that is not there: %d %v", status, m)
	}
	if after := e.reload(u.ID); after.ExpiresAt != before.ExpiresAt {
		t.Fatalf("the days stayed: %v -> %v", before.ExpiresAt, after.ExpiresAt)
	}
	if len(e.audit()) != 0 || len(e.feed()) != 0 {
		t.Fatalf("a refused grant left %d lines and %d events", len(e.audit()), len(e.feed()))
	}
	if status, m := e.grant(granterToken, u.ID, `{"days":10,"bytes":1073741824,"reason":"x","key":"half"}`); status != 200 || m["repeat"] != false {
		t.Fatalf("the same key made right: %d %v", status, m)
	}
}

// A key gives once: a repeat answers the first result and changes nothing; another user or
// another addon is told apart.
func TestGrantKeys(t *testing.T) {
	e := newUsersEnv(t)
	u, v := e.user("Ivy"), e.user("Jon")
	body := `{"days":14,"bytes":3221225472,"lifetime":"days","traffic_days":14,"reason":"referral","key":"ref:123"}`

	status, first := e.grant(granterToken, u.ID, body)
	if status != 200 || first["repeat"] != false {
		t.Fatalf("first: %d %v", status, first)
	}
	term := e.reload(u.ID).ExpiresAt
	*e.now = e.now.Add(time.Hour) // a repeat comes later: it answers the old result, not a new one

	status, again := e.grant(granterToken, u.ID, body)
	if status != 200 || again["repeat"] != true {
		t.Fatalf("repeat: %d %v", status, again)
	}
	delete(first, "repeat")
	delete(again, "repeat")
	if fmt.Sprint(first) != fmt.Sprint(again) {
		t.Fatalf("a repeat answers another result:\n %v\n %v", first, again)
	}
	// The same key with other numbers is still the same grant: the first result stands.
	if status, m := e.grant(granterToken, u.ID, `{"days":365,"reason":"changed my mind","key":"ref:123"}`); status != 200 || m["repeat"] != true || m["days"] != float64(14) {
		t.Fatalf("repeat with another body: %d %v", status, m)
	}
	if e.reload(u.ID).ExpiresAt != term || len(e.trafficGrants(u.ID)) != 1 || len(e.feed()) != 1 || len(e.audit()) != 2 {
		t.Fatalf("a repeat changed something: term %v, %d grants, %d events, %d lines", e.reload(u.ID).ExpiresAt, len(e.trafficGrants(u.ID)), len(e.feed()), len(e.audit()))
	}
	notified := e.changes.n.Load()
	if _, m := e.grant(granterToken, u.ID, body); m["repeat"] != true || e.changes.n.Load() != notified {
		t.Fatalf("a repeat told the nodes")
	}

	// The same key for another user is a conflict, and gives that user nothing.
	vTerm := e.reload(v.ID).ExpiresAt
	if status, m := e.grant(granterToken, v.ID, body); status != 409 || code2(m) != "conflict" {
		t.Fatalf("another user: %d %v", status, m)
	}
	if e.reload(v.ID).ExpiresAt != vTerm || len(e.trafficGrants(v.ID)) != 0 {
		t.Fatal("a conflict gave something")
	}
	// Keys are the addon's own: another addon may use the same one.
	if status, m := e.grant(otherToken, v.ID, body); status != 200 || m["repeat"] != false {
		t.Fatalf("another addon: %d %v", status, m)
	}
	if status, m := e.grant(bothToken, u.ID, body); status == 403 {
		t.Fatalf("both holds users:grant: %d %v", status, m)
	}
}

// Requests with one key at once give once.
func TestGrantKeyRace(t *testing.T) {
	e := newUsersEnv(t)
	u := e.user("Kim")
	before := e.reload(u.ID)
	const n = 8
	var wg sync.WaitGroup
	var fresh, repeats, failed atomic.Int32
	for range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			status, m := e.grant(granterToken, u.ID, `{"days":5,"bytes":1073741824,"reason":"race","key":"race-1"}`)
			switch {
			case status != 200:
				failed.Add(1)
			case m["repeat"] == true:
				repeats.Add(1)
			default:
				fresh.Add(1)
			}
		}()
	}
	wg.Wait()
	if fresh.Load() != 1 || repeats.Load() != n-1 || failed.Load() != 0 {
		t.Fatalf("%d given, %d repeats, %d failed", fresh.Load(), repeats.Load(), failed.Load())
	}
	if after := e.reload(u.ID); after.ExpiresAt.Int64 != before.ExpiresAt.Int64+5*86400 || len(e.trafficGrants(u.ID)) != 1 || len(e.feed()) != 1 {
		t.Fatalf("given more than once: %v, %d grants, %d events", after.ExpiresAt, len(e.trafficGrants(u.ID)), len(e.feed()))
	}
}

// An addon that reads the feed sees the grant, with the Telegram account of the owner.
func TestGrantEventCarriesTheOwner(t *testing.T) {
	e := newUsersEnv(t)
	u := e.user("Lea")
	if err := e.s.d.Store.Q.LinkTg(context.Background(), db.LinkTgParams{UserID: u.ID, TgID: 5150, CreatedAt: 1}); err != nil {
		t.Fatal(err)
	}
	if status, m := e.grant(granterToken, u.ID, `{"days":2,"reason":"thanks","key":"t1"}`); status != 200 {
		t.Fatalf("%d %v", status, m)
	}
	evs := e.feed()
	if len(evs) != 1 || evs[0].Type != "user.granted" || evs[0].TgID.Int64 != 5150 {
		t.Fatalf("events: %+v", evs)
	}
	var data map[string]any
	_ = json.Unmarshal([]byte(evs[0].Data), &data)
	if data["pool_id"] != nil || data["bytes"] != nil || data["days"] != float64(2) || data["addon"] != "granter" {
		t.Fatalf("data: %v", data)
	}
}

// A search that matches little is cut short, not run to the end of the table: the answer has
// what matched so far and a cursor to go on from, and following the cursors finds everything.
func TestUsersSearchIsBounded(t *testing.T) {
	e := newUsersEnv(t)
	oldBatch, oldCap := scanBatch, scanCap
	scanBatch, scanCap = 2, 4
	t.Cleanup(func() { scanBatch, scanCap = oldBatch, oldCap })
	var ids []int64
	for _, n := range []string{"u1", "u2", "u3", "u4", "u5", "needle", "u7"} {
		ids = append(ids, e.user(n).ID)
	}
	// Four users are read, none is "needle": an empty page, and the cursor at the fourth.
	status, b, raw := e.list("?q=needle")
	if status != 200 || len(b.Users) != 0 || b.Next == nil || *b.Next != ids[3] {
		t.Fatalf("the first chunk: %d %s", status, raw)
	}
	status, b, raw = e.list(fmt.Sprintf("?q=needle&after=%d", *b.Next))
	if status != 200 || fmt.Sprint(b.ids()) != fmt.Sprint(ids[5:6]) || b.Next != nil {
		t.Fatalf("the second chunk reaches the end: %d %s", status, raw)
	}
	// No search: pages as ever, whatever the scan's size.
	if status, b, raw = e.list("?limit=5"); status != 200 || len(b.Users) != 5 || b.Next == nil || *b.Next != ids[4] {
		t.Fatalf("a plain page: %d %s", status, raw)
	}
}
