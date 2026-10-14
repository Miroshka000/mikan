package hysteresis

import "testing"

func TestTrackerHysteresis(t *testing.T) {
	var tr Tracker
	if got := tr.Observe(Healthy, 3, 2); got != nil || tr.Level != Healthy {
		t.Fatalf("initial good sample: transition=%v level=%v", got, tr.Level)
	}
	for i := 0; i < 2; i++ {
		if got := tr.Observe(Unavailable, 3, 2); got != nil {
			t.Fatalf("early failure transition: %+v", got)
		}
	}
	if got := tr.Observe(Unavailable, 3, 2); got == nil || got.From != Healthy || got.To != Unavailable {
		t.Fatalf("failure transition: %+v", got)
	}
	if got := tr.Observe(Healthy, 3, 2); got != nil {
		t.Fatalf("early recovery transition: %+v", got)
	}
	if got := tr.Observe(Healthy, 3, 2); got == nil || got.From != Unavailable || got.To != Healthy {
		t.Fatalf("recovery transition: %+v", got)
	}
}

func TestUnknownDoesNotChangeState(t *testing.T) {
	tr := Tracker{Level: Degraded}
	if got := tr.Observe(Unknown, 3, 2); got != nil || tr.Level != Degraded {
		t.Fatalf("unknown sample changed tracker: %+v %+v", got, tr)
	}
}
