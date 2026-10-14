package panelapi

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"sync"
	"time"
)

// What the bot addon may do over the bot's RPC (tgbot.RPCHandler) beyond holding the bot:
// each method needs the permission its effect asks for, and acts only for Telegram accounts
// that are talking to the bot now. An addon cannot read or move the subscription of
// somebody who never wrote to it, whatever ids it makes up.

// rpcRule is one method's: the permission it needs, the arguments (by position) that are
// Telegram accounts which must be talking to the bot, and those that are subscriptions
// whose owner must be. tgField names the account inside a request object (Shop.Invoice).
type rpcRule struct {
	perm    string
	tg      []int
	owner   []int
	tgField int // argument holding an object with TgID; -1: none
}

func rule(perm string, tg, owner []int) rpcRule {
	return rpcRule{perm: perm, tg: tg, owner: owner, tgField: -1}
}

var botRPC = map[string]rpcRule{
	// The bot's own setup: no one's data.
	"Config": rule("", nil, nil), "Brand": rule("", nil, nil), "SupportURL": rule("", nil, nil),
	"MiniAppURL": rule("", nil, nil), "DeviceBinding": rule("", nil, nil), "Tariff": rule("", nil, nil),
	"Member": rule("", []int{1}, nil),
	// Accounts and their subscriptions.
	"Context":       rule("users:read", []int{0}, nil),
	"Chat":          rule("users:read", []int{0}, nil),
	"UserOfCode":    rule("users:read", nil, nil),
	"UserOfAddress": rule("users:read", nil, nil),
	"Owner":         rule("users:read", nil, nil),
	"SubURL":        rule("users:read", nil, nil),
	"User":          rule("users:read", nil, []int{0}),
	"GrantsLeft":    rule("users:read", nil, []int{0}),
	"Pools":         rule("users:read", nil, []int{0}),
	"DeviceCount":   rule("users:read", nil, []int{0}),
	"Devices":       rule("users:read", nil, []int{0}),
	"DeviceTraffic": rule("users:read", nil, []int{0}),
	"UnbindState":   rule("users:read", nil, []int{0}),
	"ReissueState":  rule("users:read", nil, []int{0}),
	"SetMenu":       rule("users:write", []int{0}, nil),
	"SetCurrent":    rule("users:write", []int{0}, nil),
	"Link":          rule("users:write", []int{0}, nil),
	// The programme of the fork: its own code and what it brought are the account's to
	// read, following a code and a tagged link are its to write.
	"BotUsername":   rule("", nil, nil),
	"ReferralOn":    rule("", nil, nil),
	"Referral":      rule("users:read", []int{0}, nil),
	"ReferralJoin":  rule("users:write", []int{0}, nil),
	"CampaignVisit": rule("users:write", []int{0}, nil),
	"Transfer":      rule("users:write", []int{1}, []int{0}),
	"Unlink":        rule("users:write", nil, []int{0}),
	"Unbind":        rule("users:write", nil, []int{0}),
	"Reissue":       rule("users:write", nil, []int{0}),
	// The shop: what is on offer, and what makes a payment or a subscription.
	"Shop.Config": rule("", nil, nil), "Shop.Offers": rule("", nil, nil), "Shop.AddonName": rule("", nil, nil),
	"Shop.TrialTariff":    rule("", nil, nil),
	"Shop.PackageOffers":  rule("", nil, []int{0}),
	"Shop.TrialOpen":      rule("", []int{0}, nil),
	"Shop.TrialAllowed":   rule("", []int{0}, nil),
	"Shop.TrialOffer":     rule("", []int{0}, nil),
	"Shop.FreeOffer":      rule("", []int{0}, nil),
	"Shop.Invoice":        {perm: "billing:invoice", tgField: 0},
	"Shop.PackageInvoice": {perm: "billing:invoice", tgField: 0},
	"Shop.TrialInvoice":   rule("billing:invoice", []int{0}, nil),
	"Shop.Trial":          rule("billing:invoice", []int{0}, nil),
	"Shop.FreePlan":       rule("billing:invoice", []int{0}, nil),
}

// talkWindow: how long an account counts as talking to the bot after its last message.
const talkWindow = 30 * time.Minute

// talking keeps the accounts the bot addon got updates or events about lately.
type talking struct {
	mu   sync.Mutex
	seen map[int64]time.Time
}

func (t *talking) mark(tg int64) {
	if tg == 0 {
		return
	}
	now := time.Now()
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.seen == nil {
		t.seen = map[int64]time.Time{}
	}
	t.seen[tg] = now
	if len(t.seen) > 50000 {
		for k, at := range t.seen {
			if now.Sub(at) > talkWindow {
				delete(t.seen, k)
			}
		}
	}
}

func (t *talking) recent(tg int64) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	at, ok := t.seen[tg]
	return ok && time.Since(at) <= talkWindow
}

// markUpdate notes who an update handed to the addon is from.
func (t *talking) markUpdate(raw []byte) {
	var u struct {
		Message *struct {
			From *struct{ ID int64 } `json:"from"`
		} `json:"message"`
		CallbackQuery *struct {
			From struct{ ID int64 } `json:"from"`
		} `json:"callback_query"`
	}
	if json.Unmarshal(raw, &u) != nil {
		return
	}
	if u.Message != nil && u.Message.From != nil {
		t.mark(u.Message.From.ID)
	}
	if u.CallbackQuery != nil {
		t.mark(u.CallbackQuery.From.ID)
	}
}

// guardRPC checks one call of the bot's RPC before it reaches the panel's backend.
func (s *Server) guardRPC(w http.ResponseWriter, r *http.Request, c Caller) bool {
	name := r.PathValue("method")
	rl, ok := botRPC[name]
	if !ok {
		Fail(w, http.StatusForbidden, "forbidden", "the method is not open to addons")
		return false
	}
	if rl.perm != "" && !c.Can(rl.perm) {
		Fail(w, http.StatusForbidden, "forbidden", "the addon has no permission "+rl.perm)
		return false
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		Fail(w, http.StatusBadRequest, "bad_request", "the body could not be read")
		return false
	}
	r.Body = io.NopCloser(bytes.NewReader(body))
	if len(rl.tg) == 0 && len(rl.owner) == 0 && rl.tgField < 0 {
		return true
	}
	var args []json.RawMessage
	if json.Unmarshal(body, &args) != nil {
		Fail(w, http.StatusBadRequest, "bad_request", "the body is a JSON array of the arguments")
		return false
	}
	arg := func(i int) (int64, bool) {
		var v int64
		return v, i < len(args) && json.Unmarshal(args[i], &v) == nil
	}
	quiet := func() bool {
		Fail(w, http.StatusForbidden, "not_talking", "the account is not talking to the bot")
		return false
	}
	for _, i := range rl.tg {
		if tg, ok := arg(i); !ok || !s.talk.recent(tg) {
			return quiet()
		}
	}
	if rl.tgField >= 0 {
		var req struct{ TgID int64 }
		if rl.tgField >= len(args) || json.Unmarshal(args[rl.tgField], &req) != nil || !s.talk.recent(req.TgID) {
			return quiet()
		}
	}
	for _, i := range rl.owner {
		user, ok := arg(i)
		if !ok || !s.ownerTalking(r.Context(), user) {
			return quiet()
		}
	}
	return true
}

// ownerTalking: the subscription's account is talking to the bot.
func (s *Server) ownerTalking(ctx context.Context, userID int64) bool {
	link, err := s.d.Store.Q.GetTgLink(ctx, userID)
	return err == nil && s.talk.recent(link.TgID)
}
