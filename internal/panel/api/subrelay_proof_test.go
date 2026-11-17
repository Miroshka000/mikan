package api

import (
	"database/sql"
	"errors"
	"sync"
	"testing"

	"mikan/internal/panel/store/storetest"
)

func TestRelayProofSecretIsStableConcurrentAndTokenBound(t *testing.T) {
	st, err := storetest.Open(t.Context(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	first := &relayProofSigner{q: st.Q}
	proof, err := first.proof(t.Context(), "unknown", false)
	if err != nil || proof != "" {
		t.Fatal("unknown ticket initialized signer")
	}
	if _, err := st.Q.GetSetting(t.Context(), relayProofSetting); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal("unauthenticated proof created panel secret")
	}
	var wg sync.WaitGroup
	tickets := make(chan string, 2)
	failures := make(chan error, 2)
	for range 2 {
		wg.Go(func() {
			signer := &relayProofSigner{q: st.Q}
			value, err := signer.proof(t.Context(), "first-token", true)
			if err != nil {
				failures <- err
				return
			}
			tickets <- value
		})
	}
	wg.Wait()
	close(tickets)
	close(failures)
	for err := range failures {
		t.Fatal(err)
	}
	var stored string
	for ticket := range tickets {
		if len(ticket) != 43 || stored != "" && ticket != stored {
			t.Fatal("concurrent signer initialization changed secret")
		}
		stored = ticket
	}
	// Restarting the panel retains the secret; the same ticket cannot identify another token.
	restarted := &relayProofSigner{q: st.Q}
	got, err := restarted.proof(t.Context(), "first-token", false)
	if err != nil || got != stored {
		t.Fatal("diagnostic ticket did not survive signer restart")
	}
	other, err := restarted.proof(t.Context(), "second-token", false)
	if err != nil || other == stored {
		t.Fatal("ticket was not bound to its token")
	}
}
