package infraalerts

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"mikan/internal/nodeapi"
	"mikan/internal/panel/acme"
	"mikan/internal/panel/autotune"
	"mikan/internal/panel/nodesync"
	"mikan/internal/panel/nodeupdate"
	"mikan/internal/panel/settings"
	"mikan/internal/panel/store"
	"mikan/internal/panel/tgate"
	"mikan/internal/panel/updates"
)

// roundInterval is how often the monitor samples. Node health arrives every five seconds.
// Outbound checks are cached by the node for a minute, and observe ignores an unchanged
// CheckedAt so this cadence adds no extra probes.
const roundInterval = 5 * time.Second

type Runtime interface {
	Health(id int64) (nodesync.HealthView, bool)
	Warp(ctx context.Context, id int64, force bool) (nodeapi.WarpStatus, error)
	Probe(ctx context.Context, id int64, proxy string) (nodeapi.ProbeResult, error)
}

type Tuner interface {
	Status(inboundID int64) (autotune.Status, bool)
}

type CertificateSource func() acme.Status
type UpdateSource interface {
	Host() (updates.HostStatus, bool)
}

// NodeUpdateSource knows the updates of the remote nodes that failed (nodeupdate.Service).
type NodeUpdateSource interface {
	Failures(ctx context.Context) []nodeupdate.Failure
}

// Telegram is what the monitor asks of the Telegram gateway (*tgate.Gateway): the language
// of the alerts, whether the infrastructure alerts are on, the chat of the admin who
// receives them and a client that sends. The monitor sees nothing else of the bot, needs
// none of its screens, and a test supplies a fake that sends nowhere.
type Telegram interface {
	// PolledLang and PolledInfrastructure are the bot's settings as the polling loops read
	// them (cached); InfrastructureEnabled is the same answer read straight, for delivery.
	PolledLang(ctx context.Context) string
	PolledInfrastructure(ctx context.Context) bool
	InfrastructureEnabled(ctx context.Context) bool
	InfrastructureAdminChat(ctx context.Context) (int64, bool, error)
	InfrastructureClient(ctx context.Context) (*tgate.Client, error)
}

type Monitor struct {
	store       *store.Store
	settings    *settings.Settings
	runtime     Runtime
	tuner       Tuner
	cert        CertificateSource
	updates     UpdateSource
	nodeUpdates NodeUpdateSource
	bot         Telegram
	log         *slog.Logger
	now         func() time.Time
	roundMu     sync.Mutex
	world       *world // under roundMu
	dispatchMu  sync.Mutex
	dispatching bool
	// delivering is the delivery started by a round: Run waits for it before it returns,
	// so it never works on a database the panel has closed.
	delivering sync.WaitGroup
	// stored is the state's text as last read or written (under roundMu).
	stored storedState
}

func New(st *store.Store, set *settings.Settings, runtime Runtime, tuner Tuner, cert CertificateSource, updates UpdateSource,
	bot Telegram, log *slog.Logger, now func() time.Time) *Monitor {
	return &Monitor{store: st, settings: set, runtime: runtime, tuner: tuner, cert: cert, updates: updates, bot: bot, log: log, now: now}
}

// WatchNodeUpdates makes the monitor tell the admin about the updates of nodes that failed
// (the "update" event); call it before Run.
func (m *Monitor) WatchNodeUpdates(src NodeUpdateSource) { m.nodeUpdates = src }

func (m *Monitor) Run(ctx context.Context) {
	t := time.NewTicker(roundInterval)
	defer t.Stop()
	defer m.delivering.Wait()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			m.round(ctx)
		}
	}
}

func (m *Monitor) config(ctx context.Context) AlertsConfig {
	c, _, err := settings.GetOver(ctx, m.settings, KeyConfig, Default())
	if err != nil {
		m.logError("infrastructure alerts: settings", err)
		return AlertsConfig{}
	}
	return c
}

func (m *Monitor) logError(message string, err error) {
	if m.log != nil {
		m.log.Error(message, "err", err)
	}
}
