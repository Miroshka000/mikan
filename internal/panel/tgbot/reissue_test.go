package tgbot

import (
	"strconv"
	"strings"
	"testing"
	"time"

	"mikan/internal/panel/domain"
	"mikan/internal/panel/settings"
)

// The subscriber's own new link in the bot: a button on the subscription screen, a
// confirmation that says what stops working, the new link at once, and, past the admin's
// limit, no button and the time it comes back.
func TestBotReissuesTheLink(t *testing.T) {
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
	if got := buttons(tap(anna, "s"))["🔄 Новая ссылка"]; got != "rl" {
		t.Fatalf("the subscription screen offers a new link: %q", got)
	}
	confirm := tap(anna, "rl")
	if !strings.Contains(text(confirm), "Старая ссылка перестанет работать сразу") || !strings.Contains(text(confirm), "«Анна»") ||
		buttons(confirm)["✅ Да, выпустить"] != "rn:"+uid || buttons(confirm)["↩️ Отмена"] != "s" {
		t.Fatalf("confirm: %q %v", text(confirm), buttons(confirm))
	}

	// Someone else's tap with the same data changes nothing.
	// Its answer is awaited, so that the next tap does not take it for its own.
	e.later()
	n = e.tg.count()
	e.press(other, menu, "rn:"+uid)
	e.tg.wait(t, n, "answerCallbackQuery")
	if u, _ := e.st.Q.GetUser(e.ctx, e.user.ID); u.SubToken != e.user.SubToken {
		t.Fatal("another account reissued the link")
	}

	done := tap(anna, "rn:"+uid)
	u, err := e.st.Q.GetUser(e.ctx, e.user.ID)
	if err != nil || u.SubToken == e.user.SubToken {
		t.Fatalf("the link did not change: %v", err)
	}
	if !strings.Contains(text(done), "Новая ссылка готова") || !strings.Contains(text(done), "https://vpn.example.com:21355/sub/"+u.SubToken) || strings.Contains(text(done), e.user.SubToken) {
		t.Fatalf("after reissuing: %q", text(done))
	}
	var via string
	if err := e.st.DB.QueryRowContext(e.ctx, `SELECT details::jsonb->>'actor' || '/' || (details::jsonb->>'via') FROM audit_log WHERE action = 'user.reissue'`).Scan(&via); err != nil || via != "subscriber/bot" {
		t.Fatalf("audit: %q %v", via, err)
	}

	// The default is one a day: no button, and the line says when it is back.
	screen := tap(anna, "s")
	if _, has := buttons(screen)["🔄 Новая ссылка"]; has || !strings.Contains(text(screen), "можно снова через 1 день") {
		t.Fatalf("at the limit: %q %v", text(screen), buttons(screen))
	}
	// A stale button says why there is nothing to do, and changes nothing.
	if got := text(tap(anna, "rl")); !strings.Contains(got, "можно снова через 1 день") {
		t.Fatalf("stale confirm: %q", got)
	}
	if got := text(tap(anna, "rn:"+uid)); !strings.Contains(got, "можно снова через 1 день") {
		t.Fatalf("stale reissue: %q", got)
	}
	if again, _ := e.st.Q.GetUser(e.ctx, e.user.ID); again.SubToken != u.SubToken {
		t.Fatal("the limit was passed")
	}

	// A day later it is back; the admin may switch it off for subscribers.
	e.mu.Lock()
	e.now = e.now.Add(24 * time.Hour)
	e.mu.Unlock()
	if got := buttons(tap(anna, "s"))["🔄 Новая ссылка"]; got != "rl" {
		t.Fatalf("a day later: %q", got)
	}
	if err := settings.Set(e.ctx, e.set, settings.KeyReissueRules, domain.ReissueRules{Off: true, Limit: 1, Days: 1}); err != nil {
		t.Fatal(err)
	}
	screen = tap(anna, "s")
	if _, has := buttons(screen)["🔄 Новая ссылка"]; has || strings.Contains(text(screen), "можно снова") {
		t.Fatalf("switched off: %q %v", text(screen), buttons(screen))
	}
	if got := text(tap(anna, "rn:"+uid)); !strings.Contains(got, "только администратор") {
		t.Fatalf("off, by a stale button: %q", got)
	}
}

// A limit that counts devices online at once is not "N of M" places.
func TestBotDevicesInTheOnlineMode(t *testing.T) {
	e := setup(t)
	const anna = 555
	n := e.tg.count()
	e.say(anna, "/start "+e.code(e.user.ID))
	e.tg.wait(t, n, "sendMessage")
	chat, _ := e.st.Q.GetTgChat(e.ctx, anna)
	tap := func(data string) call {
		t.Helper()
		e.later()
		n := e.tg.count()
		e.press(anna, chat.MenuMsgID, data)
		calls := e.tg.until(t, n, func(cs []call) bool { _, ok := find(cs, "editMessageText"); return ok })
		edit, _ := find(calls, "editMessageText")
		return edit
	}
	three := int64(3)
	if _, err := e.users.Update(e.ctx, e.user.ID, domain.Patch{DeviceLimit: &three}); err != nil {
		t.Fatal(err)
	}
	if got := text(tap("s")); !strings.Contains(got, "0 из 3") {
		t.Fatalf("bound mode: %q", got)
	}
	mode := domain.DeviceModeOnline
	if _, err := e.users.Update(e.ctx, e.user.ID, domain.Patch{DeviceMode: &mode}); err != nil {
		t.Fatal(err)
	}
	got := text(tap("s"))
	if !strings.Contains(got, "до 3 одновременно") || strings.Contains(got, "0 из 3") {
		t.Fatalf("online mode: %q", got)
	}
}
