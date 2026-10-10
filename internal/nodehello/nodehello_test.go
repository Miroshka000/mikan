package nodehello

import (
	"testing"
	"time"
)

func TestStatusFile(t *testing.T) {
	dir := t.TempDir()
	if _, err := Read(dir); err == nil {
		t.Fatal("no file yet")
	}
	at := time.Date(2026, 10, 10, 9, 0, 0, 0, time.UTC)
	s := Status{Result: Result{Code: "timeout", Params: map[string]string{"port": "31234"}, SeenIP: "203.0.113.7"}, At: at, Started: at.Add(-time.Minute), Attempt: 3, Final: true}
	if err := Write(dir, s); err != nil {
		t.Fatal(err)
	}
	got, err := Read(dir)
	if err != nil || got.Code != "timeout" || got.Params["port"] != "31234" || !got.At.Equal(at) || !got.Final || got.Attempt != 3 {
		t.Fatalf("%+v %v", got, err)
	}
}

func TestFinal(t *testing.T) {
	for r, want := range map[*Result]bool{
		{OK: true}:               true,
		{Code: "timeout"}:        false,
		{Code: "refused"}:        false,
		{Code: PanelUnreachable}: false,
		{Code: "pin_mismatch"}:   true,
		{Code: PanelRejected}:    true,
		{Code: NoPanelURL}:       true,
		{Code: PanelUnverified}:  true,
	} {
		if Final(*r) != want {
			t.Errorf("%+v: %v", *r, !want)
		}
	}
}
