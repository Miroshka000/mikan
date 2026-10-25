package routing

import (
	"testing"

	"mikan/internal/sub/model"
)

// The automatic groups test every five minutes and keep a server faster by less than 50 ms,
// unless the admin says otherwise.
func TestBalance(t *testing.T) {
	if b := (Servers{}).Balance(); b != (Balance{Type: model.URLTest, URL: ProbeURL, Interval: 300, Tolerance: 50}) {
		t.Fatalf("the defaults: %+v", b)
	}
	if b := (Servers{Auto: "fallback", Interval: 120}).Balance(); b != (Balance{Type: model.Fallback, URL: ProbeURL, Interval: 120, Tolerance: 50}) {
		t.Fatalf("fallback: %+v", b)
	}
}
