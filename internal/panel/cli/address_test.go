package cli

import (
	"bytes"
	"context"
	"slices"
	"strings"
	"testing"

	"mikan/internal/panel/settings"
	"mikan/internal/panel/store/storetest"
)

// `mikan domain` and `mikan address` change the panel's address from the server: the name
// left behind becomes a former name, a move of the address alone leaves none, and a domain
// is never taken away from a panel that would have no address left.
func TestDomainAndAddressCommands(t *testing.T) {
	ctx := context.Background()
	st, err := storetest.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	set := settings.New(st.Q)
	for k, v := range map[string]any{settings.KeyAdminPath: "adminpath0123456", settings.KeyPanelPort: 2053} {
		if err := settings.Set(ctx, set, k, v); err != nil {
			t.Fatal(err)
		}
	}
	formers := func() []string {
		t.Helper()
		list, err := set.Formers(ctx)
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, f := range list {
			out = append(out, f.Name)
		}
		return out
	}
	var out bytes.Buffer
	if err := domainCmd(ctx, st, set, []string{"--clear"}, nil, &out); err == nil {
		t.Fatal("a panel with no address at all")
	}
	if err := addressCmd(ctx, st, set, []string{"203.0.113.10"}, nil, &out); err != nil {
		t.Fatal(err)
	}
	if err := domainCmd(ctx, st, set, []string{"not a domain"}, nil, &out); err == nil {
		t.Fatal("a bad name")
	}
	out.Reset()
	if err := domainCmd(ctx, st, set, []string{"VPN.example.com."}, nil, &out); err != nil {
		t.Fatal(err)
	}
	if a, _ := set.Address(ctx); a.Domain != "vpn.example.com" || !slices.Equal(formers(), []string{"203.0.113.10"}) {
		t.Fatalf("the domain: %+v %v", a, formers())
	}
	if !strings.Contains(out.String(), "now vpn.example.com") || !strings.Contains(out.String(), "https://vpn.example.com:2053/adminpath0123456/") {
		t.Fatalf("said: %s", out.String())
	}
	// The server moved: the address it left is no former name.
	if err := addressCmd(ctx, st, set, []string{"198.51.100.9"}, nil, &out); err != nil {
		t.Fatal(err)
	}
	if a, _ := set.Address(ctx); a.PublicHost != "198.51.100.9" || !slices.Equal(formers(), []string{"203.0.113.10"}) {
		t.Fatalf("the address: %+v %v", a, formers())
	}
	if err := domainCmd(ctx, st, set, []string{"--clear"}, nil, &out); err != nil {
		t.Fatal(err)
	}
	if a, _ := set.Address(ctx); a.Domain != "" || !slices.Contains(formers(), "vpn.example.com") {
		t.Fatalf("no domain: %+v %v", a, formers())
	}
	out.Reset()
	if err := domainCmd(ctx, st, set, nil, nil, &out); err != nil || !strings.Contains(out.String(), "Former name:     vpn.example.com") {
		t.Fatalf("shown: %v %s", err, out.String())
	}
}
