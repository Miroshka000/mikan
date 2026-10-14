package app

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"mikan/internal/panel/domain"
	"mikan/internal/panel/settings"
	"mikan/internal/panel/store/db"
	"mikan/internal/panel/tgate"
)

// The Mini App's new link: Telegram's signature and the account's own subscription decide,
// the admin's rules limit it, and the page is handed the new token.
func TestMiniAppReissue(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	if err := domain.Seed(ctx, h.st, h.now); err != nil {
		t.Fatal(err)
	}
	clock := func() time.Time { return h.now }
	users := domain.NewUsers(h.st, domain.NewPool(h.st, clock), noChanges{}, clock)
	tariffs, _ := h.st.Q.ListTariffs(ctx)
	mine, err := users.Create(ctx, domain.CreateInput{Name: "mine", TariffID: tariffs[1].ID})
	if err != nil {
		t.Fatal(err)
	}
	theirs, err := users.Create(ctx, domain.CreateInput{Name: "theirs", TariffID: tariffs[1].ID})
	if err != nil {
		t.Fatal(err)
	}
	set := settings.New(h.st.Q)
	if err := settings.Set(ctx, set, tgate.KeyToken, tgToken); err != nil {
		t.Fatal(err)
	}
	for _, l := range []db.LinkTgParams{{UserID: mine.ID, TgID: 555}, {UserID: theirs.ID, TgID: 777}} {
		l.CreatedAt = h.now.Unix()
		if err := h.st.Q.LinkTg(ctx, l); err != nil {
			t.Fatal(err)
		}
	}
	reissue := "/" + subPath + "/tg/reissue"
	same := map[string]string{"Sec-Fetch-Site": "same-origin"}
	post := func(tgID int64, token string, hdr map[string]string) (int, map[string]any) {
		t.Helper()
		resp, body := h.do(http.MethodPost, reissue, map[string]string{"init_data": initData(tgToken, tgID, h.now), "token": token}, hdr)
		out := map[string]any{}
		_ = json.Unmarshal(body, &out)
		return resp.StatusCode, out
	}

	// Nobody but the subscription's own account, signed by Telegram, from the page itself.
	if code, _ := post(555, "", same); code != http.StatusBadRequest {
		t.Errorf("without a token: %d", code)
	}
	if code, out := post(555, theirs.SubToken, same); code != http.StatusForbidden {
		t.Errorf("another account's subscription: %d %v", code, out)
	}
	if code, _ := post(999, mine.SubToken, same); code != http.StatusForbidden {
		t.Errorf("an account without it: %d", code)
	}
	if code, _ := post(555, mine.SubToken, map[string]string{"Sec-Fetch-Site": "cross-site"}); code != http.StatusNotFound {
		t.Errorf("from another site: %d", code)
	}
	resp, _ := h.do(http.MethodPost, reissue, map[string]string{"init_data": initData("987654321:AAHdqTcvCH1vGWJxfSeofSAs0K5PALDsaw1", 555, h.now), "token": mine.SubToken}, same)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("another bot's signature: %d", resp.StatusCode)
	}
	// The link alone is not enough: that is what a leak gives.
	resp, _ = h.do(http.MethodPost, reissue, map[string]string{"token": mine.SubToken}, same)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("the token without Telegram's proof: %d", resp.StatusCode)
	}
	if u, _ := h.st.Q.GetUser(ctx, mine.ID); u.SubToken != mine.SubToken {
		t.Fatal("a refused request changed the link")
	}

	// The info the page reads before: the button is on, once.
	info := func(token string) map[string]any {
		t.Helper()
		resp, body := h.do(http.MethodGet, "/"+subPath+"/"+token+"/info", nil, nil)
		out := map[string]any{}
		if resp.StatusCode != http.StatusOK || json.Unmarshal(body, &out) != nil {
			t.Fatalf("info: %d %s", resp.StatusCode, body)
		}
		return out
	}
	if in := info(mine.SubToken); in["reissue"] != true || in["reissue_limit"] != float64(1) || in["reissue_days"] != float64(1) || in["reissue_after"] != nil {
		t.Fatalf("info before: %v", in)
	}

	code, out := post(555, mine.SubToken, same)
	fresh, _ := out["token"].(string)
	if code != http.StatusOK || len(fresh) != 24 || fresh == mine.SubToken {
		t.Fatalf("reissue: %d %v", code, out)
	}
	if resp, _ := h.do(http.MethodGet, "/"+subPath+"/"+mine.SubToken+"/info", nil, nil); resp.StatusCode != http.StatusNotFound {
		t.Errorf("the old link still opens: %d", resp.StatusCode)
	}
	if in := info(fresh); in["reissue_after"] == nil {
		t.Errorf("the page is not told when it may reissue again: %v", in)
	}

	// The default is one a day.
	code, out = post(555, fresh, same)
	if code != http.StatusTooManyRequests || out["code"] != "reissue_cooldown" || out["reissue_after"] == nil {
		t.Fatalf("the second one: %d %v", code, out)
	}
	// The admin may turn it off for subscribers.
	if err := settings.Set(ctx, set, settings.KeyReissueRules, domain.ReissueRules{Off: true, Limit: 1, Days: 1}); err != nil {
		t.Fatal(err)
	}
	if code, out = post(555, fresh, same); code != http.StatusForbidden || out["code"] != "reissue_off" {
		t.Fatalf("off: %d %v", code, out)
	}
	if in := info(fresh); in["reissue_off"] != true {
		t.Errorf("info when off: %v", in)
	}
}
