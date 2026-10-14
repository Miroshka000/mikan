package tgbot

import (
	"io"
	"log/slog"
	"strings"
	"testing"

	"mikan/internal/panel/referral"
	"mikan/internal/panel/settings"
	"mikan/internal/panel/store/db"
)

// withReferral turns the programme on with these rules and hands the panel's side the
// service.
func withReferral(rules ...referral.Rule) func(*env, *wiring) {
	return func(e *env, d *wiring) {
		cfg := referral.Program{Enabled: true, StarsRate: 100, Spend: true, Notify: true, Rules: rules}
		if err := cfg.Validate(); err != nil {
			e.t.Fatal(err)
		}
		if err := settings.Set(e.ctx, e.set, referral.KeyConfig, cfg); err != nil {
			e.t.Fatal(err)
		}
		d.ReferralProgram = referral.New(e.st, e.set, slog.New(slog.NewTextHandler(io.Discard, nil)), e.clock)
		e.refs = d.ReferralProgram
	}
}

// tap is a press on the keyboard of the message that was just sent.
func tap(e *env, sent call, data string) {
	chat, _ := sent.body["chat_id"].(float64)
	msg, _ := sent.body["message_id"].(float64)
	e.press(int64(chat), int64(msg), data)
}

// The invite link is the account's own code, and following it binds the newcomer to
// exactly one inviter: a second link changes nothing, so one invitee is never counted
// twice.
func TestReferralLinkBindsOnce(t *testing.T) {
	e := setup(t, withReferral(referral.Rule{ID: "p", Kind: referral.KindPercent, Value: 10, Enabled: true}))
	const inviter, newcomer, second = 111, 222, 333
	err := e.st.Q.UpsertTgChat(e.ctx, db.UpsertTgChatParams{TgID: inviter, Username: "inviter", CreatedAt: e.clock().Unix(), UpdatedAt: e.clock().Unix()})
	if err != nil {
		t.Fatal(err)
	}

	// The programme's button is in the welcome menu of somebody with no subscription.
	n := e.tg.count()
	e.say(newcomer, "/start")
	calls := e.tg.wait(t, n, "sendMessage")
	sent, _ := find(calls, "sendMessage")
	if buttons(sent)["🤝 Пригласить друзей"] != "f" {
		t.Fatalf("the welcome menu has no referral button: %q %v", text(sent), buttons(sent))
	}

	// The inviter's own link leads to a screen that carries it.
	n = e.tg.count()
	e.say(inviter, "/start")
	calls = e.tg.wait(t, n, "sendMessage")
	sent, _ = find(calls, "sendMessage")
	tap(e, sent, "f")
	calls = e.tg.wait(t, n, "editMessageText")
	edit, _ := find(calls, "editMessageText")
	link := e.bot.ReferralLink(e.ctx, inviter)
	if link == "" || !strings.Contains(text(edit), link) {
		t.Fatalf("the screen does not carry %q: %q", link, text(edit))
	}
	if buttons(edit)["🔗 Поделиться"] == "" {
		t.Fatalf("the screen cannot be shared: %v", buttons(edit))
	}
	code := strings.TrimPrefix(link, "https://t.me/mikan_test_bot?start=")
	if !strings.HasPrefix(code, refPrefix) {
		t.Fatalf("the link is not a referral one: %q", link)
	}

	// The newcomer follows it.
	n = e.tg.count()
	e.say(newcomer, "/start "+code)
	calls = e.tg.wait(t, n, "sendMessage")
	sent, _ = find(calls, "sendMessage")
	if !strings.Contains(text(sent), "Ссылка принята") {
		t.Fatalf("the newcomer was not bound: %q", text(sent))
	}
	ref, err := e.st.Q.GetReferralOfReferee(e.ctx, newcomer)
	if err != nil || ref.ReferrerTgID != inviter {
		t.Fatalf("the newcomer is not bound to the inviter: %+v %v", ref, err)
	}

	// A second inviter's link changes nothing.
	otherCode, err := e.refs.Code(e.ctx, second)
	if err != nil {
		t.Fatal(err)
	}
	n = e.tg.count()
	e.say(newcomer, "/start ref_"+otherCode)
	calls = e.tg.wait(t, n, "sendMessage")
	sent, _ = find(calls, "sendMessage")
	if !strings.Contains(text(sent), "уже участвуете") {
		t.Fatalf("the newcomer was re-bound: %q", text(sent))
	}
	ref, err = e.st.Q.GetReferralOfReferee(e.ctx, newcomer)
	if err != nil || ref.ReferrerTgID != inviter {
		t.Fatalf("the binding moved: %+v %v", ref, err)
	}
}

// A link that does not exist is told so, and nobody is bound.
func TestReferralLinkNotFound(t *testing.T) {
	e := setup(t, withReferral(referral.Rule{ID: "p", Kind: referral.KindPercent, Value: 10, Enabled: true}))
	n := e.tg.count()
	e.say(444, "/start ref_nosuchcd")
	calls := e.tg.wait(t, n, "sendMessage")
	sent, _ := find(calls, "sendMessage")
	if !strings.Contains(text(sent), "Ссылка не подошла") {
		t.Fatalf("a link that does not work: %q", text(sent))
	}
	if _, err := e.st.Q.GetReferralOfReferee(e.ctx, 444); err == nil {
		t.Fatal("a bad link still bound somebody")
	}
}

// With the programme off, the button is gone and a link is refused.
func TestReferralOff(t *testing.T) {
	e := setup(t, withReferral(referral.Rule{ID: "p", Kind: referral.KindPercent, Value: 10, Enabled: true}))
	cfg := referral.Program{Notify: true}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	if err := settings.Set(e.ctx, e.set, referral.KeyConfig, cfg); err != nil {
		t.Fatal(err)
	}
	n := e.tg.count()
	e.say(555, "/start")
	calls := e.tg.wait(t, n, "sendMessage")
	sent, _ := find(calls, "sendMessage")
	if _, ok := buttons(sent)["🤝 Пригласить друзей"]; ok {
		t.Fatalf("the button is there with the programme off: %v", buttons(sent))
	}
	code, err := e.refs.Code(e.ctx, 555)
	if err != nil {
		t.Fatal(err)
	}
	n = e.tg.count()
	e.say(555, "/start ref_"+code)
	calls = e.tg.wait(t, n, "sendMessage")
	sent, _ = find(calls, "sendMessage")
	if !strings.Contains(text(sent), "выключена") {
		t.Fatalf("the link was taken with the programme off: %q", text(sent))
	}
}

// A reward is told to the inviter, in the bot's language.
func TestReferralRewardIsTold(t *testing.T) {
	e := setup(t, withReferral(referral.Rule{ID: "p", Kind: referral.KindPercent, Value: 10, Enabled: true}))
	n := e.tg.count()
	e.panel.Reward(e.ctx, referral.Reward{Referrer: 777, Referee: 888, Kind: referral.KindPercent, Rub: 1990, EveryN: 1})
	calls := e.tg.wait(t, n, "sendMessage")
	sent, _ := find(calls, "sendMessage")
	if chat, _ := sent.body["chat_id"].(float64); int64(chat) != 777 {
		t.Fatalf("the reward went to %v", sent.body["chat_id"])
	}
	if !strings.Contains(text(sent), "19,90 ₽") {
		t.Fatalf("the reward as told: %q", text(sent))
	}
}

// The rules are shown to the account, each with the tariffs it counts and the ones that
// are switched off left out.
func TestReferralScreenSaysTheRules(t *testing.T) {
	var tariff string
	setupWith := func(e *env, d *wiring) {
		ts, err := e.st.Q.ListTariffs(e.ctx)
		if err != nil {
			e.t.Fatal(err)
		}
		tariff = ts[0].Name
		withReferral(
			referral.Rule{ID: "p", Kind: referral.KindPercent, Value: 10, TariffIDs: []int64{ts[0].ID}, Enabled: true},
			referral.Rule{ID: "d", Kind: referral.KindDays, Value: 30, Every: 3, Enabled: true},
			referral.Rule{ID: "off", Kind: referral.KindPercent, Value: 50, Enabled: false},
		)(e, d)
	}
	e := setup(t, setupWith)
	n := e.tg.count()
	e.say(555, "/start")
	calls := e.tg.wait(t, n, "sendMessage")
	sent, _ := find(calls, "sendMessage")
	tap(e, sent, "f")
	calls = e.tg.wait(t, n, "editMessageText")
	edit, _ := find(calls, "editMessageText")
	body := text(edit)
	for _, want := range []string{tariff + " — 10% на баланс", "любого тарифа — 30 дн. подписки за каждые 3 оплат", "👥 Пригласили: 0", "Баланс списывается сам"} {
		if !strings.Contains(body, want) {
			t.Fatalf("the screen does not say %q:\n%s", want, body)
		}
	}
	if strings.Contains(body, "50%") {
		t.Fatalf("a rule that is switched off is on the screen:\n%s", body)
	}
}
