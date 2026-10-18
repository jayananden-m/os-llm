package gateway

import (
	"context"
	"errors"
	"net/http/httputil"
	"sync"
)

// ErrQueueFull means every replica is at capacity and the wait queue is full.
var ErrQueueFull = errors.New("admission queue full")

// Replica is one model server (one vLLM pod).
type Replica struct {
	Addr     string
	proxy    *httputil.ReverseProxy
	inFlight int // guarded by Pool.mu
}

// Pool holds the replicas for one model. It admits a request only when a
// replica has a free slot, queues it (FIFO, bounded) when none do, and
// rejects it when the queue is full.
type Pool struct {
	mu            sync.Mutex
	replicas      []*Replica
	maxPerReplica int
	maxQueue      int
	waiters       []chan *Replica
}

func newPool(replicas []*Replica, maxPerReplica, maxQueue int) *Pool {
	return &Pool{replicas: replicas, maxPerReplica: maxPerReplica, maxQueue: maxQueue}
}

// Acquire returns a replica with a reserved slot, waiting in the queue if needed.
// The caller must call Release when the request finishes.
func (p *Pool) Acquire(ctx context.Context) (*Replica, error) {
	p.mu.Lock()
	if r := p.pickLocked(); r != nil {
		r.inFlight++
		p.mu.Unlock()
		return r, nil
	}
	if len(p.waiters) >= p.maxQueue {
		p.mu.Unlock()
		return nil, ErrQueueFull
	}
	ch := make(chan *Replica, 1)
	p.waiters = append(p.waiters, ch)
	p.mu.Unlock()

	select {
	case r := <-ch:
		return r, nil
	case <-ctx.Done():
		p.mu.Lock()
		removed := p.removeWaiterLocked(ch)
		p.mu.Unlock()
		if !removed {
			// Release handed us a slot at the same moment we gave up. Pass it on.
			p.Release(<-ch)
		}
		return nil, ctx.Err()
	}
}

// Release frees a slot. If requests are waiting, the slot goes straight to the
// oldest one, so the replica's in-flight count stays the same.
func (p *Pool) Release(r *Replica) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.waiters) > 0 {
		ch := p.waiters[0]
		p.waiters = p.waiters[1:]
		ch <- r // buffered, never blocks
		return
	}
	r.inFlight--
}

// Stats reports total in-flight requests and queue length.
func (p *Pool) Stats() (inFlight, queued int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, r := range p.replicas {
		inFlight += r.inFlight
	}
	return inFlight, len(p.waiters)
}

// ReplicaStat is a point-in-time view of one replica, used for metrics.
type ReplicaStat struct {
	Addr     string
	InFlight int
}

// Snapshot returns per-replica load, queue length and total slots.
func (p *Pool) Snapshot() (replicas []ReplicaStat, queued, capacity int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	replicas = make([]ReplicaStat, 0, len(p.replicas))
	for _, r := range p.replicas {
		replicas = append(replicas, ReplicaStat{Addr: r.Addr, InFlight: r.inFlight})
	}
	return replicas, len(p.waiters), len(p.replicas) * p.maxPerReplica
}

// pickLocked returns the least-busy replica with a free slot, or nil.
func (p *Pool) pickLocked() *Replica {
	var best *Replica
	for _, r := range p.replicas {
		if r.inFlight >= p.maxPerReplica {
			continue
		}
		if best == nil || r.inFlight < best.inFlight {
			best = r
		}
	}
	return best
}

func (p *Pool) removeWaiterLocked(ch chan *Replica) bool {
	for i, w := range p.waiters {
		if w == ch {
			p.waiters = append(p.waiters[:i], p.waiters[i+1:]...)
			return true
		}
	}
	return false
}
