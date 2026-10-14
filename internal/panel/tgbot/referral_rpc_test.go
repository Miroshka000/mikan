package tgbot

import (
	"testing"

	"mikan/internal/panel/referral"
	"mikan/internal/panel/store/db"
)

// The fork's programme works over the Panel API the way the bot addon reaches it: the same
// screens, behind the RPC, not only in the panel's own process.
func TestReferralAndCampaignOverRPC(t *testing.T) {
	e := setup(t, withReferral(referral.Rule{ID: "p", Kind: referral.KindPercent, Value: 10, Enabled: true}),
		withCampaigns(), func(e *env, d *wiring) { d.RPC = true })
	be := e.bot.d.Backend
	if !be.ReferralOn(e.ctx) {
		t.Fatal("the programme is off over the RPC")
	}
	if be.BotUsername(e.ctx) != "mikan_test_bot" {
		t.Fatalf("the bot's own name over the RPC: %q", be.BotUsername(e.ctx))
	}
	const inviter, newcomer, other = 111, 222, 333
	if err := e.st.Q.UpsertTgChat(e.ctx, db.UpsertTgChatParams{TgID: inviter, Username: "inviter", CreatedAt: e.now.Unix(), UpdatedAt: e.now.Unix()}); err != nil {
		t.Fatal(err)
	}
	code, err := e.refs.Code(e.ctx, inviter)
	if err != nil {
		t.Fatal(err)
	}
	v, err := be.Referral(e.ctx, inviter)
	if err != nil || !v.Enabled || v.Code != code || v.Paid != 0 {
		t.Fatalf("the account's view over the RPC: %+v %v", v, err)
	}
	j, err := be.ReferralJoin(e.ctx, newcomer, code)
	if err != nil || j.Status != ReferralDone || j.ReferrerName != "@inviter" {
		t.Fatalf("joining over the RPC: %+v %v", j, err)
	}
	otherCode, err := e.refs.Code(e.ctx, other)
	if err != nil {
		t.Fatal(err)
	}
	if j, err := be.ReferralJoin(e.ctx, newcomer, otherCode); err != nil || j.Status != ReferralBound {
		t.Fatalf("a second code over the RPC: %+v %v", j, err)
	}
	ref, err := e.st.Q.GetReferralOfReferee(e.ctx, newcomer)
	if err != nil || ref.ReferrerTgID != inviter {
		t.Fatalf("the binding moved: %+v %v", ref, err)
	}
	if g, err := be.CampaignVisit(e.ctx, newcomer, "camp_instagram"); err != nil || !g.Counted {
		t.Fatalf("the first tagged start over the RPC: %+v %v", g, err)
	}
	if g, err := be.CampaignVisit(e.ctx, newcomer, "camp_vk"); err != nil || g.Counted {
		t.Fatalf("the second tagged start counted again: %+v %v", g, err)
	}
}
