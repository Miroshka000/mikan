package tgbot

import (
	"strings"
	"testing"

	"mikan/internal/panel/campaigns"
)

// withCampaigns hands the panel's side the campaign service.
func withCampaigns() func(*env, *wiring) {
	return func(e *env, d *wiring) {
		d.Campaigns = campaigns.New(e.st, d.Log, e.clock)
	}
}

// A tagged link counts the chat once: the greeting comes with the first open, and later
// tagged links — even another campaign's — only bring the menu.
func TestCampaignStartCountsOnce(t *testing.T) {
	e := setup(t, withCampaigns())
	const newcomer = 777

	n := e.tg.count()
	e.say(newcomer, "/start camp_instagram")
	calls := e.tg.wait(t, n, "sendMessage")
	sent, _ := find(calls, "sendMessage")
	if !strings.Contains(text(sent), "Спасибо, что зашли") && !strings.Contains(text(sent), "stopping by") {
		t.Fatalf("no greeting for the first tagged /start: %q", text(sent))
	}

	// The same link again: the menu comes without the campaign's greeting.
	n = e.tg.count()
	e.say(newcomer, "/start camp_instagram")
	calls = e.tg.wait(t, n, "sendMessage")
	sent, _ = find(calls, "sendMessage")
	if strings.Contains(text(sent), "Спасибо, что зашли") || strings.Contains(text(sent), "stopping by") {
		t.Fatalf("the second tagged /start greeted again: %q", text(sent))
	}

	// Another campaign's tag does not steal the chat.
	e.say(newcomer, "/start camp_vk")

	v, err := e.st.Q.GetCampaignVisit(e.ctx, newcomer)
	if err != nil {
		t.Fatal(err)
	}
	if v.Tag != "instagram" {
		t.Fatalf("tag = %q", v.Tag)
	}
	if v.ChatID != newcomer {
		t.Fatalf("chat = %d", v.ChatID)
	}
}

// A broken tag is ignored: the menu comes, nothing is recorded.
func TestCampaignStartBadTag(t *testing.T) {
	e := setup(t, withCampaigns())
	e.say(888, "/start camp_с пробелом")
	if _, err := e.st.Q.GetCampaignVisit(e.ctx, 888); err == nil {
		t.Fatal("a bad tag recorded a visit")
	}
}

// The bot without the campaign service answers a tagged link like a plain /start.
func TestCampaignStartWithoutService(t *testing.T) {
	e := setup(t)
	e.say(889, "/start camp_x")
	if _, err := e.st.Q.GetCampaignVisit(e.ctx, 889); err == nil {
		t.Fatal("a visit was recorded without the service")
	}
}
