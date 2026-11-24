package app

import (
	"context"
	"encoding/json"
	"maps"
	"net/http"
	"slices"
	"strconv"
	"testing"
	"time"
)

// Templates of clients and batches over HTTP (#295): a template keeps what its clients get
// over the tariff and refuses a tariff, a folder or a group that is not there, a name taken in
// any letter case and a pattern without the number; a batch makes every client of its spec
// under the names of its pattern, with their links, or none of them when one name is taken; a
// single client takes the same spec; a deleted folder lets its templates go.
func TestUserTemplatesAndBatchOverHTTP(t *testing.T) {
	k := newKeyHarness(t)
	ctx := context.Background()
	q := k.st.Q
	tariffs, err := q.ListTariffs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	std := tariffs[1]
	call := func(method, path string, body any) (int, []byte) {
		t.Helper()
		resp, raw := k.do(method, k.api+path, body, k.csrf)
		return resp.StatusCode, raw
	}
	created := func(path string, body any) int64 {
		t.Helper()
		code, raw := call(http.MethodPost, path, body)
		var v struct {
			ID int64 `json:"id"`
		}
		if code != http.StatusCreated || json.Unmarshal(raw, &v) != nil {
			t.Fatalf("POST %s: %d %s", path, code, raw)
		}
		return v.ID
	}
	folder := created("/folders", map[string]any{"name": "School"})
	group := created("/groups", map[string]any{"name": "Pupils"})

	tpl := map[string]any{"name": "Class 7", "tariff_id": std.ID, "traffic_limit": 50 << 30, "device_limit": 0, "term_days": 10,
		"starts_on_connect": false, "folder_id": folder, "group_id": group, "tags": []string{"school", " 7a "}, "note": "class 7", "name_pattern": "pupil-{nn}"}
	code, raw := call(http.MethodPost, "/user-templates", tpl)
	type templateView struct {
		ID           int64    `json:"id"`
		Name         string   `json:"name"`
		TrafficLimit *int64   `json:"traffic_limit"`
		DeviceLimit  *int64   `json:"device_limit"`
		SpeedLimit   *int64   `json:"speed_limit"`
		FolderID     *int64   `json:"folder_id"`
		Tags         []string `json:"tags"`
		NamePattern  string   `json:"name_pattern"`
	}
	var view templateView
	if code != http.StatusCreated || json.Unmarshal(raw, &view) != nil {
		t.Fatalf("create template: %d %s", code, raw)
	}
	if view.TrafficLimit == nil || *view.TrafficLimit != 50<<30 || view.DeviceLimit == nil || *view.DeviceLimit != 0 || view.SpeedLimit != nil ||
		!slices.Equal(view.Tags, []string{"school", "7a"}) || view.NamePattern != "pupil-{nn}" {
		t.Fatalf("template: %s", raw)
	}

	with := func(over map[string]any) map[string]any {
		out := maps.Clone(tpl)
		out["name"] = "Other"
		maps.Copy(out, over)
		return out
	}
	for _, c := range []struct {
		over     map[string]any
		status   int
		location string
		code     string
	}{
		{map[string]any{"name": "CLASS 7"}, http.StatusConflict, "body.name", "user_template_name_taken"},
		{map[string]any{"tariff_id": 9999}, http.StatusUnprocessableEntity, "body.tariff_id", "tariff_not_found"},
		{map[string]any{"folder_id": 9999}, http.StatusUnprocessableEntity, "body.folder_id", "folder_not_found"},
		{map[string]any{"group_id": 9999}, http.StatusUnprocessableEntity, "body.group_id", "group_not_found"},
		{map[string]any{"name_pattern": "pupil"}, http.StatusUnprocessableEntity, "body.name_pattern", "name_pattern_number"},
	} {
		code, raw := call(http.MethodPost, "/user-templates", with(c.over))
		var r refusal
		_ = json.Unmarshal(raw, &r)
		if code != c.status || r.code(c.location) != c.code {
			t.Errorf("template %v: %d %s, want %d %s", c.over, code, raw, c.status, c.code)
		}
	}

	type batchUser struct {
		ID     int64  `json:"id"`
		Name   string `json:"name"`
		SubURL string `json:"sub_url"`
	}
	batch := func(start int) (int, []batchUser, refusal) {
		t.Helper()
		body := maps.Clone(tpl)
		delete(body, "name")
		maps.Copy(body, map[string]any{"count": 3, "start": start, "template_id": view.ID})
		code, raw := call(http.MethodPost, "/users/batch", body)
		var out struct {
			Users []batchUser `json:"users"`
		}
		var r refusal
		if code == http.StatusCreated {
			_ = json.Unmarshal(raw, &out)
		} else {
			_ = json.Unmarshal(raw, &r)
		}
		return code, out.Users, r
	}
	code, made, _ := batch(9)
	if code != http.StatusCreated || len(made) != 3 {
		t.Fatalf("batch: %d %+v", code, made)
	}
	for i, want := range []string{"pupil-09", "pupil-10", "pupil-11"} {
		u := made[i]
		if u.Name != want || u.SubURL == "" {
			t.Errorf("client %d: %+v, want %s with a link", i, u, want)
		}
		row, err := q.GetUser(ctx, u.ID)
		if err != nil {
			t.Fatal(err)
		}
		// Over the tariff: its own traffic, no device limit, its speed, a term of 10 days from now.
		term := time.Unix(row.ExpiresAt.Int64, 0).Sub(k.now)
		if row.TariffID.Int64 != std.ID || row.TrafficLimit.Int64 != 50<<30 || row.DeviceLimit.Valid || row.SpeedLimit != std.SpeedLimit ||
			term < 10*24*time.Hour-time.Minute || term > 10*24*time.Hour+time.Minute || row.FolderID.Int64 != folder ||
			row.Note != "class 7" || row.Tags != `["school","7a"]` {
			t.Errorf("client %s: %+v", want, row)
		}
		if g, err := q.GetUserGroup(ctx, u.ID); err != nil || g != group {
			t.Errorf("client %s group %d, %v", want, g, err)
		}
	}

	// Two of the next batch's names are taken: no client of it is made.
	all, _ := q.ListUsers(ctx)
	before := len(all)
	code, _, r := batch(10)
	if code != http.StatusConflict || r.code("body.name_pattern") != "user_names_taken" {
		t.Fatalf("taken names: %d %+v", code, r)
	}
	if len(r.Errors) == 0 || r.Errors[0].Value != "pupil-10, pupil-11" {
		t.Errorf("taken names named: %+v", r.Errors)
	}
	if all, _ := q.ListUsers(ctx); len(all) != before {
		t.Errorf("a refused batch made %d clients", len(all)-before)
	}

	// A client made alone takes the same spec.
	code, raw = call(http.MethodPost, "/users", map[string]any{"name": "teacher", "tariff_id": std.ID, "speed_limit": 20, "folder_id": folder, "template_id": view.ID})
	var one struct {
		SpeedLimit *int64 `json:"speed_limit"`
		FolderID   *int64 `json:"folder_id"`
	}
	if code != http.StatusCreated || json.Unmarshal(raw, &one) != nil || one.SpeedLimit == nil || *one.SpeedLimit != 20 || one.FolderID == nil || *one.FolderID != folder {
		t.Errorf("one client: %d %s", code, raw)
	}

	// A template is changed whole; a deleted folder lets it go; a deleted template is gone.
	path := "/user-templates/" + strconv.FormatInt(view.ID, 10)
	whole := with(map[string]any{"name": "Class 7"})
	delete(whole, "traffic_limit") // left out: the tariff's again
	if code, raw := call(http.MethodPut, path, whole); code != http.StatusOK || json.Unmarshal(raw, &view) != nil || view.TrafficLimit != nil {
		t.Fatalf("put template: %d %s", code, raw)
	}
	if code, raw := call(http.MethodDelete, "/folders/"+strconv.FormatInt(folder, 10), nil); code != http.StatusNoContent {
		t.Fatalf("delete folder: %d %s", code, raw)
	}
	code, raw = call(http.MethodGet, "/user-templates", nil)
	var list []templateView
	if code != http.StatusOK || json.Unmarshal(raw, &list) != nil || len(list) != 1 || list[0].FolderID != nil {
		t.Fatalf("templates after the folder went: %d %s", code, raw)
	}
	if code, _ := call(http.MethodDelete, path, nil); code != http.StatusNoContent {
		t.Fatalf("delete template: %d", code)
	}
	if code, _ := call(http.MethodDelete, path, nil); code != http.StatusNotFound {
		t.Errorf("delete again: %d", code)
	}
}

// An operator of support who is not shown the subscribers makes no batch: its refusal of a
// taken name would tell whether a name is a subscriber's, as a search would. With the
// subscribers shown, an operator who may change things makes one.
func TestUserBatchOfSupportOperator(t *testing.T) {
	s := newSupportHarness(t)
	tariffs, err := s.st.Q.ListTariffs(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	body := map[string]any{"count": 1, "name_pattern": "op-{n}", "tariff_id": tariffs[1].ID}
	for _, show := range []bool{false, true} {
		g, code, out := s.grant(map[string]any{"hours": 2, "level": "change", "show_people": show})
		if code != http.StatusCreated {
			t.Fatalf("grant: %d %s", code, out)
		}
		op := s.newOperator()
		if resp, raw := op.enter(g.Token); resp.StatusCode != http.StatusOK {
			t.Fatalf("enter: %d %s", resp.StatusCode, raw)
		}
		resp, raw := op.write(http.MethodPost, "/users/batch", body)
		switch {
		case !show && (resp.StatusCode != http.StatusForbidden || detail(raw) != "support_denied"):
			t.Errorf("an operator without the subscribers: %d %s", resp.StatusCode, raw)
		case show && resp.StatusCode != http.StatusCreated:
			t.Errorf("an operator with the subscribers: %d %s", resp.StatusCode, raw)
		}
	}
}
