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
// rejects it when the queue is full. The replica set can change at runtime.
type Pool struct {
	mu            sync.Mutex
	replicas      []*Replica
	maxPerReplica int
	maxQueue      int
	waiters       []chan *Replica
	makeReplica   func(addr string) (*Replica, error)
}

func newPool(maxPerReplica, maxQueue int, makeReplica func(addr string) (*Replica, error)) *Pool {
	return &Pool{maxPerReplica: maxPerReplica, maxQueue: maxQueue, makeReplica: makeReplica}
}

// SetReplicas replaces the replica set. Replicas that stay keep their in-flight
// count; removed ones finish their current requests but get no new ones.
// New capacity is handed to queued requests immediately.
func (p *Pool) SetReplicas(addrs []string) (added, removed int, err error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	want := make(map[string]bool, len(addrs))
	for _, a := range addrs {
		want[a] = true
	}
	next := make([]*Replica, 0, len(addrs))
	have := map[string]bool{}
	for _, r := range p.replicas {
		if want[r.Addr] {
			next = append(next, r)
			have[r.Addr] = true
		} else {
			removed++
		}
	}
	for _, a := range addrs {
		if have[a] {
			continue
		}
		r, mkErr := p.makeReplica(a)
		if mkErr != nil {
			err = errors.Join(err, mkErr)
			continue
		}
		next = append(next, r)
		have[a] = true
		added++
	}
	p.replicas = next
	p.dispatchLocked()
	return added, removed, err
}

// SetLimits changes per-replica capacity and queue size at runtime.
func (p *Pool) SetLimits(maxPerReplica, maxQueue int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.maxPerReplica = maxPerReplica
	p.maxQueue = maxQueue
	p.dispatchLocked() // more capacity may free queued requests
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
			// A slot was handed to us at the same moment we gave up. Pass it on.
			p.Release(<-ch)
		}
		return nil, ctx.Err()
	}
}

// Release frees a slot and hands any free capacity to the oldest waiters.
func (p *Pool) Release(r *Replica) {
	p.mu.Lock()
	defer p.mu.Unlock()
	r.inFlight--
	p.dispatchLocked()
}

// dispatchLocked moves queued requests onto free slots, oldest first.
func (p *Pool) dispatchLocked() {
	for len(p.waiters) > 0 {
		r := p.pickLocked()
		if r == nil {
			return
		}
		r.inFlight++
		ch := p.waiters[0]
		p.waiters = p.waiters[1:]
		ch <- r // buffered, never blocks
	}
}

// Stats reports in-flight requests on current replicas and queue length.
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
