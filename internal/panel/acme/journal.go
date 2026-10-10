package acme

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Attempt is one order at a CA as the admin panel lists it: when, at which CA and how it
// ended. The latest come first.
type Attempt struct {
	At     time.Time `json:"at"`
	CA     string    `json:"ca" enum:"letsencrypt,zerossl,google"`
	Error  string    `json:"error,omitempty" doc:"Код errors.acme; пусто — сертификат получен"`
	Detail string    `json:"detail,omitempty" doc:"Слова центра сертификации или системы как есть"`
	Holder string    `json:"holder,omitempty" doc:"Кто держал порт 80 (port80_busy), если это видно"`
}

// journalLen is how many attempts are kept: enough to tell a fault that keeps coming back
// from one that went away.
const journalLen = 8

// detailMax bounds what a CA's words take in the file: its problem documents can list
// every name of an order.
const detailMax = 600

const journalFile = "attempts.json"

// journal keeps the latest attempts of one certificate in a file next to it, so that they
// outlive a restart of the panel.
type journal struct {
	path string

	mu     sync.Mutex
	loaded bool
	list   []Attempt
}

func newJournal(dir string) *journal { return &journal{path: filepath.Join(dir, journalFile)} }

func (j *journal) loadLocked() {
	if j.loaded {
		return
	}
	j.loaded = true
	raw, err := os.ReadFile(j.path)
	if err != nil {
		return
	}
	var list []Attempt
	if json.Unmarshal(raw, &list) == nil {
		j.list = list[:min(len(list), journalLen)]
	}
}

// List is a copy of the attempts, the latest first.
func (j *journal) List() []Attempt {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.loadLocked()
	return append([]Attempt(nil), j.list...)
}

// Add puts a at the top. A file that cannot be written leaves the list in memory: the
// attempt itself is what matters.
func (j *journal) Add(a Attempt) {
	if r := []rune(a.Detail); len(r) > detailMax {
		a.Detail = string(r[:detailMax]) + "…"
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	j.loadLocked()
	j.list = append([]Attempt{a}, j.list[:min(len(j.list), journalLen-1)]...)
	raw, err := json.Marshal(j.list)
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(j.path), 0o700); err != nil {
		return
	}
	tmp := j.path + ".tmp"
	if os.WriteFile(tmp, raw, 0o600) == nil {
		_ = os.Rename(tmp, j.path)
	}
}
