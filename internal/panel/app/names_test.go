package app

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"mikan/internal/panel/domain"
	"mikan/internal/panel/settings"

	"go.yaml.in/yaml/v3"
)

// Names that show in Clash apps (a node's, a protocol's own, the groups') are refused with
// a line break or another control character inside, and spaces and line breaks around
// them are trimmed. One written to the database past the panel still leaves the
// subscription whole.
func TestNamesWithControlCharacters(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	if err := domain.Seed(ctx, h.st, h.now); err != nil {
		t.Fatal(err)
	}
	for k, v := range map[string]any{settings.KeyPublicHost: "203.0.113.10", settings.KeyPanelPort: 21355} {
		if err := settings.Set(ctx, settings.New(h.st.Q), k, v); err != nil {
			t.Fatal(err)
		}
	}
	if resp, _ := h.login(password, ""); resp.StatusCode != http.StatusOK {
		t.Fatal("login")
	}
	csrf := map[string]string{"X-CSRF-Token": h.csrf}
	api := "/" + adminPath + "/api/v1"
	ins, _ := h.st.Q.ListInbounds(ctx)
	inbound := api + "/inbounds/" + strconv.FormatInt(ins[0].ID, 10)

	for _, bad := range []string{"А\nБ", "А\rБ", "А\tБ", "А\x00Б", "А\u0085Б"} {
		for path, body := range map[string]map[string]any{
			api + "/nodes/1":  {"name": bad},
			inbound:           {"display_name": bad},
			api + "/settings": {"sub_group_main": bad},
		} {
			resp, raw := h.do(http.MethodPatch, path, body, csrf)
			if resp.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(string(raw), "name_bad_char") {
				t.Errorf("%s %q: %d %s", path, bad, resp.StatusCode, raw)
			}
		}
	}
	if n, _ := h.st.Q.GetNode(ctx, 1); strings.ContainsAny(n.Name, "\n\r\t\x00") {
		t.Fatalf("node name saved: %q", n.Name)
	}

	// Around the name, a line break goes as a space does.
	if resp, raw := h.do(http.MethodPatch, inbound, map[string]any{"display_name": "\nFast\n"}, csrf); resp.StatusCode != http.StatusOK {
		t.Fatalf("trimmed name: %d %s", resp.StatusCode, raw)
	}
	if in, _ := h.st.Q.GetInbound(ctx, ins[0].ID); in.DisplayName != "Fast" {
		t.Fatalf("display name %q, want Fast", in.DisplayName)
	}

	// Written to the database past the panel: the YAML encoder cannot write a name that starts
	// with a line break, and the user would lose the whole profile.
	if _, err := h.st.DB.ExecContext(ctx, "UPDATE inbounds SET display_name = $1 WHERE id = $2", "\nFast", ins[0].ID); err != nil {
		t.Fatal(err)
	}
	tariffs, _ := h.st.Q.ListTariffs(ctx)
	clock := func() time.Time { return h.now }
	u, err := domain.NewUsers(h.st, domain.NewPool(h.st, clock), noChanges{}, clock).Create(ctx, domain.CreateInput{Name: "a", TariffID: tariffs[1].ID})
	if err != nil {
		t.Fatal(err)
	}
	resp, body := h.do(http.MethodGet, "/"+subPath+"/"+u.SubToken, nil, map[string]string{"User-Agent": "clash.meta/1.19"})
	var cfg struct {
		Proxies []struct {
			Name string `yaml:"name"`
		} `yaml:"proxies"`
	}
	if resp.StatusCode != http.StatusOK || yaml.Unmarshal(body, &cfg) != nil {
		t.Fatalf("subscription: %d %s", resp.StatusCode, body)
	}
	var names []string
	for _, p := range cfg.Proxies {
		names = append(names, p.Name)
	}
	if !strings.Contains("|"+strings.Join(names, "|")+"|", "|Fast|") {
		t.Fatalf("proxies %q, want Fast among them", names)
	}
}
