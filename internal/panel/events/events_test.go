package events_test

import (
	"encoding/json"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"mikan/internal/panel/events"
)

// Every type the panel emits is documented once, with data that is valid JSON of the struct
// it names, and no secret: the list PROTOCOL-v2.md is written from.
func TestEveryTypeIsDocumented(t *testing.T) {
	valid := regexp.MustCompile(`^[a-z0-9_]+(\.[a-z0-9_]+)+$`)
	seen := map[string]bool{}
	for _, d := range events.Docs {
		if !valid.MatchString(d.Type) || seen[d.Type] || d.About == "" || strings.Contains(d.About, "—") {
			t.Errorf("%q: not a name, listed twice or no description", d.Type)
		}
		seen[d.Type] = true
		raw, err := json.Marshal(d.Data)
		if err != nil || !json.Valid(raw) || raw[0] != '{' {
			t.Errorf("%s: data %s %v", d.Type, raw, err)
		}
		// What is written is what is read back: no field is lost on the way.
		back := reflect.New(reflect.TypeOf(d.Data))
		if err := json.Unmarshal(raw, back.Interface()); err != nil || !reflect.DeepEqual(back.Elem().Interface(), d.Data) {
			t.Errorf("%s: %s does not read back: %v", d.Type, raw, err)
		}
		for _, secret := range []string{"token", "key", "hwid", "secret", "password", "address"} {
			if strings.Contains(strings.ToLower(string(raw)), `"`+secret) {
				t.Errorf("%s: data names %q", d.Type, secret)
			}
		}
	}
	// Every constant of the feed is in the list.
	for _, typ := range []string{events.PaymentPaid, events.PaymentRefunded, events.UserCreated, events.UserExpiring, events.UserExpired,
		events.UserStarted, events.UserSwitched, events.UserRemoved,
		events.UserGranted, events.Traffic90, events.Traffic100, events.TrialStarted, events.TrialNotGiven, events.DeviceBound, events.DeviceUnbound,
		events.NodeDown, events.NodeUp, events.BalanceTopup} {
		if !seen[typ] {
			t.Errorf("%s is not documented", typ)
		}
	}
	if len(events.Docs) != 18 {
		t.Errorf("%d types documented", len(events.Docs))
	}
}
