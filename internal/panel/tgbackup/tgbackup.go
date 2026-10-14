// Package tgbackup sends the panel's database to the admin's Telegram chat once a day,
// encrypted with a password the admin chose.
//
// Only the database goes: users, plans, settings, the bot's token. The server's .env,
// certificates and node keys stay on the server; the panel cannot read them, by design,
// and a backup sent from the panel must not reach further than the panel does.
//
// The file is a tar.gz with data/panel/backup.dump inside (pg_dump, custom format, as the
// host's own backups have it), encrypted with age (scrypt):
//
//	age -d -o mikan.tar.gz mikan-….tar.gz.age
//	mikan restore mikan.tar.gz
//
// Nothing is held in memory: the dump and the encrypted file are written next to the
// panel's data, in a private directory removed when the file is sent (or left by a crash
// and removed at the next start), and the upload reads the file.
package tgbackup

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/metacubex/age"

	"mikan/internal/panel/settings"
	"mikan/internal/panel/tgate"
)

const (
	KeyEnabled  = "tg_backup"          // on/off, off by default
	KeyHour     = "tg_backup_hour"     // UTC hour of the daily backup, DefaultHour when unset
	KeyPassword = "tg_backup_password" // the age passphrase; never shown back
	KeyState    = "tg_backup_state"    // State
	DefaultHour = 3
	// MinPassword: the file stays in the chat's history for good, so a short phrase could be
	// guessed offline at leisure. age's scrypt slows each guess, the length does the rest.
	MinPassword = 20
	// DumpTimeout bounds pg_dump: a dump that hangs must not hold the next backup forever.
	DumpTimeout = 10 * time.Minute
	// SendTimeout bounds a whole backup: dump, packing and the upload.
	SendTimeout = 20 * time.Minute
	// tmpPrefix names the private directories a backup is made in.
	tmpPrefix = ".telegram-backup-"
)

// Enabled is the switch.
var Enabled = settings.Switch{Key: KeyEnabled}

// State is what the last backups did.
type State struct {
	LastOK      int64  `json:"last_ok,omitempty"`       // unix time of the last backup sent
	LastTry     int64  `json:"last_try,omitempty"`      // unix time of the last attempt
	LastError   string `json:"last_error,omitempty"`    // the error code of the last attempt, "" when it did not fail
	LastSize    int64  `json:"last_size,omitempty"`     // bytes of the last file sent
	LastOKDay   int64  `json:"last_ok_day,omitempty"`   // unix day of LastOK
	LastTryHour int64  `json:"last_try_hour,omitempty"` // unix hour of LastTry
}

// Errors the admin sees, as codes; State keeps only the code, the details go to the log.
var (
	ErrNoChat      = errors.New("no_admin_chat")      // no admin chat connected to the bot
	ErrNoPassword  = errors.New("no_backup_password") // no password to encrypt with
	ErrTooBig      = errors.New("backup_too_big")     // over the 50 MB a bot may send
	ErrBusy        = errors.New("backup_busy")        // one is being made right now
	ErrDump        = errors.New("backup_dump_failed") // pg_dump failed or took too long
	ErrUnavailable = errors.New("backup_unavailable") // the panel has no data directory
)

// Bot is the part of the Telegram gateway the backups use (tgate.Gateway): no screens of
// the bot, only its token, route and the admin's chat.
type Bot interface {
	InfrastructureClient(ctx context.Context) (*tgate.Client, error)
	InfrastructureAdminChat(ctx context.Context) (int64, bool, error)
}

// Dumper writes a pg_dump archive (custom format) of the panel's database to path.
type Dumper func(ctx context.Context, path string) error

type Service struct {
	dump    Dumper
	set     *settings.Settings
	bot     Bot
	dataDir string
	name    func(ctx context.Context) string // the server's name in the file name and caption
	now     func() time.Time
	log     *slog.Logger
	mu      sync.Mutex
	// A backup Start began runs on Run's context and Run waits for it, so it never works
	// on a database the panel has closed: life is that context, closed set once Run ended.
	lifeMu  sync.Mutex
	life    context.Context
	closed  bool
	started sync.WaitGroup
}

// New: dataDir is the panel's data directory, where the dump is made before it is sent;
// without one there are no backups.
func New(dump Dumper, set *settings.Settings, bot Bot, dataDir string, name func(ctx context.Context) string, now func() time.Time, log *slog.Logger) *Service {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &Service{dump: dump, set: set, bot: bot, dataDir: dataDir, name: name, now: now, log: log}
}

// Cleanup removes what a backup interrupted by a crash, an OOM kill or a stop left in the
// data directory: a plain-text dump of every secret of the panel.
func (s *Service) Cleanup() {
	if s.dataDir == "" {
		return
	}
	entries, err := os.ReadDir(s.dataDir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), tmpPrefix) {
			if err := os.RemoveAll(filepath.Join(s.dataDir, e.Name())); err != nil {
				s.log.Warn("telegram backup: a leftover was not removed", "path", e.Name(), "err", err)
			}
		}
	}
}

// Run sends the day's backup at the chosen hour; a failed one is tried again each hour of
// that day.
func (s *Service) Run(ctx context.Context) {
	s.Cleanup()
	s.lifeMu.Lock()
	s.life = ctx
	s.lifeMu.Unlock()
	defer func() {
		s.lifeMu.Lock()
		s.closed = true
		s.lifeMu.Unlock()
		s.started.Wait()
	}()
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		if s.due(ctx) {
			if _, err := s.Send(ctx); err != nil && !errors.Is(err, ErrBusy) {
				s.log.Warn("telegram backup", "err", err)
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func (s *Service) hour(ctx context.Context) (int, error) {
	hour, ok, err := settings.Get[int](ctx, s.set, KeyHour)
	if err != nil || !ok {
		return DefaultHour, err
	}
	return hour, nil
}

func (s *Service) due(ctx context.Context) bool {
	on, err := s.set.On(ctx, Enabled)
	if err != nil || !on {
		return false
	}
	hour, err := s.hour(ctx)
	if err != nil {
		return false
	}
	st, _, err := settings.Get[State](ctx, s.set, KeyState)
	if err != nil {
		return false
	}
	now := s.now().UTC()
	day, h := now.Unix()/86400, now.Unix()/3600
	return now.Hour() >= hour && st.LastOKDay < day && st.LastTryHour < h
}

// Enabling is called when the admin switches backups on: the first one goes at the chosen
// hour, not at once when that hour of today is past ("Send now" is there for that).
func (s *Service) Enabling(ctx context.Context) error {
	hour, err := s.hour(ctx)
	if err != nil {
		return err
	}
	st, _, err := settings.Get[State](ctx, s.set, KeyState)
	if err != nil {
		return err
	}
	now := s.now().UTC()
	if now.Hour() >= hour && st.LastOKDay < now.Unix()/86400 {
		st.LastOKDay = now.Unix() / 86400
		return settings.Set(ctx, s.set, KeyState, st)
	}
	return nil
}

// Busy says whether a backup is being made.
func (s *Service) Busy() bool {
	if s.mu.TryLock() {
		s.mu.Unlock()
		return false
	}
	return true
}

// Start makes a backup in the background, apart from the request that asked for it;
// State tells how it went.
func (s *Service) Start() error {
	if s.Busy() {
		return ErrBusy
	}
	s.lifeMu.Lock()
	defer s.lifeMu.Unlock()
	if s.closed {
		return ErrBusy
	}
	life := s.life
	if life == nil { // Run not started: the CLI, tests
		life = context.Background()
	}
	s.started.Go(func() {
		ctx, cancel := context.WithTimeout(life, SendTimeout)
		defer cancel()
		if _, err := s.Send(ctx); err != nil && !errors.Is(err, ErrBusy) {
			s.log.Warn("telegram backup", "err", err)
		}
	})
	return nil
}

// Send makes a backup and sends it now.
func (s *Service) Send(ctx context.Context) (State, error) {
	if !s.mu.TryLock() {
		return State{}, ErrBusy
	}
	defer s.mu.Unlock()
	ctx, cancel := context.WithTimeout(ctx, SendTimeout)
	defer cancel()
	st, _, err := settings.Get[State](ctx, s.set, KeyState)
	if err != nil {
		return st, err
	}
	now := s.now().UTC()
	st.LastTry, st.LastTryHour = now.Unix(), now.Unix()/3600
	size, err := s.send(ctx, now)
	st.LastError = ""
	if err != nil {
		st.LastError = Code(err)
	} else {
		st.LastOK, st.LastOKDay, st.LastSize = now.Unix(), now.Unix()/86400, size
	}
	// The state is written even when the backup ran out of its time.
	sctx, scancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer scancel()
	if serr := settings.Set(sctx, s.set, KeyState, st); serr != nil && err == nil {
		err = serr
	}
	return st, err
}

// Code is the error code State keeps for err: never pg_dump's or Telegram's own text, which
// may name hosts, ports and databases.
func Code(err error) string {
	var ae *tgate.APIError
	for _, known := range []error{ErrNoChat, ErrNoPassword, ErrTooBig, ErrBusy, ErrDump, ErrUnavailable} {
		if errors.Is(err, known) {
			return known.Error()
		}
	}
	switch {
	case errors.Is(err, tgate.ErrOff):
		return "bot_off"
	case errors.Is(err, tgate.ErrUnreachable), errors.As(err, &ae), errors.Is(err, context.DeadlineExceeded):
		return "tg_unreachable"
	}
	return "backup_failed"
}

func (s *Service) send(ctx context.Context, now time.Time) (int64, error) {
	if s.dataDir == "" {
		return 0, ErrUnavailable
	}
	password, err := s.set.String(ctx, KeyPassword)
	if err != nil {
		return 0, err
	}
	if len(password) < MinPassword {
		return 0, ErrNoPassword
	}
	chat, ok, err := s.bot.InfrastructureAdminChat(ctx)
	if err != nil {
		return 0, err
	}
	if !ok {
		return 0, ErrNoChat
	}
	client, err := s.bot.InfrastructureClient(ctx)
	if err != nil {
		return 0, err
	}
	tmp, err := os.MkdirTemp(s.dataDir, tmpPrefix)
	if err != nil {
		return 0, err
	}
	defer os.RemoveAll(tmp)
	path, size, err := Make(ctx, s.dump, tmp, password, now)
	if err != nil {
		return 0, err
	}
	f, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	host := safeName(s.name(ctx))
	name := "mikan-" + host + "-" + now.Format("20060102-1504") + ".tar.gz.age"
	caption := fmt.Sprintf("mikan · %s · %s UTC · %s\n\nage -d -o mikan.tar.gz %s\nmikan restore mikan.tar.gz",
		host, now.Format("2006-01-02 15:04"), sizeText(size), name)
	if _, err := client.SendDocument(ctx, chat, name, f, size, caption); err != nil {
		return 0, err
	}
	return size, nil
}

// workFactor overrides age's scrypt cost (2^18) when not zero; tests lower it, a backup
// never does.
var workFactor int

// packOverhead is what tar, gzip and age add at most to a dump that is already compressed
// (pg_dump's custom format): the size check before packing leaves this much room.
const packOverhead = 1 << 20

// Make dumps the database into dir (a private directory of the caller's), packs the dump
// as data/panel/backup.dump (where `mikan restore` looks for it) and encrypts the archive
// into dir/backup.tar.gz.age, streaming from file to file. A dump too big for a bot is
// refused before it is packed.
func Make(ctx context.Context, dump Dumper, dir, password string, now time.Time) (path string, size int64, err error) {
	dumpPath := filepath.Join(dir, "backup.dump")
	dctx, cancel := context.WithTimeout(ctx, DumpTimeout)
	err = dump(dctx, dumpPath)
	cancel()
	if err != nil {
		return "", 0, fmt.Errorf("%w: %v", ErrDump, err)
	}
	defer os.Remove(dumpPath)
	in, err := os.Open(dumpPath)
	if err != nil {
		return "", 0, err
	}
	defer in.Close()
	fi, err := in.Stat()
	if err != nil {
		return "", 0, err
	}
	if fi.Size() > tgate.MaxDocument-packOverhead {
		return "", 0, ErrTooBig
	}
	r, err := age.NewScryptRecipient(password)
	if err != nil {
		return "", 0, err
	}
	if workFactor > 0 {
		r.SetWorkFactor(workFactor)
	}
	path = filepath.Join(dir, "backup.tar.gz.age")
	out, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return "", 0, err
	}
	err = func() error {
		enc, err := age.Encrypt(out, r)
		if err != nil {
			return err
		}
		if err := pack(enc, in, fi.Size(), now); err != nil {
			return err
		}
		if err := enc.Close(); err != nil {
			return err
		}
		return out.Sync()
	}()
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(path)
		return "", 0, err
	}
	st, err := os.Stat(path)
	if err != nil {
		return "", 0, err
	}
	if st.Size() > tgate.MaxDocument {
		os.Remove(path)
		return "", 0, ErrTooBig
	}
	return path, st.Size(), nil
}

// pack writes a tar.gz with the directories data and data/panel and the dump in it.
func pack(w io.Writer, dump io.Reader, size int64, mtime time.Time) error {
	gz := gzip.NewWriter(w)
	tw := tar.NewWriter(gz)
	for _, d := range []string{"data/", "data/panel/"} {
		if err := tw.WriteHeader(&tar.Header{Name: d, Typeflag: tar.TypeDir, Mode: 0o700, ModTime: mtime}); err != nil {
			return err
		}
	}
	if err := tw.WriteHeader(&tar.Header{Name: "data/panel/backup.dump", Typeflag: tar.TypeReg, Mode: 0o600, Size: size, ModTime: mtime}); err != nil {
		return err
	}
	if _, err := io.CopyN(tw, dump, size); err != nil {
		return err
	}
	if err := tw.Close(); err != nil {
		return err
	}
	return gz.Close()
}

// safeName keeps letters, digits, dots and dashes of a host for a file name.
func safeName(s string) string {
	s = strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '-':
			return r
		case r == ':':
			return '-'
		}
		return -1
	}, s)
	if s == "" {
		return "panel"
	}
	return s
}

func sizeText(n int64) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%d KB", n>>10)
	}
	return fmt.Sprintf("%d B", n)
}
