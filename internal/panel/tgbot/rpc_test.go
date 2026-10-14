package tgbot

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"mikan/internal/panel/billing"
	"mikan/internal/panel/domain"
	"mikan/internal/panel/tgate"
)

// Everything the screens get over the Panel API crosses as JSON whole: no field of the
// arguments and results is unexported or left out of JSON, or the addon would see less
// than the panel's screens do.
func TestRPCTypesCrossWhole(t *testing.T) {
	marshaler := reflect.TypeFor[json.Marshaler]()
	seen := map[reflect.Type]bool{}
	var walk func(path string, tp reflect.Type)
	walk = func(path string, tp reflect.Type) {
		if seen[tp] || tp.Implements(marshaler) || reflect.PointerTo(tp).Implements(marshaler) {
			return
		}
		seen[tp] = true
		switch tp.Kind() {
		case reflect.Pointer, reflect.Slice, reflect.Array:
			walk(path, tp.Elem())
		case reflect.Map:
			walk(path, tp.Key())
			walk(path, tp.Elem())
		case reflect.Struct:
			for i := range tp.NumField() {
				f := tp.Field(i)
				if !f.IsExported() || f.Tag.Get("json") == "-" {
					t.Errorf("%s: %s.%s does not cross", path, tp, f.Name)
				}
				walk(path, f.Type)
			}
		case reflect.Interface, reflect.Func, reflect.Chan:
			t.Errorf("%s: %s cannot cross", path, tp)
		}
	}
	for _, it := range []reflect.Type{backendType, shopType} {
		methods := []reflect.Type{it}
		if it.Kind() == reflect.Interface {
			methods = methods[:0]
			for i := range it.NumMethod() {
				if m := it.Method(i); m.Name != "Shop" {
					methods = append(methods, m.Type)
				}
			}
		}
		for _, m := range methods {
			for i := range m.NumIn() {
				if m.In(i) != contextType {
					walk(m.String(), m.In(i))
				}
			}
			for i := range m.NumOut() {
				if m.Out(i) != errorType {
					walk(m.String(), m.Out(i))
				}
			}
		}
	}
}

type stubBackend struct {
	Backend
	err error
}

func (s stubBackend) Owner(_ context.Context, userID int64) (int64, error)      { return userID * 10, s.err }
func (s stubBackend) Shop() Shop                                                { return (*billing.Service)(nil) }
func (s stubBackend) Member(context.Context, string, int64, bool) (bool, error) { return true, nil }

// Errors the screens tell apart come back as themselves; only the interfaces' methods are
// reachable; a panel that cannot be asked is an error, not a zero.
func TestRPCErrorsAndMethods(t *testing.T) {
	serve := func(b Backend) *Remote {
		mux := http.NewServeMux()
		mux.Handle("POST /bot/{method}", RPCHandler(b))
		srv := httptest.NewServer(mux)
		t.Cleanup(srv.Close)
		return NewRemote(srv.URL, "t", nil)
	}
	ctx := context.Background()
	r := serve(stubBackend{})
	if tg, err := r.Owner(ctx, 7); err != nil || tg != 70 {
		t.Fatalf("Owner: %d %v", tg, err)
	}
	if in, err := r.Member(ctx, "@chan", 1, true); err != nil || !in {
		t.Fatalf("Member: %v %v", in, err)
	}
	for _, want := range []error{ErrLinkLimit, domain.ErrNotFound, billing.ErrTrialUsed, tgate.ErrLinkExpired} {
		_, err := serve(stubBackend{err: want}).Owner(ctx, 1)
		if !errors.Is(err, want) {
			t.Fatalf("%v came back as %v", want, err)
		}
	}
	other := errors.New("something else")
	if _, err := serve(stubBackend{err: other}).Owner(ctx, 1); err == nil || errors.Is(err, ErrLinkLimit) || !strings.Contains(err.Error(), "something else") {
		t.Fatalf("another error: %v", err)
	}
	// Not a method of the interfaces: refused, whatever the backend has.
	for _, m := range []string{"Shop", "Shop.Close", "store", "Owners", "String", "Notices", "NoticeSent", "NoticeDelivered", "PruneNotices", "BroadcastTargets"} {
		if err := r.call(ctx, m, nil); err == nil || !strings.Contains(err.Error(), "no_method") && !strings.Contains(err.Error(), "no method") {
			t.Fatalf("%s: %v", m, err)
		}
	}
	if err := r.call(ctx, "Owner", []any{1, 2}); err == nil {
		t.Fatal("wrong arguments passed")
	}
	down := NewRemote("http://127.0.0.1:1", "t", nil)
	if _, err := down.Owner(ctx, 1); err == nil {
		t.Fatal("an unreachable panel is no error")
	}
}
