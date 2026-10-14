package tgate

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"mikan/internal/panel/settings"
)

func TestInfrastructureAdminLinkIsOneTime(t *testing.T) {
	e := setup(t)
	link, err := e.gate.BeginInfrastructureAdminConnect(e.ctx)
	if err != nil || !strings.Contains(link, "https://t.me/mikan_test_bot?start=infra_") {
		t.Fatalf("admin connect link: %q %v", link, err)
	}
	code := strings.TrimPrefix(link, "https://t.me/mikan_test_bot?start=infra_")
	e.say(555, "/start infra_"+code)
	e.tg.until(t, 0, func(cs []call) bool {
		for _, c := range cs {
			if c.method == "sendMessage" && c.body["chat_id"] == float64(555) && strings.Contains(text(c), "подключён") {
				return true
			}
		}
		return false
	})
	if id, ok, err := e.gate.InfrastructureAdminChat(e.ctx); err != nil || !ok || id != 555 {
		t.Fatalf("connected admin: %d %v %v", id, ok, err)
	}
	e.say(777, "/start infra_"+code)
	time.Sleep(80 * time.Millisecond)
	if id, ok, err := e.gate.InfrastructureAdminChat(e.ctx); err != nil || !ok || id != 555 {
		t.Fatalf("one-time link was reused: %d %v %v", id, ok, err)
	}
}

// Another program on the token: the status names the conflict rather than Telegram's
// English, and once it lasts the admin's chat is told, once; polling again clears it.
func TestBotTokenConflict(t *testing.T) {
	defer func(d time.Duration) { conflictLasting = d }(conflictLasting)
	conflictLasting = 0
	e := setup(t)
	if err := settings.Set(e.ctx, e.set, KeyInfraAdminChat, int64(4242)); err != nil {
		t.Fatal(err)
	}
	from := len(e.tg.calls)
	e.tg.mu.Lock()
	e.tg.conflict = true
	e.tg.mu.Unlock()
	sent := e.tg.wait(t, from, "sendMessage")
	if c := sent[len(sent)-1]; c.body["chat_id"] != float64(4242) || !strings.Contains(c.body["text"].(string), "BotFather") {
		t.Fatalf("the admin was told %+v", c.body)
	}
	deadline := time.Now().Add(3 * time.Second)
	for e.gate.Status().Error != "token_conflict" {
		if time.Now().After(deadline) {
			t.Fatalf("status %+v", e.gate.Status())
		}
		time.Sleep(10 * time.Millisecond)
	}
	e.tg.mu.Lock()
	e.tg.conflict = false
	e.tg.mu.Unlock()
	deadline = time.Now().Add(5 * time.Second)
	for e.gate.Status().Error != "" {
		if time.Now().After(deadline) {
			t.Fatalf("the conflict stays after it ended: %+v", e.gate.Status())
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// The gateway needs none of the bot's screens: without them it still runs, connects the
// infrastructure admin's chat, takes a Stars payment, makes the accounts that write known,
// moves past what nobody answers, and gives the alerts and the backups a client that sends.
func TestGatewayWithoutScreens(t *testing.T) {
	var paid atomic.Int32
	e := start(t, false, func(e *env, d *Deps) {
		d.stars = func(context.Context, int64, string, string, string, int64) error {
			paid.Add(1)
			return nil
		}
	})
	link, err := e.gate.BeginInfrastructureAdminConnect(e.ctx)
	if err != nil {
		t.Fatal(err)
	}
	e.say(555, "/start infra_"+strings.TrimPrefix(link, "https://t.me/mikan_test_bot?start=infra_"))
	e.tg.until(t, 0, func(cs []call) bool {
		for _, c := range cs {
			if c.method == "sendMessage" && c.body["chat_id"] == float64(555) && strings.Contains(text(c), "подключён") {
				return true
			}
		}
		return false
	})
	e.say(777, "привет")
	e.tg.push(Update{Message: &Message{MessageID: 9, From: &User{ID: 777}, Chat: Chat{ID: 777, Type: "private"},
		SuccessfulPayment: &SuccessfulPayment{Currency: "XTR", TotalAmount: 10, InvoicePayload: "p", ChargeID: "ch1"}}})
	until(t, "the payment is taken", func() bool { return paid.Load() == 1 })
	until(t, "every update is acknowledged", func() bool {
		rec, ok, _ := settings.Get[offsetRecord](e.ctx, e.set, KeyOffset)
		return ok && rec.Offset == 1004
	})
	if _, err := e.st.Q.GetTgChat(e.ctx, 777); err != nil {
		t.Fatalf("the account that wrote is not known: %v", err)
	}

	if id, ok, err := e.gate.InfrastructureAdminChat(e.ctx); err != nil || !ok || id != 555 {
		t.Fatalf("admin chat: %d %v %v", id, ok, err)
	}
	if !e.gate.InfrastructureEnabled(e.ctx) || !e.gate.PolledInfrastructure(e.ctx) || e.gate.PolledLang(e.ctx) != "ru" {
		t.Fatal("the alerts are off")
	}
	c, err := e.gate.InfrastructureClient(e.ctx)
	if err != nil {
		t.Fatal(err)
	}
	n := e.tg.count()
	if _, err := c.Send(e.ctx, 555, "alert", nil, false); err != nil {
		t.Fatal(err)
	}
	if send, _ := find(e.tg.wait(t, n, "sendMessage"), "sendMessage"); text(send) != "alert" {
		t.Fatalf("sent %q", text(send))
	}
}
