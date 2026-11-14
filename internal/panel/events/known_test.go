package events_test

import (
	"slices"
	"testing"

	"mikan/internal/panel/events"
)

// The known types are the documented ones, each once and in the documented order; a name
// that merely looks like a type, or is the empty string, is not one.
func TestKnownTypes(t *testing.T) {
	types := events.Types()
	if len(types) != len(events.Docs) {
		t.Fatalf("%d types for %d documented", len(types), len(events.Docs))
	}
	for i, d := range events.Docs {
		if types[i] != d.Type || !events.Known(d.Type) {
			t.Errorf("%s: listed as %q, known %v", d.Type, types[i], events.Known(d.Type))
		}
	}
	// A caller that changes the list leaves the next one's alone.
	types[0] = "changed"
	if events.Types()[0] == "changed" || slices.Contains(events.Types(), "changed") {
		t.Fatal("Types hands out the list it keeps")
	}
	for _, bad := range []string{"", "payment", "payment.", "PAYMENT.PAID", " payment.paid", "payment.paid ", "payment.paid\n", "webhook.test", "bot.trial_not_given"} {
		if events.Known(bad) {
			t.Errorf("%q is known", bad)
		}
	}
}
