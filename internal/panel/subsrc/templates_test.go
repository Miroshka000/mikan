package subsrc

import (
	"context"
	"database/sql"
	"reflect"
	"testing"

	"mikan/internal/panel/settings"
	"mikan/internal/panel/store/db"
	"mikan/internal/sub/apps"
	submodel "mikan/internal/sub/model"
	"mikan/internal/sub/routing"
)

// The templates come back as they were saved, the defaults marked; a response rule may name
// one that exists, and one that names a template gone is left out of the policy.
func TestTemplatesAndTheRulesThatNameThem(t *testing.T) {
	ctx := context.Background()
	st := openStore(t)
	clash, err := st.Q.CreateSubTemplate(ctx, db.CreateSubTemplateParams{Core: "mihomo", Name: "Clash", Body: "rules: [MATCH,DIRECT]\n", At: 1})
	if err != nil {
		t.Fatal(err)
	}
	sb, err := st.Q.CreateSubTemplate(ctx, db.CreateSubTemplateParams{Core: "singbox", Name: "sing-box", Body: "{}", At: 1})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Q.SetSubTemplateDefault(ctx, clash.ID); err != nil {
		t.Fatal(err)
	}
	simple, err := st.Q.CreateSubTemplate(ctx, db.CreateSubTemplateParams{Core: "mihomo", Name: "Simple", At: 1,
		Routing: sql.NullString{Valid: true, String: `{"mode": "all", "routes": {"services": {"youtube": "direct"}}, "rules": "DOMAIN,a.example,DIRECT\n# mine\nDOMAIN,b.example,Gone"}`}})
	if err != nil {
		t.Fatal(err)
	}
	g := submodel.GroupNames{}.WithDefaults("en")
	got, routings, err := LoadTemplates(ctx, st.Q, g)
	want := []submodel.Template{{ID: clash.ID, Core: submodel.CoreMihomo, Body: clash.Body, Default: true}, {ID: simple.ID, Core: submodel.CoreMihomo, Simple: true},
		{ID: sb.ID, Core: submodel.CoreSingBox, Body: "{}"}}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("%+v %v", got, err)
	}
	// The simple one's routing is served as the settings' is: the rule to a group nobody has goes.
	r := routings[simple.ID]
	if len(routings) != 1 || r.Mode != routing.All || r.Routes.Services["youtube"] != routing.TargetDirect || len(r.Rules) != 1 || r.Rules[0].When[0].Value != "a.example" {
		t.Fatalf("routings: %+v", routings)
	}
	ids, err := TemplateIDs(ctx, st.Q)
	if err != nil || !reflect.DeepEqual(ids, []int64{clash.ID, simple.ID, sb.ID}) {
		t.Fatalf("%v %v", ids, err)
	}
	// A template's routing must be an object, and of a simple Clash template with no text.
	for _, bad := range []db.CreateSubTemplateParams{
		{Core: "mihomo", Name: "list", Routing: sql.NullString{Valid: true, String: "[]"}},
		{Core: "singbox", Name: "of sing-box", Routing: sql.NullString{Valid: true, String: "{}"}},
		{Core: "mihomo", Name: "with text", Body: "rules: []", Routing: sql.NullString{Valid: true, String: "{}"}},
	} {
		if _, err := st.Q.CreateSubTemplate(ctx, bad); err == nil {
			t.Errorf("%s: taken", bad.Name)
		}
	}
	if r := TemplateRoutingOf(`{"mode": 5`); r.Mode != "" || r.Rules != "" {
		t.Errorf("a routing that does not read: %+v", r)
	}
	named := testRule("named", apps.Action{Template: sb.ID})
	if err := named.Check("rule", RuleOpts(ids)); err != nil {
		t.Fatalf("a rule naming a template there is: %v", err)
	}
	if err := testRule("gone", apps.Action{Template: 99}).Check("rule", RuleOpts(ids)); err == nil {
		t.Fatal("a rule naming no template is taken")
	}
	if err := SaveRules(ctx, st.Q, []apps.Rule{named, testRule("clash", apps.Action{Template: clash.ID})}); err != nil {
		t.Fatal(err)
	}
	if n, err := st.Q.DeleteSubTemplate(ctx, clash.ID); n != 1 || err != nil {
		t.Fatal(n, err)
	}
	p, err := LoadPolicy(ctx, st.Q, settings.New(st.Q), nil)
	if err != nil || len(p.Rules) != 1 || p.Rules[0].Name != "named" {
		t.Fatalf("%+v %v", p.Rules, err)
	}
}
