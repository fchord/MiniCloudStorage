package api

import (
	"context"
	"strconv"
	"sync"

	"github.com/google/uuid"
)

// chunkGate serializes PUT of the same (session, index) so two concurrent
// writes to one Filer object cannot interleave. Different indexes on the
// same session proceed in parallel — there is no session-wide mutex.
type chunkGate struct {
	mu    sync.Mutex
	locks map[string]*indexLock
}

type indexLock struct {
	mu   sync.Mutex
	refs int
}

func newChunkGate() *chunkGate {
	return &chunkGate{locks: make(map[string]*indexLock)}
}

func (g *chunkGate) acquire(id uuid.UUID, index int) func() {
	key := id.String() + ":" + strconv.Itoa(index)
	g.mu.Lock()
	l, ok := g.locks[key]
	if !ok {
		l = &indexLock{}
		g.locks[key] = l
	}
	l.refs++
	g.mu.Unlock()

	l.mu.Lock()
	return func() {
		l.mu.Unlock()
		g.mu.Lock()
		l.refs--
		if l.refs == 0 {
			delete(g.locks, key)
		}
		g.mu.Unlock()
	}
}

// sessionGate serializes complete/cancel commits for one upload id so only
// one side can INSERT completed or failed_sessions. A merge job may run
// concat without holding the lock; cancel interrupts that job via context.
type sessionGate struct {
	mu    sync.Mutex
	locks map[uuid.UUID]*indexLock
	jobs  map[uuid.UUID]*mergeJob
}

type mergeJob struct {
	cancel context.CancelFunc
}

func newSessionGate() *sessionGate {
	return &sessionGate{
		locks: make(map[uuid.UUID]*indexLock),
		jobs:  make(map[uuid.UUID]*mergeJob),
	}
}

func (g *sessionGate) acquire(id uuid.UUID) func() {
	g.mu.Lock()
	l, ok := g.locks[id]
	if !ok {
		l = &indexLock{}
		g.locks[id] = l
	}
	l.refs++
	g.mu.Unlock()

	l.mu.Lock()
	return func() {
		l.mu.Unlock()
		g.mu.Lock()
		l.refs--
		if l.refs == 0 {
			delete(g.locks, id)
		}
		g.mu.Unlock()
	}
}

func (g *sessionGate) startMerge(id uuid.UUID, parent context.Context) (context.Context, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if _, ok := g.jobs[id]; ok {
		return nil, false
	}
	ctx, cancel := context.WithCancel(parent)
	g.jobs[id] = &mergeJob{cancel: cancel}
	return ctx, true
}

func (g *sessionGate) cancelMerge(id uuid.UUID) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if j, ok := g.jobs[id]; ok {
		j.cancel()
	}
}

func (g *sessionGate) finishMerge(id uuid.UUID) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if j, ok := g.jobs[id]; ok {
		j.cancel()
		delete(g.jobs, id)
	}
}

func (g *sessionGate) merging(id uuid.UUID) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	_, ok := g.jobs[id]
	return ok
}
