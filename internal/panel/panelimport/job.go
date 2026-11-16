package panelimport

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"runtime/debug"
	"sync"
	"time"

	"mikan/internal/panel/domain"
	"mikan/internal/panel/model"
	"mikan/internal/panel/store"
)

// JobTimeout bounds a whole job: reading the old panel and, for an import, making users.
// A variable for the tests.
var JobTimeout = time.Hour

// jobHook runs after the old panel is read; tests use it to break a job.
var jobHook func(mode string)

var (
	ErrBusy      = errors.New("import_busy")      // a job is running already
	ErrCancelled = errors.New("import_cancelled") // the admin stopped it, or the panel stopped
)

// JobState is where the job is: a preview (what an import would do) or an import.
type JobState struct {
	Mode    string   `json:"mode,omitempty" enum:"preview,import"`
	State   string   `json:"state" enum:"idle,fetching,checking,importing,done,failed" doc:"idle — ещё не было; fetching — читается старая панель; importing — создаются пользователи"`
	From    string   `json:"from,omitempty" doc:"Панель и её адрес (без пути и данных для входа)"`
	Total   int      `json:"total"`
	Done    int      `json:"done"`
	Kind    string   `json:"kind,omitempty" doc:"Откуда: marzban, pasarguard, remnawave, 3xui или bedolaga"`
	Preview *Preview `json:"preview,omitempty"`
	Report  *Report  `json:"report,omitempty" doc:"Что сделано; при ошибке посреди импорта — сделанное до неё"`
	// The same for a sales bot (kind bedolaga).
	BotPreview *BotPreview `json:"bot_preview,omitempty"`
	BotReport  *BotReport  `json:"bot_report,omitempty"`
	Error      string      `json:"error,omitempty" doc:"Код ошибки, когда state = failed"`
	Started    *time.Time  `json:"started,omitempty"`
	Finished   *time.Time  `json:"finished,omitempty"`
}

// Importer runs one job at a time, apart from the request that asked for it: a closed
// tab or a proxy timeout does not stop it, and its result waits in State. Run ties the
// jobs to the panel: when the panel stops, a running job is stopped and waited for.
type Importer struct {
	st    *store.Store
	users *domain.Users
	hc    *http.Client
	now   func() time.Time
	log   *slog.Logger
	// Done, when not nil, runs after an import that made users, also one that stopped
	// part way (it files the old links' kind in the settings).
	Done func(ctx context.Context, kind Kind)
	// Balance is the setup of the accounts' balance, which a bot's balances are carried into;
	// nil: they are not.
	Balance func(ctx context.Context) (model.BalanceConfig, error)

	mu     sync.Mutex
	state  JobState
	base   context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

func NewImporter(st *store.Store, users *domain.Users, hc *http.Client, now func() time.Time, log *slog.Logger) *Importer {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	if hc == nil {
		hc = Client()
	}
	return &Importer{st: st, users: users, hc: hc, now: now, log: log, state: JobState{State: "idle"}, base: context.Background()}
}

// HTTP is the client the importer reads old panels with.
func (im *Importer) HTTP() *http.Client { return im.hc }

// Run keeps the jobs within ctx, the panel's life: when it ends, a running job is stopped
// and Run returns once it has.
func (im *Importer) Run(ctx context.Context) {
	im.mu.Lock()
	im.base = ctx
	im.mu.Unlock()
	<-ctx.Done()
	im.Cancel()
	im.wg.Wait()
}

// State is a copy of where the job is.
func (im *Importer) State() JobState {
	im.mu.Lock()
	defer im.mu.Unlock()
	s := im.state
	if s.Report != nil {
		r := *s.Report
		s.Report = &r
	}
	if s.Preview != nil {
		p := *s.Preview
		s.Preview = &p
	}
	if s.BotReport != nil {
		r := *s.BotReport
		s.BotReport = &r
	}
	if s.BotPreview != nil {
		p := *s.BotPreview
		s.BotPreview = &p
	}
	return s
}

func (im *Importer) update(fn func(*JobState)) {
	im.mu.Lock()
	fn(&im.state)
	im.mu.Unlock()
}

// Cancel stops a running job; a stopped import keeps the users made so far and its report.
func (im *Importer) Cancel() {
	im.mu.Lock()
	if im.cancel != nil {
		im.cancel()
	}
	im.mu.Unlock()
}

// Preview reads the old panel and works out what an import would do, in the background.
func (im *Importer) Preview(src Source, from string) error {
	return im.start("preview", src, 0, from)
}

// Start reads the old panel and makes its users on tariffID in the background; from names
// the source for the state ("marzban https://panel.example.com").
func (im *Importer) Start(src Source, tariffID int64, from string) error {
	return im.start("import", src, tariffID, from)
}

// PreviewBot reads a sales bot and works out what its import would do, in the background.
func (im *Importer) PreviewBot(src Source, from string) error {
	return im.startWith("preview", src.Kind, from, func(ctx context.Context) { im.runBot(ctx, "preview", src, 0) })
}

// StartBot brings a sales bot's accounts over in the background; adminID ran it.
func (im *Importer) StartBot(src Source, from string, adminID int64) error {
	return im.startWith("import", src.Kind, from, func(ctx context.Context) { im.runBot(ctx, "import", src, adminID) })
}

func (im *Importer) start(mode string, src Source, tariffID int64, from string) error {
	return im.startWith(mode, src.Kind, from, func(ctx context.Context) { im.run(ctx, mode, src, tariffID) })
}

func (im *Importer) startWith(mode string, kind Kind, from string, work func(ctx context.Context)) error {
	im.mu.Lock()
	defer im.mu.Unlock()
	switch im.state.State {
	case "fetching", "checking", "importing":
		return ErrBusy
	}
	if im.base.Err() != nil {
		return ErrCancelled
	}
	ctx, cancel := context.WithTimeout(im.base, JobTimeout)
	im.cancel = cancel
	started := im.now()
	im.state = JobState{Mode: mode, Kind: string(kind), State: "fetching", From: from, Started: &started}
	im.wg.Add(1)
	go func() {
		defer im.wg.Done()
		defer cancel()
		work(ctx)
	}()
	return nil
}

func (im *Importer) run(ctx context.Context, mode string, src Source, tariffID int64) {
	var report *Report
	finish := func(err error) {
		finished := im.now()
		im.update(func(s *JobState) {
			s.Finished, s.Report = &finished, report
			if err != nil {
				s.State, s.Error = "failed", Code(err)
			} else {
				s.State = "done"
			}
		})
	}
	defer func() {
		// A bug here must not take the panel down with it.
		if p := recover(); p != nil {
			im.log.Error("import: panic", "panic", p, "stack", string(debug.Stack()))
			finish(fmt.Errorf("panic: %v", p))
		}
	}()
	list, err := Fetch(ctx, im.hc, src)
	if err != nil {
		im.log.Warn("import", "from", src.Kind, "err", err)
		finish(stopped(ctx, err))
		return
	}
	if jobHook != nil {
		jobHook(mode)
	}
	if mode == "preview" {
		im.update(func(s *JobState) { s.State, s.Total = "checking", len(list) })
		p, err := Check(ctx, im.st.Q, list)
		if err != nil {
			finish(stopped(ctx, err))
			return
		}
		im.update(func(s *JobState) { s.Preview = &p })
		finish(nil)
		return
	}
	im.update(func(s *JobState) { s.State, s.Total = "importing", len(list) })
	r, err := Apply(ctx, im.st, im.users, im.now(), src.Kind, tariffID, list, func(done int) {
		im.update(func(s *JobState) { s.Done = done })
	})
	report = &r
	if r.Created > 0 && im.Done != nil {
		// Users were made, also when the import stopped part way: their links need the kind.
		dctx, dcancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		im.Done(dctx, src.Kind)
		dcancel()
	}
	if err != nil {
		im.log.Warn("import stopped part way", "from", src.Kind, "created", r.Created, "err", err)
	}
	finish(stopped(ctx, err))
}

// finisher is how a job ends: its state, and the report kept also on an error.
func (im *Importer) finisher(set func(*JobState)) func(error) {
	return func(err error) {
		finished := im.now()
		im.update(func(s *JobState) {
			s.Finished = &finished
			if set != nil {
				set(s)
			}
			if err != nil {
				s.State, s.Error = "failed", Code(err)
			} else {
				s.State = "done"
			}
		})
	}
}

func (im *Importer) runBot(ctx context.Context, mode string, src Source, adminID int64) {
	var report *BotReport
	finish := func(err error) { im.finisher(func(s *JobState) { s.BotReport = report })(err) }
	defer func() {
		if p := recover(); p != nil {
			im.log.Error("import: panic", "panic", p, "stack", string(debug.Stack()))
			finish(fmt.Errorf("panic: %v", p))
		}
	}()
	var bc model.BalanceConfig
	if im.Balance != nil {
		var err error
		if bc, err = im.Balance(ctx); err != nil {
			finish(err)
			return
		}
	}
	bot, err := FetchBot(ctx, im.hc, src)
	if err != nil {
		im.log.Warn("import", "from", src.Kind, "err", err)
		finish(stopped(ctx, err))
		return
	}
	if jobHook != nil {
		jobHook(mode)
	}
	if mode == "preview" {
		im.update(func(s *JobState) { s.State, s.Total = "checking", len(bot.Accounts) })
		p, err := CheckBot(ctx, im.st.Q, bc, bot)
		if err != nil {
			finish(stopped(ctx, err))
			return
		}
		im.update(func(s *JobState) { s.BotPreview = &p })
		finish(nil)
		return
	}
	im.update(func(s *JobState) { s.State, s.Total = "importing", len(bot.Accounts) })
	r, err := ApplyBot(ctx, im.st, im.now(), bc, adminID, bot, func(done int) {
		im.update(func(s *JobState) { s.Done = done })
	})
	report = &r
	if err != nil {
		im.log.Warn("bot import stopped part way", "from", src.Kind, "accounts", r.Accounts, "err", err)
	}
	finish(stopped(ctx, err))
}

// stopped tells a cancel (the admin, or the panel stopping) from a timeout and other errors.
func stopped(ctx context.Context, err error) error {
	if err != nil && errors.Is(context.Cause(ctx), context.Canceled) {
		return ErrCancelled
	}
	return err
}

// Code is the error code for err.
func Code(err error) string {
	for _, known := range []error{ErrAuth, ErrUnreachable, ErrTLS, ErrRedirect, ErrAddress, ErrIncomplete, ErrAnswer, ErrBusy, ErrCancelled} {
		if errors.Is(err, known) {
			return known.Error()
		}
	}
	switch {
	case errors.Is(err, domain.ErrNotFound):
		return "tariff_not_found"
	case errors.Is(err, context.DeadlineExceeded):
		return "import_timeout"
	}
	return "import_failed"
}
