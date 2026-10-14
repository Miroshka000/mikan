package api

import (
	"context"
	"errors"
	"testing"

	"github.com/danielgtaylor/huma/v2"

	"mikan/internal/panel/addons"
	"mikan/internal/panel/store/db"
)

// A code setting is a script an addon runs, so an API key, even a "full" one, never sets
// it: the handler refuses before it looks at the addon or its settings. The route's
// session-only mark stops a key first (TestFullKeyCannotTakeOverThePanel); this is the
// second wall.
func TestAddonSettingsRefuseAnAPIKey(t *testing.T) {
	in := &patchAddonInput{ID: "telegram"}
	in.Body.Settings = map[string]any{"script": "print(1)"}
	for _, scope := range []string{"full", "read"} {
		key := context.WithValue(context.Background(), keyAPIKey, db.ApiKey{Scope: scope})
		_, err := (&handlers{}).patchAddon(key, in)
		var he huma.StatusError
		if !errors.As(err, &he) || he.GetStatus() != 403 {
			t.Errorf("%s key: %v, want 403 session_only", scope, err)
		}
	}
}

// The form is described to the admin per type: only what belongs to the type is sent,
// a secret keeps its value back, and a type the panel does not know is flagged.
func TestAddonFieldView(t *testing.T) {
	lo, hi := int64(1), int64(9)
	values := addons.Settings{"mode": "b", "limit": float64(4), "script": "x = 1", "token": "secret", "greeting": "hi"}
	sel := addonField(addons.Field{Key: "mode", Type: "select", Pattern: "^x$", Min: &lo, Default: "a", Help: addons.Text{"en": "How"},
		Options: []addons.Option{{Value: "a", Label: addons.Text{"en": "A"}}, {Value: "b"}}}, values)
	if sel.Pattern != "" || sel.Min != nil || len(sel.Options) != 2 || sel.Options[0].Label["en"] != "A" || sel.Options[1].Label == nil ||
		sel.Default != "a" || sel.Help["en"] != "How" || sel.Value != "b" || !sel.Set || sel.Unsupported {
		t.Errorf("select: %+v", sel)
	}
	num := addonField(addons.Field{Key: "limit", Type: "number", Min: &lo, Max: &hi}, values)
	if num.Min == nil || *num.Min != 1 || num.Max == nil || *num.Max != 9 || num.Value != float64(4) {
		t.Errorf("number: %+v", num)
	}
	if code := addonField(addons.Field{Key: "script", Type: "code"}, values); code.Value != "x = 1" || code.Unsupported {
		t.Errorf("code: %+v", code)
	}
	if tok := addonField(addons.Field{Key: "token", Type: "text", Secret: true}, values); tok.Value != nil || !tok.Set || !tok.Secret {
		t.Errorf("secret text: %+v", tok)
	}
	unknown := addonField(addons.Field{Key: "greeting", Type: "color", Pattern: "x", Default: "#fff"}, values)
	if !unknown.Unsupported || unknown.Type != "color" || unknown.Value != nil || unknown.Pattern != "" || unknown.Default != nil {
		t.Errorf("unknown type: %+v", unknown)
	}
}
