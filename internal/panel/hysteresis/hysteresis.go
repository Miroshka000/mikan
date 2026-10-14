// Package hysteresis holds what decides that a node, an exit or an inbound is down or back:
// a bad sample must persist for a number of rounds before it counts, and so must a good one.
// The admin's alerts (infraalerts) and the addons' node.down and node.up (nodesync) read the
// same node health through it, so the two tell the same story at the same moment.
package hysteresis

// How many samples in a row it takes. Node health arrives every five seconds.
const (
	NodeFailAfter = 3 // failed checks before a node is down
	RecoverAfter  = 2 // good checks before what was down is back
)

type Level uint8

const (
	Unknown Level = iota
	Healthy
	Degraded
	Unavailable
)

// Tracker applies failure and recovery hysteresis. A bad sample must persist for the
// configured number of rounds; a good sample must persist too before recovery is emitted.
type Tracker struct {
	Level      Level
	BadRounds  int
	GoodRounds int
}

type Transition struct {
	From Level
	To   Level
}

func (t *Tracker) Observe(sample Level, failAfter, recoverAfter int) *Transition {
	if sample == Unknown {
		return nil
	}
	if failAfter < 1 {
		failAfter = 1
	}
	if recoverAfter < 1 {
		recoverAfter = 1
	}
	if sample == Healthy {
		t.BadRounds = 0
		if t.Level == Unknown || t.Level == Healthy {
			t.Level, t.GoodRounds = Healthy, 0
			return nil
		}
		t.GoodRounds++
		if t.GoodRounds >= recoverAfter {
			from := t.Level
			t.Level, t.GoodRounds = Healthy, 0
			return &Transition{From: from, To: Healthy}
		}
		return nil
	}
	t.GoodRounds = 0
	if t.Level == sample {
		t.BadRounds = 0
		return nil
	}
	t.BadRounds++
	if t.BadRounds >= failAfter {
		from := t.Level
		t.Level, t.BadRounds = sample, 0
		return &Transition{From: from, To: sample}
	}
	return nil
}
