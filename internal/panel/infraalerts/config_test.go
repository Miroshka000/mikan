package infraalerts

import "testing"

func TestConfigValidatesPublicTarget(t *testing.T) {
	for _, tc := range []struct {
		channel string
		valid   bool
	}{{"@status_channel", true}, {"-1001234567890", true}, {"", false}, {"https://example.com", false}, {"@no", false}} {
		c := AlertsConfig{PublicEnabled: true, PublicChannel: tc.channel}
		err := c.Validate()
		if (err == nil) != tc.valid {
			t.Errorf("Validate(%q) error=%v, valid=%v", tc.channel, err, tc.valid)
		}
	}
}

func TestAlertsConfigPatchKeepsOmittedSwitches(t *testing.T) {
	base := Default()
	disabled := false
	got := (AlertsConfigPatch{Events: &EventsPatch{Node: &disabled}}).Merge(base)
	if got.Events.Node || !got.Events.Warp || !got.Events.Inbound || !got.Events.Update {
		t.Fatalf("partial patch lost existing event switches: %+v", got.Events)
	}
}

func TestAppendPendingDeduplicates(t *testing.T) {
	a := delivery{Key: "same", Target: "admin", Text: "one"}
	got := appendPending([]delivery{a}, delivery{Key: "same", Target: "admin", Text: "two"})
	if len(got) != 1 || got[0].Text != "one" {
		t.Fatalf("duplicate queue item: %+v", got)
	}
}
