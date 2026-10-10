package app

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"mikan/internal/panel/domain"
	"mikan/internal/panel/settings"
	"mikan/internal/panel/store/db"
)

// Renaming clients and devices and banning devices, end to end over HTTP: the admin's API,
// the subscription page, the profiles a banned device gets, the journal.
func TestRenameAndBanOverHTTP(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	if err := domain.Seed(ctx, h.st, h.now); err != nil {
		t.Fatal(err)
	}
	set := settings.New(h.st.Q)
	for k, v := range map[string]any{settings.KeyPublicHost: "203.0.113.10", settings.KeyPanelPort: 21355} {
		if err := settings.Set(ctx, set, k, v); err != nil {
			t.Fatal(err)
		}
	}
	clock := func() time.Time { return h.now }
	tariffs, _ := h.st.Q.ListTariffs(ctx)
	users := domain.NewUsers(h.st, domain.NewPool(h.st, clock), noChanges{}, clock)
	u, err := users.Create(ctx, domain.CreateInput{Name: "a", TariffID: tariffs[1].ID})
	if err != nil {
		t.Fatal(err)
	}
	other, err := users.Create(ctx, domain.CreateInput{Name: "b", TariffID: tariffs[1].ID})
	if err != nil {
		t.Fatal(err)
	}
	sub := "/" + subPath + "/" + u.SubToken
	fetch := func(token, hwid string) (*http.Response, string) {
		hdr := map[string]string{"User-Agent": "Happ/3.4.1"}
		if hwid != "" {
			hdr["X-Hwid"], hdr["X-Device-Os"], hdr["X-Device-Model"] = hwid, "Android", "Pixel 9"
		}
		resp, body := h.do(http.MethodGet, "/"+subPath+"/"+token, nil, hdr)
		links, _ := base64.StdEncoding.DecodeString(string(body))
		return resp, string(links)
	}
	notice := func(links string) string {
		name, _ := url.PathUnescape(links[strings.LastIndex(links, "#")+1:])
		return name
	}
	const phone, tablet = "phone-0123456789", "tablet-0123456789"
	for _, id := range []string{phone, tablet} {
		if resp, links := fetch(u.SubToken, id); resp.StatusCode != http.StatusOK || strings.Contains(links, "127.0.0.1:1") {
			t.Fatalf("%s: %d %q", id, resp.StatusCode, links)
		}
	}
	if _, links := fetch(other.SubToken, "other-0123456789"); strings.Contains(links, "127.0.0.1:1") {
		t.Fatalf("the other user's device: %q", links)
	}
	foreign, _ := h.st.Q.ListBoundDevices(ctx, other.ID)

	if resp, _ := h.login(password, ""); resp.StatusCode != http.StatusOK {
		t.Fatalf("login: %d", resp.StatusCode)
	}
	csrf := map[string]string{"X-CSRF-Token": h.csrf}
	userAPI := "/" + adminPath + "/api/v1/users/" + strconv.FormatInt(u.ID, 10)
	var bound []struct {
		ID   int64  `json:"id"`
		Name string `json:"name"`
		HWID string `json:"hwid"`
	}
	resp, body := h.do(http.MethodGet, userAPI+"/bound-devices", nil, nil)
	if resp.StatusCode != http.StatusOK || json.Unmarshal(body, &bound) != nil || len(bound) != 2 {
		t.Fatalf("bound: %d %s", resp.StatusCode, body)
	}
	dev := func(hwid string) int64 {
		for _, d := range bound {
			if d.HWID == hwid {
				return d.ID
			}
		}
		t.Fatalf("no device %s", hwid)
		return 0
	}
	errCode := func(body []byte) string {
		var e struct {
			Detail string `json:"detail"`
			Errors []struct {
				Message string `json:"message"`
			} `json:"errors"`
		}
		_ = json.Unmarshal(body, &e)
		if len(e.Errors) > 0 {
			return e.Errors[0].Message
		}
		return e.Detail
	}

	// The client: a new name, nothing else; a blank or broken one is refused.
	resp, body = h.do(http.MethodPatch, userAPI, map[string]any{"name": "  <b>Иван</b>  "}, csrf)
	var view struct {
		Name   string `json:"name"`
		SubURL string `json:"sub_url"`
	}
	if resp.StatusCode != http.StatusOK || json.Unmarshal(body, &view) != nil || view.Name != "<b>Иван</b>" || !strings.HasSuffix(view.SubURL, "/"+u.SubToken) {
		t.Fatalf("rename the client: %d %s", resp.StatusCode, body)
	}
	for bad, code := range map[string]string{"   ": domain.CodeUserNameEmpty, "a\nb": domain.CodeNameChars} {
		if resp, body := h.do(http.MethodPatch, userAPI, map[string]any{"name": bad}, csrf); resp.StatusCode != http.StatusUnprocessableEntity || errCode(body) != code {
			t.Fatalf("name %q: %d %s", bad, resp.StatusCode, body)
		}
	}
	if resp, _ := h.do(http.MethodPatch, userAPI, map[string]any{"name": strings.Repeat("я", 101)}, csrf); resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("a name too long: %d", resp.StatusCode)
	}
	if resp, links := fetch(u.SubToken, phone); resp.StatusCode != http.StatusOK || strings.Contains(links, "127.0.0.1:1") {
		t.Fatalf("the keys after the rename: %q", links)
	}

	// The device, by the admin: another client's device is not found, the name is checked.
	deviceAPI := userAPI + "/bound-devices/" + strconv.FormatInt(dev(phone), 10)
	resp, body = h.do(http.MethodPatch, deviceAPI, map[string]any{"name": " Мамин <i>телефон</i> "}, csrf)
	var renamed struct {
		Name string `json:"name"`
	}
	if resp.StatusCode != http.StatusOK || json.Unmarshal(body, &renamed) != nil || renamed.Name != "Мамин <i>телефон</i>" {
		t.Fatalf("rename the device: %d %s", resp.StatusCode, body)
	}
	if resp, _ := h.do(http.MethodPatch, userAPI+"/bound-devices/"+strconv.FormatInt(foreign[0].ID, 10), map[string]any{"name": "x"}, csrf); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("another client's device: %d", resp.StatusCode)
	}
	if resp, _ := h.do(http.MethodPatch, deviceAPI, map[string]any{"name": strings.Repeat("x", 41)}, csrf); resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("a device name too long: %d", resp.StatusCode)
	}
	if resp, body := h.do(http.MethodPatch, deviceAPI, map[string]any{"name": "a‮b"}, csrf); resp.StatusCode != http.StatusUnprocessableEntity || errCode(body) != domain.CodeNameChars {
		t.Fatalf("a device name turned around: %d %s", resp.StatusCode, body)
	}

	// The device, by the subscriber: from the page only, only the subscription's own.
	pageName := sub + "/devices/" + strconv.FormatInt(dev(tablet), 10) + "/name"
	same := map[string]string{"Sec-Fetch-Site": "same-origin"}
	if resp, _ := h.do(http.MethodPost, pageName, map[string]string{"name": "x"}, map[string]string{"Sec-Fetch-Site": "cross-site"}); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("a rename from another site: %d", resp.StatusCode)
	}
	if resp, _ := h.do(http.MethodPost, sub+"/devices/"+strconv.FormatInt(foreign[0].ID, 10)+"/name", map[string]string{"name": "x"}, same); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("another subscription's device: %d", resp.StatusCode)
	}
	if resp, body := h.do(http.MethodPost, pageName, map[string]string{"name": strings.Repeat("x", 41)}, same); resp.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(string(body), domain.CodeDeviceNameLong) {
		t.Fatalf("a subscriber's name too long: %d %s", resp.StatusCode, body)
	}
	if resp, _ := h.do(http.MethodPost, pageName, map[string]string{"name": "Планшет"}, same); resp.StatusCode != http.StatusOK {
		t.Fatalf("the subscriber renames: %d", resp.StatusCode)
	}
	resp, body = h.do(http.MethodGet, sub+"/info", nil, nil)
	var info struct {
		Devices []struct {
			Name string `json:"name"`
		} `json:"devices"`
	}
	if resp.StatusCode != http.StatusOK || json.Unmarshal(body, &info) != nil || len(info.Devices) != 2 ||
		info.Devices[0].Name != "Мамин <i>телефон</i>" || info.Devices[1].Name != "Планшет" {
		t.Fatalf("info: %s", body)
	}

	// The ban: not another client's device, not the shared place; then the device is gone,
	// its keys with it, and every fetch of it gets the notice.
	if resp, _ := h.do(http.MethodPost, userAPI+"/bound-devices/"+strconv.FormatInt(foreign[0].ID, 10)+"/ban", nil, csrf); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("ban another client's device: %d", resp.StatusCode)
	}
	if resp, _ := fetch(u.SubToken, ""); resp.StatusCode != http.StatusOK {
		t.Fatal("the shared place")
	}
	resp, body = h.do(http.MethodGet, userAPI+"/bound-devices", nil, nil)
	if json.Unmarshal(body, &bound) != nil || len(bound) != 3 {
		t.Fatalf("bound: %s", body)
	}
	if resp, body := h.do(http.MethodPost, userAPI+"/bound-devices/"+strconv.FormatInt(dev(""), 10)+"/ban", nil, csrf); resp.StatusCode != http.StatusUnprocessableEntity || errCode(body) != "device_no_hwid" {
		t.Fatalf("ban the shared place: %d %s", resp.StatusCode, body)
	}
	resp, body = h.do(http.MethodPost, deviceAPI+"/ban", nil, csrf)
	var ban struct {
		ID    int64  `json:"id"`
		Label string `json:"label"`
		Admin string `json:"admin"`
	}
	if resp.StatusCode != http.StatusCreated || json.Unmarshal(body, &ban) != nil || ban.Label != "Мамин <i>телефон</i>" || ban.Admin != "admin" {
		t.Fatalf("ban: %d %s", resp.StatusCode, body)
	}
	resp, links := fetch(u.SubToken, phone)
	if resp.StatusCode != http.StatusOK || !strings.Contains(links, "127.0.0.1:1") || strings.Count(links, "://") != 1 || notice(links) != "⛔ Устройство заблокировано" {
		t.Fatalf("a banned device gets only the notice: %q", links)
	}
	resp, body = h.do(http.MethodGet, sub+"?format=clash", nil, map[string]string{"User-Agent": "clash-verge/v2", "X-Hwid": phone})
	if !strings.Contains(string(body), "Устройство заблокировано") || strings.Contains(string(body), "vless") {
		t.Fatalf("the banned device's Clash profile: %s", body)
	}
	resp, body = h.do(http.MethodGet, userAPI+"/bound-devices", nil, nil)
	if json.Unmarshal(body, &bound) != nil || len(bound) != 2 {
		t.Fatalf("the banned device stays bound: %s", body)
	}
	resp, body = h.do(http.MethodGet, userAPI+"/device-bans", nil, nil)
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), phone) || !strings.Contains(string(body), `"admin":"admin"`) {
		t.Fatalf("bans: %d %s", resp.StatusCode, body)
	}
	// Without binding the device still gets nothing.
	if err := settings.Set(ctx, set, settings.KeyDeviceBinding, false); err != nil {
		t.Fatal(err)
	}
	if _, links := fetch(u.SubToken, phone); notice(links) != "⛔ Устройство заблокировано" {
		t.Fatalf("banned without binding: %q", links)
	}
	if _, links := fetch(u.SubToken, tablet); strings.Contains(links, "127.0.0.1:1") {
		t.Fatalf("another device without binding: %q", links)
	}
	if err := settings.Set(ctx, set, settings.KeyDeviceBinding, true); err != nil {
		t.Fatal(err)
	}

	// The unban: another client's path is not found; then the device binds again.
	banPath := userAPI + "/device-bans/" + strconv.FormatInt(ban.ID, 10)
	if resp, _ := h.do(http.MethodDelete, "/"+adminPath+"/api/v1/users/"+strconv.FormatInt(other.ID, 10)+"/device-bans/"+strconv.FormatInt(ban.ID, 10), nil, csrf); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("unban through another client: %d", resp.StatusCode)
	}
	if resp, _ := h.do(http.MethodDelete, banPath, nil, csrf); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("unban: %d", resp.StatusCode)
	}
	if _, links := fetch(u.SubToken, phone); strings.Contains(links, "127.0.0.1:1") {
		t.Fatalf("the unbanned device: %q", links)
	}

	// The journal has the rename with the old name, the ban and the unban.
	rows, err := h.st.Q.ListAudit(ctx, db.ListAuditParams{BeforeID: 1 << 62, Lim: 100})
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]string{}
	for _, r := range rows {
		seen[r.Action] += r.Details.String
	}
	if !strings.Contains(seen["user.update"], `"name_was":"a"`) || seen["user.ban_device"] == "" || seen["user.unban_device"] == "" || seen["user.rename_device"] == "" {
		t.Fatalf("journal: %v", seen)
	}
}
