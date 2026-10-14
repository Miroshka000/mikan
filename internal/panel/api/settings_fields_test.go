package api

import (
	"reflect"
	"strings"
	"testing"
)

// Every field of PATCH /settings is in settingFields once, under its JSON name: a field
// added to the body without a line there would be neither gated nor written.
func TestEverySettingFieldIsDeclared(t *testing.T) {
	declared := map[string]int{}
	for _, f := range settingFields {
		declared[f.name]++
	}
	body := reflect.TypeFor[patchSettingsBody]()
	for i := range body.NumField() {
		name, _, _ := strings.Cut(body.Field(i).Tag.Get("json"), ",")
		switch declared[name] {
		case 1:
			delete(declared, name)
		case 0:
			t.Errorf("%s is in the body, not in settingFields", name)
		default:
			t.Errorf("%s is in settingFields %d times", name, declared[name])
		}
	}
	for name := range declared {
		t.Errorf("settingFields has %s, which the body has not", name)
	}
}
