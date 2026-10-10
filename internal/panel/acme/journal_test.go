package acme

import (
	"strings"
	"testing"
	"time"
)

// The latest attempt comes first, the list stays short, a long answer of the CA is cut,
// and a restart of the panel finds the list again.
func TestJournalKeepsTheLatestAttempts(t *testing.T) {
	dir := t.TempDir()
	j := newJournal(dir)
	if got := j.List(); len(got) != 0 {
		t.Fatalf("a new journal lists %d", len(got))
	}
	at := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	for i := range journalLen + 3 {
		j.Add(Attempt{At: at.Add(time.Duration(i) * time.Minute), CA: CALetsEncrypt, Error: CodePort80Busy, Holder: "nginx"})
	}
	j.Add(Attempt{At: at.Add(time.Hour), CA: CALetsEncrypt, Detail: strings.Repeat("x", detailMax+50)})

	got := newJournal(dir).List()
	if len(got) != journalLen {
		t.Fatalf("%d attempts kept, want %d", len(got), journalLen)
	}
	if !got[0].At.Equal(at.Add(time.Hour)) || got[0].Error != "" {
		t.Fatalf("the latest is not first: %+v", got[0])
	}
	if n := len([]rune(got[0].Detail)); n != detailMax+1 {
		t.Fatalf("detail of %d runes, want %d with the ellipsis", n, detailMax+1)
	}
	if got[1].Holder != "nginx" || got[1].Error != CodePort80Busy {
		t.Fatalf("the failure is not kept: %+v", got[1])
	}
}
