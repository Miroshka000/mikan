package subsrc

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"regexp"
	"testing"
	"time"

	"mikan/internal/panel/domain"
	"mikan/internal/panel/settings"
	"mikan/internal/panel/store"
	"mikan/internal/panel/store/db"
	"mikan/internal/panel/store/storetest"
	"mikan/internal/sub/apps"
	"mikan/internal/sub/serve"
)

func openStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := storetest.Open(context.Background(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func newUser(t *testing.T, st *store.Store, i int) db.User {
	t.Helper()
	u, err := st.Q.CreateUser(context.Background(), db.CreateUserParams{Name: fmt.Sprint("u", i), SubToken: fmt.Sprintf("%024d", i), PeriodStart: 1, CreatedAt: 1, UpdatedAt: 1,
		ResetStrategy: "none", Tags: "[]", Source: "admin"})
	if err != nil {
		t.Fatal(err)
	}
	return u
}

func testRule(name string, do apps.Action) apps.Rule {
	return apps.Rule{Name: name, Enabled: true, Do: do, When: apps.Group{Op: apps.Or,
		Conds:  []apps.Cond{{Header: "User-Agent", Op: apps.OpRegex, Value: `(?i)^` + name}},
		Groups: []apps.Inner{{Op: apps.And, Conds: []apps.Cond{{Header: "X-Device-Os", Op: apps.OpEquals, Value: "ios"}, {Header: "X-Hwid", Op: apps.OpEquals, Value: "", Not: true}}}}}}
}

func TestRulesKeepTheirOrderAndContent(t *testing.T) {
	ctx := context.Background()
	st := openStore(t)
	want := []apps.Rule{
		testRule("a", apps.Action{Respond: apps.Respond403, Headers: []apps.Header{{Name: "X-A", Value: "1"}}}),
		testRule("b", apps.Action{Respond: apps.RespondHTML, HTML: "<p>Use Happ</p>"}),
		testRule("c", apps.Action{Respond: apps.RespondServe, Format: serve.FormatClash, SkipHWID: true}),
	}
	want[2].Enabled = false
	if err := SaveRules(ctx, st, want); err != nil {
		t.Fatal(err)
	}
	got, err := LoadRules(ctx, st.Q)
	if err != nil {
		t.Fatal(err)
	}
	for i := range want {
		want[i] = want[i].Clean()
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v\nwant %+v", got, want)
	}
	// Saving again replaces: nothing of the old list stays.
	if err := SaveRules(ctx, st, want[1:2]); err != nil {
		t.Fatal(err)
	}
	if got, _ := LoadRules(ctx, st.Q); len(got) != 1 || got[0].Name != "b" {
		t.Errorf("%+v", got)
	}
	if err := SaveRules(ctx, st, nil); err != nil {
		t.Fatal(err)
	}
	if got, err := LoadRules(ctx, st.Q); err != nil || len(got) != 0 {
		t.Errorf("%+v %v", got, err)
	}
}

func TestBlockedAppsAreSortedAndOnce(t *testing.T) {
	ctx := context.Background()
	st := openStore(t)
	if err := SaveBlocked(ctx, st, []string{"incy", "happ", "incy"}); err != nil {
		t.Fatal(err)
	}
	if got, err := LoadBlocked(ctx, st.Q); err != nil || !reflect.DeepEqual(got, []string{"happ", "incy"}) {
		t.Errorf("%v %v", got, err)
	}
}

// One bad row, left by a panel of another version or an edit by hand, must not stop every
// subscription: it is left out.
func TestLoadPolicyLeavesBadRowsOut(t *testing.T) {
	ctx := context.Background()
	st := openStore(t)
	set := settings.New(st.Q)
	good := testRule("good", apps.Action{Respond: apps.Respond403})
	if err := SaveRules(ctx, st, []apps.Rule{good}); err != nil {
		t.Fatal(err)
	}
	cond, _ := json.Marshal(good.When)
	for i, row := range []db.InsertSubRuleParams{
		{Name: "no json", Cond: "{", Action: `{"respond":"403"}`},
		{Name: "bad regex", Cond: `{"op":"and","conds":[{"header":"User-Agent","op":"regex","value":"("}],"groups":[]}`, Action: `{"respond":"403"}`},
		{Name: "no such answer", Cond: string(cond), Action: `{"respond":"redirect"}`},
	} {
		row.Position, row.Enabled = int64(10+i), true
		if err := st.Q.InsertSubRule(ctx, row); err != nil {
			t.Fatal(err)
		}
	}
	if err := SaveBlocked(ctx, st, []string{"happ"}); err != nil {
		t.Fatal(err)
	}
	if err := st.Q.InsertBlockedApp(ctx, "gone-from-the-catalog"); err != nil {
		t.Fatal(err)
	}
	if err := settings.Set(ctx, set, settings.KeySubBlockedText, "Use INCY"); err != nil {
		t.Fatal(err)
	}
	p, err := LoadPolicy(ctx, st.Q, set, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Rules) != 1 || p.Rules[0].Name != "good" || !reflect.DeepEqual(p.Blocked, []string{"happ"}) || p.Text != "Use INCY" || p.NoBuiltin {
		t.Errorf("%+v", p)
	}
	if err := settings.Set(ctx, set, settings.KeySubBuiltin, false); err != nil {
		t.Fatal(err)
	}
	if p, _ := LoadPolicy(ctx, st.Q, set, nil); !p.NoBuiltin {
		t.Error("the built-in rules are switched off")
	}
	if _, err := apps.NewResponder(p); err != nil {
		t.Errorf("what LoadPolicy gives compiles: %v", err)
	}
}

func TestRecordWritesWhatAskedAndCapsAHammeredLink(t *testing.T) {
	ctx := context.Background()
	st := openStore(t)
	now := time.Unix(1_800_000_000, 0)
	h := NewHandler(st, func(context.Context) (Config, error) { return Config{}, nil }, nil, func() time.Time { return now }, nil, false)
	u := newUser(t, st, 1)
	sub := &subscription{h: h, u: u, cfg: Config{LogDays: 14}}
	e := serve.LogEntry{At: now, IP: "192.0.2.7", Agent: "Happ/3.4.1", App: "happ", Rule: "builtin:old", Format: serve.FormatURI, Outcome: serve.OutcomeStub}
	sub.Record(ctx, e)
	rows, err := st.Q.ListSubLog(ctx, db.ListSubLogParams{UserID: u.ID, Limit: 10})
	if err != nil || len(rows) != 1 {
		t.Fatalf("%v %+v", err, rows)
	}
	if r := rows[0]; r.At != now.Unix() || r.Ip != e.IP || r.Agent != e.Agent || r.App != "happ" || r.Rule != "builtin:old" || r.Format != "uri" || r.Outcome != "stub" {
		t.Errorf("%+v", r)
	}
	for range 100 {
		sub.Record(ctx, e)
	}
	if rows, _ := st.Q.ListSubLog(ctx, db.ListSubLogParams{UserID: u.ID, Limit: 200}); len(rows) != maxLogPerMinute {
		t.Errorf("%d rows in a minute, want %d", len(rows), maxLogPerMinute)
	}
	// The cap is the link's own: another user's requests still go in; a minute later too.
	other := newUser(t, st, 2)
	(&subscription{h: h, u: other, cfg: Config{LogDays: 14}}).Record(ctx, e)
	e.At = now.Add(2 * time.Minute)
	sub.Record(ctx, e)
	if rows, _ := st.Q.ListSubLog(ctx, db.ListSubLogParams{UserID: other.ID, Limit: 10}); len(rows) != 1 {
		t.Errorf("another user's log: %d rows", len(rows))
	}
	if rows, _ := st.Q.ListSubLog(ctx, db.ListSubLogParams{UserID: u.ID, Limit: 200}); len(rows) != maxLogPerMinute+1 {
		t.Errorf("%d rows after a minute", len(rows))
	}
	// With the log off nothing is written.
	off := &subscription{h: h, u: newUser(t, st, 3), cfg: Config{LogDays: 0}}
	off.Record(ctx, e)
	if rows, _ := st.Q.ListSubLog(ctx, db.ListSubLogParams{UserID: off.u.ID, Limit: 10}); len(rows) != 0 {
		t.Errorf("%d rows with the log off", len(rows))
	}
}

func TestUserLogIsNewestFirstAndPrunedByRetention(t *testing.T) {
	ctx := context.Background()
	st := openStore(t)
	set := settings.New(st.Q)
	now := time.Unix(1_800_000_000, 0)
	u := newUser(t, st, 1)
	other := newUser(t, st, 2)
	day := 24 * time.Hour
	for _, ago := range []time.Duration{20 * day, 15 * day, 13 * day, time.Hour} {
		for _, id := range []int64{u.ID, other.ID} {
			if _, err := st.Q.AddSubLog(ctx, db.AddSubLogParams{UserID: id, At: now.Add(-ago).Unix(), Ip: "192.0.2.1", Agent: "x", Outcome: "served", MaxPerMinute: 30}); err != nil {
				t.Fatal(err)
			}
		}
	}
	items, err := UserLog(ctx, st.Q, u.ID, 3)
	if err != nil || len(items) != 3 {
		t.Fatalf("%v %d", err, len(items))
	}
	if !items[0].At.After(items[1].At) || !items[1].At.After(items[2].At) || items[0].Outcome != "served" {
		t.Errorf("%+v", items)
	}
	// 14 days by default: the two oldest of each user go.
	if n, err := PruneLog(ctx, st.Q, set, now); err != nil || n != 4 {
		t.Fatalf("pruned %d: %v", n, err)
	}
	if items, _ := UserLog(ctx, st.Q, u.ID, 10); len(items) != 2 {
		t.Errorf("%d left", len(items))
	}
	// A shorter retention, and then none: what was written goes with the log.
	if err := settings.Set(ctx, set, settings.KeySubLogDays, 1); err != nil {
		t.Fatal(err)
	}
	if n, _ := PruneLog(ctx, st.Q, set, now); n != 2 {
		t.Errorf("one day pruned %d", n)
	}
	if err := settings.Set(ctx, set, settings.KeySubLogDays, 0); err != nil {
		t.Fatal(err)
	}
	if n, _ := PruneLog(ctx, st.Q, set, now.Add(time.Minute)); n != 2 {
		t.Errorf("log off pruned %d", n)
	}
}

func TestLogDays(t *testing.T) {
	ctx := context.Background()
	set := settings.New(openStore(t).Q)
	if got := LogDays(ctx, set); got != DefaultLogDays || DefaultLogDays != 14 {
		t.Errorf("default %d", got)
	}
	for in, want := range map[int]int{0: 0, 1: 1, 90: 90, 91: DefaultLogDays, -1: DefaultLogDays} {
		if err := settings.Set(ctx, set, settings.KeySubLogDays, in); err != nil {
			t.Fatal(err)
		}
		if got := LogDays(ctx, set); got != want {
			t.Errorf("%d days set: %d, want %d", in, got, want)
		}
	}
}

// binder is a Binder that remembers what it was asked to require.
type binder struct {
	Binder
	required []bool
}

func (b *binder) Bind(_ context.Context, _ db.User, _ domain.DeviceInfo, requireHWID bool) (db.Slot, error) {
	b.required = append(b.required, requireHWID)
	return db.Slot{Name: "s", Uuid: "u", Secret: "x"}, nil
}

// A rule that skips the device id lets an app that sends none get its keys, whatever the
// panel requires of the other apps.
func TestSkipHWIDWaivesWhatThePanelRequires(t *testing.T) {
	ctx := context.Background()
	st := openStore(t)
	b := &binder{}
	h := NewHandler(st, nil, nil, time.Now, b, false)
	sub := &subscription{h: h, u: newUser(t, st, 1), cfg: Config{Config: serve.Config{Binding: true, RequireHWID: true}}}
	for _, dev := range []serve.Device{{}, {SkipHWID: true}} {
		if _, err := sub.Keys(ctx, dev); err != nil {
			t.Fatal(err)
		}
	}
	if !reflect.DeepEqual(b.required, []bool{true, false}) {
		t.Errorf("required: %v", b.required)
	}
}

// The page's /info names the blocked apps, which its catalog leaves out.
func TestInfoNamesTheBlockedApps(t *testing.T) {
	st := openStore(t)
	u := newUser(t, st, 1)
	resp, err := apps.NewResponder(apps.Policy{Blocked: []string{"streisand", "happ"}})
	if err != nil {
		t.Fatal(err)
	}
	cfg := Config{Config: serve.Config{Brand: "Mikan", Responses: resp}}
	h := NewHandler(st, func(context.Context) (Config, error) { return cfg, nil }, nil, time.Now, nil, false)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/"+u.SubToken+"/info", nil))
	var info Info
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &info) != nil {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	if !reflect.DeepEqual(info.BlockedApps, []string{"happ", "streisand"}) {
		t.Errorf("blocked apps %v", info.BlockedApps)
	}
	cfg.Responses = nil
	w = httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/"+u.SubToken+"/info", nil))
	if raw := w.Body.String(); regexp.MustCompile(`blocked_apps`).MatchString(raw) {
		t.Errorf("an empty list is left out: %s", raw)
	}
}

// The page names its apps by the catalog's ids (web/src/sub/apps.ts): an id the panel does
// not know would never be hidden by the list.
func TestPageCatalogUsesKnownAppIDs(t *testing.T) {
	raw, err := os.ReadFile("../../../web/src/sub/apps.ts")
	if err != nil {
		t.Skip("the web sources are not here")
	}
	ids := regexp.MustCompile(`\bid: "([a-z0-9]+)"`).FindAllStringSubmatch(string(raw), -1)
	if len(ids) == 0 {
		t.Fatal("the page's catalog names no app by id")
	}
	for _, m := range ids {
		if !apps.ValidID(m[1]) {
			t.Errorf("web/src/sub/apps.ts names %q, which is not in apps.Catalog", m[1])
		}
	}
}
