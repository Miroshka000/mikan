package app

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"mikan/internal/panel/domain"
	"mikan/internal/panel/settings"
	"mikan/internal/panel/store/db"
	"mikan/internal/panel/tgate"
	"mikan/internal/panel/tgbot"
)

const tgToken = "123456789:AAHdqTcvCH1vGWJxfSeofSAs0K5PALDsaw0"

// initData signs Mini App launch parameters the way Telegram does.
func initData(token string, tgID int64, at time.Time) string {
	vals := url.Values{"auth_date": {strconv.FormatInt(at.Unix(), 10)}, "user": {`{"id":` + strconv.FormatInt(tgID, 10) + `,"first_name":"Anna"}`}}
	keys := []string{}
	for k := range vals {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	lines := []string{}
	for _, k := range keys {
		lines = append(lines, k+"="+vals.Get(k))
	}
	secret := hmac.New(sha256.New, []byte("WebAppData"))
	secret.Write([]byte(token))
	m := hmac.New(sha256.New, secret.Sum(nil))
	m.Write([]byte(strings.Join(lines, "\n")))
	vals.Set("hash", hex.EncodeToString(m.Sum(nil)))
	return vals.Encode()
}

// The bot in the admin API and the Mini App over HTTP: the token never comes back, the
// Mini App may be framed by Telegram Web only, and it signs in with Telegram's signature.
func TestTelegramOverHTTP(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	if err := domain.Seed(ctx, h.st, h.now); err != nil {
		t.Fatal(err)
	}
	clock := func() time.Time { return h.now }
	tariffs, _ := h.st.Q.ListTariffs(ctx)
	u, err := domain.NewUsers(h.st, domain.NewPool(h.st, clock), noChanges{}, clock).Create(ctx, domain.CreateInput{Name: "a", TariffID: tariffs[1].ID})
	if err != nil {
		t.Fatal(err)
	}
	if resp, _ := h.login(password, ""); resp.StatusCode != http.StatusOK {
		t.Fatal("login")
	}
	api := "/" + adminPath + "/api/v1/telegram"
	csrf := map[string]string{"X-CSRF-Token": h.csrf}
	if resp, body := h.do(http.MethodPatch, api, map[string]any{"token": "not-a-token"}, csrf); resp.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(string(body), "tg_token_format") {
		t.Fatalf("token format: %d %s", resp.StatusCode, body)
	}
	if resp, body := h.do(http.MethodPatch, api, map[string]any{"enabled": true}, csrf); resp.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(string(body), "tg_no_token") {
		t.Fatalf("on without a token: %d %s", resp.StatusCode, body)
	}
	set := settings.New(h.st.Q)
	if err := settings.Set(ctx, set, tgate.KeyToken, tgToken); err != nil {
		t.Fatal(err)
	}
	resp, body := h.do(http.MethodGet, api, nil, nil)
	var v struct {
		TokenSet  bool   `json:"token_set"`
		TokenHint string `json:"token_hint"`
	}
	if resp.StatusCode != http.StatusOK || json.Unmarshal(body, &v) != nil || !v.TokenSet || v.TokenHint != "123456789" || strings.Contains(string(body), "AAHdqTc") {
		t.Fatalf("the token never leaves the panel: %d %s", resp.StatusCode, body)
	}
	cfg := tgbot.Default("ru")
	cfg.Buttons = append(cfg.Buttons, tgbot.MenuButton{Action: "url", Label: "Канал", URL: "javascript:alert(1)", On: true})
	if resp, body := h.do(http.MethodPatch, api, map[string]any{"config": cfg}, csrf); resp.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(string(body), "tg_button_url") {
		t.Fatalf("a script link in the menu: %d %s", resp.StatusCode, body)
	}

	// The Mini App page: framed by Telegram Web and nobody else.
	resp, _ = h.do(http.MethodGet, "/"+subPath+"/tg", nil, nil)
	if resp.StatusCode != http.StatusOK || !strings.Contains(resp.Header.Get("Content-Security-Policy"), "frame-ancestors https://web.telegram.org") || resp.Header.Get("X-Frame-Options") != "" {
		t.Fatalf("mini app page: %d %v", resp.StatusCode, resp.Header)
	}
	if resp, _ := h.do(http.MethodGet, "/"+subPath+"/"+u.SubToken, nil, map[string]string{"Accept": "text/html"}); strings.Contains(resp.Header.Get("Content-Security-Policy"), "telegram") {
		t.Fatal("the subscription page itself stays unframeable")
	}

	// Signed in by Telegram: the account's subscriptions; anything else gets nothing.
	if err := h.st.Q.LinkTg(ctx, db.LinkTgParams{UserID: u.ID, TgID: 555, CreatedAt: h.now.Unix()}); err != nil {
		t.Fatal(err)
	}
	session := "/" + subPath + "/tg/session"
	same := map[string]string{"Sec-Fetch-Site": "same-origin"}
	resp, body = h.do(http.MethodPost, session, map[string]string{"init_data": initData(tgToken, 555, h.now)}, same)
	var s struct {
		Subs []struct {
			Token string `json:"token"`
		} `json:"subs"`
	}
	if resp.StatusCode != http.StatusOK || json.Unmarshal(body, &s) != nil || len(s.Subs) != 1 || s.Subs[0].Token != u.SubToken {
		t.Fatalf("mini app session: %d %s", resp.StatusCode, body)
	}
	for name, c := range map[string]struct {
		data string
		hdr  map[string]string
		code int
	}{
		"another bot's signature": {initData("987654321:AAHdqTcvCH1vGWJxfSeofSAs0K5PALDsaw1", 555, h.now), same, http.StatusUnauthorized},
		"an old signature":        {initData(tgToken, 555, h.now.Add(-48*time.Hour)), same, http.StatusUnauthorized},
		"from another site":       {initData(tgToken, 555, h.now), map[string]string{"Sec-Fetch-Site": "cross-site"}, http.StatusNotFound},
	} {
		if resp, _ := h.do(http.MethodPost, session, map[string]string{"init_data": c.data}, c.hdr); resp.StatusCode != c.code {
			t.Errorf("%s: %d, want %d", name, resp.StatusCode, c.code)
		}
	}
	resp, body = h.do(http.MethodPost, session, map[string]string{"init_data": initData(tgToken, 777, h.now)}, same)
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), `"subs":[]`) {
		t.Fatalf("an account without subscriptions: %d %s", resp.StatusCode, body)
	}

	// The user card shows the link; the admin can undo it.
	userAPI := "/" + adminPath + "/api/v1/users/" + strconv.FormatInt(u.ID, 10)
	if resp, body := h.do(http.MethodGet, userAPI, nil, nil); resp.StatusCode != http.StatusOK || !strings.Contains(string(body), `"telegram":{"id":555`) {
		t.Fatalf("user card: %d %s", resp.StatusCode, body)
	}
	if resp, _ := h.do(http.MethodDelete, userAPI+"/telegram", nil, csrf); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("unlink: %d", resp.StatusCode)
	}
	if _, err := h.st.Q.GetTgLink(ctx, u.ID); err == nil {
		t.Fatal("still linked")
	}
}
