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
)

// Devices bound to a subscription, end to end over HTTP: own keys per device, a stub
// once the places are taken, the subscription page's list and unbind (same site only,
// once a day), the admin's list with ids and unlimited unbind.
func TestDeviceBindingOverHTTP(t *testing.T) {
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
	u, err := domain.NewUsers(h.st, domain.NewPool(h.st, clock), noChanges{}, clock).Create(ctx, domain.CreateInput{Name: "a", TariffID: tariffs[1].ID}) // 3 devices
	if err != nil {
		t.Fatal(err)
	}
	own, _ := h.st.Q.GetSlot(ctx, u.SlotID.Int64)
	sub := "/" + subPath + "/" + u.SubToken
	fetch := func(hwid string) (*http.Response, string) {
		hdr := map[string]string{"User-Agent": "Happ/3.4.1"}
		if hwid != "" {
			hdr["X-Hwid"], hdr["X-Device-Os"], hdr["X-Device-Model"] = hwid, "Android", "Pixel 9"
		}
		resp, body := h.do(http.MethodGet, sub, nil, hdr)
		links, _ := base64.StdEncoding.DecodeString(string(body))
		return resp, string(links)
	}

	seen := map[string]bool{}
	for _, id := range []string{"phone-0123456789", "laptop-0123456789", "tablet-0123456789"} {
		resp, links := fetch(id)
		if resp.StatusCode != http.StatusOK || strings.Contains(links, own.Uuid) || strings.Contains(links, "127.0.0.1:1") {
			t.Fatalf("%s: %d, gets its own keys, not the user's: %.120s", id, resp.StatusCode, links)
		}
		uuid := links[strings.Index(links, "vless://")+8 : strings.Index(links, "vless://")+44]
		if seen[uuid] {
			t.Fatalf("%s shares keys with another device", id)
		}
		seen[uuid] = true
		if _, again := fetch(id); !strings.Contains(again, uuid) {
			t.Fatalf("%s: the same keys on every fetch", id)
		}
	}
	resp, links := fetch("fourth-0123456789")
	if resp.Header.Get("X-Hwid-Max-Devices-Reached") != "true" || !strings.Contains(links, "127.0.0.1:1") || strings.Count(links, "://") != 1 {
		t.Fatalf("a fourth device gets the stub: %v %q", resp.Header, links)
	}
	// The stub's name is the notice the app shows; it is in the panel's language.
	notice := func(links string) string {
		name, _ := url.PathUnescape(links[strings.LastIndex(links, "#")+1:])
		return name
	}
	if n := notice(links); !strings.HasPrefix(n, "⛔ Все места") {
		t.Fatalf("notice without a language: %q", n)
	}
	if err := settings.Set(ctx, set, settings.KeyDefaultLang, "en"); err != nil {
		t.Fatal(err)
	}
	if _, links := fetch("fourth-0123456789"); !strings.HasPrefix(notice(links), "⛔ All device places are taken") {
		t.Fatalf("notice in English: %q", notice(links))
	}

	resp, body := h.do(http.MethodGet, sub+"/info", nil, nil)
	var info struct {
		Binding bool `json:"binding"`
		Devices []struct {
			ID    int64  `json:"id"`
			Model string `json:"model"`
		} `json:"devices"`
	}
	if resp.StatusCode != http.StatusOK || json.Unmarshal(body, &info) != nil || !info.Binding || len(info.Devices) != 3 || info.Devices[0].Model != "Pixel 9" {
		t.Fatalf("info: %d %s", resp.StatusCode, body)
	}
	if strings.Contains(string(body), "phone-0123456789") {
		t.Fatal("the page must not show device ids")
	}
	unbind := sub + "/devices/" + strconv.FormatInt(info.Devices[0].ID, 10) + "/unbind"
	if resp, _ := h.do(http.MethodPost, unbind, nil, map[string]string{"Sec-Fetch-Site": "cross-site"}); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("unbind from another site: %d", resp.StatusCode)
	}
	if resp, _ := h.do(http.MethodPost, unbind, nil, map[string]string{"Sec-Fetch-Site": "same-origin"}); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("unbind from the page: %d", resp.StatusCode)
	}
	if resp, links := fetch("fourth-0123456789"); resp.Header.Get("X-Hwid-Max-Devices-Reached") != "" || strings.Contains(links, "127.0.0.1:1") {
		t.Fatalf("the freed place: %q", links)
	}
	second := sub + "/devices/" + strconv.FormatInt(info.Devices[1].ID, 10) + "/unbind"
	resp, body = h.do(http.MethodPost, second, nil, map[string]string{"Sec-Fetch-Site": "same-origin"})
	if resp.StatusCode != http.StatusTooManyRequests || !strings.Contains(string(body), `"unbind_after":"`+h.now.Add(24*time.Hour).UTC().Format(time.RFC3339)) {
		t.Fatalf("a second unbind the same day: %d %s", resp.StatusCode, body)
	}
	// The page tells the rules: none left, when the next comes, the pause of an unbound device.
	var rules struct {
		Limit  int        `json:"unbind_limit"`
		Days   int        `json:"unbind_days"`
		Left   int        `json:"unbinds_left"`
		Return int        `json:"return_hours"`
		After  *time.Time `json:"unbind_after"`
	}
	if _, body := h.do(http.MethodGet, sub+"/info", nil, nil); json.Unmarshal(body, &rules) != nil || rules.Limit != 1 || rules.Days != 1 || rules.Left != 0 || rules.Return != 24 || rules.After == nil {
		t.Fatalf("the rules on the page: %+v %s", rules, body)
	}
	// The unbound phone, its app still open, asks again: it waits out the pause, told so.
	if resp, links := fetch("phone-0123456789"); resp.StatusCode != http.StatusOK || !strings.Contains(links, "127.0.0.1:1") || notice(links) != "⏸ This device was unbound — it can connect again in 24 h" {
		t.Fatalf("the unbound phone comes back at once: %q", notice(links))
	}

	if resp, _ := h.login(password, ""); resp.StatusCode != http.StatusOK {
		t.Fatalf("login: %d", resp.StatusCode)
	}
	api := "/" + adminPath + "/api/v1/users/" + strconv.FormatInt(u.ID, 10) + "/bound-devices"
	resp, body = h.do(http.MethodGet, api, nil, nil)
	var bound []struct {
		ID   int64  `json:"id"`
		HWID string `json:"hwid"`
	}
	if resp.StatusCode != http.StatusOK || json.Unmarshal(body, &bound) != nil || len(bound) != 3 || bound[0].HWID == "" {
		t.Fatalf("admin list: %d %s", resp.StatusCode, body)
	}
	if resp, _ := h.do(http.MethodDelete, api+"/"+strconv.FormatInt(bound[0].ID, 10), nil, map[string]string{"X-CSRF-Token": h.csrf}); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("the admin unbinds without the daily limit: %d", resp.StatusCode)
	}
	// The places taken are the bound devices, in the user and in the list alike: two of
	// the three are left, however many addresses they connect from.
	var one struct {
		BoundDevices int64 `json:"bound_devices"`
	}
	resp, body = h.do(http.MethodGet, "/"+adminPath+"/api/v1/users/"+strconv.FormatInt(u.ID, 10), nil, nil)
	if resp.StatusCode != http.StatusOK || json.Unmarshal(body, &one) != nil || one.BoundDevices != 2 {
		t.Fatalf("the user's bound devices: %d %s", resp.StatusCode, body)
	}
	var list struct {
		Items []struct {
			ID           int64 `json:"id"`
			BoundDevices int64 `json:"bound_devices"`
		} `json:"items"`
	}
	resp, body = h.do(http.MethodGet, "/"+adminPath+"/api/v1/users", nil, nil)
	if resp.StatusCode != http.StatusOK || json.Unmarshal(body, &list) != nil {
		t.Fatalf("users: %d %s", resp.StatusCode, body)
	}
	for _, it := range list.Items {
		if it.ID == u.ID && it.BoundDevices != 2 {
			t.Fatalf("the list's bound devices: %d", it.BoundDevices)
		}
	}

	// The admin sees the phone's pause among the bans, with its end.
	resp, body = h.do(http.MethodGet, "/"+adminPath+"/api/v1/users/"+strconv.FormatInt(u.ID, 10)+"/device-bans", nil, nil)
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), `"until":"`+h.now.Add(24*time.Hour).UTC().Format(time.RFC3339)) {
		t.Fatalf("the pause among the bans: %d %s", resp.StatusCode, body)
	}
	// The admin's rules: broken ones are refused, good ones apply to the next unbind at once.
	settingsAPI := "/" + adminPath + "/api/v1/settings"
	csrf := map[string]string{"X-CSRF-Token": h.csrf}
	for _, bad := range []map[string]any{{"limit": -1, "days": 1, "return_hours": 0}, {"limit": 1, "days": 0, "return_hours": 0}, {"limit": 1, "days": 1, "return_hours": 9999}} {
		if resp, body := h.do(http.MethodPatch, settingsAPI, map[string]any{"device_unbind": bad}, csrf); resp.StatusCode != http.StatusUnprocessableEntity {
			t.Fatalf("rules %v: %d %s", bad, resp.StatusCode, body)
		}
	}
	resp, body = h.do(http.MethodPatch, settingsAPI, map[string]any{"device_unbind": map[string]any{"limit": 3, "days": 7, "return_hours": 0}}, csrf)
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), `"device_unbind":{"limit":3,"days":7,"return_hours":0}`) {
		t.Fatalf("rules saved: %d %s", resp.StatusCode, body)
	}
	third := sub + "/devices/" + strconv.FormatInt(info.Devices[2].ID, 10) + "/unbind"
	if resp, _ := h.do(http.MethodPost, third, nil, map[string]string{"Sec-Fetch-Site": "same-origin"}); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("one of three a week: %d", resp.StatusCode)
	}

	// Strict mode: an app without an id gets no keys.
	if err := settings.Set(ctx, set, settings.KeyRequireHWID, true); err != nil {
		t.Fatal(err)
	}
	if resp, links := fetch(""); resp.Header.Get("X-Hwid-Not-Supported") != "true" || !strings.Contains(links, "127.0.0.1:1") {
		t.Fatalf("an app without an id in strict mode: %v %q", resp.Header, links)
	}
}
