package campaigns

import (
	"context"
	"database/sql"
	"io"
	"log/slog"
	"strconv"
	"testing"
	"time"

	"mikan/internal/panel/domain"
	"mikan/internal/panel/store"
	"mikan/internal/panel/store/db"
	"mikan/internal/panel/store/storetest"
)

type env struct {
	t      *testing.T
	ctx    context.Context
	st     *store.Store
	s      *Service
	now    time.Time
	tariff db.Tariff
	seq    int
}

func (e *env) next() string { return e.t.Name() + "-" + strconv.Itoa(e.seq) }

const (
	buyer   = int64(501)
	second  = int64(502)
	strange = int64(503)
)

func newEnv(t *testing.T) *env {
	t.Helper()
	ctx := context.Background()
	e := &env{t: t, ctx: ctx, now: time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)}
	var err error
	if e.st, err = storetest.Open(ctx, t.TempDir()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { e.st.Close() })
	if err := domain.Seed(ctx, e.st, e.now); err != nil {
		t.Fatal(err)
	}
	e.s = New(e.st, slog.New(slog.NewTextHandler(io.Discard, nil)), func() time.Time { return e.now })
	ts, err := e.st.Q.ListTariffs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(ts) == 0 {
		t.Fatal("the fixture wants a tariff, the seed has none")
	}
	e.tariff = ts[0]
	return e
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// payment is a tariff invoice for a chat; the kind is new or renew. A package carries no
// tariff: it tops traffic up.
func (e *env) payment(tgID, amount int64, kind string) db.Payment {
	e.t.Helper()
	tariff := sql.NullInt64{Int64: e.tariff.ID, Valid: kind != "package"}
	name := e.tariff.Name
	if kind == "package" {
		name = "5 GB"
	}
	p, err := e.st.Q.CreatePayment(e.ctx, db.CreatePaymentParams{Provider: "stars", Payload: e.next(),
		TgID: tgID, Kind: kind, TariffID: tariff, TariffName: name,
		Amount: amount, Currency: "RUB", CreatedAt: e.now.Unix()})
	e.seq++
	must(e.t, err)
	return p
}

func (e *env) events(campaignID int64) db.CountCampaignEventsRow {
	e.t.Helper()
	row, err := e.st.Q.CountCampaignEvents(e.ctx, campaignID)
	must(e.t, err)
	return row
}

// An unknown tag is created on the spot and the chat is counted once: a second tagged link,
// another campaign's, changes nothing.
func TestVisitBindsOnce(t *testing.T) {
	e := newEnv(t)
	c, counted, err := e.s.Visit(e.ctx, buyer, "instagram")
	must(t, err)
	if !counted {
		t.Fatal("the first visit was not counted")
	}
	if c.Tag != "instagram" {
		t.Fatalf("tag = %q", c.Tag)
	}
	// A second campaign's tag does not steal the chat.
	if _, counted, err = e.s.Visit(e.ctx, buyer, "telegram"); err != nil {
		t.Fatal(err)
	} else if counted {
		t.Fatal("a second tagged link was counted as an arrival")
	}
	if v, err := e.st.Q.GetCampaignVisit(e.ctx, buyer); err != nil {
		t.Fatal(err)
	} else if v.CampaignID != c.ID {
		t.Fatalf("the chat moved to campaign %d", v.CampaignID)
	}
	// The same link twice is still one arrival.
	if _, counted, _ = e.s.Visit(e.ctx, buyer, "instagram"); counted {
		t.Fatal("the same link counted twice")
	}
	n, err := e.st.Q.CountCampaignVisits(e.ctx, c.ID)
	must(t, err)
	if n.Visits != 1 {
		t.Fatalf("visits = %d", n.Visits)
	}
}

// A disabled campaign still answers the link but is not counted.
func TestDisabledCampaignNotCounted(t *testing.T) {
	e := newEnv(t)
	c, _, err := e.s.Visit(e.ctx, buyer, "paused")
	must(t, err)
	must(t, e.s.SetEnabled(e.ctx, c.ID, false))
	if _, counted, err := e.s.Visit(e.ctx, second, "paused"); err != nil {
		t.Fatal(err)
	} else if counted {
		t.Fatal("a disabled campaign counted an arrival")
	}
}

// A first purchase is "bought", later tariff payments are "renewed", and a refund takes the
// event back rather than leaving a sale that did not stand.
func TestAttributeBoughtAndRenewed(t *testing.T) {
	e := newEnv(t)
	c, _, err := e.s.Visit(e.ctx, buyer, "vk")
	must(t, err)

	bought := e.payment(buyer, 15000, "new")
	must(t, e.s.Attribute(e.ctx, e.st.Q, bought))
	renew := e.payment(buyer, 15000, "renew")
	must(t, e.s.Attribute(e.ctx, e.st.Q, renew))
	// A package is neither.
	must(t, e.s.Attribute(e.ctx, e.st.Q, e.payment(buyer, 5000, "package")))

	ev := e.events(c.ID)
	if ev.Bought != 1 || ev.Renewed != 1 {
		t.Fatalf("bought=%d renewed=%d", ev.Bought, ev.Renewed)
	}
	if ev.Rub != 30000 {
		t.Fatalf("rub = %d", ev.Rub)
	}
	// Counting the same payment again changes nothing (the event row is unique per payment).
	must(t, e.s.Attribute(e.ctx, e.st.Q, renew))
	if ev = e.events(c.ID); ev.Renewed != 1 {
		t.Fatalf("a repeated apply counted twice: renewed=%d", ev.Renewed)
	}
	// A refund of the first purchase takes it back.
	must(t, e.s.Revert(e.ctx, e.st.Q, bought.ID))
	ev = e.events(c.ID)
	if ev.Bought != 0 || ev.Renewed != 1 {
		t.Fatalf("after a refund: bought=%d renewed=%d", ev.Bought, ev.Renewed)
	}
	if ev.Rub != 15000 {
		t.Fatalf("after a refund rub = %d", ev.Rub)
	}
}

// A stranger who never followed a tagged link is attributed to nothing.
func TestAttributeWithoutVisit(t *testing.T) {
	e := newEnv(t)
	must(t, e.s.Attribute(e.ctx, e.st.Q, e.payment(strange, 15000, "new")))
}

// Views carry the campaign with its numbers, and one campaign's events do not leak into
// another's.
func TestViewsAreSeparate(t *testing.T) {
	e := newEnv(t)
	a, _, err := e.s.Visit(e.ctx, buyer, "a")
	must(t, err)
	b, _, err := e.s.Visit(e.ctx, second, "b")
	must(t, err)

	must(t, e.s.Attribute(e.ctx, e.st.Q, e.payment(buyer, 7000, "new")))
	must(t, e.s.Attribute(e.ctx, e.st.Q, e.payment(second, 15000, "new")))

	views, total, err := e.s.Views(e.ctx, 50, 0)
	must(t, err)
	if total != 2 {
		t.Fatalf("total = %d", total)
	}
	byID := map[int64]View{}
	for _, v := range views {
		byID[v.ID] = v
	}
	if got := byID[a.ID]; got.Visits != 1 || got.Bought != 1 || got.Rub != 7000 {
		t.Fatalf("a: %+v", got)
	}
	if got := byID[b.ID]; got.Visits != 1 || got.Bought != 1 || got.Rub != 15000 {
		t.Fatalf("b: %+v", got)
	}
}

// The tag is normalized and checked: the prefix is stripped, odd characters are refused.
func TestNormalizeTag(t *testing.T) {
	ok := map[string]string{
		"instagram":    "instagram",
		"camp_vk":      "vk",
		"  tg-oct_2  ": "tg-oct_2",
	}
	for in, want := range ok {
		got, valid := NormalizeTag(in)
		if !valid || got != want {
			t.Errorf("NormalizeTag(%q) = %q, %v; want %q", in, got, valid, want)
		}
	}
	for _, bad := range []string{"", "  ", "camp_", "a b", "привет", "a/b", "a?b"} {
		if _, valid := NormalizeTag(bad); valid {
			t.Errorf("NormalizeTag(%q) accepted", bad)
		}
	}
}

// A hand-made tag cannot be made twice.
func TestCreateRefusesDuplicate(t *testing.T) {
	e := newEnv(t)
	_, err := e.s.Create(e.ctx, "dup", "Instagram", "")
	must(t, err)
	if _, err = e.s.Create(e.ctx, "dup", "", ""); err != ErrTagInvalid {
		t.Fatalf("a duplicate tag: %v", err)
	}
}
