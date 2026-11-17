package app

import (
	"encoding/json"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	"mikan/internal/panel/domain"
)

// A bulk action by filter takes the users the list shows for the same filter: the preview
// counts them as the list does, the action reaches them all and nobody else, past the 1000
// ids a list of ids may name. The set the admin confirmed (the preview's digest) is required
// and guards the action, the filter and the ids exclude each other, a read key reaches
// neither, and the journal says which filter it was.
func TestBulkByFilter(t *testing.T) {
	k := newKeyHarness(t)
	ctx := t.Context()
	tariffs, _ := k.st.Q.ListTariffs(ctx)
	resp, out := k.asKey(k.full, http.MethodPost, "/folders", map[string]any{"name": "Family"})
	var folder struct {
		ID int64 `json:"id"`
	}
	if resp.StatusCode != http.StatusCreated || json.Unmarshal(out, &folder) != nil {
		t.Fatalf("a folder: %d %s", resp.StatusCode, out)
	}
	ids := map[string]int64{}
	for _, name := range []string{"anna", "boris", "clara", "dmitry", "elena", "Фёдор", "gleb"} {
		u, err := k.p.users.Create(ctx, domain.CreateInput{Name: name, TariffID: tariffs[1].ID})
		if err != nil {
			t.Fatal(err)
		}
		ids[name] = u.ID
	}
	// Every part of the filter has users it keeps and users it leaves out.
	for _, q := range []struct {
		sql  string
		args []any
	}{
		{"UPDATE users SET note = 'VIP friend', folder_id = $2 WHERE id = $1", []any{ids["anna"], folder.ID}},
		{"UPDATE users SET source = 'bot', status = 'disabled' WHERE id = $1", []any{ids["boris"]}},
		{"UPDATE users SET hidden = 1 WHERE id = $1", []any{ids["clara"]}},
		{"UPDATE users SET expires_at = 1 WHERE id = $1", []any{ids["dmitry"]}},
		{"UPDATE users SET source = 'trial', hidden = 1, folder_id = $2, tags = '[\"vip\"]' WHERE id = $1", []any{ids["elena"], folder.ID}},
		{"UPDATE users SET traffic_limit = 1, used_down = 10 WHERE id = $1", []any{ids["gleb"]}},
	} {
		if _, err := k.st.DB.ExecContext(ctx, q.sql, q.args...); err != nil {
			t.Fatal(err)
		}
	}
	folderID := idOf(folder.ID)

	type preview struct {
		Requested, Found, Missing, Changed, Unchanged int
		Sample                                        []struct {
			UserID int64 `json:"user_id"`
		}
		Digest string `json:"digest"`
	}
	ask := func(key string, body map[string]any) (int, preview, string) {
		t.Helper()
		resp, out := k.asKey(key, http.MethodPost, "/users/bulk/preview", body)
		var p preview
		_ = json.Unmarshal(out, &p)
		return resp.StatusCode, p, string(out)
	}
	listIDs := func(query url.Values) (int, []int64) {
		t.Helper()
		query.Set("limit", "500")
		resp, body := k.do(http.MethodGet, k.api+"/users?"+query.Encode(), nil, nil)
		var l userList
		if resp.StatusCode != http.StatusOK || json.Unmarshal(body, &l) != nil {
			t.Fatalf("list %s: %d %s", query.Encode(), resp.StatusCode, body)
		}
		out := []int64{}
		for _, u := range l.Items {
			out = append(out, u.ID)
		}
		slices.Sort(out)
		return l.Total, out
	}

	// The same filter in the list's query and in the bulk body: the same users. An extension
	// changes everyone, so its sample (up to 10, in id order) is the whole set here.
	for _, f := range []map[string]string{
		{},
		{"state": "disabled"},
		{"state": "expired"},
		{"state": "limited"},
		{"state": "active"},
		{"state": "attention"},
		{"q": "vip"},
		{"q": "ФЁД"},
		{"q": "  anna "},
		{"folder": folderID},
		{"folder": "none"},
		{"source": "bot"},
		{"source": "trial", "hidden": "show"},
		{"hidden": "hide"},
		{"hidden": "only"},
		{"folder": folderID, "q": "vip", "hidden": "hide"},
		{"state": "active", "source": "admin", "hidden": "hide"},
		{"q": "nobody"},
	} {
		query, filter := url.Values{}, map[string]any{}
		for key, v := range f {
			query.Set(key, v)
			filter[key] = v
		}
		total, want := listIDs(query)
		code, p, out := ask(k.full, map[string]any{"filter": filter, "action": "extend"})
		got := []int64{}
		for _, s := range p.Sample {
			got = append(got, s.UserID)
		}
		if code != http.StatusOK || p.Requested != total || p.Found != total || p.Missing != 0 || p.Changed != total || !slices.Equal(got, want) {
			t.Errorf("filter %v: the list has %d %v, the preview %d %s", f, total, want, code, out)
		}
	}

	bulk := func(key string, body map[string]any) (int, string) {
		t.Helper()
		resp, out := k.asKey(key, http.MethodPost, "/users/bulk", body)
		return resp.StatusCode, string(out)
	}
	inFolder := map[string]any{"folder": folderID}
	digest := func(filter map[string]any) string {
		t.Helper()
		code, p, out := ask(k.full, map[string]any{"filter": filter, "action": "disable"})
		if code != http.StatusOK || len(p.Digest) != 64 {
			t.Fatalf("the preview of %v: %d %s", filter, code, out)
		}
		return p.Digest
	}
	disabled := func() []int64 {
		t.Helper()
		_, got := listIDs(url.Values{"state": {"disabled"}})
		return got
	}
	// By filter the action needs the set the preview named: without it nothing is done, not
	// even to everyone a bare filter matches.
	for name, body := range map[string]map[string]any{
		"delete everyone":  {"filter": map[string]any{}, "action": "delete"},
		"disable a folder": {"filter": inFolder, "action": "disable"},
	} {
		if code, out := bulk(k.full, body); code != http.StatusUnprocessableEntity || !strings.Contains(out, "body.expect") || !strings.Contains(out, "expect_required") {
			t.Errorf("%s without expect: %d %s", name, code, out)
		}
	}
	if total, _ := listIDs(url.Values{}); total != len(ids) {
		t.Fatalf("an action without expect deleted users: %d left", total)
	}
	// One user out of the folder and another in: the same number, other users. Nothing is done.
	stale := digest(inFolder)
	if _, err := k.st.DB.ExecContext(ctx, "UPDATE users SET folder_id = CASE WHEN id = $1 THEN NULL ELSE $3::bigint END WHERE id IN ($1, $2)", ids["elena"], ids["gleb"], folder.ID); err != nil {
		t.Fatal(err)
	}
	if code, p, _ := ask(k.full, map[string]any{"filter": inFolder, "action": "disable"}); code != http.StatusOK || p.Found != 2 || p.Digest == stale {
		t.Fatalf("the swap: %d %+v", code, p)
	}
	if code, out := bulk(k.full, map[string]any{"filter": inFolder, "action": "disable", "expect": stale}); code != http.StatusConflict || !strings.Contains(out, "filter_changed") {
		t.Fatalf("a swapped set: %d %s", code, out)
	}
	if got := disabled(); !slices.Equal(got, []int64{ids["boris"]}) {
		t.Fatalf("a refused action disabled users: %v", got)
	}
	// The set confirmed: the folder's two users now, and nobody else.
	if code, out := bulk(k.full, map[string]any{"filter": inFolder, "action": "disable", "expect": digest(inFolder)}); code != http.StatusOK || !strings.Contains(out, `"affected":2`) {
		t.Fatalf("disable by filter: %d %s", code, out)
	}
	if got := disabled(); !slices.Equal(got, []int64{ids["anna"], ids["boris"], ids["gleb"]}) {
		t.Fatalf("disabled after the action: %v", got)
	}
	var details string
	if err := k.st.DB.QueryRowContext(ctx, "SELECT details FROM audit_log WHERE action = 'user.bulk_disable' ORDER BY id DESC LIMIT 1").Scan(&details); err != nil ||
		!strings.Contains(details, `"filter":{`) || !strings.Contains(details, `"folder":"`+folderID+`"`) || !strings.Contains(details, `"count":2`) || !strings.Contains(details, `"requested":2`) {
		t.Fatalf("the journal: %q %v", details, err)
	}
	// expect is for a filter: beside ids it is refused, as is no user at all (an old client's
	// empty list), and a malformed one.
	for name, body := range map[string]map[string]any{
		"expect with ids":    {"ids": []int64{ids["anna"]}, "action": "enable", "expect": stale},
		"no ids":             {"ids": []int64{}, "action": "enable"},
		"a malformed digest": {"filter": inFolder, "action": "enable", "expect": "3"},
	} {
		if code, out := bulk(k.full, body); code != http.StatusUnprocessableEntity {
			t.Errorf("%s: %d %s", name, code, out)
		}
	}
	if code, out := bulk(k.full, map[string]any{"ids": []int64{ids["anna"]}, "action": "enable", "expect": stale}); !strings.Contains(out, "body.expect") || !strings.Contains(out, "expect_with_ids") {
		t.Errorf("expect with ids: %d %s", code, out)
	}
	if code, out := bulk(k.full, map[string]any{"filter": inFolder, "action": "enable", "expect": "3"}); !strings.Contains(out, "body.expect") {
		t.Errorf("a malformed digest's place: %d %s", code, out)
	}
	if code, out := bulk(k.full, map[string]any{"ids": []int64{}, "action": "enable"}); !strings.Contains(out, "body.ids") {
		t.Errorf("no ids: %d %s", code, out)
	}
	if got := disabled(); !slices.Equal(got, []int64{ids["anna"], ids["boris"], ids["gleb"]}) {
		t.Fatalf("a refused action enabled users: %v", got)
	}

	// Ids or a filter, one of the two; a bad folder is refused where it is.
	for name, body := range map[string]map[string]any{
		"both":         {"ids": []int64{ids["anna"]}, "filter": map[string]any{}, "action": "disable"},
		"neither":      {"action": "disable"},
		"a bad folder": {"filter": map[string]any{"folder": "family"}, "action": "disable"},
		"a bad state":  {"filter": map[string]any{"state": "sleepy"}, "action": "disable"},
	} {
		if code, _, out := ask(k.full, body); code != http.StatusUnprocessableEntity {
			t.Errorf("%s: %d %s", name, code, out)
		}
	}
	if code, _, out := ask(k.full, map[string]any{"filter": map[string]any{"folder": "family"}, "action": "disable"}); !strings.Contains(out, "body.filter.folder") {
		t.Errorf("a bad folder's place: %d %s", code, out)
	}
	// A read key previews nothing and changes nothing, by filter as by ids.
	if code, _, out := ask(k.read, map[string]any{"filter": map[string]any{}, "action": "disable"}); code != http.StatusForbidden {
		t.Errorf("a read key previews by filter: %d %s", code, out)
	}
	if code, out := bulk(k.read, map[string]any{"filter": map[string]any{}, "action": "delete"}); code != http.StatusForbidden {
		t.Errorf("a read key deletes by filter: %d %s", code, out)
	}

	// More users than a list of ids may name: the filter reaches them all in one action.
	const many = 1200
	if _, err := k.st.DB.ExecContext(ctx, `INSERT INTO users (name, period_start, sub_token, created_at, updated_at, expires_at)
		SELECT 'bulk-' || g, 0, 'bulk-token-' || g, 0, 0, $1 FROM generate_series(1, $2::int) g`, k.now.Add(24*time.Hour).Unix(), many); err != nil {
		t.Fatal(err)
	}
	byName := map[string]any{"q": "bulk-"}
	if total, _ := listIDs(url.Values{"q": {"bulk-"}}); total != many {
		t.Fatalf("the list of the many: %d", total)
	}
	manyCode, p, manyOut := ask(k.full, map[string]any{"filter": byName, "action": "hide"})
	if manyCode != http.StatusOK || p.Found != many || p.Changed != many || len(p.Sample) != domain.BulkSampleSize {
		t.Fatalf("the preview of the many: %d %s", manyCode, manyOut)
	}
	if code, out := bulk(k.full, map[string]any{"filter": byName, "action": "hide", "expect": p.Digest}); code != http.StatusOK || !strings.Contains(out, `"affected":1200`) {
		t.Fatalf("hide the many: %d %s", code, out)
	}
	if total, _ := listIDs(url.Values{"q": {"bulk-"}, "hidden": {"only"}}); total != many {
		t.Fatalf("hidden of the many: %d", total)
	}
	if total, _ := listIDs(url.Values{"hidden": {"only"}}); total != many+2 { // clara and elena
		t.Fatalf("the action hid others: %d hidden", total)
	}
}
