package tgbackup

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/metacubex/age"

	"mikan/internal/panel/settings"
	"mikan/internal/panel/store"
	"mikan/internal/panel/store/storetest"
	"mikan/internal/panel/tgate"
)

const password = "correct horse battery staple"

// TestMain lowers age's scrypt cost: each backup here would otherwise spend a second or
// more of a slow CI runner on it. Decryption reads the cost from the file.
func TestMain(m *testing.M) {
	workFactor = 10
	os.Exit(m.Run())
}

func open(t *testing.T) *store.Store {
	t.Helper()
	st, err := storetest.Open(context.Background(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

// fakeDump writes content as the "dump"; the real pg_dump and pg_restore are covered by
// cli's TestTelegramBackupRestoresOnANewServer.
func fakeDump(content []byte) Dumper {
	return func(_ context.Context, path string) error { return os.WriteFile(path, content, 0o600) }
}

// unpack decrypts a backup and returns the archive's entries by name.
func unpack(t *testing.T, file []byte, pass string) map[string][]byte {
	t.Helper()
	id, err := age.NewScryptIdentity(pass)
	if err != nil {
		t.Fatal(err)
	}
	r, err := age.Decrypt(bytes.NewReader(file), id)
	if err != nil {
		t.Fatalf("decrypt: %v", err)
	}
	gz, err := gzip.NewReader(r)
	if err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(gz)
	out := map[string][]byte{}
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return out
		}
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(tr)
		out[h.Name] = b
	}
}

// The archive is what `mikan restore` takes: data/panel/backup.dump and its directories,
// encrypted; without the password it does not open, and the plain dump is gone.
func TestMakeIsAnEncryptedArchive(t *testing.T) {
	dir := t.TempDir()
	dump := []byte("PGDMP" + strings.Repeat("x", 100_000))
	path, size, err := Make(context.Background(), fakeDump(dump), dir, password, time.Unix(1_800_000_000, 0))
	if err != nil {
		t.Fatal(err)
	}
	file, _ := os.ReadFile(path)
	if int64(len(file)) != size || !bytes.HasPrefix(file, []byte("age-encryption.org/v1")) {
		t.Fatalf("not an age file of %d bytes: %d", size, len(file))
	}
	entries := unpack(t, file, password)
	if len(entries) != 3 || entries["data/"] == nil || entries["data/panel/"] == nil || !bytes.Equal(entries["data/panel/backup.dump"], dump) {
		t.Fatalf("entries: %d", len(entries))
	}
	id, _ := age.NewScryptIdentity("wrong password, long enough")
	if _, err := age.Decrypt(bytes.NewReader(file), id); err == nil {
		t.Fatal("a wrong password opens the backup")
	}
	if _, err := os.Stat(filepath.Join(dir, "backup.dump")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("the plain dump is left behind")
	}
	if fi, _ := os.Stat(path); runtime.GOOS != "windows" && fi.Mode().Perm() != 0o600 {
		t.Fatalf("the encrypted file is %v", fi.Mode().Perm())
	}
}

// A dump too big for a bot is refused before it is read, packed or encrypted.
func TestMakeRefusesATooBigDumpFirst(t *testing.T) {
	dir := t.TempDir()
	big := func(_ context.Context, path string) error {
		f, err := os.Create(path)
		if err != nil {
			return err
		}
		defer f.Close()
		return f.Truncate(tgate.MaxDocument) // sparse: no 50 MB written
	}
	if _, _, err := Make(context.Background(), big, dir, password, time.Now()); !errors.Is(err, ErrTooBig) {
		t.Fatalf("a 50 MB dump: %v", err)
	}
	if left, _ := os.ReadDir(dir); len(left) != 0 {
		t.Fatalf("left behind: %v", left)
	}
}

func TestMakeWithAFailedDump(t *testing.T) {
	dir := t.TempDir()
	failing := func(context.Context, string) error {
		return errors.New("pg_dump: could not connect to db.internal:5432")
	}
	_, _, err := Make(context.Background(), failing, dir, password, time.Now())
	if !errors.Is(err, ErrDump) || Code(err) != "backup_dump_failed" {
		t.Fatalf("a failed dump: %v", err)
	}
	if left, _ := os.ReadDir(dir); len(left) != 0 {
		t.Fatalf("left behind: %v", left)
	}
}

// A dump that hangs is stopped at DumpTimeout; here the context the dump gets has a deadline.
func TestMakeGivesTheDumpADeadline(t *testing.T) {
	var deadline bool
	probe := func(ctx context.Context, path string) error {
		_, deadline = ctx.Deadline()
		return os.WriteFile(path, []byte("PGDMP"), 0o600)
	}
	if _, _, err := Make(context.Background(), probe, t.TempDir(), password, time.Now()); err != nil || !deadline {
		t.Fatalf("the dump ran without a deadline: %v", err)
	}
}

type fakeBot struct {
	client *tgate.Client
	chat   int64
}

func (b fakeBot) InfrastructureClient(context.Context) (*tgate.Client, error) {
	if b.client == nil {
		return nil, tgate.ErrOff
	}
	return b.client, nil
}

func (b fakeBot) InfrastructureAdminChat(context.Context) (int64, bool, error) {
	return b.chat, b.chat != 0, nil
}

type sent struct {
	chat, name, caption string
	length              int64
	file                []byte
}

// fakeTelegram answers sendDocument and keeps what it got.
func fakeTelegram(t *testing.T) (*tgate.Client, func() []sent) {
	api, got := fakeAPI(t)
	return tgate.NewClient(api, "123:abc", nil), got
}

// fakeAPI is fakeTelegram's Bot API at its address.
func fakeAPI(t *testing.T) (string, func() []sent) {
	var mu sync.Mutex
	var got []sent
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/sendDocument") {
			http.NotFound(w, r)
			return
		}
		s := sent{length: r.ContentLength}
		_, params, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
		mr := multipart.NewReader(r.Body, params["boundary"])
		for {
			p, err := mr.NextPart()
			if err != nil {
				break
			}
			b, _ := io.ReadAll(p)
			switch p.FormName() {
			case "chat_id":
				s.chat = string(b)
			case "caption":
				s.caption = string(b)
			case "document":
				s.name, s.file = p.FileName(), b
			}
		}
		mu.Lock()
		got = append(got, s)
		mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": map[string]any{"message_id": 1, "chat": map[string]any{"id": 42}}})
	}))
	t.Cleanup(srv.Close)
	return srv.URL, func() []sent {
		mu.Lock()
		defer mu.Unlock()
		return append([]sent(nil), got...)
	}
}

func TestSendGoesToTheAdminChat(t *testing.T) {
	st := open(t)
	ctx := context.Background()
	set := settings.New(st.Q)
	client, got := fakeTelegram(t)
	dir := t.TempDir()
	now := time.Date(2026, 10, 3, 3, 30, 0, 0, time.UTC)
	s := New(fakeDump([]byte("PGDMP-test")), set, fakeBot{client: client, chat: 42}, dir, func(context.Context) string { return "vpn.example.com" }, func() time.Time { return now }, nil)

	if _, err := s.Send(ctx); !errors.Is(err, ErrNoPassword) {
		t.Fatalf("without a password: %v", err)
	}
	if err := settings.Set(ctx, set, KeyPassword, password); err != nil {
		t.Fatal(err)
	}
	stt, err := s.Send(ctx)
	if err != nil {
		t.Fatal(err)
	}
	g := got()
	if len(g) != 1 || g[0].chat != "42" || g[0].name != "mikan-vpn.example.com-20261003-0330.tar.gz.age" {
		t.Fatalf("sent: %+v", g)
	}
	if !strings.Contains(g[0].caption, "mikan restore") || string(unpack(t, g[0].file, password)["data/panel/backup.dump"]) != "PGDMP-test" {
		t.Fatalf("caption %q", g[0].caption)
	}
	if g[0].length <= int64(len(g[0].file)) {
		t.Fatalf("the upload had no exact length: %d for a %d-byte file", g[0].length, len(g[0].file))
	}
	if stt.LastError != "" || stt.LastOKDay != now.Unix()/86400 || stt.LastSize != int64(len(g[0].file)) {
		t.Fatalf("state: %+v", stt)
	}
	if left, _ := os.ReadDir(dir); len(left) != 0 {
		t.Fatalf("left in the data directory: %v", left)
	}

	s.bot = fakeBot{client: client}
	if stt, err := s.Send(ctx); !errors.Is(err, ErrNoChat) || stt.LastError != "no_admin_chat" || stt.LastOKDay == 0 {
		t.Fatalf("without a chat: %v %+v", err, stt)
	}
	// What the state keeps is a code, never the tool's own text.
	s.bot = fakeBot{client: client, chat: 42}
	s.dump = func(context.Context, string) error { return errors.New("pg_dump: db.internal:5432 refused") }
	if stt, _ := s.Send(ctx); stt.LastError != "backup_dump_failed" {
		t.Fatalf("a failed dump: %+v", stt)
	}
}

// The backups need the Telegram gateway only: its token, route and admin chat, not the
// bot's screens, which may run elsewhere or not at all.
func TestSendThroughTheGatewayAlone(t *testing.T) {
	st := open(t)
	ctx := context.Background()
	set := settings.New(st.Q)
	api, got := fakeAPI(t)
	for k, v := range map[string]any{tgate.KeyToken: "123:abc", tgate.KeyInfraAdminChat: int64(42), KeyPassword: password} {
		if err := settings.Set(ctx, set, k, v); err != nil {
			t.Fatal(err)
		}
	}
	gate := tgate.New(tgate.Deps{Store: st, Settings: set, API: api, Now: time.Now})
	s := New(fakeDump([]byte("PGDMP-gate")), set, gate, t.TempDir(), func(context.Context) string { return "vpn.example.com" }, time.Now, nil)
	if _, err := s.Send(ctx); err != nil {
		t.Fatal(err)
	}
	if g := got(); len(g) != 1 || g[0].chat != "42" || string(unpack(t, g[0].file, password)["data/panel/backup.dump"]) != "PGDMP-gate" {
		t.Fatalf("sent: %+v", g)
	}
}

// Start returns at once; the backup runs apart from the request and State tells the end.
func TestStartRunsInTheBackground(t *testing.T) {
	st := open(t)
	ctx := context.Background()
	set := settings.New(st.Q)
	_ = settings.Set(ctx, set, KeyPassword, password)
	client, got := fakeTelegram(t)
	release := make(chan struct{})
	slow := func(_ context.Context, path string) error {
		<-release
		return os.WriteFile(path, []byte("PGDMP"), 0o600)
	}
	s := New(slow, set, fakeBot{client: client, chat: 42}, t.TempDir(), func(context.Context) string { return "x" }, time.Now, nil)
	if err := s.Start(); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(30 * time.Second)
	for !s.Busy() && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if !s.Busy() {
		t.Fatal("the background backup did not start")
	}
	if err := s.Start(); !errors.Is(err, ErrBusy) {
		t.Fatalf("a second start: %v", err)
	}
	close(release)
	deadline = time.Now().Add(60 * time.Second)
	for s.Busy() && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if s.Busy() || len(got()) != 1 {
		t.Fatalf("the background backup did not finish: busy %v, sent %d", s.Busy(), len(got()))
	}
	if stt, _, _ := settings.Get[State](ctx, set, KeyState); stt.LastOK == 0 {
		t.Fatalf("state: %+v", stt)
	}
}

// A backup cut short by a crash leaves a plain dump; the next start removes it.
func TestCleanupRemovesLeftovers(t *testing.T) {
	dir := t.TempDir()
	left := filepath.Join(dir, tmpPrefix+"123")
	if err := os.MkdirAll(left, 0o700); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(left, "backup.dump"), []byte("secrets"), 0o600)
	_ = os.WriteFile(filepath.Join(dir, "keep.me"), []byte("x"), 0o600)
	New(nil, nil, fakeBot{}, dir, nil, time.Now, nil).Cleanup()
	if _, err := os.Stat(left); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("the leftover is still there")
	}
	if _, err := os.Stat(filepath.Join(dir, "keep.me")); err != nil {
		t.Fatal("something else was removed")
	}
}

// Once a day at the chosen hour; a failed one again the next hour, not every minute.
func TestDue(t *testing.T) {
	st := open(t)
	ctx := context.Background()
	set := settings.New(st.Q)
	now := time.Date(2026, 10, 2, 2, 59, 0, 0, time.UTC)
	s := New(nil, set, fakeBot{}, t.TempDir(), func(context.Context) string { return "" }, func() time.Time { return now }, nil)
	if s.due(ctx) {
		t.Fatal("off by default")
	}
	_ = settings.Set(ctx, set, KeyEnabled, true)
	if s.due(ctx) {
		t.Fatal("before the hour")
	}
	now = now.Add(time.Minute) // 03:00
	if !s.due(ctx) {
		t.Fatal("at the hour")
	}
	_ = settings.Set(ctx, set, KeyState, State{LastTryHour: now.Unix() / 3600})
	if s.due(ctx) {
		t.Fatal("a failed try is not repeated within the hour")
	}
	now = now.Add(time.Hour)
	if !s.due(ctx) {
		t.Fatal("the next hour it is tried again")
	}
	_ = settings.Set(ctx, set, KeyState, State{LastOKDay: now.Unix() / 86400})
	if s.due(ctx) {
		t.Fatal("one a day")
	}
	_ = settings.Set(ctx, set, KeyHour, 23)
	_ = settings.Set(ctx, set, KeyState, State{})
	if s.due(ctx) {
		t.Fatal("an hour of its own")
	}
}

// Switched on at 15:00 with the hour 03: the first backup goes tomorrow at 03:00, not now.
func TestEnablingWaitsForTheHour(t *testing.T) {
	st := open(t)
	ctx := context.Background()
	set := settings.New(st.Q)
	now := time.Date(2026, 10, 3, 15, 0, 0, 0, time.UTC)
	s := New(nil, set, fakeBot{}, t.TempDir(), func(context.Context) string { return "" }, func() time.Time { return now }, nil)
	_ = settings.Set(ctx, set, KeyEnabled, true)
	if err := s.Enabling(ctx); err != nil {
		t.Fatal(err)
	}
	if s.due(ctx) {
		t.Fatal("a backup within a minute of switching on")
	}
	now = time.Date(2026, 10, 4, 3, 0, 0, 0, time.UTC)
	if !s.due(ctx) {
		t.Fatal("no backup at the hour the next day")
	}
	// Switched on before the hour: today's goes at the hour.
	_ = settings.Set(ctx, set, KeyState, State{})
	now = time.Date(2026, 10, 5, 1, 0, 0, 0, time.UTC)
	_ = s.Enabling(ctx)
	now = now.Add(2 * time.Hour)
	if !s.due(ctx) {
		t.Fatal("switched on before the hour: today's backup is skipped")
	}
}

func TestCode(t *testing.T) {
	for err, want := range map[error]string{
		ErrNoChat:                     "no_admin_chat",
		ErrTooBig:                     "backup_too_big",
		tgate.ErrOff:                  "bot_off",
		tgate.ErrUnreachable:          "tg_unreachable",
		&tgate.APIError{Code: 400}:    "tg_unreachable",
		errors.New("pg: host x:5432"): "backup_failed",
	} {
		if got := Code(err); got != want {
			t.Errorf("Code(%v) = %q, want %q", err, got, want)
		}
	}
}

func TestSafeName(t *testing.T) {
	for in, want := range map[string]string{"vpn.example.com": "vpn.example.com", "203.0.113.10": "203.0.113.10", "2001:db8::1": "2001-db8--1", "": "panel", "../x": "..x"} {
		if got := safeName(in); got != want {
			t.Errorf("safeName(%q) = %q, want %q", in, got, want)
		}
	}
}
