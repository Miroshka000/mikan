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

// The group takes a chat as the channel does; a topic is of a group, and is no negative number.
func TestConfigValidatesGroup(t *testing.T) {
	for _, tc := range []struct {
		group  string
		thread int64
		valid  bool
	}{
		{"", 0, true}, {"-1001234567890", 0, true}, {" -1001234567890 ", 42, true}, {"@team_chat", 7, true},
		{"", 42, false}, {"-1001234567890", -1, false}, {"team", 0, false}, {"-", 0, false},
		{"123456789", 0, false}, // a private chat, not a group
	} {
		c := AlertsConfig{GroupChat: tc.group, GroupThread: tc.thread}
		if err := c.Validate(); (err == nil) != tc.valid {
			t.Errorf("Validate(%q, %d) error=%v, valid=%v", tc.group, tc.thread, err, tc.valid)
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
