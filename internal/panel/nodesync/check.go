package nodesync

import (
	"context"
	"time"

	"mikan/internal/nodeapi"
	"mikan/internal/nodehello"
)

// CheckNow asks a node for its health now instead of at the next tick, and returns the
// view the Nodes page then shows.
func (m *Manager) CheckNow(ctx context.Context, id int64) (HealthView, bool) {
	s, ok := m.Syncer(id)
	if !ok {
		return HealthView{}, false
	}
	s.refreshHealth(ctx)
	return s.Health(), true
}

// HelloView is a node's last hello: when it came and how the panel's dial back went.
type HelloView struct {
	At     time.Time
	Result nodehello.Result
}

// RecordHello keeps a node's hello for the Nodes page. Only in memory: a hello matters
// while somebody installs the node.
func (m *Manager) RecordHello(id int64, r nodehello.Result, at time.Time) {
	m.helloMu.Lock()
	defer m.helloMu.Unlock()
	if m.hellos == nil {
		m.hellos = map[int64]HelloView{}
	}
	m.hellos[id] = HelloView{At: at, Result: r}
}

// Hello is a node's last hello since the panel started.
func (m *Manager) Hello(id int64) (HelloView, bool) {
	m.helloMu.Lock()
	defer m.helloMu.Unlock()
	v, ok := m.hellos[id]
	return v, ok
}

// forgetHello drops what a removed node said.
func (m *Manager) forgetHello(id int64) {
	m.helloMu.Lock()
	defer m.helloMu.Unlock()
	delete(m.hellos, id)
}

// Diagnose asks a node how its server is (nodeapi.Client.Diagnose).
func (m *Manager) Diagnose(ctx context.Context, id int64) (nodeapi.Diagnosis, error) {
	c, err := clientOf[interface {
		Diagnose(context.Context) (nodeapi.Diagnosis, error)
	}](m, id)
	if err != nil {
		return nodeapi.Diagnosis{}, err
	}
	return c.Diagnose(ctx)
}
