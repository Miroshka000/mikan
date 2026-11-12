package nodesync

import (
	"context"
	"mikan/internal/frontpage"
	"slices"
	"testing"

	"mikan/internal/proto"
)

// A node's front page reaches it: the state names the page, and with the shared 443 the
// router gives it the ClientHellos with no name; without the shared 443 the state names the
// page all the same, for the inbounds on an HTTP transport.
func TestFrontPageInTheState(t *testing.T) {
	s, _, st, _, _ := setup(t)
	ctx := context.Background()
	q := st.Q
	ins, _ := q.ListInbounds(ctx)
	routed := false
	for _, in := range ins {
		tm, err := proto.Parse(in.Config)
		if err != nil || !proto.Reality(tm) || !proto.CanSNI443(tm) {
			continue
		}
		mk, _ := tm["mikan"].(map[string]any)
		if mk == nil {
			mk = map[string]any{}
		}
		mk["sni443"] = true
		tm["mikan"] = mk
		if _, err := st.DB.ExecContext(ctx, "UPDATE inbounds SET config = $1 WHERE id = $2", proto.Marshal(tm), in.ID); err != nil {
			t.Fatal(err)
		}
		routed = true
		break
	}
	if !routed {
		t.Fatal("no REALITY inbound for the shared 443 seeded")
	}
	node := func(router int, page string) {
		t.Helper()
		if _, err := st.DB.ExecContext(ctx, "UPDATE nodes SET sni_router = $1, front_page = $2 WHERE id = $3", router, page, s.id); err != nil {
			t.Fatal(err)
		}
	}

	node(1, "cats")
	d, err := s.desired(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if d.FrontPage != "cats" || d.Router == nil || !slices.Contains(d.Router.Stub, "") {
		t.Fatalf("the page with the shared 443: page %q router %+v", d.FrontPage, d.Router)
	}
	node(1, "")
	if d, _ = s.desired(ctx); d.FrontPage != "" || d.Router == nil || len(d.Router.Stub) != 0 {
		t.Fatalf("no page: page %q router %+v", d.FrontPage, d.Router)
	}
	node(0, "cats")
	if d, _ = s.desired(ctx); d.FrontPage != "cats" || d.Router != nil {
		t.Fatalf("the page without the shared 443: page %q router %+v", d.FrontPage, d.Router)
	}
}

// The installation's seed goes to a node with its page, and only with it: a node that shows
// no page has nothing to make its own.
func TestFrontSeedGoesWithThePage(t *testing.T) {
	s, _, st, _, _ := setup(t)
	ctx := context.Background()
	page := func(name string) {
		t.Helper()
		if _, err := st.DB.ExecContext(ctx, "UPDATE nodes SET front_page = $1 WHERE id = $2", name, s.id); err != nil {
			t.Fatal(err)
		}
	}
	page("cats")
	if d, err := s.desired(ctx); err != nil || d.FrontSeed != "" {
		t.Fatalf("no seed set: %q %v", d.FrontSeed, err)
	}
	s.m.SetFrontSeed("the-installations-own")
	if d, err := s.desired(ctx); err != nil || d.FrontSeed != "the-installations-own" || d.FrontPage != "cats" {
		t.Fatalf("with the page: seed %q page %q %v", d.FrontSeed, d.FrontPage, err)
	}
	page("")
	if d, err := s.desired(ctx); err != nil || d.FrontSeed != "" {
		t.Fatalf("no page, no seed: %q %v", d.FrontSeed, err)
	}
}

// The admin's own page goes to a node whole. A node from before it knows only the built-in
// pages: it gets the installation's built-in one, never an empty answer. With no page saved
// the node shows none.
func TestOwnFrontPageInTheState(t *testing.T) {
	s, _, st, _, _ := setup(t)
	ctx := context.Background()
	if _, err := st.DB.ExecContext(ctx, "UPDATE nodes SET front_page = 'custom' WHERE id = $1", s.id); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { frontpage.SetCustom(nil) })
	s.m.SetFrontSeed("the-installations-own")
	if d, err := s.desired(ctx); err != nil || d.FrontPage != "" || d.FrontHTML != "" || d.FrontSeed != "" {
		t.Fatalf("no page saved: page %q, %d bytes, seed %q, %v", d.FrontPage, len(d.FrontHTML), d.FrontSeed, err)
	}
	const page = "<!doctype html><title>Bakery</title>"
	frontpage.SetCustom([]byte(page))
	if d, err := s.desired(ctx); err != nil || d.FrontPage != frontpage.Custom || d.FrontHTML != page {
		t.Fatalf("the page whole: page %q html %q %v", d.FrontPage, d.FrontHTML, err)
	}
	s.nodeVersion = "0.5.2.1-rc.9"
	if d, err := s.desired(ctx); err != nil || d.FrontPage != frontpage.Pick("the-installations-own") || d.FrontHTML != "" || d.FrontSeed == "" {
		t.Fatalf("a node from before the page: page %q, %d bytes, seed %q, %v", d.FrontPage, len(d.FrontHTML), d.FrontSeed, err)
	}
	for _, v := range []string{"0.5.2.1-rc.10", "0.5.2.1", "0.6.0", "dev"} {
		s.nodeVersion = v
		if d, err := s.desired(ctx); err != nil || d.FrontPage != frontpage.Custom || d.FrontHTML != page {
			t.Fatalf("a node on %s: page %q, %d bytes, %v", v, d.FrontPage, len(d.FrontHTML), err)
		}
	}
}
