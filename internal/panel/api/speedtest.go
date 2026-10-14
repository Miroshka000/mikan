package api

import (
	"context"
	"errors"
	"math"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"mikan/internal/nodeapi"
	"mikan/internal/panel/store/db"
	"mikan/internal/textutil"
)

// The speed test of a node's own way to the internet: run on the admin's word, the
// latest hundred kept per node.

type SpeedTestView struct {
	ID       int64     `json:"id"`
	At       time.Time `json:"at"`
	PingMs   float64   `json:"ping_ms" doc:"Медиана задержки, мс; -1 если ответов не было"`
	JitterMs float64   `json:"jitter_ms" doc:"Средний разброс задержки, мс"`
	LossPct  float64   `json:"loss_pct" doc:"Потери, %"`
	DownBps  int64     `json:"down_bps" doc:"Загрузка, бит/с"`
	UpBps    int64     `json:"up_bps" doc:"Отдача, бит/с"`
	Error    string    `json:"error,omitempty" doc:"Где тест оборвался; измеренное до того сохранено"`
}

type speedTestOutput struct{ Body SpeedTestView }
type speedTestsOutput struct{ Body []SpeedTestView }

type speedTestsInput struct {
	ID    int64 `path:"id" minimum:"1"`
	Limit int64 `query:"limit" minimum:"1" maximum:"100" default:"30"`
}

func (h *handlers) registerSpeedTests() {
	tags := []string{"node"}
	huma.Register(h.api, huma.Operation{OperationID: "run-node-speedtest", Method: http.MethodPost, Path: "/api/v1/nodes/{id}/speedtest", Summary: "Проверить скорость ноды",
		Description: "Задержка, разброс и потери по 20 DNS-запросам к 1.1.1.1, затем загрузка и отдача через speed.cloudflare.com: каждое до 8 секунд или до лимита объёма, что наступит раньше, не больше 250 МБ на загрузку и 100 МБ на отдачу. Занимает около 20 секунд; пользователи ноды могут почувствовать нагрузку на время теста. Одновременно на ноде идёт один тест, на одну ноду не чаще раза в 5 минут (иначе 429 speed_test_cooldown).",
		Tags:        tags}, h.runSpeedTest)
	huma.Register(h.api, huma.Operation{OperationID: "list-node-speedtests", Method: http.MethodGet, Path: "/api/v1/nodes/{id}/speedtests", Summary: "История проверок скорости ноды", Description: "Новые сверху; хранятся последние 100.", Tags: tags}, h.listSpeedTests)
}

func speedTestView(r db.NodeSpeedtest) SpeedTestView {
	return SpeedTestView{ID: r.ID, At: time.Unix(r.At, 0).UTC(), PingMs: r.PingMs, JitterMs: r.JitterMs, LossPct: r.LossPct,
		DownBps: r.DownBps, UpBps: r.UpBps, Error: r.Error}
}

func (h *handlers) runSpeedTest(ctx context.Context, in *nodeIDInput) (*speedTestOutput, error) {
	if _, err := h.getNode(ctx, in.ID); err != nil {
		return nil, err
	}
	if h.d.Nodes == nil {
		return nil, huma.Error502BadGateway("node_unavailable")
	}
	// A test moves up to 350 MB through the node: one per node per cooldown, claimed
	// before the node is asked so that two requests at once cannot both pass.
	now := h.d.Now()
	if wait := h.speedCooldown.claim(in.ID, now); wait > 0 {
		secs := int(wait.Round(time.Second) / time.Second)
		return nil, huma.ErrorWithHeaders(huma.Error429TooManyRequests("speed_test_cooldown"), http.Header{"Retry-After": {strconv.Itoa(max(1, secs))}})
	}
	h.audit(ctx, sessionOf(ctx).AdminID, "node.speedtest", "node", strconv.FormatInt(in.ID, 10), nil)
	// The test runs and is saved even if the admin closes the page meanwhile: the traffic
	// is spent either way, and the result is what it was for.
	runCtx, cancelRun := context.WithTimeout(context.WithoutCancel(ctx), speedTestRunTimeout)
	defer cancelRun()
	res, err := h.d.Nodes.SpeedTest(runCtx, in.ID)
	if err != nil {
		var ne *nodeapi.Error
		var se *nodeapi.StatusError
		switch {
		case errors.As(err, &ne) && ne.Code == "speed_test_busy":
			h.speedCooldown.release(in.ID) // nothing ran
			return nil, huma.Error409Conflict("speed_test_busy")
		case errors.As(err, &se) && se.Status == http.StatusNotFound:
			h.speedCooldown.release(in.ID)
			return nil, huma.Error409Conflict("node_too_old")
		}
		h.d.Log.Warn("node speed test", "node", in.ID, "err", err)
		return nil, huma.Error502BadGateway("node_unavailable")
	}
	// What a node says is kept within sense: it is a server somebody else may run.
	sane := func(v, hi float64) float64 {
		if math.IsNaN(v) || v < -1 {
			return -1
		}
		return min(v, hi)
	}
	at := res.At.Unix()
	if res.At.IsZero() || math.Abs(float64(at-h.d.Now().Unix())) > 3600 {
		at = h.d.Now().Unix()
	}
	saveCtx, cancelSave := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancelSave()
	var row db.NodeSpeedtest
	err = h.d.Store.Tx(saveCtx, func(q *db.Queries) error {
		var err error
		row, err = q.AddNodeSpeedTest(saveCtx, db.AddNodeSpeedTestParams{NodeID: in.ID, At: at,
			PingMs: sane(res.PingMs, 60_000), JitterMs: sane(res.JitterMs, 60_000), LossPct: math.Max(0, sane(res.LossPct, 100)),
			DownBps: max(0, min(res.DownBps, 1e12)), UpBps: max(0, min(res.UpBps, 1e12)), Error: textutil.Clip(res.Error, 300)})
		if err != nil {
			return err
		}
		return q.PruneNodeSpeedTests(saveCtx, in.ID)
	})
	if err != nil {
		return nil, err
	}
	return &speedTestOutput{Body: speedTestView(row)}, nil
}

const (
	// speedTestCooldown is the least time between two tests of one node.
	speedTestCooldown = 5 * time.Minute
	// speedTestRunTimeout bounds a test (the node's own limit is 60 s) detached from the request.
	speedTestRunTimeout = 100 * time.Second
)

// speedCooldown remembers when each node's last test started; in memory, so a restart of
// the panel forgets it.
type speedCooldown struct {
	mu   sync.Mutex
	last map[int64]time.Time
}

// claim starts the cooldown of a node and returns 0, or returns how long is left of one
// running already.
func (c *speedCooldown) claim(id int64, now time.Time) time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	if t, ok := c.last[id]; ok && now.Sub(t) < speedTestCooldown {
		return speedTestCooldown - now.Sub(t)
	}
	if c.last == nil {
		c.last = map[int64]time.Time{}
	}
	c.last[id] = now
	return 0
}

// release gives the claim back: the test never ran.
func (c *speedCooldown) release(id int64) {
	c.mu.Lock()
	delete(c.last, id)
	c.mu.Unlock()
}

func (h *handlers) listSpeedTests(ctx context.Context, in *speedTestsInput) (*speedTestsOutput, error) {
	if _, err := h.getNode(ctx, in.ID); err != nil {
		return nil, err
	}
	rows, err := h.d.Store.Q.ListNodeSpeedTests(ctx, db.ListNodeSpeedTestsParams{NodeID: in.ID, Limit: int32(in.Limit)})
	if err != nil {
		return nil, err
	}
	out := &speedTestsOutput{Body: make([]SpeedTestView, 0, len(rows))}
	for _, r := range rows {
		out.Body = append(out.Body, speedTestView(r))
	}
	return out, nil
}
