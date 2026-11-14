package subsrc

import (
	"context"
	"reflect"
	"testing"

	"mikan/internal/panel/settings"
	"mikan/internal/panel/store/db"
	"mikan/internal/sub/apps"
	submodel "mikan/internal/sub/model"
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
	got, err := LoadTemplates(ctx, st.Q)
	want := []submodel.Template{{ID: clash.ID, Core: submodel.CoreMihomo, Body: clash.Body, Default: true}, {ID: sb.ID, Core: submodel.CoreSingBox, Body: "{}"}}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("%+v %v", got, err)
	}
	ids, err := TemplateIDs(ctx, st.Q)
	if err != nil || !reflect.DeepEqual(ids, []int64{clash.ID, sb.ID}) {
		t.Fatalf("%v %v", ids, err)
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
