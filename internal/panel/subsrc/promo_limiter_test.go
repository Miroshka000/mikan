package subsrc

import (
	"testing"
	"time"
)

func TestPromoLimiterBlocksByTelegramAccountAndExpires(t *testing.T) {
	var l promoLimiter
	now := time.Unix(1000, 0)
	for i := 0; i < promoAttemptLimit; i++ {
		if !l.allow(11, now, string(rune('A'+i))) {
			t.Fatal("account blocked before reaching attempt limit")
		}
	}
	if l.allow(11, now, "another") {
		t.Fatal("account was not blocked after reaching attempt limit")
	}
	if !l.allow(12, now, "A") {
		t.Fatal("one Telegram account blocked another")
	}
	if !l.allow(11, now.Add(promoBlockDuration), "A") {
		t.Fatal("temporary block did not expire")
	}
	if !l.allow(11, now.Add(promoBlockDuration), "B") {
		t.Fatal("temporary block did not reset its window")
	}
}

func TestPromoLimiterRepeatedPreviewDoesNotUseMoreSlots(t *testing.T) {
	var l promoLimiter
	now := time.Unix(1000, 0)
	for range 20 {
		if !l.allow(11, now, " welcome 30 ") {
			t.Fatal("rechecking the same code consumed another slot")
		}
	}
	for i := 0; i < promoAttemptLimit-1; i++ {
		if !l.allow(11, now, string(rune('B'+i))) {
			t.Fatalf("account blocked after %d distinct codes", i+2)
		}
	}
	if l.allow(11, now, "sixth") {
		t.Fatal("different codes bypassed the per-account limit")
	}
}
